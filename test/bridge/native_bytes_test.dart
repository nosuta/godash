// Tests for the pure FFI byte-conversion helpers extracted from the native
// bridge. These exercise the Dart<->Go memory contract without a native
// library: Dart allocates the request container, reads back response
// containers and frees the C heap exactly like the bridge does. The
// response-side free is wired to a Dart implementation mirroring the Go
// `FreeBytesContainer` export (which frees message + container in one call).
library;

import 'dart:convert';
import 'dart:ffi';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';
import 'package:fixnum/fixnum.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:godash/bridge/native_bytes.dart';
import 'package:godash/pb/core.pb.dart';
import 'package:godash/bridge/native_library.dart';

/// Allocates a BytesContainer via the helper under test (same shape the Go
/// side receives) and returns it plus its message bytes.
Pointer<BytesContainer> makeContainer(List<int> bytes) {
  return bytesToBytesContainerPointer(Uint8List.fromList(bytes));
}

/// Test stand-in for the Go-exported `FreeBytesContainer`: frees the message
/// and the container struct in one call (message read before the struct is
/// freed to avoid touching freed memory).
void freeContainer(Pointer<BytesContainer> c) {
  final message = c.ref.message;
  if (message.address != 0) {
    malloc.free(message.cast<Uint8>());
  }
  malloc.free(c);
}

void main() {
  setUp(() {
    configureResponseContainerFree(freeContainer);
  });

  test('bytesToBytesContainerPointer roundtrip', () {
    final bytes = Uint8List.fromList([1, 2, 3, 4, 5, 6, 7, 8]);
    final c = makeContainer(bytes);
    expect(c.ref.size, bytes.length);
    final back = c.ref.message.cast<Uint8>().asTypedList(bytes.length);
    expect(back, bytes);
    freeContainer(c);
  });

  test('bytesToBytesContainerPointer with empty payload', () {
    final c = makeContainer([]);
    expect(c.ref.size, 0);
    // The message pointer may be null for zero-length payloads; the Go side
    // only ever reads size bytes from it, so either value is safe as long as
    // reading is guarded by size == 0.
    if (c.ref.message.address != 0) {
      final back = c.ref.message.cast<Uint8>().asTypedList(0);
      expect(back, isEmpty);
    }
    freeContainer(c);
  });

  test('bytesToBytesContainerPointer with 1 MiB payload', () {
    final bytes = Uint8List.fromList(
      List.generate(1 << 20, (i) => i & 0xFF),
    );
    final c = makeContainer(bytes);
    expect(c.ref.size, bytes.length);
    final back = c.ref.message.cast<Uint8>().asTypedList(bytes.length);
    expect(back, bytes);
    freeContainer(c);
  });

  test('pointerAddressToBytes returns a zero-copy view', () {
    final bytes = Uint8List.fromList([9, 8, 7, 6, 5, 4, 3, 2, 1]);
    final c = makeContainer(bytes);
    final (got, container) = pointerAddressToBytes(c.address);
    expect(got, bytes);
    // Nothing is freed by the helper: the caller owns both the view (until
    // parse) and the container pointer (freed via the response free).
    expect(container.address, c.address);
    freeResponseContainer(container);
  });

  test('pointerAddressToBytes throws on null message', () {
    final c = calloc<BytesContainer>();
    c.ref.size = 0;
    c.ref.message = nullptr;
    // The helper frees the whole container (via the configured free) before
    // throwing.
    expect(() => pointerAddressToBytes(c.address), throwsException);
  });

  test('responseFromPointerAddress roundtrip (rpc_response)', () {
    final want = Response(
      rpcResponse: RpcResponse(payload: [1, 2, 3, 4, 5]),
    );
    final c = makeContainer(want.writeToBuffer());
    final got = responseFromPointerAddress(c.address);
    expect(got.hasRpcResponse(), isTrue);
    expect(got.rpcResponse.payload, want.rpcResponse.payload);
  });

  test('responseFromPointerAddress roundtrip (error)', () {
    final want = Response(
      error: Error(code: 404, message: 'RPC path not found'),
    );
    final c = makeContainer(want.writeToBuffer());
    final got = responseFromPointerAddress(c.address);
    expect(got.hasError(), isTrue);
    expect(got.error.code, 404);
    expect(got.error.message, 'RPC path not found');
  });

  test('responseFromPointerAddress roundtrip (push)', () {
    final want = Response(
      push: Push(
        type: 'tick',
        payload: [42],
        reversePort: Int64(777),
      ),
    );
    final c = makeContainer(want.writeToBuffer());
    final got = responseFromPointerAddress(c.address);
    expect(got.hasPush(), isTrue);
    expect(got.push.type, 'tick');
    expect(got.push.payload, want.push.payload);
    expect(got.push.reversePort, Int64(777));
  });

  test('stringFromPointerAddress roundtrip', () {
    for (final s in ['hello', '', 'こんにちは', '🙂👍']) {
      final c = makeContainer(utf8.encode(s));
      expect(stringFromPointerAddress(c.address), s);
    }
  });

  test('free failure propagates out of the parse helpers', () {
    configureResponseContainerFree(
      (c) => throw StateError('free not configured'),
    );
    final c = makeContainer(Response(done: Done()).writeToBuffer());
    expect(
      () => responseFromPointerAddress(c.address),
      throwsStateError,
    );
    // The container was not freed by the helper in this case.
    freeContainer(c);
    // Restore for other tests.
    configureResponseContainerFree(freeContainer);
  });
}
