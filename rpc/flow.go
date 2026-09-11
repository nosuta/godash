package rpc

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
)

// FlowCreditPath is the reserved RpcRequest path used by the Dart
// `Backpressure.block` strategy to grant the Go producer credits. It travels
// over the existing envelope and is intercepted before the application
// dispatcher, so it never reaches a generated handler. It must match
// `kFlowCreditPath` on the Dart side.
const FlowCreditPath = "/godash.flow/Credit"

// flowCreditPayloadLen is the little-endian wire layout of a credit grant:
//
//	[0:8]  stream port (int64)
//	[8:12] credits (int32)
const flowCreditPayloadLen = 12

// FlowGate is a credit-based backpressure gate for a streaming RPC.
//
// A gate starts **unbounded**: [Acquire] returns immediately, so a handler can
// safely call it unconditionally. The first credit grant received for the
// stream switches the gate to bounded mode and becomes its initial window;
// further grants add credits. When bounded credits run out, [Acquire] blocks
// until the Dart consumer drains and grants more.
//
// Retrieve the gate from the handler context with [FlowFromContext]:
//
//	gate := rpc.FlowFromContext(ctx)
//	for {
//	    if gate != nil {
//	        if err := gate.Acquire(ctx); err != nil {
//	            return err
//	        }
//	    }
//	    ch <- response
//	}
//
// Handlers that never call [Acquire] are unaffected; for them the client's
// `block` policy degrades to Dart-side buffering.
type FlowGate struct {
	mu      sync.Mutex
	bounded bool
	credits int
	closed  bool
	waiter  chan struct{}
}

// newFlowGate returns an unbounded gate. It becomes bounded on the first
// credit grant.
func newFlowGate() *FlowGate {
	return &FlowGate{}
}

// AddCredits grants n credits. The first call switches the gate to bounded mode
// (n becomes the initial window); later calls add to the remaining credits.
func (g *FlowGate) AddCredits(n int) {
	if n <= 0 {
		return
	}
	g.mu.Lock()
	if !g.bounded {
		g.bounded = true
		g.credits = 0
	}
	g.credits += n
	waiter := g.waiter
	g.waiter = nil
	g.mu.Unlock()
	if waiter != nil {
		close(waiter)
	}
}

// Acquire consumes one credit. It returns immediately while the gate is
// unbounded, blocks while bounded with no credits left, and returns ctx.Err()
// on cancellation or an error once the gate is closed.
func (g *FlowGate) Acquire(ctx context.Context) error {
	g.mu.Lock()
	if !g.bounded {
		g.mu.Unlock()
		return nil
	}
	if g.credits > 0 {
		g.credits--
		g.mu.Unlock()
		return nil
	}
	if g.closed {
		g.mu.Unlock()
		return fmt.Errorf("flow gate is closed")
	}
	waiter := make(chan struct{})
	g.waiter = waiter
	g.mu.Unlock()

	select {
	case <-waiter:
		g.mu.Lock()
		switch {
		case g.credits > 0:
			g.credits--
			g.mu.Unlock()
			return nil
		case g.closed:
			g.mu.Unlock()
			return fmt.Errorf("flow gate is closed")
		default:
			// Spurious wake (e.g. AddCredits raced with cancellation); retry.
			g.mu.Unlock()
			return g.Acquire(ctx)
		}
	case <-ctx.Done():
		g.mu.Lock()
		if g.waiter == waiter {
			g.waiter = nil
		}
		g.mu.Unlock()
		return ctx.Err()
	}
}

func (g *FlowGate) close() {
	g.mu.Lock()
	g.closed = true
	waiter := g.waiter
	g.waiter = nil
	g.mu.Unlock()
	if waiter != nil {
		close(waiter)
	}
}

type flowGateCtxKey struct{}

func withFlowGate(ctx context.Context, g *FlowGate) context.Context {
	return context.WithValue(ctx, flowGateCtxKey{}, g)
}

// FlowFromContext returns the [FlowGate] for the current streaming RPC, or nil
// when the call was not an RPC request. The gate is unbounded unless the Dart
// client enabled `Backpressure.block`.
func FlowFromContext(ctx context.Context) *FlowGate {
	g, _ := ctx.Value(flowGateCtxKey{}).(*FlowGate)
	return g
}

func (r *rpc) registerFlowGate(port int64, g *FlowGate) {
	r.flowMu.Lock()
	r.flowGates[port] = g
	pending := r.pendingFlowCredits[port]
	delete(r.pendingFlowCredits, port)
	r.flowMu.Unlock()
	if pending > 0 {
		g.AddCredits(pending)
	}
}

func (r *rpc) unregisterFlowGate(port int64, g *FlowGate) {
	r.flowMu.Lock()
	if r.flowGates[port] == g {
		delete(r.flowGates, port)
	}
	r.flowMu.Unlock()
	if g != nil {
		g.close()
	}
}

// receiveFlowCredit decodes a credit grant and applies it to the stream gate.
// If the gate does not exist yet (the credit arrived before the stream request
// was dispatched) the credits are buffered and applied on registration.
func (r *rpc) receiveFlowCredit(payload []byte) {
	if len(payload) < flowCreditPayloadLen {
		slog.Warn("receiveFlowCredit: short payload", "len", len(payload))
		return
	}
	port := int64(binary.LittleEndian.Uint64(payload[0:8]))
	credits := int(int32(binary.LittleEndian.Uint32(payload[8:12])))
	if credits <= 0 {
		return
	}

	r.flowMu.Lock()
	g := r.flowGates[port]
	if g == nil {
		r.pendingFlowCredits[port] += credits
		r.flowMu.Unlock()
		return
	}
	r.flowMu.Unlock()
	g.AddCredits(credits)
}
