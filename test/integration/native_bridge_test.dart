// Native bridge integration test.
//
// Builds the benchmark c-shared backend and drives the real FFI surface
// (InitializeDartAPI / RPC / CallSync / FreeBytesContainer / a packed hot
// export) through the same helpers the Bridge uses. This covers the allocator
// contract (Dart allocates+frees requests, Go allocates responses, Dart frees
// them through FreeBytesContainer), the zero-copy request path and the hot ABI.
//
// Skipped automatically when there is no Go toolchain or the platform is not a
// supported native host.
library;

import 'dart:async';
import 'dart:ffi' as ffi;
import 'dart:ffi';
import 'dart:io';
import 'dart:isolate';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';
import 'package:fixnum/fixnum.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:godash/bridge/backpressure.dart';
import 'package:godash/bridge/native_bytes.dart';
import 'package:godash/bridge/native_library.dart';
import 'package:godash/pb/core.pb.dart';

const _echoPath = '/bench.EchoService/Echo';
const _gatedStreamPath = '/bench.EchoService/GatedStream';

bool _goAvailable() {
  try {
    return Process.runSync('go', ['version']).exitCode == 0;
  } catch (_) {
    return false;
  }
}

final String _root = Directory.current.path;
final bool _repoRootOk = File('$_root/benchmark/native/main.go').existsSync();
final String? _skipReason = (!(Platform.isMacOS || Platform.isLinux) || !_goAvailable() || !_repoRootOk)
    ? 'native integration test requires Go and a macOS/Linux host in the repo root'
    : null;

String get _libPath {
  final ext = Platform.isMacOS ? 'dylib' : 'so';
  return '${Directory.systemTemp.path}/godash_native_test/libgodash_test.$ext';
}

void main() {
  group('native bridge integration', () {
    late NativeLibrary lib;
    late DynamicLibrary dyn;

    setUpAll(() async {
      final dir = Directory('${Directory.systemTemp.path}/godash_native_test');
      dir.createSync(recursive: true);
      final result = await Process.run(
        'go',
        ['build', '-buildmode=c-shared', '-o', _libPath, './benchmark/native'],
        workingDirectory: _root,
      );
      if (result.exitCode != 0) {
        throw StateError('go build failed:\n${result.stdout}\n${result.stderr}');
      }
      dyn = DynamicLibrary.open(_libPath);
      lib = NativeLibrary(dyn);
      final ret = lib.InitializeDartAPI(NativeApi.initializeApiDLData);
      if (ret != 0) {
        throw StateError('InitializeDartAPI failed: $ret');
      }
      configureResponseContainerFree(lib.FreeBytesContainer);
    });

    test('async unary round trip (zero-copy request, Go-free response)', () async {
      final payload = Uint8List.fromList(List.generate(256, (i) => i & 0xFF));
      final resp = await _rpcAsync(
        lib,
        Request(rpcRequest: RpcRequest(path: _echoPath, payload: payload)),
      );
      expect(resp.hasError(), isFalse, reason: resp.hasError() ? resp.error.message : null);
      expect(resp.hasRpcResponse(), isTrue);
      expect(resp.rpcResponse.payload, payload);
    });

    test('sync unary round trip (CallSync)', () {
      final payload = Uint8List.fromList([1, 2, 3, 4, 5]);
      final resp = _rpcSync(
        lib,
        Request(rpcRequest: RpcRequest(path: _echoPath, payload: payload)),
      );
      expect(resp.hasRpcResponse(), isTrue);
      expect(resp.rpcResponse.payload, payload);
    });

    test('error response is delivered and freed', () async {
      final resp = await _rpcAsync(
        lib,
        Request(rpcRequest: RpcRequest(path: '/does/not/exist', payload: Uint8List(0))),
      );
      expect(resp.hasError(), isTrue);
      expect(resp.error.code, 404);
    });

    test('packed hot path export', () {
      final fn = dyn.lookupFunction<
          ffi.Int32 Function(ffi.Pointer<ffi.Void>, ffi.Pointer<ffi.Void>),
          int Function(ffi.Pointer<ffi.Void>, ffi.Pointer<ffi.Void>)>('BenchHotAdd');
      final reqPtr = malloc<Uint8>(8);
      final respPtr = calloc<Uint8>(8);
      try {
        reqPtr.cast<ffi.Int64>().value = 41;
        final status = fn(reqPtr.cast<ffi.Void>(), respPtr.cast<ffi.Void>());
        expect(status, 0);
        expect(respPtr.cast<ffi.Int64>().value, 42);
      } finally {
        malloc.free(reqPtr);
        malloc.free(respPtr);
      }
    });

    test('repeated calls do not leak or corrupt (allocator smoke)', () async {
      final payload = Uint8List.fromList(List.generate(64, (i) => i));
      for (var i = 0; i < 200; i++) {
        final resp = _rpcSync(
          lib,
          Request(rpcRequest: RpcRequest(path: _echoPath, payload: payload)),
        );
        if (resp.rpcResponse.payload.length != payload.length) {
          fail('payload mismatch at iteration $i');
        }
      }
    });

    test('high-volume payloads do not leak request/response containers', () {
      final payload = Uint8List.fromList(
        List.generate(64 * 1024, (i) => i & 0xFF),
      );
      Request makeReq() => Request(
        rpcRequest: RpcRequest(path: _echoPath, payload: payload),
      );

      // Warm up allocators/JIT so the baseline is stable.
      for (var i = 0; i < 50; i++) {
        _rpcSync(lib, makeReq());
      }

      final before = ProcessInfo.currentRss;
      const iterations = 2000; // ~125 MiB of request + response churn
      for (var i = 0; i < iterations; i++) {
        final resp = _rpcSync(lib, makeReq());
        if (resp.rpcResponse.payload.length != payload.length) {
          fail('payload mismatch at iteration $i');
        }
      }
      final growth = ProcessInfo.currentRss - before;

      // A missing free on either the Dart request container or the Go response
      // container would add ~125 MiB here; allow generous allocator slack.
      expect(
        growth,
        lessThan(96 * 1024 * 1024),
        reason: 'RSS grew by $growth bytes over $iterations sync calls',
      );
    });

    test('block backpressure gates a Go streaming producer', () async {
      final receive = ReceivePort();
      final port = receive.sendPort.nativePort;
      final raw = StreamController<Response>();
      receive.listen((msg) {
        final resp = responseFromPointerAddress(msg as int);
        if (resp.hasDone()) {
          receive.close();
          raw.close();
          return;
        }
        raw.add(resp);
      });

      // Seed the block window before the stream request so the gate starts
      // bounded at 1 (an unseeded gate is unbounded and never gates).
      await _sendCredit(lib, port, 1);

      final req = Request(
        rpcRequest: RpcRequest(
          path: _gatedStreamPath,
          payload: Uint8List.fromList([4]),
        ),
        port: Int64(port),
      );
      final payload = bytesToBytesContainerPointer(req.writeToBuffer());
      lib.RPC(port, payload);
      freeBytesContainerPointer(payload);

      var creditsSent = 0;
      final out = applyBackpressure(
        raw.stream,
        const BackpressurePolicy.block(bufferSize: 1),
        onDemand: (credits) {
          creditsSent += credits;
          unawaited(_sendCredit(lib, port, credits));
        },
      );
      final items = await out
          .map((r) => r.rpcResponse.payload.single)
          .toList()
          .timeout(const Duration(seconds: 10));

      expect(items, [0, 1, 2, 3]);
      expect(creditsSent, greaterThan(0));
    });
  }, skip: _skipReason);
}

Future<void> _sendCredit(NativeLibrary lib, int streamPort, int credits) async {
  final payload = Uint8List(12);
  final view = ByteData.sublistView(payload);
  view.setInt64(0, streamPort, Endian.little);
  view.setInt32(8, credits, Endian.little);
  await _rpcAsync(
    lib,
    Request(rpcRequest: RpcRequest(path: kFlowCreditPath, payload: payload)),
  );
}

Future<Response> _rpcAsync(NativeLibrary lib, Request req) {
  final comp = Completer<Response>();
  final receive = ReceivePort();
  final nativePort = receive.sendPort.nativePort;
  req.port = Int64(nativePort);

  receive.listen((msg) {
    if (comp.isCompleted) {
      return;
    }
    try {
      comp.complete(responseFromPointerAddress(msg as int));
    } catch (e) {
      comp.completeError(e);
    }
    receive.close();
  });

  final payload = bytesToBytesContainerPointer(req.writeToBuffer());
  // The export parses the request synchronously, so Dart frees it immediately.
  lib.RPC(nativePort, payload);
  freeBytesContainerPointer(payload);

  return comp.future.timeout(const Duration(seconds: 10));
}

Response _rpcSync(NativeLibrary lib, Request req) {
  final payload = bytesToBytesContainerPointer(req.writeToBuffer());
  final respPtr = lib.CallSync(payload);
  freeBytesContainerPointer(payload);
  if (respPtr.address == 0) {
    throw StateError('CallSync returned a null response');
  }
  return responseFromPointerAddress(respPtr.address);
}
