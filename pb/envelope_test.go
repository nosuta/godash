package pb

import (
	"bytes"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestRequestRoundTrip verifies the envelope wire format for every Request
// oneof branch (PLAN compatibility boundary).
func TestRequestRoundTrip(t *testing.T) {
	cases := []*Request{
		{Port: 7, Requests: &Request_Init{Init: &Init{
			PushPort:         1,
			TempDir:          "/tmp",
			SupportDir:       "/support",
			DocumentsDir:     "/docs",
			AppEncryptionKey: "secret",
		}}},
		{Port: 8, Requests: &Request_Cancel{Cancel: &Cancel{
			Port: 3, Code: 1, Message: "cancel",
		}}},
		{Port: 9, Requests: &Request_RpcRequest{RpcRequest: &RpcRequest{
			Path: "/svc/method", Payload: []byte{0, 1, 2, 255},
		}}},
		{Port: 10, Requests: &Request_ReverseResponse{ReverseResponse: &ReverseResponse{
			ReversePort: 4, Payload: []byte("reply"),
		}}},
		{Port: 11}, // no oneof set
	}
	for i, want := range cases {
		b, err := want.MarshalVT()
		if err != nil {
			t.Fatalf("case %d: MarshalVT: %v", i, err)
		}
		var got Request
		if err := got.UnmarshalVT(b); err != nil {
			t.Fatalf("case %d: UnmarshalVT: %v", i, err)
		}
		if !proto.Equal(want, &got) {
			t.Fatalf("case %d: round trip mismatch\n want %v\n got  %v", i, want, &got)
		}
	}
}

// TestResponseRoundTrip verifies the envelope wire format for every Response
// oneof branch.
func TestResponseRoundTrip(t *testing.T) {
	cases := []*Response{
		{Responses: &Response_Error{Error: &Error{Code: 404, Message: "nope"}}},
		{Responses: &Response_Done{Done: &Done{Code: 1, Message: "done"}}},
		{Responses: &Response_Push{Push: &Push{
			Type: "pb.Tick", Payload: []byte{9, 9}, ReversePort: 42,
		}}},
		{Responses: &Response_RpcResponse{RpcResponse: &RpcResponse{
			Payload: []byte("payload"),
		}}},
		{}, // no oneof set
	}
	for i, want := range cases {
		b, err := want.MarshalVT()
		if err != nil {
			t.Fatalf("case %d: MarshalVT: %v", i, err)
		}
		var got Response
		if err := got.UnmarshalVT(b); err != nil {
			t.Fatalf("case %d: UnmarshalVT: %v", i, err)
		}
		if !proto.Equal(want, &got) {
			t.Fatalf("case %d: round trip mismatch\n want %v\n got  %v", i, want, &got)
		}
	}
}

func TestMarshalHelper(t *testing.T) {
	resp := &Response{Responses: &Response_RpcResponse{
		RpcResponse: &RpcResponse{Payload: []byte("hi")},
	}}
	b := MarshalHelper(resp)
	if len(b) == 0 {
		t.Fatal("MarshalHelper returned no bytes")
	}
	var got Response
	if err := got.UnmarshalVT(b); err != nil {
		t.Fatalf("UnmarshalVT: %v", err)
	}
	if !bytes.Equal(got.GetRpcResponse().GetPayload(), []byte("hi")) {
		t.Fatalf("unexpected payload: %q", got.GetRpcResponse().GetPayload())
	}
}

// TestUnmarshalTruncated ensures malformed input fails without panicking.
func TestUnmarshalTruncated(t *testing.T) {
	var req Request
	if err := req.UnmarshalVT([]byte{0xFF, 0xFF, 0xFF, 0xFF}); err == nil {
		t.Fatal("expected an error for malformed input")
	}
}

// TestHotOptionExtension verifies the (godash.hot) method option round-trips.
func TestHotOptionExtension(t *testing.T) {
	opts := &descriptorpb.MethodOptions{}
	if v, _ := proto.GetExtension(opts, E_Hot).(bool); v {
		t.Fatal("default hot option should be false")
	}
	proto.SetExtension(opts, E_Hot, true)
	if v, _ := proto.GetExtension(opts, E_Hot).(bool); !v {
		t.Fatal("hot option should read back as true")
	}
}

// TestAllMessagesHaveVTWrappers guards cmd/gen_marshal_std: every message in
// core.proto must have MarshalVT/UnmarshalVT methods in the native (!js) build.
func TestAllMessagesHaveVTWrappers(t *testing.T) {
	var checked int
	var missing []string
	protoregistry.GlobalTypes.RangeMessages(func(mt protoreflect.MessageType) bool {
		desc := mt.Descriptor()
		if desc.ParentFile().Path() != "core.proto" {
			return true
		}
		checked++
		rt := reflect.TypeOf(mt.New().Interface())
		if _, ok := rt.MethodByName("MarshalVT"); !ok {
			missing = append(missing, string(desc.FullName())+".MarshalVT")
		}
		if _, ok := rt.MethodByName("UnmarshalVT"); !ok {
			missing = append(missing, string(desc.FullName())+".UnmarshalVT")
		}
		return true
	})
	if checked == 0 {
		t.Fatal("no core.proto messages found in the global registry")
	}
	if len(missing) > 0 {
		t.Fatalf("missing VT wrappers: %v", missing)
	}
}

// TestUnmarshalRobustness feeds malformed and adversarial inputs; unmarshalling
// must never panic (errors are fine).
func TestUnmarshalRobustness(t *testing.T) {
	inputs := [][]byte{
		nil,
		{},
		{0x00},
		{0xFF},
		{0x08},                         // field 1 varint, no value
		{0x08, 0xFF, 0xFF, 0xFF},       // truncated varint
		{0x7A, 0xFF, 0xFF, 0xFF, 0x0F}, // huge length prefix
		bytes.Repeat([]byte{0xFF}, 64),
	}
	// Deterministic pseudo-random noise.
	seed := uint32(12345)
	for i := 0; i < 64; i++ {
		b := make([]byte, i)
		for j := range b {
			seed = seed*1664525 + 1013904223
			b[j] = byte(seed >> 16)
		}
		inputs = append(inputs, b)
	}

	for i, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("input %d panicked: %v", i, r)
				}
			}()
			var req Request
			var resp Response
			_ = req.UnmarshalVT(in)
			_ = resp.UnmarshalVT(in)
		}()
	}
}
