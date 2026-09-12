//go:build !js

// Native benchmark backend: a c-shared library exposing the same FFI surface
// as the generated go/main.go (InitializeDartAPI + RPC), wired to the
// benchmark echo dispatch. Build and drive it with benchmark/native/bench.dart:
//
//	go build -buildmode=c-shared -o benchmark/native/libbench.dylib ./benchmark/native
//	dart run benchmark/native/bench.dart --n 10000
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct bytesContainer
{
    void *message;
    int size;
} BytesContainer;

extern void GoDash_FreeBytesContainer(void *ptr);
*/
import "C"
import (
	"context"
	"log/slog"
	"time"
	"unsafe"

	"github.com/nosuta/godash/v2/benchmark/bench"
	"github.com/nosuta/godash/v2/dart_api"
	"github.com/nosuta/godash/v2/pb"
	"github.com/nosuta/godash/v2/rpc"
)

func init() {
	bench.Install()
}

func main() {}

//export InitializeDartAPI
func InitializeDartAPI(api unsafe.Pointer) C.int64_t {
	// No pusher: the benchmark never pushes, but keep the contract warm.
	rpc.RPC().SetPusher(func(push *pb.Push, port int64) error { return nil })
	return C.int64_t(dart_api.InitializeDartAPI(api))
}

//export RPC
func RPC(port C.int64_t, payload *C.BytesContainer) {
	// Zero-copy request parse (PLAN.md P4): read the Dart-owned C buffer
	// directly. Never free it from Go (allocator contract); Dart frees it as
	// soon as this export returns, and UnmarshalVT copies the fields.
	req := &pb.Request{}
	if err := req.UnmarshalVT(unsafe.Slice((*byte)(payload.message), int(payload.size))); err != nil {
		e, _ := (&pb.Response{
			Responses: &pb.Response_Error{Error: &pb.Error{Message: err.Error()}},
		}).MarshalVT()
		addr := dart_api.BytesToPointerAddress(e)
		if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
			slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
		}
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10000)
		defer cancel()
		for ret := range rpc.RPC().Call(ctx, req) {
			addr := dart_api.BytesToPointerAddress(ret)
			if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
				slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
				break
			}
		}
		done, err := (&pb.Response{
			Responses: &pb.Response_Done{Done: &pb.Done{}},
		}).MarshalVT()
		if err != nil {
			return
		}
		addr := dart_api.BytesToPointerAddress(done)
		if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
			slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
		}
	}()
}

//export FreeBytesContainer
func FreeBytesContainer(payload *C.BytesContainer) {
	C.GoDash_FreeBytesContainer(unsafe.Pointer(payload))
}

//export CallSync
func CallSync(payload *C.BytesContainer) *C.BytesContainer {
	// Zero-copy: parse directly from the Dart-owned C buffer.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10000)
	defer cancel()

	rb, err := rpc.RPC().CallSync(ctx, unsafe.Slice((*byte)(payload.message), int(payload.size)))
	if err != nil {
		e, merr := (&pb.Response{
			Responses: &pb.Response_Error{
				Error: &pb.Error{Code: 500, Message: err.Error()},
			},
		}).MarshalVT()
		if merr != nil {
			slog.Error("failed to marshal sync error response", "error", merr.Error())
			return nil
		}
		rb = e
	}
	return (*C.BytesContainer)(dart_api.BytesToContainer(rb))
}

// BenchHotAdd is a packed-struct hot-path export mirroring the shape generated
// by protoc-gen-go-godash for a `(godash.hot)` method: an int64 request field
// and an int64 response field, no protobuf, no envelope, no port.
//
//export BenchHotAdd
func BenchHotAdd(req unsafe.Pointer, resp unsafe.Pointer) C.int32_t {
	*(*int64)(resp) = *(*int64)(req) + 1
	return 0
}
