// Code as template. DO NOT EDIT.

import 'dart:async';
import 'package:godash/bridge/bridge.dart';
import 'package:godash/pb/core.pb.dart' as pb;

class Transport {
  final Bridge _bridge;
  Transport(): _bridge = Bridge();

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
