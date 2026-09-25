package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nosuta/godash/v2/pb"
	"github.com/nosuta/godash/v2/pusher"
)

var instance *rpc

type rpc struct {
	mu             sync.Mutex
	nativePushPort int64
	pusher         pusher.Pusher
	cancels        map[int64]context.CancelFunc
	// reversePending holds pending Go->Dart->Go ReverseService calls keyed by reverse_port.
	reversePending map[int64]chan []byte
	reverseMu      sync.Mutex
	reverseID      int64
	// flowGates holds per-stream credit gates keyed by the stream port.
	flowGates map[int64]*FlowGate
	// pendingFlowCredits holds credit grants that arrived before the stream's
	// gate was registered, keyed by stream port.
	pendingFlowCredits map[int64]int
	flowMu             sync.Mutex
}

func RPC() *rpc {
	if instance != nil {
		return instance
	}
	instance = &rpc{
		cancels:            make(map[int64]context.CancelFunc, 0),
		reversePending:     make(map[int64]chan []byte),
		flowGates:          make(map[int64]*FlowGate),
		pendingFlowCredits: make(map[int64]int),
	}
	pb.SetReverseCallFn(instance.ReverseCall)
	pb.SetPushFn(instance.Push)
	return instance
}

// entryPoint is called during the Init request to initialize the application layer.
// It is set by the application via SetEntryPoint.
var entryPoint func(string, string) error

// SetEntryPoint registers the application initialization function.
// It receives the database path and the app encryption key.
func SetEntryPoint(fn func(string, string) error) {
	entryPoint = fn
}

// handleRPC dispatches RPC requests to the application service handlers.
// It is set by the application via SetHandleRPC.
var handleRPC func(context.Context, *pb.RpcRequest, chan<- *pb.Response)

// SetHandleRPC registers the application RPC dispatcher.
func SetHandleRPC(fn func(context.Context, *pb.RpcRequest, chan<- *pb.Response)) {
	handleRPC = fn
}

// Pusher returns the global RPC pusher, which sends pb.Push to the bridge.
// It is safe to call after Init has set the underlying pusher via SetPusher.
func Pusher() pusher.Pusher {
	return RPC().Push
}

// streamingPaths holds the RPC paths whose handlers are long-lived server
// streams. A stream must not inherit the caller's short unary deadline, or it
// is cancelled mid-stream (GUI-34). Applications register their streaming
// method paths via RegisterStreamingPath.
var streamingPaths sync.Map // path string -> struct{}

// RegisterStreamingPath marks an RPC path as a long-lived server stream. Call it
// during application initialisation, before the stream is requested.
func RegisterStreamingPath(path string) {
	streamingPaths.Store(path, struct{}{})
}

// IsStreamingPath reports whether path was registered as a long-lived stream.
func IsStreamingPath(path string) bool {
	_, ok := streamingPaths.Load(path)
	return ok
}

func (r *rpc) SetPusher(p func(*pb.Push, int64) error) {
	r.mu.Lock()
	r.pusher = func(push *pb.Push) error {
		return p(push, r.nativePushPort)
	}
	r.mu.Unlock()
}

// Push, [pusher.Pusher] compatible
func (r *rpc) Push(push *pb.Push) error {
	if r.pusher == nil {
		return fmt.Errorf("pusher is nil")
	}
	return r.pusher(push)
}

func (r *rpc) Call(ctx context.Context, req *pb.Request) chan []byte {
	ch := make(chan []byte)

	select {
	case <-ctx.Done():
		close(ch)
		return ch
	default:
	}

	go func() {
		// Streams are long-lived by design: drop the caller's unary deadline so
		// the 10s timeout used by the async envelope cannot kill a subscription
		// mid-stream. Cancellation still works through Cancel/ctx.
		base := ctx
		if rr := req.GetRpcRequest(); rr != nil && IsStreamingPath(rr.Path) {
			base = context.WithoutCancel(ctx)
		}
		r.mu.Lock()
		ctx, r.cancels[req.Port] = context.WithCancel(base)
		r.mu.Unlock()
		defer func() {
			var remained []int64
			r.mu.Lock()
			if cancel, ok := r.cancels[req.Port]; ok {
				cancel()
				delete(r.cancels, req.Port)
			}
			for k := range r.cancels {
				remained = append(remained, k)
			}
			r.mu.Unlock()
			slog.Info("remained ports in cancels", "list", remained)
			close(ch)
		}()

		slog.Info("RPC handle request", "port", req.Port)

		switch v := req.Requests.(type) {
		case *pb.Request_Cancel:
			slog.Info("request: cancel")
			targetPort := req.GetCancel().Port
			r.mu.Lock()
			if cancel, ok := r.cancels[targetPort]; ok {
				cancel()
				delete(r.cancels, targetPort)
			}
			r.mu.Unlock()
		case *pb.Request_Init:
			slog.Info("request: init")
			r.nativePushPort = v.Init.GetPushPort()
			aek := v.Init.GetAppEncryptionKey()
			databasePath := "/database.db"
			supportDir := v.Init.GetSupportDir()
			if supportDir != "" {
				databasePath = supportDir + databasePath
			}
			slog.Info("databasePath", "path", databasePath)
			if entryPoint == nil {
				sendError(ch, fmt.Errorf("entry point not set"), 500)
				break
			}
			if err := entryPoint(databasePath, aek); err != nil {
				sendError(ch, err, 500)
			}
		case *pb.Request_RpcRequest:
			slog.Debug("request: rpc", "path", v.RpcRequest.Path)
			// Reserved control path: the Dart block-backpressure strategy
			// grants producer credits here. Never reaches the dispatcher.
			if v.RpcRequest.Path == FlowCreditPath {
				r.receiveFlowCredit(v.RpcRequest.Payload)
				break
			}
			if handleRPC == nil {
				sendError(ch, fmt.Errorf("RPC handler not set"), 500)
				break
			}
			gate := newFlowGate()
			r.registerFlowGate(req.Port, gate)
			rpcCtx := withFlowGate(ctx, gate)
			rpcCh := make(chan *pb.Response)
			go func() {
				handleRPC(rpcCtx, v.RpcRequest, rpcCh)
				close(rpcCh)
			}()
			for resp := range rpcCh {
				e, err := resp.MarshalVT()
				if err != nil {
					slog.Error("failed to marshal RPC response", "error", err.Error())
					continue
				}
				ch <- e
			}
			r.unregisterFlowGate(req.Port, gate)
		case *pb.Request_ReverseResponse:
			slog.Info("request: reverse_response", "port", v.ReverseResponse.ReversePort)
			r.receiveReverseResponse(v.ReverseResponse.ReversePort, v.ReverseResponse.Payload)
		default:
			err := fmt.Errorf("unsupported request: %T", v)
			sendError(ch, err, 500)
		}
	}()

	return ch
}

// CallSync dispatches a single unary RpcRequest on the calling goroutine and
// returns the marshaled response. Unlike Call it spawns no goroutine and
// registers no port-keyed cancel — it exists for the sync FFI unary fast path
// (PLAN.md P2), where the Dart platform thread blocks until the handler
// returns.
//
// Contract: only short-lived unary handlers may be reached this way. A
// streaming handler that emits more than one response will block on its second
// send (the response channel is buffered for exactly one item) and must use
// Call instead.
func (r *rpc) CallSync(ctx context.Context, payload []byte) ([]byte, error) {
	req := &pb.Request{}
	if err := req.UnmarshalVT(payload); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}
	v, ok := req.Requests.(*pb.Request_RpcRequest)
	if !ok {
		return nil, fmt.Errorf("CallSync supports only RpcRequest, got %T", req.Requests)
	}
	if handleRPC == nil {
		return nil, fmt.Errorf("RPC handler not set")
	}

	ch := make(chan *pb.Response, 1)
	handleRPC(ctx, v.RpcRequest, ch)
	select {
	case resp := <-ch:
		return resp.MarshalVT()
	default:
		return nil, fmt.Errorf("unary handler produced no response")
	}
}

func sendError(ch chan<- []byte, err error, code int32) {
	slog.Error("sending error", "message", err)
	message := ""
	if err != nil {
		message = err.Error()
	}
	resp := &pb.Response{
		Responses: &pb.Response_Error{
			Error: &pb.Error{
				Code:    code,
				Message: message,
			},
		},
	}
	e, er := resp.MarshalVT()
	if er != nil {
		panic(err)
	}
	ch <- e
}

// ReverseCall sends a Push to Dart with a reverse_port set, then blocks until
// Dart replies via a ReverseResponse request or ctx is cancelled.
// Used by generated ReverseService client code.
func (r *rpc) ReverseCall(ctx context.Context, push *pb.Push) ([]byte, error) {
	r.reverseMu.Lock()
	r.reverseID++
	reversePort := r.reverseID
	ch := make(chan []byte, 1)
	r.reversePending[reversePort] = ch
	r.reverseMu.Unlock()

	push.ReversePort = reversePort
	if err := r.Push(push); err != nil {
		r.reverseMu.Lock()
		delete(r.reversePending, reversePort)
		r.reverseMu.Unlock()
		return nil, fmt.Errorf("ReverseCall: push failed: %w", err)
	}

	select {
	case <-ctx.Done():
		r.reverseMu.Lock()
		delete(r.reversePending, reversePort)
		r.reverseMu.Unlock()
		return nil, ctx.Err()
	case payload := <-ch:
		return payload, nil
	}
}

// receiveReverseResponse is called when Dart sends a ReverseResponse back to Go.
func (r *rpc) receiveReverseResponse(port int64, payload []byte) {
	r.reverseMu.Lock()
	ch, ok := r.reversePending[port]
	if ok {
		delete(r.reversePending, port)
	}
	r.reverseMu.Unlock()

	if !ok {
		slog.Warn("receiveReverseResponse: unknown port", "port", port)
		return
	}
	ch <- payload
}
