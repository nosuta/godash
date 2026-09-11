// Code as template. DO NOT EDIT.

/// Pure FFI byte-conversion helpers shared by the native [Bridge].
///
/// These helpers own the cross-heap memory contract between Dart and Go:
/// - Requests: Dart allocates the serialized envelope in a malloc'd
///   [BytesContainer] (see [bytesToBytesContainerPointer]) and frees it with
///   [freeBytesContainerPointer] right after the synchronous `RPC` export
///   returns (the Go side parses it zero-copy via `unsafe.Slice` before
///   spawning its goroutine).
/// - Responses/pushes: Go allocates via `BytesToPointerAddress`; Dart parses
///   directly from the `asTypedList` view (protobuf parsing copies the bytes
///   into the message object, so no defensive copy is needed) and then frees
///   the container *through the Go-exported `FreeBytesContainer` symbol*
///   (see [configureResponseContainerFree]) — no buffer is ever freed by a
///   different allocator than the one that allocated it.
library;

import 'dart:convert';
import 'dart:ffi';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';

import 'package:godash/pb/core.pb.dart';

import 'native_library.dart';

/// Allocates a [BytesContainer] on the C heap holding a copy of [bytes].
/// Dart owns this container and frees it via [freeBytesContainerPointer].
Pointer<BytesContainer> bytesToBytesContainerPointer(Uint8List bytes) {
  final n = bytes.length;
  final bytesHeap = malloc<Uint8>(n);
  bytesHeap.asTypedList(n).setRange(0, n, bytes);
  final payload = calloc<BytesContainer>()
    ..ref.size = n
    ..ref.message = bytesHeap.cast<Void>();
  return payload;
}

/// Frees a [BytesContainer] allocated by [bytesToBytesContainerPointer]
/// (Dart-owned request container). Must be called on the Dart side; the Go
/// side never frees request containers.
void freeBytesContainerPointer(Pointer<BytesContainer> payload) {
  final message = payload.ref.message;
  if (message.address != nullptr.address) {
    malloc.free(message);
  }
  malloc.free(payload);
}

/// Signature of the native free for Go-allocated response containers.
typedef ResponseContainerFree =
    void Function(Pointer<BytesContainer> container);

ResponseContainerFree _freeResponseContainer = _unconfiguredFree;

void _unconfiguredFree(Pointer<BytesContainer> container) {
  throw StateError(
    'response container free not configured; '
    'call configureResponseContainerFree(...) with the native '
    '`FreeBytesContainer` binding before parsing responses',
  );
}

/// Wires the Go-exported `FreeBytesContainer` symbol (which runs
/// `GoDash_FreeBytesContainer` in dart_api/bridge.c) into the response
/// helpers. The native bridge calls this once at startup; tests substitute
/// a Dart-side implementation matching their own allocations.
void configureResponseContainerFree(ResponseContainerFree free) {
  _freeResponseContainer = free;
}

/// Reads the [BytesContainer] at [address] as a Dart view over the Go-owned
/// C heap — zero copies.
///
/// Returns the message bytes as an `asTypedList` view plus the container
/// pointer. The view is only valid until [container] is freed via
/// [freeResponseContainer]; callers must parse the bytes (protobuf parsing
/// copies everything it keeps) before freeing.
(Uint8List bytes, Pointer<BytesContainer> container) pointerAddressToBytes(
  int address,
) {
  final p = Pointer<BytesContainer>.fromAddress(address);
  final pm = p.ref.message;
  if (pm.address == nullptr.address) {
    _freeResponseContainer(p);
    throw Exception('message.address is null');
  }
  final b = pm.cast<Uint8>().asTypedList(p.ref.size);
  return (b, p);
}

/// Frees a response container allocated on the Go side (message buffer +
/// container struct) through the Go-side allocator.
void freeResponseContainer(Pointer<BytesContainer> container) {
  _freeResponseContainer(container);
}

/// Parses a [Response] delivered as a pointer address and frees the C heap.
Response responseFromPointerAddress(int address) {
  final (b, container) = pointerAddressToBytes(address);
  final resp = Response.fromBuffer(b);
  freeResponseContainer(container);
  return resp;
}

/// Decodes a UTF-8 string delivered as a pointer address and frees the C heap.
String stringFromPointerAddress(int address) {
  final (b, container) = pointerAddressToBytes(address);
  final str = utf8.decode(b);
  freeResponseContainer(container);
  return str;
}
