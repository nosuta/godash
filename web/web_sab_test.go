//go:build js

// Shared-memory (SharedArrayBuffer) worker protocol tests. They run through the
// same wasmbrowsertest harness as web_js_test.go, whose page sets COOP/COEP and
// is therefore cross-origin isolated (SharedArrayBuffer is available):
//
//	(cd cmd/go_js_wasm_exec && go build -o /tmp/go_js_wasm_exec .)
//	GOOS=js GOARCH=wasm go test -exec /tmp/go_js_wasm_exec ./web
package web

import (
	"context"
	"testing"
	"time"

	"syscall/js"

	"github.com/nosuta/godash/v2/pb"
	"github.com/nosuta/godash/v2/rpc"
)

const (
	testRingSlots    = 4
	testRingSlotSize = 1024
)

// sabPort captures postMessage calls, separating numeric ring signals from
// transferable byte envelopes.
func sabPort(signals chan struct{}, frames chan []byte) (js.Value, func()) {
	fn := js.FuncOf(func(this js.Value, args []js.Value) any {
		out := args[0]
		if out.Type() == js.TypeNumber {
			signals <- struct{}{}
			return nil
		}
		b := make([]byte, out.Get("length").Int())
		js.CopyBytesToGo(b, out)
		frames <- b
		return nil
	})
	port := js.Global().Get("Object").New()
	port.Set("postMessage", fn)
	return port, fn.Release
}

func newTestSharedBuffer(t *testing.T, slots, slotSize int) js.Value {
	t.Helper()
	ctor := js.Global().Get("SharedArrayBuffer")
	if ctor.IsUndefined() {
		t.Skip("SharedArrayBuffer unavailable (page is not cross-origin isolated)")
	}
	return ctor.New(sharedPayloadBase + slots*slotSize)
}

// testRingReader mirrors the Dart SharedRingReader so the worker's writes are
// verified exactly as a browser consumer would read them.
type testRingReader struct {
	atomics  js.Value
	ctrl     js.Value
	words    js.Value
	view     js.Value
	slots    int
	slotSize int
	read     int
}

func newTestRingReader(sab js.Value, slots, slotSize int) *testRingReader {
	return &testRingReader{
		atomics:  js.Global().Get("Atomics"),
		ctrl:     js.Global().Get("Int32Array").New(sab, sharedUsedOffset, 1),
		words:    js.Global().Get("Int32Array").New(sab),
		view:     js.Global().Get("Uint8Array").New(sab),
		slots:    slots,
		slotSize: slotSize,
	}
}

func (r *testRingReader) drain() [][]byte {
	pending := r.atomics.Call("load", r.ctrl, sharedUsedOffset).Int()
	frames := make([][]byte, 0, pending)
	for i := 0; i < pending; i++ {
		base := sharedPayloadBase + r.read*r.slotSize
		length := r.words.Index(base / 4).Int()
		payload := make([]byte, length)
		if length > 0 {
			src := r.view.Call("subarray", base+sharedFrameHeader, base+sharedFrameHeader+length)
			js.CopyBytesToGo(payload, src)
		}
		frames = append(frames, payload)
		r.read = (r.read + 1) % r.slots
		r.atomics.Call("sub", r.ctrl, sharedUsedOffset, 1)
	}
	return frames
}

// postRequestSAB drives one request with the optional shared-memory descriptor,
// matching the Dart bridge message layout: [port, bytes, sab, slots, slotSize].
func postRequestSAB(t *testing.T, port, sab js.Value, b []byte, slots, slotSize int) {
	t.Helper()
	u8 := js.Global().Get("Uint8Array").New(len(b))
	if len(b) > 0 {
		js.CopyBytesToJS(u8, b)
	}
	data := js.ValueOf([]any{port, u8, sab, slots, slotSize})
	ev := js.Global().Get("MessageEvent").New("message", js.ValueOf(map[string]any{"data": data}))
	onMessage(js.Global(), []js.Value{ev})
}

func streamHandler(t *testing.T, n int, payloads [][]byte) {
	t.Helper()
	rpc.SetHandleRPC(func(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {
		for i := 0; i < n; i++ {
			ch <- &pb.Response{
				Responses: &pb.Response_RpcResponse{
					RpcResponse: &pb.RpcResponse{Payload: payloads[i]},
				},
			}
		}
	})
}

func sabRequest(t *testing.T, port js.Value, path string) []byte {
	t.Helper()
	req := &pb.Request{
		Port:     200,
		Requests: &pb.Request_RpcRequest{RpcRequest: &pb.RpcRequest{Path: path}},
	}
	b, err := req.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSharedRingStreamThenDone(t *testing.T) {
	resetWebHandlers(t)
	payloads := [][]byte{[]byte("a"), []byte("bb"), []byte("ccc")}
	streamHandler(t, len(payloads), payloads)

	signals := make(chan struct{}, 16)
	frames := make(chan []byte, 16)
	port, release := sabPort(signals, frames)
	defer release()
	sab := newTestSharedBuffer(t, testRingSlots, testRingSlotSize)
	reader := newTestRingReader(sab, testRingSlots, testRingSlotSize)

	postRequestSAB(t, port, sab, sabRequest(t, port, "/sab-stream"), testRingSlots, testRingSlotSize)

	// One signal per frame, including the final Done. Nothing should fall back
	// to the envelope while the ring has room.
	wantSignals := len(payloads) + 1
	for i := 0; i < wantSignals; i++ {
		select {
		case <-signals:
		case b := <-frames:
			t.Fatalf("unexpected envelope fallback: %x", b)
		case <-time.After(jsTestTimeout):
			t.Fatalf("timed out after %d signals", i)
		}
	}
	select {
	case b := <-frames:
		t.Fatalf("unexpected envelope fallback: %x", b)
	default:
	}

	got := reader.drain()
	if len(got) != wantSignals {
		t.Fatalf("drained %d frames, want %d", len(got), wantSignals)
	}
	for i, payload := range payloads {
		resp := &pb.Response{}
		if err := resp.UnmarshalVT(got[i]); err != nil {
			t.Fatalf("frame %d: unmarshal: %v", i, err)
		}
		if string(resp.GetRpcResponse().GetPayload()) != string(payload) {
			t.Fatalf("frame %d out of order: %q", i, resp.GetRpcResponse().GetPayload())
		}
	}
	done := &pb.Response{}
	if err := done.UnmarshalVT(got[wantSignals-1]); err != nil {
		t.Fatalf("done frame: unmarshal: %v", err)
	}
	if done.GetDone() == nil {
		t.Fatalf("last frame is not Done: %+v", done)
	}
}

func TestSharedRingFallsBackWhenFull(t *testing.T) {
	resetWebHandlers(t)
	const slots = 1
	payloads := [][]byte{[]byte("first"), []byte("second")}
	streamHandler(t, len(payloads), payloads)

	signals := make(chan struct{}, 16)
	frames := make(chan []byte, 16)
	port, release := sabPort(signals, frames)
	defer release()
	sab := newTestSharedBuffer(t, slots, testRingSlotSize)

	// Mark the ring full and never drain it, so every frame (and Done) must
	// fall back to byte envelopes.
	ctrl := js.Global().Get("Int32Array").New(sab, sharedUsedOffset, 1)
	js.Global().Get("Atomics").Call("store", ctrl, sharedUsedOffset, slots)

	postRequestSAB(t, port, sab, sabRequest(t, port, "/sab-full"), slots, testRingSlotSize)

	for i, payload := range payloads {
		resp := recvResponse(t, frames)
		if string(resp.GetRpcResponse().GetPayload()) != string(payload) {
			t.Fatalf("frame %d: unexpected fallback payload %q", i, resp.GetRpcResponse().GetPayload())
		}
	}
	done := recvResponse(t, frames)
	if done.GetDone() == nil {
		t.Fatalf("expected Done envelope, got %+v", done)
	}
	select {
	case <-signals:
		t.Fatal("expected no ring signals while the ring is full")
	default:
	}
}

func TestSharedRingFallsBackWhenFrameTooLarge(t *testing.T) {
	resetWebHandlers(t)
	const slotSize = 64
	large := make([]byte, slotSize*2)
	streamHandler(t, 1, [][]byte{large})

	signals := make(chan struct{}, 16)
	frames := make(chan []byte, 16)
	port, release := sabPort(signals, frames)
	defer release()
	sab := newTestSharedBuffer(t, testRingSlots, slotSize)
	reader := newTestRingReader(sab, testRingSlots, slotSize)

	postRequestSAB(t, port, sab, sabRequest(t, port, "/sab-large"), testRingSlots, slotSize)

	big := recvResponse(t, frames)
	if len(big.GetRpcResponse().GetPayload()) != len(large) {
		t.Fatalf("unexpected fallback payload size: %d", len(big.GetRpcResponse().GetPayload()))
	}
	select {
	case <-signals:
	case <-time.After(jsTestTimeout):
		t.Fatal("expected Done as a ring signal")
	}
	ringFrames := reader.drain()
	if len(ringFrames) != 1 {
		t.Fatalf("drained %d ring frames, want 1", len(ringFrames))
	}
	done := &pb.Response{}
	if err := done.UnmarshalVT(ringFrames[0]); err != nil {
		t.Fatal(err)
	}
	if done.GetDone() == nil {
		t.Fatalf("expected Done in the ring, got %+v", done)
	}
}
