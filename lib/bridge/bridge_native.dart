// Code as template. DO NOT EDIT.

import 'dart:async';
import 'dart:ffi';
import 'dart:io';
import 'dart:isolate';
import 'package:fixnum/fixnum.dart';
import 'package:godash/pb/core.pb.dart';
import 'package:flutter/foundation.dart';
import 'package:logging/logging.dart';
import 'package:path_provider/path_provider.dart';

import 'native_bytes.dart';
import 'native_library.g.dart';

/// Configuration for the native [Bridge].
/// Must be set via [Bridge.configure] before the first [Bridge] access.
class _BridgeConfig {
  final Future<String> Function() appEncryptionKey;
  final String? workerUrl;

  _BridgeConfig({
    required this.appEncryptionKey,
    this.workerUrl,
  });
}

class Bridge extends ChangeNotifier {
  static Bridge? _instance;
  static _BridgeConfig? _config;

  /// Configures the singleton bridge. Call once before using [Bridge].
  /// [workerUrl] is ignored on native platforms and only used on the web.
  static void configure({
    required Future<String> Function() appEncryptionKey,
    String? workerUrl,
  }) {
    _config = _BridgeConfig(
      appEncryptionKey: appEncryptionKey,
      workerUrl: workerUrl,
    );
  }

  Bridge._() {
    if (_config == null) {
      throw StateError(
        'Bridge not configured. Call Bridge.configure(...) before using Bridge().',
      );
    }
    _log.info('native bridge instantiate');
    // Response/push containers are allocated on the Go side; free them
    // through the Go-exported symbol (see dart_api/bridge.h).
    configureResponseContainerFree(_lib.FreeBytesContainer);
    _pushController = StreamController<Push>.broadcast();
    unawaited(_init());
  }
  factory Bridge() {
    _instance ??= Bridge._();
    return _instance!;
  }

  bool get ready => _ready;

  static const String _libName = String.fromEnvironment(
    'LIB_NAME',
    defaultValue: 'libflap',
  );
  final _log = Logger('Bridge Native');
  final _lib = NativeLibrary(_dylib());
  late final StreamController<Push> _pushController;
  bool _ready = false;
  bool _fatal = false;

  @override
  void dispose() {
    _pushController.close();
    super.dispose();
  }

  Stream<Push> get push => _pushController.stream;

  Future<void> _init() async {
    final ret = _lib.InitializeDartAPI(NativeApi.initializeApiDLData);
    if (ret != 0) {
      throw Exception(
        "failed to initialize Dart API due to a major version mismatch",
      );
    }
    final aek = await _config!.appEncryptionKey();
    final pushPort = ReceivePort();
    pushPort.listen(_pushListener);
    final tmpDir = await getTemporaryDirectory();
    final supportDir = await getApplicationSupportDirectory();
    final documentsDir = await getApplicationDocumentsDirectory();
    final req = Request(
      init: Init(
        pushPort: Int64(pushPort.sendPort.nativePort),
        tempDir: tmpDir.absolute.path,
        supportDir: supportDir.absolute.path,
        documentsDir: documentsDir.absolute.path,
        appEncryptionKey: aek,
      ),
    );
    final resp = await rpc(req);
    if (resp.hasError()) {
      _fatal = true;
      throw Exception(resp.error.message);
    }

    _ready = true;
    notifyListeners();
  }

  Future<void> _waitReady() async {
    const int waitMilliseconds = 100;
    const int waitCount = 1000;
    int count = 0;
    await Future.doWhile(() async {
      if (_fatal) {
        throw Exception('failed to launch root worker');
      }
      if (ready) {
        return false;
      }
      if (count > waitCount) {
        throw Exception(
          'worker timeout: ${waitCount * waitMilliseconds * 0.001}s',
        );
      }
      await Future.delayed(Duration(milliseconds: waitMilliseconds));
      count++;
      return true;
    });
  }

  Future<Response> rpc(Request req) async {
    final comp = Completer<Response>();
    final port = ReceivePort();
    final nativePort = port.sendPort.nativePort;
    _log.info('rpc: $nativePort');
    req.port = Int64(nativePort);

    port.listen((pointerAddr) {
      _log.info("rpc: receive pointer $pointerAddr on $nativePort");
      if (comp.isCompleted) {
        _log.severe('rpc: port $nativePort is used after completed');
        port.close();
        return;
      }
      try {
        final resp = pointerAddressToResponse(pointerAddr);
        if (resp.hasError()) {
          comp.completeError(
            'rpc: response error (${resp.error.code}) ${resp.error.message}',
          );
        } else {
          comp.complete(resp);
        }
      } catch (e) {
        comp.completeError(e);
      }
      _log.info('rpc: port $nativePort closed');
      port.close();
    });

    final buf = req.writeToBuffer();
    final payload = bytesToBytesContainerPointer(buf);
    // The RPC export copies the request synchronously before spawning its
    // goroutine, so the Dart-owned container can be freed right away.
    _lib.RPC(nativePort, payload);
    freeBytesContainerPointer(payload);

    return comp.future;
  }

  /// Synchronous unary fast path (PLAN.md P2).
  ///
  /// Blocks the calling (platform) thread until the Go handler returns — the
  /// goroutine, `ReceivePort` and port round trip of [rpc] are skipped
  /// entirely. Response containers are Go-allocated and freed through
  /// `FreeBytesContainer` after parsing (see [native_bytes.dart]).
  ///
  /// Contract: only short-lived handlers (e.g. short DB reads/writes) may be
  /// called this way. Long-running work must use the async [rpc] path or it
  /// will freeze the platform thread. Requires [ready]; callers normally use
  /// [rpcUnary], which waits for readiness first.
  Response rpcSync(Request req) {
    if (!ready) {
      throw StateError(
        'rpcSync requires a ready bridge; use rpc() before Init completes',
      );
    }
    final payload = bytesToBytesContainerPointer(req.writeToBuffer());
    final respPtr = _lib.CallSync(payload);
    freeBytesContainerPointer(payload);
    if (respPtr.address == nullptr.address) {
      throw Exception('CallSync returned a null response');
    }
    return responseFromPointerAddress(respPtr.address);
  }

  /// Unary RPC entry point used by [Transport.unary]: waits for readiness and
  /// then takes the sync fast path on native.
  Future<Response> rpcUnary(Request req) async {
    await _waitReady();
    return rpcSync(req);
  }

  Future<Stream<Response>> rpcStream(Request req) async {
    await _waitReady();

    final controller = StreamController<Response>();
    final port = ReceivePort();
    final nativePort = port.sendPort.nativePort;
    _log.info('rpc stream: $nativePort');
    req.port = Int64(nativePort);

    port.listen(
      (addr) {
        try {
          final resp = pointerAddressToResponse(addr);
          if (resp.hasError()) {
            _log.severe(
              'rpc stream: error on pointerAddressToResponse ${resp.error.message}',
              null,
              StackTrace.current,
            );
            return;
          }
          if (resp.hasDone()) {
            port.close();
            controller.close();
            _log.info('rpc stream: done $nativePort');
            return;
          }
          controller.sink.add(resp);
        } catch (e) {
          _log.warning('pointerAddressToResponse failed: $e');
        }
      },
      onError: (e) {
        _log.warning('rpc stream: listen error on $nativePort, $e');
        port.close();
        controller.close();
      },
      cancelOnError: true,
    );

    // controller.onListen = () {
    //   _log.info('rpc stream: on listen to $nativePort');
    // };

    controller.onCancel = () async {
      _log.info('rpc stream: on cancel $nativePort');
      final req = Request(cancel: Cancel(port: Int64(nativePort)));
      final resp = await rpc(req);
      if (resp.hasError()) {
        _log.severe('rpc stream: eror on cancel, ${resp.error.message}');
      }
    };

    final buf = req.writeToBuffer();
    final payload = bytesToBytesContainerPointer(buf);
    // The RPC export copies the request synchronously before spawning its
    // goroutine, so the Dart-owned container can be freed right away.
    _lib.RPC(nativePort, payload);
    freeBytesContainerPointer(payload);

    return controller.stream;
  }

  Response pointerAddressToResponse(dynamic pointerAddr) {
    if (pointerAddr is! int) {
      throw Exception('pointerAddr must be a pointer address');
    }
    return responseFromPointerAddress(pointerAddr);
  }

  Future<void> _pushListener(dynamic pointerAddr) async {
    if (pointerAddr is! int) {
      throw Exception('pointerAddr must be a pointer address');
    }
    final resp = responseFromPointerAddress(pointerAddr);
    if (!resp.hasPush()) {
      return;
    }
    _pushController.sink.add(resp.push);
  }

  /// Sends a ReverseResponse back to Go for a Go->Dart->Go ReverseService call.
  /// [reversePort] must match [Push.reversePort] from the incoming push.
  Future<void> sendReverseResponse(Int64 reversePort, List<int> payload) async {
    await _waitReady();
    final req = Request(
      reverseResponse: ReverseResponse(
        reversePort: reversePort,
        payload: payload,
      ),
    );
    final resp = await rpc(req);
    if (resp.hasError()) {
      _log.severe('sendReverseResponse error: ${resp.error.message}');
    }
  }

  String pointerAddressToString(int address) {
    return stringFromPointerAddress(address);
  }

  static DynamicLibrary _dylib() {
    if (Platform.isMacOS) {
      return DynamicLibrary.process();
    }
    if (Platform.isIOS) {
      return DynamicLibrary.process();
    }
    if (Platform.isAndroid) {
      return DynamicLibrary.open("$_libName.so");
    }
    throw UnsupportedError('Unsupported platform: ${Platform.operatingSystem}');
  }
}
