// Code as template. DO NOT EDIT.

import 'dart:async';

import 'dart:js_interop';
import 'dart:js_interop_unsafe';
import 'package:fixnum/fixnum.dart';
import 'package:flutter/foundation.dart';
import 'package:web/web.dart' as web;
import 'package:logging/logging.dart';

import 'package:godash/pb/core.pb.dart';
import 'package:godash/bridge/backpressure.dart';

/// Configuration for the web [Bridge].
/// Must be set via [Bridge.configure] before the first [Bridge] access.
class _BridgeConfig {
  final Future<String> Function() appEncryptionKey;
  final String workerUrl;

  _BridgeConfig({
    required this.appEncryptionKey,
    required this.workerUrl,
  });
}

class Bridge extends ChangeNotifier {
  static Bridge? _instance;
  static _BridgeConfig? _config;

  /// Configures the singleton bridge. Call once before using [Bridge].
  static void configure({
    required Future<String> Function() appEncryptionKey,
    required String workerUrl,
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
    _log.info('web bridge instantiate');
    final workerUrl = _config!.workerUrl;
    _log.info('creating worker: $workerUrl');
    final options = {'type': 'classic'.toJS}.jsify() as web.WorkerOptions;
    final w = web.Worker(workerUrl.toJS, options);
    if (!w.isDefinedAndNotNull) {
      _log.severe('worker is not defined or null');
    }
    w.onmessage = _onGlobalMessage.toJS;
    w.onerror = ((web.Event event) {
      _log.shout('worker error: ${event.type}');
      _fatal = true;
    }).toJS;

    _worker = w;
    _pushController = StreamController<Push>.broadcast();
  }
  factory Bridge() {
    _instance ??= Bridge._();
    return _instance!;
  }

  bool get ready => _ready;
  Stream<Push> get push => _pushController.stream;

  final _log = Logger('Bridge Web');
  late final web.Worker _worker;
  late final StreamController<Push> _pushController;

  Int64 _port = Int64(0);
  bool _ready = false;
  bool _fatal = false;

  @override
  void dispose() {
    _pushController.close();
    _worker.terminate();
    super.dispose();
  }

  Int64 _nextPort() {
    return _port++;
  }

  /// Copies [bytes] into a fresh JS-owned ArrayBuffer so it can be transferred
  /// (zero-copy ownership transfer) to the Worker via postMessage.
  ///
  /// P4 evaluation (PLAN.md): this defensive copy is **kept**. Transferring a
  /// buffer hands ownership to the Worker, and the Dart GC is unaware of the
  /// transfer, so transferring a Dart-owned buffer can leave it retained. There
  /// is no way to prove the source is already JS-owned (`bytes.toJS` on a
  /// Dart-instantiated list is a cast whose backing ArrayBuffer is managed by
  /// the Dart/Flutter runtime), and the request side is not a measured
  /// bottleneck. The copy is deliberate, not a bug.
  ///
  /// Using [Uint8List.toJS] on a Dart-instantiated list returns a JSUint8Array
  /// whose backing ArrayBuffer is managed by the Dart/Flutter runtime.
  /// Transferring that buffer hands ownership to the Worker but the Dart GC
  /// is unaware of the transfer, which can leave the buffer retained.
  /// By allocating a new [JSArrayBuffer] explicitly and copying the bytes into
  /// it we get a purely JS-owned buffer that the browser can freely reclaim
  /// once the Worker consumes it.
  (JSUint8Array view, JSArrayBuffer ab) _toTransferableBuffer(List<int> bytes) {
    final ab = JSArrayBuffer(bytes.length);
    final view = JSUint8Array(ab);
    // Uint8Array.set(src) bulk-copies src into view in one JS call.
    // bytes.toJS on a Dart-instantiated Uint8List is a cast on JS targets,
    // but the data is copied into the new JS-owned `ab` here, so `ab` is
    // fully independent of Dart's heap and safe to transfer.
    final src = (bytes is Uint8List ? bytes : Uint8List.fromList(bytes)).toJS;
    view.callMethod('set'.toJS, src);
    return (view, ab);
  }

  void _onGlobalMessage(web.MessageEvent message) {
    _log.info('_onGlobalMessage: ${message.data}');
    if (message.isUndefinedOrNull || message.data.isUndefinedOrNull) {
      _log.shout('unsupported system (see browser logs)');
      _fatal = true;
      return;
    }
    final b = (message.data as JSUint8Array?)?.toDart;
    if (b == null) {
      _log.shout('missing message');
      _fatal = true;
      return;
    }
    final resp = Response.fromBuffer(b);
    if (resp.hasError()) {
      _log.shout('bridge global error: ${resp.error.message}');
      _fatal = true;
      return;
    }

    if (resp.hasDone()) {
      _config!.appEncryptionKey().then((key) {
        _log.info('app encryption key: $key');
        final req = Request(init: Init(appEncryptionKey: key));
        rpcUnsafe(req).then((resp) {
          _log.info('bridge global init response');
          if (resp.hasError()) {
            _log.shout('bridge global error: ${resp.error.message}');
            _fatal = true;
            return;
          }
          if (resp.hasDone()) {
            _log.info('worker is ready');
            _ready = true;
            notifyListeners();
            return;
          }
          _log.shout('unknown fatal situation');
          _fatal = true;
        });
      });
      return;
    } else if (resp.hasPush()) {
      _pushController.sink.add(resp.push);
      return;
    }
    _log.shout('unknown fatal situation');
    _fatal = true;
  }

  Future<void> _waitReady() async {
    const int waitMilliseconds = 100;
    const int waitCount = 100;
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

  Future<Response> rpcUnsafe(Request req) async {
    final comp = Completer<Response>();
    req.port = _nextPort();
    final ch = web.MessageChannel();

    ch.port1.onmessage = ((web.MessageEvent message) {
      if (comp.isCompleted) {
        _log.severe('port is used after completed: $req.port');
        ch.port2.close();
        ch.port1.close();
        return;
      }
      final b = (message.data as JSUint8Array?)?.toDart;
      if (b != null) {
        final resp = Response.fromBuffer(b);
        if (resp.hasError()) {
          comp.completeError(
            'response error (${resp.error.code}): ${resp.error.message}',
          );
        } else {
          comp.complete(resp);
        }
      } else {
        comp.completeError('rpc response data is null');
      }
      ch.port2.close();
      ch.port1.close();
    }).toJS;

    final (view, ab) = _toTransferableBuffer(req.writeToBuffer());
    final m = <JSObject>{ch.port2, view}.jsify() as JSArray;
    final t = <JSObject>{ch.port2, ab}.jsify() as JSArray;
    _log.info('rpc post message');
    _worker.postMessage(m, t);
    return comp.future;
  }

  Future<Response> rpc(Request req) async {
    await _waitReady();

    return await rpcUnsafe(req);
  }

  /// Unary RPC entry point used by [Transport.unary]. The synchronous FFI fast
  /// path does not exist on the web (everything is postMessage-based), so this
  /// is equivalent to [rpc]; the method keeps the Bridge interface shared
  /// between native and web.
  Future<Response> rpcUnary(Request req) => rpc(req);

  /// False on web: generated hot wrappers fall back to the protobuf envelope.
  bool get supportsHotPath => false;

  /// The packed-struct hot path is native-only.
  Uint8List hotRaw(String symbol, Uint8List request, int responseSize) {
    throw UnsupportedError('hot path is native-only ($symbol)');
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
    final resp = await rpcUnsafe(req);
    if (resp.hasError()) {
      _log.severe('sendReverseResponse error: ${resp.error.message}');
    }
  }

  Future<Stream<Response>> rpcStream(
    Request req, {
    BackpressurePolicy? backpressure,
  }) async {
    await _waitReady();

    final controller = StreamController<Response>();
    final port = _nextPort();
    _log.info('rpc stream: $port');
    req.port = port;
    final ch = web.MessageChannel();

    ch.port1.onmessage = ((web.MessageEvent message) {
      // log.info('prc stream: on message from $port');
      final b = (message.data as JSUint8Array?)?.toDart;
      if (b != null) {
        final resp = Response.fromBuffer(b);
        if (resp.hasError()) {
          _log.severe(resp.error.message, null, StackTrace.current);
          return;
        }
        if (resp.hasDone()) {
          ch.port2.close();
          ch.port1.close();
          controller.close();
          _log.info('rpc stream: done $port');
          return;
        }
        controller.sink.add(resp);
      }
    }).toJS;

    controller.onListen = () {
      _log.info('rpc stream: on listen to $port');
    };

    controller.onCancel = () async {
      _log.info('rpc stream: on cancel $port');
      // Close ports immediately to release MessageChannel resources,
      // regardless of whether Go sends Done.
      ch.port1.onmessage = null;
      ch.port2.close();
      ch.port1.close();
      final req = Request(cancel: Cancel(port: port));
      final resp = await rpcUnsafe(req);
      if (resp.hasError()) {
        _log.severe('rpc stream error on cancel: ${resp.error.message}');
      }
    };

    final (view, ab) = _toTransferableBuffer(req.writeToBuffer());
    final m = <JSObject>{ch.port2, view}.jsify() as JSArray;
    final t = <JSObject>{ch.port2, ab}.jsify() as JSArray;
    _log.info('rpc stream: post message to $port');
    _worker.postMessage(m, t);

    final policy = backpressure;
    if (policy == null || policy.strategy == Backpressure.none) {
      return controller.stream;
    }
    if (policy.strategy == Backpressure.block) {
      // The web worker runs the same Go dispatcher, so the credit control
      // request travels over the same envelope.
      unawaited(_sendFlowCredit(port, policy.bufferSize));
    }
    return applyBackpressure(
      controller.stream,
      policy,
      onDemand: policy.strategy == Backpressure.block
          ? (credits) => unawaited(_sendFlowCredit(port, credits))
          : null,
    );
  }

  /// Sends a block-strategy credit grant to the Go worker producer over the
  /// existing envelope (reserved [kFlowCreditPath]).
  Future<void> _sendFlowCredit(Int64 port, int credits) async {
    if (credits <= 0) {
      return;
    }
    final payload = Uint8List(12);
    final view = ByteData.sublistView(payload);
    view.setInt64(0, port.toInt(), Endian.little);
    view.setInt32(8, credits, Endian.little);
    try {
      await rpcUnsafe(
        Request(
          rpcRequest: RpcRequest(path: kFlowCreditPath, payload: payload),
        ),
      );
    } catch (e) {
      _log.warning('flow credit failed: $e');
    }
  }
}
