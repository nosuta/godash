package rpc

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nosuta/godash/v2/pb"
)

const testTimeout = 3 * time.Second

func TestMain(m *testing.M) {
	// Silence per-request info logs during tests.
	slog.SetLogLoggerLevel(slog.LevelError)
	exit := m.Run()
	// Restore for other tooling.
	slog.SetLogLoggerLevel(slog.LevelInfo)
	os.Exit(exit)
}

// recvBytes receives one []byte from ch or fails the test on timeout or
// premature close.
func recvBytes(t *testing.T, ch chan []byte) []byte {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed unexpectedly")
		}
		return b
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for response")
		return nil
	}
}

// recvAll drains ch until it is closed and returns every received payload.
func recvAll(t *testing.T, ch chan []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, b)
		case <-time.After(testTimeout):
			t.Fatalf("timed out draining channel (got %d responses)", len(out))
			return out
		}
	}
}

func parseResponse(t *testing.T, b []byte) *pb.Response {
	t.Helper()
	resp := &pb.Response{}
	if err := resp.UnmarshalVT(b); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// resetForTest returns the singleton rpc and resets its application hooks so
// tests do not leak handlers into each other.
func resetForTest(t *testing.T) *rpc {
	t.Helper()
	r := RPC()
	r.mu.Lock()
	r.pusher = nil
	r.cancels = make(map[int64]context.CancelFunc)
	r.mu.Unlock()
	r.flowMu.Lock()
	r.flowGates = make(map[int64]*FlowGate)
	r.pendingFlowCredits = make(map[int64]int)
	r.flowMu.Unlock()
	entryPoint = nil
	handleRPC = nil
	t.Cleanup(func() {
		entryPoint = nil
		handleRPC = nil
	})
	return r
}

func TestRPCSingleton(t *testing.T) {
	r := RPC()
	if r == nil {
		t.Fatal("RPC() returned nil")
	}
	if RPC() != r {
		t.Fatal("RPC() must return the same singleton instance")
	}
	if pb.ReverseCallFn == nil {
		t.Fatal("pb.ReverseCallFn must be wired by RPC()")
	}
	if pb.PushFn == nil {
		t.Fatal("pb.PushFn must be wired by RPC()")
	}
}

func TestCallUnsupportedRequest(t *testing.T) {
	resetForTest(t)
	ch := RPC().Call(context.Background(), &pb.Request{Port: 1})
	resps := recvAll(t, ch)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	resp := parseResponse(t, resps[0])
	if resp.GetError() == nil {
		t.Fatalf("expected error response, got %+v", resp)
	}
	if resp.GetError().GetCode() != 500 {
		t.Fatalf("expected code 500, got %d", resp.GetError().GetCode())
	}
	if !strings.Contains(resp.GetError().GetMessage(), "unsupported request") {
		t.Fatalf("unexpected error message: %q", resp.GetError().GetMessage())
	}
}

func TestCallInitWithoutEntryPoint(t *testing.T) {
	r := resetForTest(t)
	r.nativePushPort = 0
	ch := r.Call(context.Background(), &pb.Request{
		Port: 2,
		Requests: &pb.Request_Init{Init: &pb.Init{
			PushPort:   100,
			SupportDir: "/support",
		}},
	})
	resps := recvAll(t, ch)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	resp := parseResponse(t, resps[0])
	if resp.GetError() == nil || resp.GetError().GetCode() != 500 {
		t.Fatalf("expected 500 error, got %+v", resp)
	}
	if !strings.Contains(resp.GetError().GetMessage(), "entry point not set") {
		t.Fatalf("unexpected error message: %q", resp.GetError().GetMessage())
	}
	if r.nativePushPort != 100 {
		t.Fatalf("nativePushPort should be set by Init before dispatch, got %d", r.nativePushPort)
	}
}

func TestCallInitEntryPoint(t *testing.T) {
	r := resetForTest(t)

	var (
		mu     sync.Mutex
		dbPath string
		aek    string
	)
	SetEntryPoint(func(databasePath, appEncryptionKey string) error {
		mu.Lock()
		defer mu.Unlock()
		dbPath = databasePath
		aek = appEncryptionKey
		return nil
	})

	ch := r.Call(context.Background(), &pb.Request{
		Port: 3,
		Requests: &pb.Request_Init{Init: &pb.Init{
			PushPort:         200,
			SupportDir:       "/support",
			AppEncryptionKey: "secret",
		}},
	})
	if resps := recvAll(t, ch); len(resps) != 0 {
		t.Fatalf("successful init should produce no responses, got %d", len(resps))
	}

	mu.Lock()
	if dbPath != "/support/database.db" {
		t.Fatalf("unexpected databasePath: %q", dbPath)
	}
	if aek != "secret" {
		t.Fatalf("unexpected appEncryptionKey: %q", aek)
	}
	mu.Unlock()
	if r.nativePushPort != 200 {
		t.Fatalf("nativePushPort not set, got %d", r.nativePushPort)
	}

	// Empty support dir falls back to the default database path.
	mu.Lock()
	dbPath = ""
	mu.Unlock()
	ch = r.Call(context.Background(), &pb.Request{
		Port:     4,
		Requests: &pb.Request_Init{Init: &pb.Init{PushPort: 201}},
	})
	if resps := recvAll(t, ch); len(resps) != 0 {
		t.Fatalf("successful init should produce no responses, got %d", len(resps))
	}
	mu.Lock()
	defer mu.Unlock()
	if dbPath != "/database.db" {
		t.Fatalf("expected default databasePath, got %q", dbPath)
	}
}

func TestCallInitEntryPointError(t *testing.T) {
	resetForTest(t)
	SetEntryPoint(func(databasePath, appEncryptionKey string) error {
		return errors.New("boom")
	})
	ch := RPC().Call(context.Background(), &pb.Request{
		Port:     5,
		Requests: &pb.Request_Init{Init: &pb.Init{PushPort: 300}},
	})
	resps := recvAll(t, ch)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	resp := parseResponse(t, resps[0])
	if resp.GetError() == nil || resp.GetError().GetCode() != 500 || resp.GetError().GetMessage() != "boom" {
		t.Fatalf("unexpected error response: %+v", resp)
	}
}

func TestCallRPCWithoutHandler(t *testing.T) {
	resetForTest(t)
	ch := RPC().Call(context.Background(), &pb.Request{
		Port:     6,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/echo"}},
	})
	resps := recvAll(t, ch)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	resp := parseResponse(t, resps[0])
	if resp.GetError() == nil || resp.GetError().GetCode() != 500 {
		t.Fatalf("expected 500 error, got %+v", resp)
	}
	if !strings.Contains(resp.GetError().GetMessage(), "RPC handler not set") {
		t.Fatalf("unexpected error message: %q", resp.GetError().GetMessage())
	}
}

func TestCallRPCUnary(t *testing.T) {
	resetForTest(t)
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		if req.Path != "/echo" {
			t.Errorf("unexpected path: %q", req.Path)
		}
		if string(req.Payload) != "hello" {
			t.Errorf("unexpected payload: %q", req.Payload)
		}
		if ctx == nil || ctx.Err() != nil {
			t.Errorf("handler ctx should be live")
		}
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: []byte("world")},
			},
		}
	})
	ch := RPC().Call(context.Background(), &pb.Request{
		Port: 7,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{
			Path:    "/echo",
			Payload: []byte("hello"),
		}},
	})
	resps := recvAll(t, ch)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	resp := parseResponse(t, resps[0])
	if resp.GetRpcResponse() == nil || string(resp.GetRpcResponse().GetPayload()) != "world" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCallSyncUnary(t *testing.T) {
	r := resetForTest(t)
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		if req.Path != "/echo" {
			t.Errorf("unexpected path: %q", req.Path)
		}
		if string(req.Payload) != "hello" {
			t.Errorf("unexpected payload: %q", req.Payload)
		}
		if ctx == nil || ctx.Err() != nil {
			t.Errorf("handler ctx should be live")
		}
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: []byte("world")},
			},
		}
	})

	req := &pb.Request{
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{
			Path:    "/echo",
			Payload: []byte("hello"),
		}},
	}
	payload, err := req.MarshalVT()
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	got, err := r.CallSync(context.Background(), payload)
	if err != nil {
		t.Fatalf("CallSync failed: %v", err)
	}
	resp := parseResponse(t, got)
	if resp.GetRpcResponse() == nil || string(resp.GetRpcResponse().GetPayload()) != "world" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// The sync path must not register a port-keyed cancel entry.
	r.mu.Lock()
	n := len(r.cancels)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("CallSync must not register cancels, got %d", n)
	}
}

func TestCallSyncWithoutHandler(t *testing.T) {
	r := resetForTest(t)
	req := &pb.Request{
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/echo"}},
	}
	payload, _ := req.MarshalVT()
	_, err := r.CallSync(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "RPC handler not set") {
		t.Fatalf("expected handler not set error, got %v", err)
	}
}

func TestCallSyncUnsupportedRequest(t *testing.T) {
	r := resetForTest(t)
	req := &pb.Request{
		Requests: &pb.Request_Init{Init: &pb.Init{PushPort: 1}},
	}
	payload, _ := req.MarshalVT()
	_, err := r.CallSync(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "supports only RpcRequest") {
		t.Fatalf("expected unsupported request error, got %v", err)
	}
}

func TestCallSyncEmptyResponse(t *testing.T) {
	r := resetForTest(t)
	// A handler that returns without sending anything (e.g. an unimplemented
	// path) must surface an error rather than block.
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {})
	req := &pb.Request{
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/void"}},
	}
	payload, _ := req.MarshalVT()
	_, err := r.CallSync(context.Background(), payload)
	if err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("expected no response error, got %v", err)
	}
}

func TestCallSyncBadPayload(t *testing.T) {
	r := resetForTest(t)
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {})
	_, err := r.CallSync(context.Background(), []byte{0xFF, 0xFF, 0xFF, 0xFF})
	if err == nil || !strings.Contains(err.Error(), "unmarshal request") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}
}

func TestCallSyncHonoursContextTimeout(t *testing.T) {
	r := resetForTest(t)
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		// Wait for cancellation rather than producing a response.
		<-ctx.Done()
	})
	req := &pb.Request{
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/slow"}},
	}
	payload, _ := req.MarshalVT()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.CallSync(ctx, payload)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error when the handler yields no response")
	}
	if elapsed > testTimeout {
		t.Fatalf("CallSync did not return promptly after ctx timeout: %v", elapsed)
	}
}

func TestCallRPCStreamOrdering(t *testing.T) {
	resetForTest(t)
	const n = 5
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		for i := 0; i < n; i++ {
			ch <- &pb.Response{
				Responses: &pb.Response_RpcResponse{
					RpcResponse: &pb.RpcResponse{Payload: []byte{byte(i)}},
				},
			}
		}
	})
	ch := RPC().Call(context.Background(), &pb.Request{
		Port:     8,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/stream"}},
	})
	resps := recvAll(t, ch)
	if len(resps) != n {
		t.Fatalf("expected %d responses, got %d", n, len(resps))
	}
	for i, b := range resps {
		resp := parseResponse(t, b)
		if resp.GetRpcResponse() == nil {
			t.Fatalf("response %d is not RpcResponse: %+v", i, resp)
		}
		if got := resp.GetRpcResponse().GetPayload(); len(got) != 1 || got[0] != byte(i) {
			t.Fatalf("response %d out of order: %v", i, got)
		}
	}
}

func TestCallPortKeyedCancel(t *testing.T) {
	resetForTest(t)

	const (
		targetPort = int64(9001)
		cancelPort = int64(9002)
	)

	started := make(chan struct{})
	handlerDone := make(chan struct{})
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		if req.Path != "/slow" {
			return
		}
		// Announce that the handler (and therefore the port-keyed cancel
		// entry registered in Call) is live, then block until cancelled.
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: []byte("started")},
			},
		}
		close(started)
		<-ctx.Done()
		ch <- &pb.Response{
			Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: []byte("cancelled")},
			},
		}
		close(handlerDone)
	})

	target := RPC().Call(context.Background(), &pb.Request{
		Port:     targetPort,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/slow"}},
	})

	b := recvBytes(t, target)
	if resp := parseResponse(t, b); string(resp.GetRpcResponse().GetPayload()) != "started" {
		t.Fatalf("expected started marker, got %+v", resp)
	}
	<-started

	// Verify the cancel entry is registered for targetPort.
	r := RPC()
	r.mu.Lock()
	_, registered := r.cancels[targetPort]
	r.mu.Unlock()
	if !registered {
		t.Fatal("cancel entry for target port not registered")
	}

	// Cancel the target port from a second request.
	cancelCh := r.Call(context.Background(), &pb.Request{
		Port:     cancelPort,
		Requests: &pb.Request_Cancel{Cancel: &pb.Cancel{Port: targetPort}},
	})

	// The cancelled handler must resume and deliver its final response.
	b = recvBytes(t, target)
	if resp := parseResponse(t, b); string(resp.GetRpcResponse().GetPayload()) != "cancelled" {
		t.Fatalf("expected cancelled marker, got %+v", resp)
	}
	select {
	case <-handlerDone:
	case <-time.After(testTimeout):
		t.Fatal("handler did not finish")
	}
	if resps := recvAll(t, target); len(resps) != 0 {
		t.Fatalf("target channel should close after cancel, got %d extra", len(resps))
	}
	if resps := recvAll(t, cancelCh); len(resps) != 0 {
		t.Fatalf("cancel request should produce no responses, got %d", len(resps))
	}

	r.mu.Lock()
	_, stillTarget := r.cancels[targetPort]
	_, stillCancel := r.cancels[cancelPort]
	r.mu.Unlock()
	if stillTarget || stillCancel {
		t.Fatalf("cancel entries not cleaned up: target=%v cancel=%v", stillTarget, stillCancel)
	}
}

func TestCallCancelUnknownPort(t *testing.T) {
	r := resetForTest(t)
	ch := r.Call(context.Background(), &pb.Request{
		Port:     9003,
		Requests: &pb.Request_Cancel{Cancel: &pb.Cancel{Port: 99999}},
	})
	if resps := recvAll(t, ch); len(resps) != 0 {
		t.Fatalf("cancel of unknown port should be a no-op, got %d responses", len(resps))
	}
}

func TestCallCtxAlreadyCancelled(t *testing.T) {
	resetForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := RPC().Call(ctx, &pb.Request{
		Port:     9004,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/echo"}},
	})
	if resps := recvAll(t, ch); len(resps) != 0 {
		t.Fatalf("pre-cancelled ctx should yield closed channel, got %d responses", len(resps))
	}
}

func TestPushWithoutPusher(t *testing.T) {
	r := resetForTest(t)
	err := r.Push(&pb.Push{Type: "t"})
	if err == nil || !strings.Contains(err.Error(), "pusher is nil") {
		t.Fatalf("expected pusher is nil error, got %v", err)
	}
}

func TestSetPusherUsesNativePushPort(t *testing.T) {
	r := resetForTest(t)

	// Init sets nativePushPort.
	SetEntryPoint(func(databasePath, appEncryptionKey string) error { return nil })
	ch := r.Call(context.Background(), &pb.Request{
		Port:     10,
		Requests: &pb.Request_Init{Init: &pb.Init{PushPort: 4242}},
	})
	recvAll(t, ch)

	type capturedPush struct {
		push *pb.Push
		port int64
	}
	captured := make(chan capturedPush, 1)
	r.SetPusher(func(push *pb.Push, port int64) error {
		captured <- capturedPush{push: push, port: port}
		return nil
	})

	if err := r.Push(&pb.Push{Type: "tick", Payload: []byte("p")}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	select {
	case c := <-captured:
		if c.port != 4242 {
			t.Fatalf("expected port 4242, got %d", c.port)
		}
		if c.push.GetType() != "tick" || string(c.push.GetPayload()) != "p" {
			t.Fatalf("unexpected push: %+v", c.push)
		}
	case <-time.After(testTimeout):
		t.Fatal("pusher was not called")
	}
}

func TestReverseCallSuccess(t *testing.T) {
	r := resetForTest(t)

	type capturedPush struct {
		push *pb.Push
		port int64
	}
	captured := make(chan capturedPush, 1)
	r.SetPusher(func(push *pb.Push, port int64) error {
		captured <- capturedPush{push: push, port: port}
		return nil
	})

	result := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		payload, err := r.ReverseCall(context.Background(), &pb.Push{
			Type:    "reverse",
			Payload: []byte("request"),
		})
		if err != nil {
			errCh <- err
			return
		}
		result <- payload
	}()

	var c capturedPush
	select {
	case c = <-captured:
	case <-time.After(testTimeout):
		t.Fatal("pusher was not called")
	}
	if c.push.GetType() != "reverse" || string(c.push.GetPayload()) != "request" {
		t.Fatalf("unexpected push: %+v", c.push)
	}
	if c.push.GetReversePort() == 0 {
		t.Fatal("reverse_port must be set on the push")
	}

	// Dart replies with a ReverseResponse request on the same reverse_port.
	ch := r.Call(context.Background(), &pb.Request{
		Port: 11,
		Requests: &pb.Request_ReverseResponse{ReverseResponse: &pb.ReverseResponse{
			ReversePort: c.push.GetReversePort(),
			Payload:     []byte("reply"),
		}},
	})
	if resps := recvAll(t, ch); len(resps) != 0 {
		t.Fatalf("reverse response request should produce no responses, got %d", len(resps))
	}

	select {
	case payload := <-result:
		if string(payload) != "reply" {
			t.Fatalf("unexpected reverse payload: %q", payload)
		}
	case err := <-errCh:
		t.Fatalf("ReverseCall failed: %v", err)
	case <-time.After(testTimeout):
		t.Fatal("ReverseCall did not resolve")
	}
}

func TestReverseCallPushFailure(t *testing.T) {
	r := resetForTest(t)
	r.SetPusher(func(push *pb.Push, port int64) error {
		return errors.New("post failed")
	})
	_, err := r.ReverseCall(context.Background(), &pb.Push{Type: "t"})
	if err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("expected push failed error, got %v", err)
	}
}

func TestReverseCallCtxCancelled(t *testing.T) {
	r := resetForTest(t)
	captured := make(chan struct{}, 1)
	r.SetPusher(func(push *pb.Push, port int64) error {
		captured <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := r.ReverseCall(ctx, &pb.Push{Type: "t"})
		errCh <- err
	}()
	select {
	case <-captured:
	case <-time.After(testTimeout):
		t.Fatal("pusher was not called")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("ReverseCall did not resolve on ctx cancel")
	}
}

func TestReverseCallConcurrent(t *testing.T) {
	r := resetForTest(t)
	// echo pusher: captures push and replies via a ReverseResponse request.
	r.SetPusher(func(push *pb.Push, port int64) error {
		go func() {
			ch := r.Call(context.Background(), &pb.Request{
				Port: push.ReversePort + 10000,
				Requests: &pb.Request_ReverseResponse{ReverseResponse: &pb.ReverseResponse{
					ReversePort: push.ReversePort,
					Payload:     push.Payload,
				}},
			})
			<-ch
		}()
		return nil
	})

	var wg sync.WaitGroup
	var seq atomic.Int64
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := []byte{byte(seq.Add(1))}
			payload, err := r.ReverseCall(context.Background(), &pb.Push{Payload: want})
			if err != nil {
				errs <- err
				return
			}
			if string(payload) != string(want) {
				errs <- errors.New("payload mismatch in concurrent ReverseCall")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
