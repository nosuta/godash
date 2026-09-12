//go:build js

// Web worker protocol tests. These run the real worker message handling
// (web.onMessage) inside a browser via the wasmbrowsertest harness:
//
//	(cd cmd/go_js_wasm_exec && go build -o /tmp/go_js_wasm_exec .)
//	GOOS=js GOARCH=wasm go test -exec /tmp/go_js_wasm_exec ./web
//
// The Dart side of the protocol sends [MessagePort, Uint8Array] pairs and
// expects Uint8Array responses posted back to the port, finishing with a
// Done response. Here the port is a plain JS object with a postMessage
// capture, which lets us assert the exact bytes the worker posts back.
package web

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"syscall/js"

	"github.com/nosuta/godash/v2/pb"
	"github.com/nosuta/godash/v2/rpc"
)

const jsTestTimeout = 10 * time.Second

func TestMain(m *testing.M) {
	slog.SetLogLoggerLevel(slog.LevelError)
	os.Exit(m.Run())
}

// fakePort returns a JS object with a postMessage function that pushes the
// posted Uint8Array bytes onto ch, mirroring the MessagePort the Dart side
// transfers to the worker.
func fakePort(ch chan []byte) (port js.Value, release func()) {
	fn := js.FuncOf(func(this js.Value, args []js.Value) any {
		out := args[0]
		b := make([]byte, out.Get("length").Int())
		js.CopyBytesToGo(b, out)
		ch <- b
		return nil
	})
	port = js.Global().Get("Object").New()
	port.Set("postMessage", fn)
	return port, fn.Release
}

// postRequest drives one request through the worker's onMessage handler the
// same way the Dart bridge would: a MessageEvent whose data is [port, bytes].
func postRequest(t *testing.T, port js.Value, b []byte) {
	t.Helper()
	u8 := js.Global().Get("Uint8Array").New(len(b))
	if len(b) > 0 {
		js.CopyBytesToJS(u8, b)
	}
	data := js.ValueOf([]any{port, u8})
	ev := js.Global().Get("MessageEvent").New("message", js.ValueOf(map[string]any{"data": data}))
	onMessage(js.Global(), []js.Value{ev})
}

func recvResponse(t *testing.T, ch chan []byte) *pb.Response {
	t.Helper()
	select {
	case b := <-ch:
		resp := &pb.Response{}
		if err := resp.UnmarshalVT(b); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp
	case <-time.After(jsTestTimeout):
		t.Fatal("timed out waiting for worker response")
		return nil
	}
}

func resetWebHandlers(t *testing.T) {
	t.Helper()
	rpc.SetEntryPoint(nil)
	rpc.SetHandleRPC(nil)
	t.Cleanup(func() {
		rpc.SetEntryPoint(nil)
		rpc.SetHandleRPC(nil)
	})
}

func TestOnMessageBadProtobuf(t *testing.T) {
	resetWebHandlers(t)
	ch := make(chan []byte, 16)
	port, release := fakePort(ch)
	defer release()

	postRequest(t, port, []byte{0xFF, 0xFF, 0xFF})

	resp := recvResponse(t, ch)
	if resp.GetError() == nil {
		t.Fatalf("expected error response, got %+v", resp)
	}
	if resp.GetError().GetMessage() == "" {
		t.Fatal("error message must not be empty")
	}
}

func TestOnMessageRPCEcho(t *testing.T) {
	resetWebHandlers(t)
	rpc.SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		if req.GetPath() != "/echo" {
			t.Errorf("unexpected path: %q", req.GetPath())
		}
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: req.GetPayload()},
			},
		}
	})

	ch := make(chan []byte, 16)
	port, release := fakePort(ch)
	defer release()

	req := &pb.Request{
		Port:     100,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/echo", Payload: []byte("hello web")}},
	}
	b, err := req.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	postRequest(t, port, b)

	resp := recvResponse(t, ch)
	if resp.GetRpcResponse() == nil {
		t.Fatalf("expected rpc_response, got %+v", resp)
	}
	if string(resp.GetRpcResponse().GetPayload()) != "hello web" {
		t.Fatalf("unexpected payload: %q", resp.GetRpcResponse().GetPayload())
	}

	done := recvResponse(t, ch)
	if done.GetDone() == nil {
		t.Fatalf("expected done response after rpc, got %+v", done)
	}
}

func TestOnMessageStreamThenDone(t *testing.T) {
	resetWebHandlers(t)
	const n = 3
	rpc.SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		for i := 0; i < n; i++ {
			ch <- &pb.Response{
				Responses: &pb.Response_RpcResponse{
					RpcResponse: &pb.RpcResponse{Payload: []byte{byte(i)}},
				},
			}
		}
	})

	ch := make(chan []byte, 16)
	port, release := fakePort(ch)
	defer release()

	req := &pb.Request{
		Port:     101,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/stream"}},
	}
	b, err := req.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	postRequest(t, port, b)

	for i := 0; i < n; i++ {
		resp := recvResponse(t, ch)
		if resp.GetRpcResponse() == nil {
			t.Fatalf("response %d: expected rpc_response, got %+v", i, resp)
		}
		if got := resp.GetRpcResponse().GetPayload(); len(got) != 1 || got[0] != byte(i) {
			t.Fatalf("response %d out of order: %v", i, got)
		}
	}
	done := recvResponse(t, ch)
	if done.GetDone() == nil {
		t.Fatalf("expected done response after stream, got %+v", done)
	}
}

func TestOnMessageInit(t *testing.T) {
	resetWebHandlers(t)

	var (
		dbPath string
		aek    string
	)
	rpc.SetEntryPoint(func(databasePath, appEncryptionKey string) error {
		dbPath = databasePath
		aek = appEncryptionKey
		return nil
	})

	ch := make(chan []byte, 16)
	port, release := fakePort(ch)
	defer release()

	req := &pb.Request{
		Port: 102,
		Requests: &pb.Request_Init{Init: &pb.Init{
			PushPort:         1234,
			SupportDir:       "/support",
			AppEncryptionKey: "web-key",
		}},
	}
	b, err := req.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	postRequest(t, port, b)

	// A successful init produces no responses other than the final Done.
	done := recvResponse(t, ch)
	if done.GetDone() == nil {
		t.Fatalf("expected done response after init, got %+v", done)
	}
	if dbPath != "/support/database.db" {
		t.Fatalf("unexpected databasePath: %q", dbPath)
	}
	if aek != "web-key" {
		t.Fatalf("unexpected appEncryptionKey: %q", aek)
	}
}

func TestOnMessageNonMessageEvent(t *testing.T) {
	resetWebHandlers(t)
	ch := make(chan []byte, 16)
	_, release := fakePort(ch)
	defer release()

	// A plain object (not a MessageEvent) must be ignored: no panic, no
	// responses.
	notEvent := js.Global().Get("Object").New()
	onMessage(js.Global(), []js.Value{notEvent})

	select {
	case b := <-ch:
		t.Fatalf("expected no response, got %x", b)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWebPusherPostsResponseEnvelope(t *testing.T) {
	posted := make(chan []byte, 16)
	orig := js.Global().Get("postMessage")
	stub := js.FuncOf(func(this js.Value, args []js.Value) any {
		out := args[0]
		b := make([]byte, out.Get("length").Int())
		js.CopyBytesToGo(b, out)
		posted <- b
		return nil
	})
	js.Global().Set("postMessage", stub)
	defer func() {
		js.Global().Set("postMessage", orig)
		stub.Release()
	}()

	err := webPusher(&pb.Push{Type: "tick", Payload: []byte("p")}, 7)
	if err != nil {
		t.Fatalf("webPusher failed: %v", err)
	}
	select {
	case b := <-posted:
		resp := &pb.Response{}
		if err := resp.UnmarshalVT(b); err != nil {
			t.Fatalf("unmarshal posted response: %v", err)
		}
		if resp.GetPush() == nil {
			t.Fatalf("expected push response, got %+v", resp)
		}
		if resp.GetPush().GetType() != "tick" || string(resp.GetPush().GetPayload()) != "p" {
			t.Fatalf("unexpected push: %+v", resp.GetPush())
		}
	case <-time.After(jsTestTimeout):
		t.Fatal("webPusher did not post")
	}
}
