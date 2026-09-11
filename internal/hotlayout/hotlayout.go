// Package hotlayout computes the packed fixed-layout representation used by
// the godash hot-path FFI bridge (PLAN.md P3).
//
// Both the Go and the Dart protoc plugins use this package so the byte layout
// of a hot method's request/response is guaranteed to match on both sides of
// the FFI boundary. Only fixed-layout scalar fields are supported; anything
// else makes the message (and therefore the method) ineligible, and callers
// fall back to the normal protobuf envelope.
//
// Layout rules (identical to a C struct with natural alignment):
//   - fields are laid out in ascending field-number order,
//   - each field is aligned to its own size (1, 4 or 8 bytes),
//   - the total size is padded to the largest field alignment,
//   - multi-byte scalars use the native (little-endian) byte order.
package hotlayout

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Field is one packed field: its descriptor, byte offset and generated Go type.
type Field struct {
	Desc   protoreflect.FieldDescriptor
	Number protoreflect.FieldNumber
	Offset int
	Size   int
	// GoType is one of bool, int32, uint32, int64, uint64, float32, float64.
	GoType string
}

// Layout is the packed representation of a message.
type Layout struct {
	Fields []Field
	// Size is the total buffer size in bytes (already padded).
	Size int
}

// Compute returns the packed layout of m, or an error explaining why the
// message cannot be packed.
func Compute(m protoreflect.MessageDescriptor) (Layout, error) {
	src := m.Fields()
	fields := make([]protoreflect.FieldDescriptor, 0, src.Len())
	for i := 0; i < src.Len(); i++ {
		fields = append(fields, src.Get(i))
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Number() < fields[j].Number() })

	var out Layout
	off := 0
	align := 1
	for _, f := range fields {
		goType, size, err := scalarType(f)
		if err != nil {
			return Layout{}, fmt.Errorf("field %s: %w", f.Name(), err)
		}
		off = alignUp(off, size)
		out.Fields = append(out.Fields, Field{
			Desc:   f,
			Number: f.Number(),
			Offset: off,
			Size:   size,
			GoType: goType,
		})
		off += size
		if size > align {
			align = size
		}
	}
	out.Size = alignUp(off, align)
	return out, nil
}

// Eligible reports whether m can be packed.
func Eligible(m protoreflect.MessageDescriptor) bool {
	_, err := Compute(m)
	return err == nil
}

func scalarType(f protoreflect.FieldDescriptor) (string, int, error) {
	if f.IsMap() {
		return "", 0, fmt.Errorf("map fields are not supported")
	}
	if f.IsList() {
		return "", 0, fmt.Errorf("repeated fields are not supported")
	}
	if f.ContainingOneof() != nil {
		return "", 0, fmt.Errorf("oneof fields are not supported")
	}
	if f.HasPresence() {
		return "", 0, fmt.Errorf("explicit presence (optional/message) is not supported")
	}
	switch f.Kind() {
	case protoreflect.BoolKind:
		return "bool", 1, nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32", 4, nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32", 4, nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64", 8, nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64", 8, nil
	case protoreflect.FloatKind:
		return "float32", 4, nil
	case protoreflect.DoubleKind:
		return "float64", 8, nil
	default:
		return "", 0, fmt.Errorf("unsupported kind %s", f.Kind())
	}
}

func alignUp(x, a int) int {
	if a <= 1 {
		return x
	}
	return (x + a - 1) / a * a
}
