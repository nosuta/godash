// Code as template. DO NOT EDIT.

import 'dart:async';
import 'dart:typed_data';

import 'package:godash/bridge/bridge.dart';
import 'package:godash/pb/core.pb.dart' as pb;

class Transport {
  final Bridge _bridge;
  Transport(): _bridge = Bridge();

  /// True when the platform supports the packed-struct hot path (native).
  /// On web the generated hot wrappers fall back to [unary].
  bool get supportsHotPath => _bridge.supportsHotPath;

  /// Invokes the native hot-path export [symbol] with a packed [request]
  /// buffer and returns [responseSize] packed response bytes.
  ///
  /// Blocks the platform thread; only used for short-lived unary handlers.
  Uint8List hotRaw(String symbol, Uint8List request, int responseSize) =>
      _bridge.hotRaw(symbol, request, responseSize);

  Future<O> unary<I extends Object, O extends Object>(
    String path,
    I input,
    O Function() outputFactory,
  ) async {
    final payload = (input as dynamic).writeToBuffer();
    final req = pb.Request(
      rpcRequest: pb.RpcRequest(path: path, payload: payload),
    );

    final resp = await _bridge.rpcUnary(req);
    if (resp.hasError()) {
      throw Exception('[${resp.error.code}] ${resp.error.message}');
    }
    if (!resp.hasRpcResponse()) {
      throw Exception('Missing RpcResponse');
    }

    final output = outputFactory();
    (output as dynamic).mergeFromBuffer(resp.rpcResponse.payload);
    return output;
  }

  Stream<O> stream<I extends Object, O extends Object>(
    String path,
    I input,
    O Function() outputFactory,
  ) async* {
    final payload = (input as dynamic).writeToBuffer();
    final req = pb.Request(
      rpcRequest: pb.RpcRequest(path: path, payload: payload),
    );

    final respStream = await _bridge.rpcStream(req);
    await for (final resp in respStream) {
      if (resp.hasError()) {
        throw Exception('[${resp.error.code}] ${resp.error.message}');
      }
      if (resp.hasRpcResponse()) {
        final output = outputFactory();
        (output as dynamic).mergeFromBuffer(resp.rpcResponse.payload);
        yield output;
      }
    }
  }
}
