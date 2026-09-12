// Code as template. DO NOT EDIT.

import 'dart:typed_data';

/// Bytes reserved for the shared ring control region. Must match
/// `sharedPayloadBase` in `web/shared.go`.
const int kSharedRingControlBytes = 16;

/// Bytes of length prefix in front of every frame. Must match
/// `sharedFrameHeader` in `web/shared.go`.
const int kSharedRingFrameHeader = 4;

/// Backing storage for a [SharedRingReader]: an atomically-managed frame
/// counter plus byte-addressable shared memory.
///
/// The web implementation wraps a `SharedArrayBuffer`; tests use an in-memory
/// fake, which keeps the ring arithmetic unit-testable without a browser.
abstract interface class SharedRingMemory {
  /// Atomically loads the number of frames the consumer has not read yet.
  int loadUsed();

  /// Atomically decrements the unread frame count by one.
  void consume();

  /// Reads the little-endian int32 frame length at [byteOffset].
  int readInt32(int byteOffset);

  /// Copies [length] bytes starting at [byteOffset] into Dart memory.
  Uint8List readBytes(int byteOffset, int length);
}

/// Reads framed responses published by the Go worker into a
/// single-producer/single-consumer shared ring.
///
/// The producer (Go) owns the write cursor and the consumer (Dart) owns the
/// read cursor, so the only shared state is the atomic frame count. Frames too
/// large for a slot, and frames published while the ring is full, fall back to
/// the transferable envelope on the same `MessagePort`; the worker emits
/// exactly one message per frame, in order, so draining only on numeric signals
/// preserves ordering with those fallbacks.
class SharedRingReader {
  SharedRingReader({
    required this.memory,
    required this.slots,
    required this.slotBytes,
    this.payloadBase = kSharedRingControlBytes,
  })  : assert(slots > 0),
        assert(slotBytes > kSharedRingFrameHeader),
        assert(payloadBase % 4 == 0);

  final SharedRingMemory memory;
  final int slots;
  final int slotBytes;
  final int payloadBase;

  int _readCursor = 0;

  /// Drains all frames currently published, in order.
  List<Uint8List> drain() {
    final pending = memory.loadUsed();
    if (pending <= 0) {
      return const [];
    }
    final frames = <Uint8List>[];
    for (var i = 0; i < pending; i++) {
      final base = payloadBase + _readCursor * slotBytes;
      final length = memory.readInt32(base);
      frames.add(memory.readBytes(base + kSharedRingFrameHeader, length));
      _readCursor = (_readCursor + 1) % slots;
      memory.consume();
    }
    return frames;
  }
}
