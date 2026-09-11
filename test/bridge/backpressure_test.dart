// Tests for the pure-Dart backpressure strategies (PLAN.md P5).
//
// The transformer lives above the transport and only sees a Stream<T>, so the
// exact same code path backs both the native FFI bridge and the web Worker
// bridge. These tests therefore cover the shared implementation used by both.
library;

import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:godash/bridge/backpressure.dart';

void main() {
  test('none is a pass-through', () async {
    final source = StreamController<int>();
    final got = <int>[];
    final sub = applyBackpressure(
      source.stream,
      const BackpressurePolicy(strategy: Backpressure.none),
    ).listen(got.add);
    await pumpEventQueue();
    source.add(1);
    source.add(2);
    await pumpEventQueue();
    expect(got, [1, 2]);
    await source.close();
    await sub.cancel();
  });

  test('dropLatest keeps only the most recent item while paused', () async {
    final source = StreamController<int>();
    final got = <int>[];
    final sub = applyBackpressure(
      source.stream,
      const BackpressurePolicy.dropLatest(),
    ).listen(got.add);
    await pumpEventQueue();

    sub.pause();
    for (var i = 0; i < 5; i++) {
      source.add(i);
    }
    await pumpEventQueue();
    expect(got, isEmpty, reason: 'nothing is delivered while paused');

    sub.resume();
    await pumpEventQueue();
    expect(got, [4], reason: 'stale items are dropped, only the latest survives');

    await source.close();
    await sub.cancel();
  });

  test('bufferDrop keeps a bounded ring buffer, dropping the oldest', () async {
    final source = StreamController<int>();
    final got = <int>[];
    final sub = applyBackpressure(
      source.stream,
      const BackpressurePolicy.bufferDrop(bufferSize: 2),
    ).listen(got.add);
    await pumpEventQueue();

    sub.pause();
    for (var i = 0; i < 4; i++) {
      source.add(i);
    }
    await pumpEventQueue();

    sub.resume();
    await pumpEventQueue();
    expect(got, [2, 3], reason: 'capacity 2: 0 and 1 are dropped');

    await source.close();
    await sub.cancel();
  });

  test('block is lossless and reports drained credits', () async {
    final source = StreamController<int>();
    final got = <int>[];
    final credits = <int>[];
    final sub = applyBackpressure(
      source.stream,
      const BackpressurePolicy.block(bufferSize: 2),
      onDemand: credits.add,
    ).listen(got.add);
    await pumpEventQueue();

    sub.pause();
    for (var i = 0; i < 4; i++) {
      source.add(i);
    }
    await pumpEventQueue();
    expect(got, isEmpty);

    sub.resume();
    await pumpEventQueue();
    expect(got, [0, 1, 2, 3], reason: 'block never drops');
    expect(credits.fold<int>(0, (a, b) => a + b), greaterThanOrEqualTo(2));

    await source.close();
    await sub.cancel();
  });

  test('batch flushes when batchMaxSize is reached', () async {
    final source = StreamController<int>();
    final got = <int>[];
    final sub = applyBackpressure(
      source.stream,
      const BackpressurePolicy.batch(
        batchMaxSize: 3,
        batchMaxDelay: Duration(seconds: 10),
      ),
    ).listen(got.add);
    await pumpEventQueue();

    source.add(1);
    source.add(2);
    await pumpEventQueue();
    expect(got, isEmpty, reason: 'below batchMaxSize and timer not fired');

    source.add(3);
    await pumpEventQueue();
    expect(got, [1, 2, 3]);

    await source.close();
    await sub.cancel();
  });

  test('batch flushes on batchMaxDelay for a partial batch', () async {
    final source = StreamController<int>();
    final got = <int>[];
    final sub = applyBackpressure(
      source.stream,
      const BackpressurePolicy.batch(
        batchMaxSize: 64,
        batchMaxDelay: Duration(milliseconds: 10),
      ),
    ).listen(got.add);
    await pumpEventQueue();

    source.add(7);
    await pumpEventQueue();
    expect(got, isEmpty);

    await Future<void>.delayed(const Duration(milliseconds: 30));
    await pumpEventQueue();
    expect(got, [7]);

    await source.close();
    await sub.cancel();
  });

  test('cancelling the subscription cancels the source', () async {
    final source = StreamController<int>();
    var sourceCancelled = false;
    source.onCancel = () => sourceCancelled = true;

    final out = applyBackpressure(
      source.stream,
      const BackpressurePolicy.bufferDrop(bufferSize: 4),
    );
    final sub = out.listen((_) {});
    await pumpEventQueue();
    await sub.cancel();
    await pumpEventQueue();
    expect(sourceCancelled, isTrue);
  });
}
