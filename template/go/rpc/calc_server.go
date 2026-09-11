package rpc

import (
	"context"

	flap "flap/pb"
)

// CalcServer implements CalcRPCHandler. Add is a hot-path method: on native it
// is reached through the packed-struct FFI export (CalcService_Add); on web it
// goes through the normal protobuf envelope.
type CalcServer struct{}

func (s *CalcServer) Add(ctx context.Context, req *flap.AddRequest) (*flap.AddResponse, error) {
	return &flap.AddResponse{Sum: req.A + req.B}, nil
}
