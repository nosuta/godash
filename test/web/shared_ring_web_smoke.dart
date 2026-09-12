// Smoke test for the Dart SharedArrayBuffer interop (PLAN.md P6).
//
// Flutter's test runner serves a page without COOP/COEP, so a browser test
// cannot allocate a SharedArrayBuffer there. Node always exposes it, so this
// program is compiled to JS and run under Node in CI:
//
//   dart compile js -o /tmp/shared_ring_web_smoke.js test/web/shared_ring_web_smoke.dart
//   node /tmp/shared_ring_web_smoke.js
//
// It writes frames exactly like web/shared.go (length prefix + payload, atomic
// frame count) and asserts SharedRingReader drains them in order through the
// real _JsSharedRingMemory. It complements web/web_sab_test.go (Go producer)
// and test/bridge/shared_ring_test.dart (pure reader logic).
library;

import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:godash/bridge/shared_ring.dart';
import 'package:godash/bridge/shared_ring_web.dart';

@JS('DataView')
extension type _DataView._(JSObject _) implements JSObject {
  external _DataView(JSObject buffer, int byteOffset, int byteLength);
  external void setInt32(int byteOffset, int value, [bool littleEndian]);
}

@JS('Int32Array')
extension type _Int32View._(JSObject _) implements JSObject {
  external _Int32View(JSObject buffer, int byteOffset, int byteLength);
}

@JS('Uint8Array')
extension type _Uint8View._(JSObject _) implements JSObject {
  external _Uint8View(JSObject buffer, int byteOffset, int byteLength);
  external void set(JSArray<JSNumber> source, int offset);
}

@JS('Atomics')
extension type _Atomics._(JSObject _) implements JSObject {
  external int add(JSObject array, int index, int value);
}

const int _slots = 2;
const int _slotBytes = 32;

late final _DataView _data;
late final _Uint8View _bytes;
late final _Int32View _counter;
late final _Atomics _atomics;
int _cursor = 0;

/// Publishes [frame] the way the Go worker does.
void _publish(List<int> frame) {
  final base = kSharedRingControlBytes + _cursor * _slotBytes;
  _data.setInt32(base, frame.length, true);
  _bytes.set(
    frame.map((e) => e.toJS).toList().toJS,
    base + kSharedRingFrameHeader,
  );
  _cursor = (_cursor + 1) % _slots;
  _atomics.add(_counter, 0, 1);
}

void _check(bool condition, String message) {
  if (!condition) {
    throw StateError(message);
  }
}

void main() {
  final total = kSharedRingControlBytes + _slots * _slotBytes;
  final buffer = createSharedRingBuffer(slots: _slots, slotBytes: _slotBytes);
  _data = _DataView(buffer, 0, total);
  _bytes = _Uint8View(buffer, 0, total);
  _counter = _Int32View(buffer, 0, 1);
  _atomics = _Atomics._(globalContext.getProperty<JSObject>('Atomics'.toJS));
  final reader = createSharedRingReader(
    buffer,
    slots: _slots,
    slotBytes: _slotBytes,
  );

  _publish([1, 2, 3]);
  _publish([4, 5]);
  var frames = reader.drain();
  _check(frames.length == 2, 'first drain count: ${frames.length}');
  _check('${frames[0]}' == '[1, 2, 3]', 'frame 0: ${frames[0]}');
  _check('${frames[1]}' == '[4, 5]', 'frame 1: ${frames[1]}');

  // The read cursor wraps back to slot 0.
  _publish([6, 7, 8, 9]);
  frames = reader.drain();
  _check(frames.length == 1, 'second drain count: ${frames.length}');
  _check('${frames[0]}' == '[6, 7, 8, 9]', 'frame 2: ${frames[0]}');

  print('shared_ring_web_smoke OK');
}
