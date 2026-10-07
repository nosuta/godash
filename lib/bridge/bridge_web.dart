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
import 'package:godash/bridge/shared_ring.dart';
import 'package:godash/bridge/shared_ring_web.dart';

/// Prefix the web worker's bootstrap (worker.js) uses to report a fatal worker
/// condition on the global postMessage channel. A release worker builds with
/// TinyGo -panic=trap, whose wasm target cannot recover a panic or a
/// syscall/js exception, so the Go program can stop without answering another
/// RPC. The report turns that silent death into [Bridge.fatal] plus failed
/// in-flight requests, so the frontend can prompt a reload instead of hanging.
const _kWorkerFatalPrefix = '__godash_worker_fatal__ ';

/// Configuration for the web [Bridge].
/// Must be set via [Bridge.configure] before the first [Bridge] access.
class _BridgeConfig {
  final Future<String> Function() appEncryptionKey;
  final String workerUrl;
  final bool useSharedMemory;
  final int sharedMemorySlots;
  final int sharedMemorySlotBytes;

  _BridgeConfig({
    required this.appEncryptionKey,
    required this.workerUrl,
    required this.useSharedMemory,
    required this.sharedMemorySlots,
    required this.sharedMemorySlotBytes,
  });
}

class Bridge extends ChangeNotifier {
  static Bridge? _instance;
  static _BridgeConfig? _config;

  /// Configures the singleton bridge. Call once before using [Bridge].
  ///
  /// [useSharedMemory] opts streaming RPCs into a `SharedArrayBuffer` ring
  /// (P6, PLAN.md). It only takes effect when the page is cross-origin isolated
  /// (`SharedArrayBuffer` available); otherwise the bridge logs a warning and
  /// keeps the transferable envelope. [sharedMemorySlots] and
  /// [sharedMemorySlotBytes] size the per-stream ring; frames that do not fit
  /// fall back to the envelope automatically.
  static void configure({
    required Future<String> Function() appEncryptionKey,
    required String workerUrl,
    bool useSharedMemory = false,
    int sharedMemorySlots = 4,
    int sharedMemorySlotBytes = 16 * 1024,
  }) {
    _config = _BridgeConfig(
      appEncryptionKey: appEncryptionKey,
      workerUrl: workerUrl,
      useSharedMemory: useSharedMemory,
      sharedMemorySlots: sharedMemorySlots,
      sharedMemorySlotBytes: sharedMemorySlotBytes,
    );
  }

  Bridge._() {
    if (_config == null) {
      throw StateError(
        'Bridge not configured. Call Bridge.configure(...) before using Bridge().',
      );
    }
    final config = _config!;
    _log.info('web bridge instantiate');
    final workerUrl = config.workerUrl;
    _log.info('creating worker: $workerUrl');
    final options = {'type': 'classic'.toJS}.jsify() as web.WorkerOptions;
    final w = web.Worker(workerUrl.toJS, options);
    if (!w.isDefinedAndNotNull) {
      _log.severe('worker is not defined or null');
    }
    w.onmessage = _onGlobalMessage.toJS;
    w.onerror = ((web.Event event) {
      _markFatal('worker error: ${event.type}');
    }).toJS;

    _worker = w;
    _pushController = StreamController<Push>.broadcast();

    _sharedMemorySlots = config.sharedMemorySlots;
    _sharedMemorySlotBytes = config.sharedMemorySlotBytes;
    _sharedMemoryEnabled =
        config.useSharedMemory &&
        sharedMemorySupported &&
        config.sharedMemorySlots > 0 &&
        config.sharedMemorySlotBytes > kSharedRingFrameHeader &&
        config.sharedMemorySlotBytes % 4 == 0;
    if (config.useSharedMemory && !sharedMemorySupported) {
      _log.warning(
        'shared memory requested but SharedArrayBuffer is unavailable '
        '(page is not cross-origin isolated); using the envelope',
      );
    }
  }
  factory Bridge() {
    _instance ??= Bridge._();
    return _instance!;
  }

  bool get ready => _ready;
  Stream<Push> get push => _pushController.stream;

  /// True once the worker has died unrecoverably: a Go panic/exited main
  /// goroutine, a syscall/js trap, or a worker `error` event. Once fatal, every
  /// RPC fails immediately and in-flight requests are completed with an error,
  /// so callers never wait forever on a dead worker.
  bool get fatal => _fatal;

  /// Human-readable reason for [fatal], when the worker reported one.
  String? get fatalReason => _fatalReason;

  final _log = Logger('Bridge Web');
  late final web.Worker _worker;
  late final StreamController<Push> _pushController;

  Int64 _port = Int64(0);
  bool _ready = false;
  bool _fatal = false;
  String? _fatalReason;
  bool _sharedMemoryEnabled = false;
  int _sharedMemorySlots = 0;
  int _sharedMemorySlotBytes = 0;

  /// In-flight unary completers and streaming controllers, failed when the
  /// worker dies so a dead worker cannot leave callers awaiting forever.
  final Set<Completer<Response>> _pendingUnary = {};
  final Set<StreamController<Response>> _pendingStreams = {};

  /// True when streaming RPCs use the shared-memory ring (P6). Requires the
  /// page to be cross-origin isolated and the caller to opt in.
  bool get usesSharedMemory => _sharedMemoryEnabled;

  @override
  void dispose() {
    _pushController.close();
    _worker.terminate();
    super.dispose();
  }

  /// Transitions the bridge to its fatal state: logs the reason, completes
  /// every in-flight unary request and stream with an error, and notifies
  /// listeners so the app can surface a reload prompt. Idempotent: the first
  /// reason wins, so a later generic report does not mask the specific one.
  void _markFatal(String reason) {
    if (_fatal) {
      return;
    }
    _fatal = true;
    _fatalReason = reason;
    _log.shout('worker fatal: $reason');
    final pending = List<Completer<Response>>.of(_pendingUnary);
    _pendingUnary.clear();
    for (final comp in pending) {
      if (!comp.isCompleted) {
        comp.completeError(StateError('worker fatal: $reason'));
      }
    }
    final streams = List<StreamController<Response>>.of(_pendingStreams);
    _pendingStreams.clear();
    for (final controller in streams) {
      if (!controller.isClosed) {
        controller.addError(StateError('worker fatal: $reason'));
        controller.close();
      }
    }
    notifyListeners();
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

  /// Builds the worker message and its transferable list. The request buffer is
  /// transferred (ownership moves to the Worker); an optional [sharedBuffer] is
  /// referenced, never transferred, so both sides keep their views.
  (JSArray<JSAny?> message, JSArray<JSAny?> transfer) _buildWorkerMessage(
    web.MessagePort port,
    JSUint8Array view,
    JSArrayBuffer buffer, {
    JSObject? sharedBuffer,
    int slots = 0,
    int slotBytes = 0,
  }) {
    final message = JSArray<JSAny?>()
      ..add(port)
      ..add(view);
    if (sharedBuffer != null) {
      message
        ..add(sharedBuffer)
        ..add(slots.toJS)
        ..add(slotBytes.toJS);
    }
    final transfer = JSArray<JSAny?>()
      ..add(port)
      ..add(buffer);
    return (message, transfer);
  }

  void _onGlobalMessage(web.MessageEvent message) {
    // Never log message.data here: a Go->Dart reverse call (for example a
    // NIP-07 Nip44Decrypt payload) can be several KB, and stringifying both it
    // and its transferable buffer on every push flooded the release main thread
    // and starved the UI. Diagnostic only, so it lives at config (shown in
    // debug builds, suppressed at the INFO release level).
    _log.config('_onGlobalMessage');
    // The worker's JS bootstrap reports a death (panic, trap, unhandled
    // rejection, failed import) as a plain prefixed string on this channel.
    if (!message.isUndefinedOrNull && message.data.isA<JSString>()) {
      final text = (message.data as JSString).toDart;
      if (text.startsWith(_kWorkerFatalPrefix)) {
        _markFatal(text.substring(_kWorkerFatalPrefix.length));
      } else {
        _markFatal('unexpected worker message: $text');
      }
      return;
    }
    if (message.isUndefinedOrNull || message.data.isUndefinedOrNull) {
      _markFatal('unsupported system (see browser logs)');
      return;
    }
    final b = (message.data as JSUint8Array?)?.toDart;
    if (b == null) {
      _markFatal('missing message');
      return;
    }
    final resp = Response.fromBuffer(b);
    if (resp.hasError()) {
      _markFatal('bridge global error: ${resp.error.message}');
      return;
    }

    if (resp.hasDone()) {
      _config!.appEncryptionKey().then((key) {
        _log.info('app encryption key: $key');
        final req = Request(init: Init(appEncryptionKey: key));
        rpcUnsafe(req).then((resp) {
          _log.info('bridge global init response');
          if (resp.hasError()) {
            _markFatal('bridge global error: ${resp.error.message}');
            return;
          }
          if (resp.hasDone()) {
            _log.info('worker is ready');
            _ready = true;
            notifyListeners();
            return;
          }
          _markFatal('unknown fatal situation');
        });
      });
      return;
    } else if (resp.hasPush()) {
      _pushController.sink.add(resp.push);
      return;
    }
    _markFatal('unknown fatal situation');
  }

  Future<void> _waitReady() async {
    const int waitMilliseconds = 100;
    const int waitCount = 100;
    int count = 0;
    await Future.doWhile(() async {
      if (_fatal) {
        throw StateError('worker fatal: ${_fatalReason ?? 'unknown'}');
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
    if (_fatal) {
      throw StateError('worker fatal: ${_fatalReason ?? 'unknown'}');
    }
    final comp = Completer<Response>();
    _pendingUnary.add(comp);
    req.port = _nextPort();
    final ch = web.MessageChannel();

    ch.port1.onmessage = ((web.MessageEvent message) {
      _pendingUnary.remove(comp);
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
    final (m, t) = _buildWorkerMessage(ch.port2, view, ab);
    _log.config('rpc post message');
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
    _pendingStreams.add(controller);
    final port = _nextPort();
    _log.config('rpc stream: $port');
    req.port = port;
    final ch = web.MessageChannel();

    // P6: when opted in and cross-origin isolated, the worker publishes
    // streaming frames into a shared ring and only signals with a bare number.
    SharedRingReader? ring;
    JSObject? sharedBuffer;
    if (_sharedMemoryEnabled) {
      sharedBuffer = createSharedRingBuffer(
        slots: _sharedMemorySlots,
        slotBytes: _sharedMemorySlotBytes,
      );
      ring = createSharedRingReader(
        sharedBuffer,
        slots: _sharedMemorySlots,
        slotBytes: _sharedMemorySlotBytes,
      );
    }

    void handle(Response resp) {
      if (resp.hasError()) {
        _log.severe(resp.error.message, null, StackTrace.current);
        return;
      }
      if (resp.hasDone()) {
        _pendingStreams.remove(controller);
        ch.port2.close();
        ch.port1.close();
        controller.close();
        _log.config('rpc stream: done $port');
        return;
      }
      controller.sink.add(resp);
    }

    ch.port1.onmessage = ((web.MessageEvent message) {
      // log.info('prc stream: on message from $port');
      if (ring != null && message.data.isA<JSNumber>()) {
        for (final frame in ring.drain()) {
          handle(Response.fromBuffer(frame));
        }
        return;
      }
      final b = (message.data as JSUint8Array?)?.toDart;
      if (b != null) {
        handle(Response.fromBuffer(b));
      }
    }).toJS;

    controller.onListen = () {
      _log.config('rpc stream: on listen to $port');
    };

    controller.onCancel = () async {
      _log.config('rpc stream: on cancel $port');
      _pendingStreams.remove(controller);
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
    final (m, t) = _buildWorkerMessage(
      ch.port2,
      view,
      ab,
      sharedBuffer: sharedBuffer,
      slots: _sharedMemorySlots,
      slotBytes: _sharedMemorySlotBytes,
    );
    _log.config('rpc stream: post message to $port');
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
