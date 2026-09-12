package rpc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nosuta/godash/v2/pb"
	godashapp "godashapp/pb"
)

type EchoServer struct{}

func (s *EchoServer) Echo(ctx context.Context, req *godashapp.EchoRequest) (*godashapp.EchoResponse, error) {
	slog.Info("EchoServer.Echo called", "msg", req.Message)
	return &godashapp.EchoResponse{Message: req.Message}, nil
}

func (s *EchoServer) ServerStream(ctx context.Context, req *godashapp.EchoRequest, ch chan<- *pb.Response) error {
	slog.Info("EchoServer.ServerStream called", "msg", req.Message)
	for i := 0; i < 5; i++ {
		msg := fmt.Sprintf("Stream %d: %s", i, req.Message)
		slog.Debug("EchoServer.ServerStream: sending", "msg", msg)
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{
					Payload: pb.MarshalHelper(&godashapp.EchoResponse{Message: msg}),
				},
			},
		}
	}
	slog.Info("EchoServer.ServerStream finished")
	return nil
}
