// Package web provides the Web Worker entry point for godash.

//go:build js

package web

import (
	"syscall/js"

	"github.com/nosuta/godash/v2/pb"
)

// Shared-memory ring layout (mirrored by lib/bridge/shared_ring.dart):
//
//	offset 0             int32  atomically-managed count of unread frames
//	offset 16 + i*slot   int32  little-endian frame length, then payload
//
// A frame written to the ring is signalled with a bare numeric postMessage on
// the stream's MessagePort. Frames that do not fit (or a full ring) fall back
// to the existing transferable-envelope postMessage, so the consumer never
// observes reordering: the worker emits exactly one message per frame, in
// order, and the consumer only drains the ring on numeric signals.
const (
	sharedUsedOffset  = 0
	sharedPayloadBase = 16
	sharedFrameHeader = 4
)

// responseSink abstracts where worker responses go: the envelope (default) or
// a shared-memory ring when the request opted in.
type responseSink interface {
	send(message []byte)
	done()
}

// envelopeSink posts each frame as a transferable Uint8Array envelope.
type envelopeSink struct{ port js.Value }

func (s envelopeSink) send(message []byte) { postMessage(s.port, message) }
func (s envelopeSink) done()               { postDone(s.port) }

// sharedSink publishes frames into the shared ring and falls back to the
// envelope when a frame cannot be published.
type sharedSink struct {
	port js.Value
	ring *sharedRing
}

func (s sharedSink) send(message []byte) {
	if s.ring.write(s.port, message) {
		return
	}
	postMessage(s.port, message)
}

func (s sharedSink) done() {
	done, err := (&pb.Response{Responses: &pb.Response_Done{Done: &pb.Done{}}}).MarshalVT()
	if err != nil || !s.ring.write(s.port, done) {
		postDone(s.port)
	}
}

// newResponseSink reads the optional shared-memory ring descriptor from the
// worker message. Messages without a descriptor keep the envelope path.
func newResponseSink(data js.Value, port js.Value) responseSink {
	sab := data.Index(2)
	if sab.Type() != js.TypeObject || sab.Get("byteLength").IsUndefined() {
		return envelopeSink{port: port}
	}
	slots := data.Index(3).Int()
	slotSize := data.Index(4).Int()
	if slots <= 0 || slotSize <= sharedFrameHeader || slotSize%4 != 0 {
		return envelopeSink{port: port}
	}
	required := sharedPayloadBase + slots*slotSize
	if sab.Get("byteLength").Int() < required {
		return envelopeSink{port: port}
	}
	return sharedSink{port: port, ring: newSharedRing(sab, slots, slotSize)}
}

// sharedRing writes framed responses into a SharedArrayBuffer. Exactly one
// worker goroutine owns a ring, so the only cross-thread state is the atomic
// frame counter; the write cursor stays in Go and the read cursor in Dart.
type sharedRing struct {
	atomics  js.Value
	ctrl     js.Value // Int32Array over the frame counter
	words    js.Value // Int32Array over the whole buffer, for length headers
	view     js.Value // Uint8Array over the whole buffer, for payloads
	slots    int
	slotSize int
	cursor   int
}

func newSharedRing(sab js.Value, slots, slotSize int) *sharedRing {
	return &sharedRing{
		atomics:  js.Global().Get("Atomics"),
		ctrl:     js.Global().Get("Int32Array").New(sab, sharedUsedOffset, 1),
		words:    js.Global().Get("Int32Array").New(sab),
		view:     js.Global().Get("Uint8Array").New(sab),
		slots:    slots,
		slotSize: slotSize,
	}
}

// write attempts to publish message into the ring. It returns false when the
// frame cannot be published (too large or the ring is full), in which case the
// caller must fall back to a transferable envelope postMessage.
func (r *sharedRing) write(port js.Value, message []byte) bool {
	if len(message) > r.slotSize-sharedFrameHeader {
		return false
	}
	if r.atomics.Call("load", r.ctrl, sharedUsedOffset).Int() >= r.slots {
		return false
	}
	base := sharedPayloadBase + r.cursor*r.slotSize
	// The payload base is 16-byte aligned and slotSize is a multiple of 4, so
	// the length prefix is 4-byte aligned and a plain write is safe. The frame
	// counter add below publishes the write to the consumer.
	r.words.SetIndex(base/4, len(message))
	if len(message) > 0 {
		dst := r.view.Call("subarray", base+sharedFrameHeader, base+sharedFrameHeader+len(message))
		js.CopyBytesToJS(dst, message)
	}
	r.atomics.Call("add", r.ctrl, sharedUsedOffset, 1)
	r.cursor = (r.cursor + 1) % r.slots
	postSignal(port)
	return true
}

// postSignal wakes the consumer without transferring any payload bytes.
func postSignal(port js.Value) {
	port.Call("postMessage", 1)
}
