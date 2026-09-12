// Code as template. DO NOT EDIT.

import 'dart:js_interop';
import 'dart:js_interop_unsafe';
import 'dart:typed_data';

import 'package:web/web.dart' as web;

import 'package:godash/bridge/shared_ring.dart';

@JS('SharedArrayBuffer')
external JSFunction get _sharedArrayBufferConstructor;

@JS('Atomics')
extension type _Atomics._(JSObject _) implements JSObject {
  external int load(JSObject array, int index);
  external int sub(JSObject array, int index, int value);
}

@JS('Int32Array')
extension type _Int32View._(JSObject _) implements JSObject {
  external _Int32View(JSObject buffer, int byteOffset, int byteLength);
}

@JS('Uint8Array')
extension type _Uint8View._(JSObject _) implements JSObject {
  external _Uint8View(JSObject buffer, int byteOffset, int byteLength);
}

@JS('DataView')
extension type _DataView._(JSObject _) implements JSObject {
  external _DataView(JSObject buffer, int byteOffset, int byteLength);
  external int getInt32(int byteOffset, [bool littleEndian]);
}

final _Atomics _atomics = _Atomics._(
  globalContext.getProperty<JSObject>('Atomics'.toJS),
);

/// True when the page can allocate shared memory: it must be cross-origin
/// isolated (`Cross-Origin-Opener-Policy: same-origin` plus
/// `Cross-Origin-Embedder-Policy: require-corp`), and `SharedArrayBuffer` must
/// be exposed.
bool get sharedMemorySupported =>
    web.window.crossOriginIsolated &&
    globalContext.has('SharedArrayBuffer');

/// Allocates a shared ring buffer with [slots] frames of [slotBytes] each plus
/// the control region. [slots] and [slotBytes] are also sent to the worker.
JSObject createSharedRingBuffer({required int slots, required int slotBytes}) {
  final totalBytes = kSharedRingControlBytes + slots * slotBytes;
  return _sharedArrayBufferConstructor.callAsConstructor<JSObject>(
    totalBytes.toJS,
  );
}

/// Creates a [SharedRingReader] backed by a `SharedArrayBuffer`.
SharedRingReader createSharedRingReader(
  JSObject buffer, {
  required int slots,
  required int slotBytes,
}) {
  final totalBytes = kSharedRingControlBytes + slots * slotBytes;
  return SharedRingReader(
    memory: _JsSharedRingMemory(buffer, totalBytes: totalBytes),
    slots: slots,
    slotBytes: slotBytes,
  );
}

class _JsSharedRingMemory implements SharedRingMemory {
  _JsSharedRingMemory(this._buffer, {required int totalBytes})
      : _counter = _Int32View(_buffer, 0, 1),
        _data = _DataView(_buffer, 0, totalBytes);

  final JSObject _buffer;
  final _Int32View _counter;
  final _DataView _data;

  @override
  int loadUsed() => _atomics.load(_counter, 0);

  @override
  void consume() => _atomics.sub(_counter, 0, 1);

  @override
  int readInt32(int byteOffset) => _data.getInt32(byteOffset, true);

  @override
  Uint8List readBytes(int byteOffset, int length) =>
      (_Uint8View(_buffer, byteOffset, length) as JSUint8Array).toDart;
}
