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
import 'dart:ffi';
import 'dart:isolate';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';
import 'package:fixnum/fixnum.dart';
import 'package:godash/bridge/native_bytes.dart';
import 'package:godash/bridge/native_library.g.dart';
import 'package:godash/pb/core.pb.dart';

const echoPath = '/bench.EchoService/Echo';

void main(List<String> args) async {
  final n = _intArg(args, 'n', 2000);
  final warmup = _intArg(args, 'warmup', 200);
  final payloadSize = _intArg(args, 'payload', 64);
  final libPath = _strArg(args, 'lib', 'benchmark/native/libbench.dylib');
  final sync = _boolArg(args, 'sync', false);

  final payload = _makePayload(payloadSize);

  final lib = NativeLibrary(DynamicLibrary.open(libPath));
  final ret = lib.InitializeDartAPI(NativeApi.initializeApiDLData);
  if (ret != 0) {
    throw StateError('InitializeDartAPI failed: $ret');
  }
  // Free Go-allocated response containers through the Go-exported symbol,
  // exactly like lib/bridge/bridge_native.dart does.
  configureResponseContainerFree(lib.FreeBytesContainer);

  // Warmup (JIT + allocator paths).
  for (var i = 0; i < warmup; i++) {
    final bytes = await _trip(sync, lib, payload);
    if (!bytesEqual(bytes, payload)) {
      throw StateError('echo mismatch during warmup');
    }
  }

  final latencies = List<double>.filled(n, 0);
  for (var i = 0; i < n; i++) {
    final sw = Stopwatch()..start();
    final bytes = await _trip(sync, lib, payload);
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
      'mode=${sync ? 'sync' : 'async'}) ===');
  print('min  : ${min.toStringAsFixed(1)} us');
  print('p50  : ${p50.toStringAsFixed(1)} us');
  print('p90  : ${p90.toStringAsFixed(1)} us');
  print('p99  : ${p99.toStringAsFixed(1)} us');
  print('max  : ${max.toStringAsFixed(1)} us');
  print('mean : ${mean.toStringAsFixed(1)} us');
  final stats = <String, Object>{
    'transport': 'native',
    'mode': sync ? 'sync' : 'async',
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

// _trip selects the sync fast path (CallSync, PLAN.md P2) or the async
// envelope path for one echo round trip.
FutureOr<Uint8List> _trip(bool sync, NativeLibrary lib, Uint8List payload) {
  return sync ? _roundTripSync(lib, payload) : _roundTrip(lib, payload);
}

// _roundTripSync performs one unary echo call through the CallSync export:
// no goroutine, no ReceivePort, no port round trip. The response container is
// Go-allocated and freed through the Go-exported FreeBytesContainer.
Uint8List _roundTripSync(NativeLibrary lib, Uint8List payload) {
  final req = Request(
    rpcRequest: RpcRequest(path: echoPath, payload: payload),
  );
  final container = bytesToBytesContainerPointer(req.writeToBuffer());
  final respPtr = lib.CallSync(container);
  freeBytesContainerPointer(container);
  if (respPtr.address == 0) {
    throw StateError('CallSync returned a null response');
  }
  final resp = responseFromPointerAddress(respPtr.address);
  if (resp.hasError()) {
    throw StateError('rpc error (${resp.error.code}) ${resp.error.message}');
  }
  return Uint8List.fromList(resp.rpcResponse.payload);
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
