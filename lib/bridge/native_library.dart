// Code as template. DO NOT EDIT.

/// Hand-written native transport bindings.
///
/// godash no longer uses ffigen. The native ABI surface is a small, stable set
/// of exports (`InitializeDartAPI`, `RPC`, `FreeBytesContainer`)
/// which are resolved at runtime with [DynamicLibrary.lookupFunction]. Generated
/// per-method hot-path exports are resolved dynamically in
/// `bridge_native.dart` (`hotRaw`), so no code generation step is required.
library;

import 'dart:ffi' as ffi;

/// Mirrors the C struct declared in the cgo preamble:
///
/// ```c
/// typedef struct bytesContainer { void *message; int size; } BytesContainer;
/// ```
final class bytesContainer extends ffi.Struct {
  external ffi.Pointer<ffi.Void> message;

  @ffi.Int()
  external int size;
}

typedef BytesContainer = bytesContainer;

typedef _InitializeDartAPINative = ffi.Int64 Function(ffi.Pointer<ffi.Void>);
typedef _InitializeDartAPIDart = int Function(ffi.Pointer<ffi.Void>);

typedef _RPCNative = ffi.Void Function(
  ffi.Int64,
  ffi.Pointer<BytesContainer>,
);
typedef _RPCDart = void Function(int, ffi.Pointer<BytesContainer>);

typedef _FreeBytesContainerNative = ffi.Void Function(
  ffi.Pointer<BytesContainer>,
);
typedef _FreeBytesContainerDart = void Function(
  ffi.Pointer<BytesContainer>,
);

/// Resolves the stable godash transport symbols from a [ffi.DynamicLibrary].
///
/// Symbols are looked up lazily on first use so a missing export surfaces as a
/// clear error at the call site rather than at construction time.
class NativeLibrary {
  NativeLibrary(this._lib);

  final ffi.DynamicLibrary _lib;

  /// The underlying library, for dynamically-resolved symbols such as the
  /// generated per-method hot-path exports.
  ffi.DynamicLibrary get handle => _lib;

  late final _InitializeDartAPI = _lib
      .lookupFunction<_InitializeDartAPINative, _InitializeDartAPIDart>(
        'InitializeDartAPI',
      );

  int InitializeDartAPI(ffi.Pointer<ffi.Void> api) => _InitializeDartAPI(api);

  late final _RPC = _lib.lookupFunction<_RPCNative, _RPCDart>('RPC');

  void RPC(int port, ffi.Pointer<BytesContainer> payload) => _RPC(port, payload);

  late final _FreeBytesContainer = _lib
      .lookupFunction<_FreeBytesContainerNative, _FreeBytesContainerDart>(
        'FreeBytesContainer',
      );

  void FreeBytesContainer(ffi.Pointer<BytesContainer> payload) =>
      _FreeBytesContainer(payload);
}
