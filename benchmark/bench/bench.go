// Package bench provides the shared EchoService-style handlers used by the
// latency benchmark harness (benchmark/).
//
// The echo semantics mirror the template EchoServer.Echo at the envelope
// level: the RpcRequest payload is returned unchanged as an RpcResponse.
package bench

import (
	"context"
	"log/slog"

	"github.com/nosuta/godash/pb"
	"github.com/nosuta/godash/rpc"
)

// EchoPath is the unary echo RPC path handled by the benchmark.
const EchoPath = "/bench.EchoService/Echo"

// GatedStreamPath is a streaming RPC used to exercise block backpressure end to
// end. Its request payload is a single byte: the number of items to emit. Each
// item is emitted only after the handler acquires a credit from the flow gate
// (see rpc.FlowFromContext), so a client using Backpressure.block bounds how
// far ahead the producer can run.
const GatedStreamPath = "/bench.EchoService/GatedStream"

// Install wires the benchmark entry point and RPC dispatch into the rpc
// package. Call it before the first request arrives (package init). It also
// silences per-request info logging so it does not distort the numbers.
func Install() {
	slog.SetLogLoggerLevel(slog.LevelError)
	rpc.SetEntryPoint(func(databasePath, appEncryptionKey string) error {
		_ = databasePath
		_ = appEncryptionKey
		return nil
	})
	rpc.SetHandleRPC(handleRPC)
}

func handleRPC(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
	switch req.GetPath() {
	case EchoPath:
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: req.GetPayload()},
			},
		}
	case GatedStreamPath:
		count := 0
		if len(req.GetPayload()) > 0 {
			count = int(req.GetPayload()[0])
		}
		gate := rpc.FlowFromContext(ctx)
		for i := 0; i < count; i++ {
			if gate != nil {
				if err := gate.Acquire(ctx); err != nil {
					return
				}
			}
			ch <- &pb.Response{
				Responses: &pb.Response_RpcResponse{
					RpcResponse: &pb.RpcResponse{Payload: []byte{byte(i)}},
				},
			}
		}
	default:
		ch <- &pb.Response{
			Responses: &pb.Response_Error{
				Error: &pb.Error{Code: 404, Message: "RPC path not found"},
			},
		}
	}
}
