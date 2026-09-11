// Code as template. DO NOT EDIT.

/// Pure-Dart stream backpressure strategies (PLAN.md P5).
///
/// These strategies live above the transport, so they apply identically to the
/// native FFI bridge and the web Worker bridge: the transformer only sees a
/// `Stream<T>`, never a transport. They bound how much a fast producer can
/// buffer ahead of a slow consumer.
///
/// The names mirror nitro's `Backpressure` enum:
///
/// - [Backpressure.dropLatest] — conflate: keep only the most recent item,
///   dropping stale pending items (best for sensors/camera frames).
/// - [Backpressure.bufferDrop] — bounded ring buffer; the oldest item is
///   dropped when the buffer is full.
/// - [Backpressure.block] — bounded buffer with true backpressure: the source
///   subscription is paused when the buffer is full, so nothing is dropped. An
///   optional [onDemand] callback reports drained slots so a cooperative Go
///   producer can be credit-gated (see `rpc.FlowGate`).
/// - [Backpressure.batch] — coalesce items and deliver them in a burst when
///   [BackpressurePolicy.batchMaxSize] is reached or
///   [BackpressurePolicy.batchMaxDelay] elapses.
library;

import 'dart:async';
import 'dart:collection';

/// Reserved `RpcRequest` path used by [Backpressure.block] to grant the Go
/// producer credits. It travels over the existing envelope (no wire-format
/// change) and is intercepted by the Go dispatcher before reaching the
/// application handler. Must match `rpc.FlowCreditPath` on the Go side.
const String kFlowCreditPath = '/godash.flow/Credit';

/// Backpressure strategy applied to a stream.
enum Backpressure {
  /// No buffering policy: pass the source through unchanged.
  none,

  /// Conflate — always deliver the most recent item, drop stale ones.
  dropLatest,

  /// Bounded ring buffer; drop the oldest item on overflow.
  bufferDrop,

  /// Bounded buffer; pause the source instead of dropping (lossless).
  block,

  /// Coalesce up to [BackpressurePolicy.batchMaxSize] items per burst.
  batch,
}

/// Configuration for [applyBackpressure].
class BackpressurePolicy {
  const BackpressurePolicy({
    required this.strategy,
    this.bufferSize = 64,
    this.batchMaxSize = 64,
    this.batchMaxDelay = const Duration(milliseconds: 10),
  });

  /// Conflate (keep the latest).
  const BackpressurePolicy.dropLatest({this.bufferSize = 1})
    : strategy = Backpressure.dropLatest,
      batchMaxSize = 1,
      batchMaxDelay = Duration.zero;

  /// Bounded ring buffer that drops the oldest item on overflow.
  const BackpressurePolicy.bufferDrop({this.bufferSize = 64})
    : strategy = Backpressure.bufferDrop,
      batchMaxSize = 1,
      batchMaxDelay = Duration.zero;

  /// Lossless bounded buffer that pauses the producer when full.
  const BackpressurePolicy.block({this.bufferSize = 64})
    : strategy = Backpressure.block,
      batchMaxSize = 1,
      batchMaxDelay = Duration.zero;

  /// Coalesce up to [batchMaxSize] items or [batchMaxDelay] before delivering.
  const BackpressurePolicy.batch({
    this.batchMaxSize = 64,
    this.batchMaxDelay = const Duration(milliseconds: 10),
  }) : strategy = Backpressure.batch,
       bufferSize = 64;

  final Backpressure strategy;

  /// Capacity for [Backpressure.bufferDrop] and [Backpressure.block].
  final int bufferSize;

  /// Maximum items coalesced per burst for [Backpressure.batch].
  final int batchMaxSize;

  /// Maximum time to hold a partial batch before flushing (batch only).
  final Duration batchMaxDelay;
}

/// Wraps [source] with the buffering policy in [policy].
///
/// [onDemand] is only invoked for [Backpressure.block]: it reports how many
/// buffers slots were drained, so a cooperative producer can be granted that
/// many credits. Passing `null` keeps the block strategy purely Dart-side.
Stream<T> applyBackpressure<T>(
  Stream<T> source,
  BackpressurePolicy policy, {
  void Function(int credits)? onDemand,
}) {
  if (policy.strategy == Backpressure.none) {
    return source;
  }
  return _BackpressureTransformer<T>(source, policy, onDemand).stream;
}

class _BackpressureTransformer<T> {
  _BackpressureTransformer(this._source, this._policy, this._onDemand) {
    _controller = StreamController<T>(
      onListen: _onListen,
      onPause: () {},
      onResume: _pump,
      onCancel: _onCancel,
    );
  }

  final Stream<T> _source;
  final BackpressurePolicy _policy;
  final void Function(int credits)? _onDemand;

  final Queue<T> _pending = Queue<T>();
  late final StreamController<T> _controller;
  StreamSubscription<T>? _sub;
  Timer? _flushTimer;

  Stream<T> get stream => _controller.stream;

  void _onListen() {
    _sub = _source.listen(
      _onData,
      onError: _controller.addError,
      onDone: _onDone,
      cancelOnError: false,
    );
  }

  void _onData(T value) {
    switch (_policy.strategy) {
      case Backpressure.none:
        _pump();
      case Backpressure.dropLatest:
        _pending.clear();
        _pending.add(value);
        _pump();
      case Backpressure.bufferDrop:
        if (_pending.length >= _policy.bufferSize) {
          _pending.removeFirst();
        }
        _pending.add(value);
        _pump();
      case Backpressure.block:
        _pending.add(value);
        if (_pending.length >= _policy.bufferSize && !(_sub?.isPaused ?? false)) {
          _sub!.pause();
        }
        _pump();
      case Backpressure.batch:
        _pending.add(value);
        if (_pending.length >= _policy.batchMaxSize) {
          _flushTimer?.cancel();
          _flushTimer = null;
          _pump();
        } else {
          _flushTimer ??= Timer(_policy.batchMaxDelay, () {
            _flushTimer = null;
            _pump();
          });
        }
    }
  }

  void _pump() {
    var drained = 0;
    while (_pending.isNotEmpty && !_controller.isPaused) {
      _controller.add(_pending.removeFirst());
      drained++;
    }
    if (_policy.strategy == Backpressure.block) {
      if (drained > 0) {
        _onDemand?.call(drained);
      }
      if ((_sub?.isPaused ?? false) && _pending.length < _policy.bufferSize) {
        _sub!.resume();
      }
    }
  }

  void _onDone() {
    _flushTimer?.cancel();
    _flushTimer = null;
    // Deliver whatever is still pending before closing.
    while (_pending.isNotEmpty) {
      _controller.add(_pending.removeFirst());
    }
    _controller.close();
  }

  Future<void> _onCancel() async {
    _flushTimer?.cancel();
    _flushTimer = null;
    await _sub?.cancel();
  }
}
