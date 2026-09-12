package rpc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nosuta/godash/pb"
	flap "flap/pb"
)

type EchoServer struct{}

func (s *EchoServer) Echo(ctx context.Context, req *flap.EchoRequest) (*flap.EchoResponse, error) {
	slog.Info("EchoServer.Echo called", "msg", req.Message)
	return &flap.EchoResponse{Message: req.Message}, nil
}

func (s *EchoServer) ServerStream(ctx context.Context, req *flap.EchoRequest, ch chan<- *pb.Response) error {
	slog.Info("EchoServer.ServerStream called", "msg", req.Message)
	for i := 0; i < 5; i++ {
		msg := fmt.Sprintf("Stream %d: %s", i, req.Message)
		slog.Debug("EchoServer.ServerStream: sending", "msg", msg)
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{
					Payload: pb.MarshalHelper(&flap.EchoResponse{Message: msg}),
				},
			},
		}
	}
	slog.Info("EchoServer.ServerStream finished")
	return nil
}
