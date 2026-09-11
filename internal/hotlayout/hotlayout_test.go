package hotlayout

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// message builds a single-message file descriptor with the given fields and
// returns the message descriptor. Field numbers are assigned in order.
func message(t *testing.T, fields ...*descriptorpb.FieldDescriptorProto) protoreflect.MessageDescriptor {
	t.Helper()
	for i, f := range fields {
		if f.Number == nil {
			f.Number = proto.Int32(int32(i + 1))
		}
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("hotlayout_test.proto"),
		Package: proto.String("hotlayout"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("M"), Field: fields},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	return fd.Messages().Get(0)
}

func scalar(name string, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:  proto.String(name),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:  typ.Enum(),
	}
}

func TestComputeMixedAlignment(t *testing.T) {
	m := message(t,
		scalar("a", descriptorpb.FieldDescriptorProto_TYPE_INT32),
		scalar("b", descriptorpb.FieldDescriptorProto_TYPE_INT64),
	)
	got, err := Compute(m)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got.Size != 16 {
		t.Fatalf("size = %d, want 16", got.Size)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(got.Fields))
	}
	if got.Fields[0].Offset != 0 || got.Fields[0].GoType != "int32" {
		t.Fatalf("field a = %+v", got.Fields[0])
	}
	if got.Fields[1].Offset != 8 || got.Fields[1].GoType != "int64" {
		t.Fatalf("field b = %+v", got.Fields[1])
	}
}

func TestComputePadsToAlignment(t *testing.T) {
	m := message(t,
		scalar("flag", descriptorpb.FieldDescriptorProto_TYPE_BOOL),
		scalar("n", descriptorpb.FieldDescriptorProto_TYPE_INT32),
	)
	got, err := Compute(m)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got.Fields[0].Offset != 0 || got.Fields[0].Size != 1 {
		t.Fatalf("bool field = %+v", got.Fields[0])
	}
	if got.Fields[1].Offset != 4 {
		t.Fatalf("int32 field offset = %d, want 4", got.Fields[1].Offset)
	}
	if got.Size != 8 {
		t.Fatalf("size = %d, want 8", got.Size)
	}
}

func TestComputeSortedByFieldNumber(t *testing.T) {
	a := scalar("a", descriptorpb.FieldDescriptorProto_TYPE_INT32)
	a.Number = proto.Int32(2)
	b := scalar("b", descriptorpb.FieldDescriptorProto_TYPE_INT32)
	b.Number = proto.Int32(1)
	got, err := Compute(message(t, a, b))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got.Fields[0].Desc.Name() != "b" || got.Fields[1].Desc.Name() != "a" {
		t.Fatalf("fields not sorted by number: %v, %v", got.Fields[0].Desc.Name(), got.Fields[1].Desc.Name())
	}
}

func TestComputeEmptyMessage(t *testing.T) {
	got, err := Compute(message(t))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got.Size != 0 || len(got.Fields) != 0 {
		t.Fatalf("empty message = %+v", got)
	}
}

func TestComputeFloats(t *testing.T) {
	got, err := Compute(message(t,
		scalar("f", descriptorpb.FieldDescriptorProto_TYPE_FLOAT),
		scalar("d", descriptorpb.FieldDescriptorProto_TYPE_DOUBLE),
	))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got.Fields[0].GoType != "float32" || got.Fields[0].Size != 4 {
		t.Fatalf("float = %+v", got.Fields[0])
	}
	if got.Fields[1].GoType != "float64" || got.Fields[1].Offset != 8 || got.Size != 16 {
		t.Fatalf("double = %+v size=%d", got.Fields[1], got.Size)
	}
}

func TestComputeRejectsUnsupported(t *testing.T) {
	cases := map[string]protoreflect.MessageDescriptor{
		"string": message(t, scalar("s", descriptorpb.FieldDescriptorProto_TYPE_STRING)),
		"bytes":  message(t, scalar("b", descriptorpb.FieldDescriptorProto_TYPE_BYTES)),
	}
	for name, m := range cases {
		if _, err := Compute(m); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestComputeRejectsRepeated(t *testing.T) {
	f := scalar("xs", descriptorpb.FieldDescriptorProto_TYPE_INT32)
	f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	if _, err := Compute(message(t, f)); err == nil {
		t.Fatal("repeated int32 should be rejected")
	}
}
