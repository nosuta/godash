// Package web provides the Web Worker entry point for godash.

//go:build js

package web

import (
	"context"
	"log/slog"
	"syscall/js"
	"time"

	"github.com/nosuta/godash/pb"
	"github.com/nosuta/godash/rpc"
)

// RunWebWorker installs the message handler, wires the pusher, and blocks forever.
// It is intended to be called from the main function of a Go/TinyGo WASM build.
func RunWebWorker() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("RunWebWorker recovered from panic", "message", r)
		}
	}()
	rpc.RPC().SetPusher(webPusher)
	js.Global().Set("onmessage", js.FuncOf(onMessage))
	postDone(js.Global())
	select {}
}

func webPusher(push *pb.Push, port int64) error {
	resp := &pb.Response{
		Responses: &pb.Response_Push{
			Push: push,
		},
	}
	b, err := resp.MarshalVT()
	if err != nil {
		return err
	}
	postMessage(js.Global(), b)
	return nil
}

func onMessage(global js.Value, args []js.Value) any {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("onMessage recovered from panic", "message", r)
		}
	}()

	me := args[0]

	if !me.InstanceOf(js.Global().Get("MessageEvent")) {
		slog.Warn("onMessage: no MessageEvent")
		return nil
	}

	data := me.Get("data")
	messagePort := data.Index(0)
	b := data.Index(1)
	message := make([]byte, b.Length())
	js.CopyBytesToGo(message, b)
	sink := newResponseSink(data, messagePort)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		req := pb.Request{}
		if err := req.UnmarshalVT(message); err != nil {
			postError(messagePort, err)
			return
		}
		for resp := range rpc.RPC().Call(ctx, &req) {
			sink.send(resp)
		}
		sink.done()
	}()
	return nil
}

func postMessage(port js.Value, message []byte) {
	if message == nil {
		buf := js.Global().Get("ArrayBuffer").New(0)
		out := js.Global().Get("Uint8Array").New(buf)
		port.Call("postMessage", out, []any{buf})
		return
	}
	buf := js.Global().Get("ArrayBuffer").New(len(message))
	out := js.Global().Get("Uint8Array").New(buf)
	js.CopyBytesToJS(out, message)
	port.Call("postMessage", out, []any{buf})
}

func postError(port js.Value, err error) {
	resp := &pb.Response{
		Responses: &pb.Response_Error{
			Error: &pb.Error{
				Message: err.Error(),
			},
		},
	}
	errmsg, err := resp.MarshalVT()
	if err != nil {
		panic(err)
	}
	postMessage(port, errmsg)
}

func postDone(port js.Value) {
	resp := &pb.Response{
		Responses: &pb.Response_Done{
			Done: &pb.Done{},
		},
	}
	done, err := resp.MarshalVT()
	if err != nil {
		panic(err)
	}
	postMessage(port, done)
}
