// Tests for the shared-memory ring reader (PLAN.md P6).
//
// The reader logic is transport-independent pure Dart; the web implementation
// only supplies a SharedArrayBuffer-backed SharedRingMemory. These tests drive
// it with an in-memory fake that writes frames exactly like web/shared.go.
library;

import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:godash/bridge/shared_ring.dart';

/// Writes frames into a byte buffer using the same layout as the Go worker.
class FakeRingMemory implements SharedRingMemory {
  FakeRingMemory({required this.slots, required this.slotBytes})
      : bytes = Uint8List(kSharedRingControlBytes + slots * slotBytes),
        _data = ByteData(kSharedRingControlBytes + slots * slotBytes);

  final int slots;
  final int slotBytes;
  final Uint8List bytes;
  final ByteData _data;

  int used = 0;
  int cursor = 0;

  /// Publishes [frame] as the producer would, assuming the ring has room.
  void publish(List<int> frame) {
    final base = kSharedRingControlBytes + cursor * slotBytes;
    _data.setInt32(base, frame.length, Endian.little);
    bytes.setRange(
      base + kSharedRingFrameHeader,
      base + kSharedRingFrameHeader + frame.length,
      frame,
    );
    cursor = (cursor + 1) % slots;
    used++;
  }

  @override
  int loadUsed() => used;

  @override
  void consume() {
    expect(used, greaterThan(0));
    used--;
  }

  @override
  int readInt32(int byteOffset) => _data.getInt32(byteOffset, Endian.little);

  @override
  Uint8List readBytes(int byteOffset, int length) =>
      Uint8List.fromList(bytes.sublist(byteOffset, byteOffset + length));
}

void main() {
  test('drains published frames in order', () {
    final memory = FakeRingMemory(slots: 4, slotBytes: 64);
    memory.publish([1, 2, 3]);
    memory.publish([4, 5]);
    memory.publish([6]);

    final reader = SharedRingReader(memory: memory, slots: 4, slotBytes: 64);
    final frames = reader.drain();

    expect(frames, [
      [1, 2, 3],
      [4, 5],
      [6],
    ]);
    expect(memory.used, 0);
  });

  test('wraps around the slot ring', () {
    final memory = FakeRingMemory(slots: 2, slotBytes: 32);
    final reader = SharedRingReader(memory: memory, slots: 2, slotBytes: 32);

    memory.publish([10]);
    expect(reader.drain(), [
      [10],
    ]);

    memory.publish([20]);
    memory.publish([30]);
    expect(reader.drain(), [
      [20],
      [30],
    ]);

    // Cursor wrapped back to slot 0.
    memory.publish([40]);
    expect(reader.drain(), [
      [40],
    ]);
    expect(memory.used, 0);
  });

  test('drain with an empty ring returns nothing', () {
    final memory = FakeRingMemory(slots: 2, slotBytes: 32);
    final reader = SharedRingReader(memory: memory, slots: 2, slotBytes: 32);
    expect(reader.drain(), isEmpty);
  });

  test('drain snapshots the pending count and leaves later frames queued', () {
    final memory = FakeRingMemory(slots: 4, slotBytes: 32);
    memory.publish([1]);
    memory.publish([2]);

    final reader = SharedRingReader(memory: memory, slots: 4, slotBytes: 32);
    expect(reader.drain(), [
      [1],
      [2],
    ]);

    memory.publish([3]);
    expect(reader.drain(), [
      [3],
    ]);
  });
}
