package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	godashpb "github.com/nosuta/godash/pb"
)

// buildDartRequest builds a request with a normal service (unary hot + server
// stream) plus a Push-prefixed message, and an import of godash/options.proto.
func buildDartRequest(t *testing.T) *pluginpb.CodeGeneratorRequest {
	t.Helper()
	hot := &descriptorpb.MethodOptions{}
	proto.SetExtension(hot, godashpb.E_Hot, true)

	mField := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:   proto.String(name),
			Number: proto.Int32(num),
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
		}
	}

	app := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test.proto"),
		Package:    proto.String("test"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"godash/options.proto"},
		Options:    &descriptorpb.FileOptions{GoPackage: proto.String("example.com/test/pb")},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("M"), Field: []*descriptorpb.FieldDescriptorProto{mField("v", 1)}},
			{Name: proto.String("PushTick"), Field: []*descriptorpb.FieldDescriptorProto{mField("n", 1)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("S"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{Name: proto.String("Do"), InputType: proto.String(".test.M"), OutputType: proto.String(".test.M"), Options: hot},
				{Name: proto.String("Watch"), InputType: proto.String(".test.M"), OutputType: proto.String(".test.M"), ServerStreaming: proto.Bool(true)},
			},
		}},
	}
	options := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("godash/options.proto"),
		Package: proto.String("godash"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("github.com/nosuta/godash/pb")},
	}
	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"test.proto"},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{app, options},
	}
}

func TestDartPluginGeneratesClientAndHandlers(t *testing.T) {
	gen, err := protogen.Options{}.New(buildDartRequest(t))
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	for _, f := range gen.Files {
		if f.Generate {
			generateFile(gen, f)
		}
	}
	resp := gen.Response()
	if resp.GetError() != "" {
		t.Fatalf("plugin error: %s", resp.GetError())
	}
	if len(resp.GetFile()) == 0 {
		t.Fatal("no files generated")
	}
	src := resp.GetFile()[0].GetContent()

	for _, want := range []string{
		"class SRpcClient",
		// Streaming opt-in backpressure parameter.
		"Stream<M> watch(",
		"BackpressurePolicy? backpressure,",
		"backpressure: backpressure,",
		// Hot wrapper.
		"_doHot(",
		"hotRaw('S_Do'",
		// Imports.
		"import 'package:godash/bridge/backpressure.dart';",
		"import 'dart:typed_data';",
		"import 'package:godash/bridge/transport.dart';",
		// Push handler.
		"class PushHandler",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated Dart missing %q:\n%s", want, src)
		}
	}

	// The godash options proto must never be imported (no options.pb.dart).
	if strings.Contains(src, "options.pb.dart") {
		t.Errorf("generated Dart must not import options.pb.dart:\n%s", src)
	}
}
