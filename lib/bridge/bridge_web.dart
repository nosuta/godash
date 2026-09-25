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

  /// isConfigured reports whether [configure] has run. A frontend can check it
  /// before touching [Bridge] (and [onRestart]) without risking a StateError.
  static bool get isConfigured => _config != null;

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
    _pushController = StreamController<Push>.broadcast();

    _sharedMemorySlots = config.sharedMemorySlots;
    _sharedMemorySlotBytes = config.sharedMemorySlotBytes;
    _sharedMemoryEnabled = config.useSharedMemory &&
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
    _createWorker();
  }
  factory Bridge() {
    _instance ??= Bridge._();
    return _instance!;
  }

  /// Creates a worker and wires its handlers. Called once from the constructor
  /// and again on every auto-restart.
  ///
  /// A TinyGo release worker is built with `-panic=trap` and cannot recover a
  /// JS exception: a throw from a `syscall/js` call becomes a Go panic and
  /// traps the wasm instance, killing the worker. Rather than freezing the app
  /// on a dead RPC channel (GUI-34), the bridge terminates the dead worker and
  /// spawns a fresh one, then re-runs the global Init handshake. The Go side
  /// starts empty again but its persistent store survives, so the frontend can
  /// re-run its own Initialize after [onRestart] fires.
  void _createWorker() {
    final config = _config!;
    final workerUrl = config.workerUrl;
    _log.info('creating worker: $workerUrl');
    _dead = false;
    final options = {'type': 'classic'.toJS}.jsify() as web.WorkerOptions;
    final w = web.Worker(workerUrl.toJS, options);
    if (!w.isDefinedAndNotNull) {
      _log.severe('worker is not defined or null');
    }
    w.onmessage = _onGlobalMessage.toJS;
    w.onerror = ((web.Event event) {
      _log.shout('worker error: ${event.type}');
      _handleWorkerDeath('error event');
    }).toJS;
    _worker = w;
  }

  bool get ready => _ready;
  Stream<Push> get push => _pushController.stream;

  /// generation increments each time the worker is replaced and comes back
  /// ready. A frontend that keeps engine state in Dart should listen to
  /// [onRestart] (or watch [generation]) and re-run its own Initialize, because
  /// the new worker's Go process starts empty.
  int get generation => _generation;

  /// onRestart emits the new [generation] after an auto-restart completes. It
  /// does not fire for the initial worker (generation 0).
  Stream<int> get onRestart => _restartController.stream;

  /// fatal is true when the worker could not be (re)started at all. The bridge
  /// is unusable until the app is reloaded.
  bool get fatal => _fatal;

  final _log = Logger('Bridge Web');
  late web.Worker _worker;
  late final StreamController<Push> _pushController;

  Int64 _port = Int64(0);
  bool _ready = false;
  bool _fatal = false;
  bool _sharedMemoryEnabled = false;
  int _sharedMemorySlots = 0;
  int _sharedMemorySlotBytes = 0;
  // _dead guards duplicate death signals for the same worker; it resets when a
  // new worker is created. _restartAttempts caps the restart loop so a worker
  // that cannot start at all ends as fatal instead of spinning forever.
  bool _dead = false;
  bool _disposed = false;
  int _generation = 0;
  int _restartAttempts = 0;
  Timer? _restartTimer;
  // _pending tracks in-flight unary RPC completers so a worker death fails them
  // promptly instead of leaving the caller hanging on a dead MessageChannel.
  final Set<Completer<Response>> _pending = <Completer<Response>>{};
  // _streams tracks open server-streaming controllers so a worker death closes
  // them (the `await for` in the generated client then ends) rather than
  // hanging on a port that will never speak again.
  final Set<StreamController<Response>> _streams = <StreamController<Response>>{};
  final StreamController<int> _restartController =
      StreamController<int>.broadcast();

  /// How many times the bridge respawns a dead worker before giving up.
  static const int _maxRestartAttempts = 6;

  /// How long to wait before respawning. Fixed and short: a worker start is
  /// fast, and the total (attempts x delay) stays well under _waitReady's
  /// 10s budget.
  static const Duration _restartDelay = Duration(milliseconds: 500);

  /// True when streaming RPCs use the shared-memory ring (P6). Requires the
  /// page to be cross-origin isolated and the caller to opt in.
  bool get usesSharedMemory => _sharedMemoryEnabled;

  @override
  void dispose() {
    _disposed = true;
    _restartTimer?.cancel();
    _failPending('bridge disposed');
    _pushController.close();
    _restartController.close();
    _worker.terminate();
    super.dispose();
  }

  /// _handleWorkerDeath tears down a dead worker and schedules a fresh one. It
  /// is idempotent per worker: _dead blocks the duplicate signals a single trap
  /// can produce (an `onerror` plus the worker's `postMessage(undefined)`).
  void _handleWorkerDeath(String reason) {
    if (_disposed || _dead) return;
    _dead = true;
    _log.shout('worker died ($reason); restarting');
    _ready = false;
    _failPending('worker restarted');
    _closeStreams();
    try {
      _worker.terminate();
    } catch (e) {
      _log.warning('worker terminate failed: $e');
    }
    if (_restartAttempts >= _maxRestartAttempts) {
      _log.shout('worker restart attempts exhausted; bridge is fatal');
      _fatal = true;
      notifyListeners();
      return;
    }
    _restartAttempts++;
    _restartTimer?.cancel();
    _restartTimer = Timer(_restartDelay, () {
      if (_disposed) return;
      _log.info(
        'respawning worker (attempt $_restartAttempts/$_maxRestartAttempts)',
      );
      _createWorker();
    });
  }

  /// _failPending completes every in-flight unary RPC with an error so callers
  /// do not wait on a MessageChannel owned by the dead worker.
  void _failPending(String reason) {
    final pending = _pending.toList();
    _pending.clear();
    for (final comp in pending) {
      if (!comp.isCompleted) {
        comp.completeError(StateError(reason));
      }
    }
  }

  /// _closeStreams ends every open server-streaming controller. The dead
  /// worker cannot send a Done frame, so closing here is what unwinds the
  /// generated client's `await for`.
  void _closeStreams() {
    final streams = _streams.toList();
    _streams.clear();
    for (final controller in streams) {
      if (!controller.isClosed) {
        controller.close();
      }
    }
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
    _log.info('_onGlobalMessage: ${message.data}');
    // The worker posts `undefined` when its wasm instance traps or fails to
    // load (worker.js catches the `go.run` rejection). Treat it as a death so
    // the bridge can respawn rather than freezing (GUI-34).
    if (message.isUndefinedOrNull || message.data.isUndefinedOrNull) {
      _handleWorkerDeath('worker posted undefined');
      return;
    }
    final b = (message.data as JSUint8Array?)?.toDart;
    if (b == null) {
      _handleWorkerDeath('missing message');
      return;
    }
    final resp = Response.fromBuffer(b);
    if (resp.hasError()) {
      _log.shout('bridge global error: ${resp.error.message}');
      _fatal = true;
      notifyListeners();
      return;
    }

    if (resp.hasDone()) {
      _config!.appEncryptionKey().then((key) {
        _log.info('app encryption key: $key');
        final req = Request(init: Init(appEncryptionKey: key));
        // The handshake runs before the bridge is ready, so it bypasses the
        // readiness gate; an error is treated as another death so a worker
        // that cannot initialize is respawned (up to the attempt cap).
        rpcUnsafe(req, requireReady: false).then(
          (resp) {
            _log.info('bridge global init response');
            if (resp.hasError()) {
              _log.shout('bridge global error: ${resp.error.message}');
              _handleWorkerDeath('init error');
              return;
            }
            if (resp.hasDone()) {
              final restarted = _restartAttempts > 0;
              _log.info('worker is ready');
              _ready = true;
              _fatal = false;
              _restartAttempts = 0;
              if (restarted) {
                _generation++;
                _restartController.add(_generation);
              }
              notifyListeners();
              return;
            }
            _log.shout('unknown fatal situation');
            _fatal = true;
            notifyListeners();
          },
          onError: (Object error) {
            _log.shout('bridge init failed: $error');
            _handleWorkerDeath('init failed');
          },
        );
      });
      return;
    } else if (resp.hasPush()) {
      _pushController.sink.add(resp.push);
      return;
    }
    _log.shout('unknown fatal situation');
    _fatal = true;
    notifyListeners();
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

  Future<Response> rpcUnsafe(Request req, {bool requireReady = true}) async {
    if (_fatal) {
      throw StateError('bridge worker is not running');
    }
    if (requireReady && !_ready) {
      // The worker is dead and a respawn is pending. Fail fast so the caller
      // can retry after onRestart instead of hanging on a dead port.
      throw StateError('bridge worker is restarting');
    }
    final comp = Completer<Response>();
    _pending.add(comp);
    req.port = _nextPort();
    final ch = web.MessageChannel();

    ch.port1.onmessage = ((web.MessageEvent message) {
      if (comp.isCompleted) {
        _log.severe('port is used after completed: $req.port');
        ch.port2.close();
        ch.port1.close();
        return;
      }
      _pending.remove(comp);
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
    if (_fatal || !_ready) {
      throw StateError('bridge worker is restarting');
    }

    final controller = StreamController<Response>();
    _streams.add(controller);
    final port = _nextPort();
    _log.info('rpc stream: $port');
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
        ch.port2.close();
        ch.port1.close();
        _streams.remove(controller);
        controller.close();
        _log.info('rpc stream: done $port');
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
      _log.info('rpc stream: on listen to $port');
    };

    controller.onCancel = () async {
      _log.info('rpc stream: on cancel $port');
      _streams.remove(controller);
      // Close ports immediately to release MessageChannel resources,
      // regardless of whether Go sends Done.
      ch.port1.onmessage = null;
      ch.port2.close();
      ch.port1.close();
      final req = Request(cancel: Cancel(port: port));
      // After a worker restart there is nothing to cancel; do not let the
      // failure escape an onCancel callback.
      try {
        final resp = await rpcUnsafe(req);
        if (resp.hasError()) {
          _log.severe('rpc stream error on cancel: ${resp.error.message}');
        }
      } catch (e) {
        _log.info('rpc stream cancel skipped: $e');
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
