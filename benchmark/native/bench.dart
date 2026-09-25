// Native latency benchmark driver.
//
// Build the c-shared benchmark backend first, then run this driver from the
// repository root:
//
//	go build -buildmode=c-shared -o benchmark/native/libbench.dylib ./benchmark/native
//	dart run benchmark/native/bench.dart --n 10000
//
// The driver mirrors lib/bridge/bridge_native.dart exactly: a fresh
// ReceivePort per call, the request envelope copied into a malloc'd
// BytesContainer, the response parsed from the returned pointer address.
// Each iteration's stopwatch covers the full Dart->Go->Dart round trip.
//
// ignore_for_file: avoid_print
library;

import 'dart:async';
import 'dart:convert';
import 'dart:ffi' as ffi;
import 'dart:ffi';
import 'dart:isolate';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';
import 'package:fixnum/fixnum.dart';
import 'package:godash/bridge/native_bytes.dart';
import 'package:godash/bridge/native_library.dart';
import 'package:godash/pb/core.pb.dart';

const echoPath = '/bench.EchoService/Echo';

void main(List<String> args) async {
  final n = _intArg(args, 'n', 2000);
  final warmup = _intArg(args, 'warmup', 200);
  final payloadSize = _intArg(args, 'payload', 64);
  final libPath = _strArg(args, 'lib', 'benchmark/native/libbench.dylib');
  final hot = _boolArg(args, 'hot', false);

  final payload = _makePayload(payloadSize);

  final dyn = DynamicLibrary.open(libPath);
  final lib = NativeLibrary(dyn);
  final ret = lib.InitializeDartAPI(NativeApi.initializeApiDLData);
  if (ret != 0) {
    throw StateError('InitializeDartAPI failed: $ret');
  }
  // Free Go-allocated response containers through the Go-exported symbol,
  // exactly like lib/bridge/bridge_native.dart does.
  configureResponseContainerFree(lib.FreeBytesContainer);

  if (hot) {
    final fn = dyn.lookupFunction<_HotNative, _HotDart>('BenchHotAdd');
    await _runHot(n, warmup, fn);
    return;
  }

  // Warmup (JIT + allocator paths).
  for (var i = 0; i < warmup; i++) {
    final bytes = await _roundTrip(lib, payload);
    if (!bytesEqual(bytes, payload)) {
      throw StateError('echo mismatch during warmup');
    }
  }

  final latencies = List<double>.filled(n, 0);
  for (var i = 0; i < n; i++) {
    final sw = Stopwatch()..start();
    final bytes = await _roundTrip(lib, payload);
    sw.stop();
    latencies[i] = sw.elapsedMicroseconds.toDouble();
    if (!bytesEqual(bytes, payload)) {
      throw StateError('echo mismatch at iteration $i');
    }
  }

  latencies.sort();
  final mean = latencies.reduce((a, b) => a + b) / latencies.length;
  final min = latencies.first;
  final p50 = _percentile(latencies, 0.50);
  final p90 = _percentile(latencies, 0.90);
  final p99 = _percentile(latencies, 0.99);
  final max = latencies.last;
  print('');
  print('=== Native round-trip latency (payload=$payloadSize B, n=$n, '
      'mode=async) ===');
  print('min  : ${min.toStringAsFixed(1)} us');
  print('p50  : ${p50.toStringAsFixed(1)} us');
  print('p90  : ${p90.toStringAsFixed(1)} us');
  print('p99  : ${p99.toStringAsFixed(1)} us');
  print('max  : ${max.toStringAsFixed(1)} us');
  print('mean : ${mean.toStringAsFixed(1)} us');
  final stats = <String, Object>{
    'transport': 'native',
    'mode': 'async',
    'iterations': n,
    'warmup': warmup,
    'payload_bytes': payloadSize,
    'unit': 'us',
    'min': min,
    'p50': p50,
    'p90': p90,
    'p99': p99,
    'max': max,
    'mean': mean,
  };
  print('json : ${jsonEncode(stats)}');
}

// ffi.Int32 is used (not the bare Int32) because package:fixnum also exports
// an Int32 class, which otherwise shadows dart:ffi's native type.
typedef _HotNative =
    ffi.Int32 Function(ffi.Pointer<ffi.Void>, ffi.Pointer<ffi.Void>);
typedef _HotDart =
    int Function(ffi.Pointer<ffi.Void>, ffi.Pointer<ffi.Void>);

// _runHot measures the packed-struct hot path (PLAN.md P3): one int64 in, one
// int64 out, no protobuf, no envelope, no port, no goroutine.
Future<void> _runHot(int n, int warmup, _HotDart fn) async {
  for (var i = 0; i < warmup; i++) {
    _roundTripHot(fn, i);
  }
  final latencies = List<double>.filled(n, 0);
  for (var i = 0; i < n; i++) {
    final sw = Stopwatch()..start();
    final got = _roundTripHot(fn, i);
    sw.stop();
    latencies[i] = sw.elapsedMicroseconds.toDouble();
    if (got != i + 1) {
      throw StateError('hot mismatch: got $got want ${i + 1}');
    }
  }
  latencies.sort();
  final mean = latencies.reduce((a, b) => a + b) / latencies.length;
  final min = latencies.first;
  final p50 = _percentile(latencies, 0.50);
  final p90 = _percentile(latencies, 0.90);
  final p99 = _percentile(latencies, 0.99);
  final max = latencies.last;
  print('');
  print('=== Native hot-path latency (packed int64, n=$n) ===');
  print('min  : ${min.toStringAsFixed(1)} us');
  print('p50  : ${p50.toStringAsFixed(1)} us');
  print('p90  : ${p90.toStringAsFixed(1)} us');
  print('p99  : ${p99.toStringAsFixed(1)} us');
  print('max  : ${max.toStringAsFixed(1)} us');
  print('mean : ${mean.toStringAsFixed(1)} us');
  print('json : ${jsonEncode(<String, Object>{
        'transport': 'native',
        'mode': 'hot',
        'iterations': n,
        'warmup': warmup,
        'payload_bytes': 8,
        'unit': 'us',
        'min': min,
        'p50': p50,
        'p90': p90,
        'p99': p99,
        'max': max,
        'mean': mean,
      })}');
}

// _roundTripHot mirrors the generated hot wrapper: pack the request into a
// native buffer, call the export, unpack the response.
int _roundTripHot(_HotDart fn, int value) {
  final reqBytes = Uint8List(8);
  ByteData.sublistView(reqBytes).setInt64(0, value, Endian.host);
  final reqPtr = malloc<Uint8>(8);
  final respPtr = calloc<Uint8>(8);
  try {
    reqPtr.asTypedList(8).setAll(0, reqBytes);
    final status = fn(reqPtr.cast<Void>(), respPtr.cast<Void>());
    if (status != 0) {
      throw StateError('hot status=$status');
    }
    return ByteData.sublistView(
      Uint8List.fromList(respPtr.asTypedList(8)),
    ).getInt64(0, Endian.host);
  } finally {
    malloc.free(reqPtr);
    malloc.free(respPtr);
  }
}

// _roundTrip performs one full Dart->Go->Dart unary echo call using the same
// allocation and free pattern as lib/bridge/bridge_native.dart, including the
// per-call envelope serialization and a fresh ReceivePort per call.
Future<Uint8List> _roundTrip(
  NativeLibrary lib,
  Uint8List payload,
) async {
  final comp = Completer<Uint8List>();
  final port = ReceivePort();
  final nativePort = port.sendPort.nativePort;

  late final StreamSubscription<dynamic> sub;
  sub = port.listen((msg) {
    if (msg is! int) {
      comp.completeError(StateError('unexpected port message: $msg'));
      return;
    }
    // Zero-copy: parse straight from the C-heap view, then free via the
    // Go-exported FreeBytesContainer (mirrors Bridge.pointerAddressToResponse).
    final (bytes, container) = pointerAddressToBytes(msg);
    sub.cancel();
    port.close();
    final resp = Response.fromBuffer(bytes);
    freeResponseContainer(container);
    if (resp.hasError()) {
      comp.completeError(
        StateError('rpc error (${resp.error.code}) ${resp.error.message}'),
      );
      return;
    }
    comp.complete(Uint8List.fromList(resp.rpcResponse.payload));
  });

  final req = Request(
    port: Int64(nativePort),
    rpcRequest: RpcRequest(path: echoPath, payload: payload),
  );
  final container = bytesToBytesContainerPointer(req.writeToBuffer());
  // The RPC export copies the request synchronously before spawning its
  // goroutine, so the Dart-owned container is freed right away.
  lib.RPC(nativePort, container);
  freeBytesContainerPointer(container);

  final resp = await comp.future.timeout(const Duration(seconds: 10));
  return resp;
}

Uint8List _makePayload(int size) {
  final b = Uint8List(size);
  for (var i = 0; i < size; i++) {
    b[i] = i & 0x7F;
  }
  return b;
}

bool bytesEqual(Uint8List a, Uint8List b) {
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return false;
  }
  return true;
}

double _percentile(List<double> sorted, double p) {
  final idx = (p * (sorted.length - 1)).round().clamp(0, sorted.length - 1);
  return sorted[idx];
}

int _intArg(List<String> args, String name, int fallback) {
  final i = args.indexOf('--$name');
  if (i >= 0 && i + 1 < args.length) {
    return int.parse(args[i + 1]);
  }
  return fallback;
}

String _strArg(List<String> args, String name, String fallback) {
  final i = args.indexOf('--$name');
  if (i >= 0 && i + 1 < args.length) {
    return args[i + 1];
  }
  return fallback;
}

bool _boolArg(List<String> args, String name, bool fallback) {
  if (args.contains('--$name')) {
    return true;
  }
  final i = args.indexOf('--$name');
  if (i >= 0 && i + 1 < args.length) {
    return args[i + 1] != 'false';
  }
  return fallback;
}
