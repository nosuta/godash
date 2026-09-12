package rpc

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/nosuta/godash/v2/pb"
)

func creditPayload(port int64, credits int32) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint64(b[0:8], uint64(port))
	binary.LittleEndian.PutUint32(b[8:12], uint32(credits))
	return b
}

func TestFlowGateUnboundedNeverBlocks(t *testing.T) {
	g := newFlowGate()
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("unbounded Acquire should not block: %v", err)
	}
}

func TestFlowGateAcquireWithCredit(t *testing.T) {
	g := newFlowGate()
	g.AddCredits(1) // first grant switches the gate to bounded(1)
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
}

func TestFlowGateAcquireBlocksUntilCredit(t *testing.T) {
	g := &FlowGate{bounded: true}
	done := make(chan error, 1)
	go func() { done <- g.Acquire(context.Background()) }()

	select {
	case <-done:
		t.Fatal("Acquire returned before a credit was granted")
	case <-time.After(20 * time.Millisecond):
	}

	g.AddCredits(1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Acquire did not wake on AddCredits")
	}
}

func TestFlowGateAcquireCtxCancel(t *testing.T) {
	g := &FlowGate{bounded: true}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- g.Acquire(ctx) }()
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Acquire did not return on ctx cancel")
	}
}

func TestFlowGateClosed(t *testing.T) {
	g := &FlowGate{bounded: true}
	g.close()
	if err := g.Acquire(context.Background()); err == nil {
		t.Fatal("Acquire on a closed gate should error")
	}
}

// TestCallFlowCreditGatesHandler exercises the full block-backpressure path:
// the initial window is granted before the stream starts (pending-credit path),
// the handler acquires one credit per item, and a later credit control request
// unblocks it.
func TestCallFlowCreditGatesHandler(t *testing.T) {
	r := resetForTest(t)

	gateSeen := make(chan struct{}, 1)
	done := make(chan struct{})
	SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		gate := FlowFromContext(ctx)
		if gate == nil {
			return
		}
		gateSeen <- struct{}{}
		for _, item := range []string{"one", "two"} {
			if err := gate.Acquire(ctx); err != nil {
				return
			}
			ch <- &pb.Response{Responses: &pb.Response_RpcResponse{
				RpcResponse: &pb.RpcResponse{Payload: []byte(item)},
			}}
		}
		close(done)
	})

	const streamPort = int64(7777)

	// Seed the initial block window before the stream request is dispatched,
	// which also covers the pending-credit path.
	seed := r.Call(context.Background(), &pb.Request{
		Port: 7001,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{
			Path:    FlowCreditPath,
			Payload: creditPayload(streamPort, 1),
		}},
	})
	if resps := recvAll(t, seed); len(resps) != 0 {
		t.Fatalf("seed credit should produce no responses, got %d", len(resps))
	}

	ch := r.Call(context.Background(), &pb.Request{
		Port:     streamPort,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: "/flow"}},
	})

	if b := recvBytes(t, ch); string(parseResponse(t, b).GetRpcResponse().GetPayload()) != "one" {
		t.Fatalf("expected first item")
	}
	select {
	case <-gateSeen:
	case <-time.After(testTimeout):
		t.Fatal("handler did not observe a flow gate in ctx")
	}

	select {
	case <-done:
		t.Fatal("handler produced a second item without a credit")
	case <-time.After(20 * time.Millisecond):
	}

	// Grant one more credit over the reserved control path.
	ctrl := r.Call(context.Background(), &pb.Request{
		Port: 8888,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{
			Path:    FlowCreditPath,
			Payload: creditPayload(streamPort, 1),
		}},
	})
	if resps := recvAll(t, ctrl); len(resps) != 0 {
		t.Fatalf("flow credit control request should produce no responses, got %d", len(resps))
	}

	if b := recvBytes(t, ch); string(parseResponse(t, b).GetRpcResponse().GetPayload()) != "two" {
		t.Fatal("expected second item after credit")
	}
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("handler did not finish after credit")
	}
	if resps := recvAll(t, ch); len(resps) != 0 {
		t.Fatalf("stream channel should be closed, got %d extra", len(resps))
	}
}

func TestCallFlowCreditUnknownPortIsNoop(t *testing.T) {
	r := resetForTest(t)
	ctrl := r.Call(context.Background(), &pb.Request{
		Port: 9000,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{
			Path:    FlowCreditPath,
			Payload: creditPayload(424242, 4),
		}},
	})
	// A credit for an unknown port is buffered (it may arrive before the stream
	// request), so the control call itself still produces no responses.
	if resps := recvAll(t, ctrl); len(resps) != 0 {
		t.Fatalf("unknown-port credit should produce no responses, got %d", len(resps))
	}
}

// TestFlowGateConcurrentAcquire verifies that many goroutines can wait on the
// same gate and are all served as credits arrive.
func TestFlowGateConcurrentAcquire(t *testing.T) {
	g := newFlowGate()
	g.AddCredits(1) // bounded(1)

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Acquire(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	g.AddCredits(n - 1)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("concurrent Acquire calls did not all complete")
	}
	close(errs)
	for err := range errs {
		t.Errorf("Acquire: %v", err)
	}
}

// TestFlowGateConcurrentAcquireCtxCancel ensures a cancelled waiter does not
// consume a later credit or leak into the waiter list.
func TestFlowGateConcurrentAcquireCtxCancel(t *testing.T) {
	g := &FlowGate{bounded: true}
	cancelled, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- g.Acquire(cancelled) }()
	cancel()
	if err := <-errCh; err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	// A later credit must still be consumable.
	g.AddCredits(1)
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after cancelled waiter: %v", err)
	}
}

// TestReceiveFlowCreditMalformedPayloadIsIgnored verifies short/zero/negative
// grants are dropped without panicking or polluting the pending buffer.
func TestReceiveFlowCreditMalformedPayloadIsIgnored(t *testing.T) {
	r := resetForTest(t)
	r.receiveFlowCredit([]byte{1, 2, 3})
	r.receiveFlowCredit(creditPayload(1, 0))
	r.receiveFlowCredit(creditPayload(2, -5))
	if len(r.pendingFlowCredits) != 0 {
		t.Fatalf("malformed credits should not be buffered: %+v", r.pendingFlowCredits)
	}
}
