package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	godashpb "github.com/nosuta/godash/v2/pb"
)

// buildGoRequest builds a CodeGeneratorRequest for a single service `S` with a
// unary `Do` method and optionally a server-streaming `Watch` method. The
// message field type controls hot-path eligibility.
func buildGoRequest(t *testing.T, fieldType descriptorpb.FieldDescriptorProto_Type, hot, streaming bool) *pluginpb.CodeGeneratorRequest {
	t.Helper()
	methodOpts := &descriptorpb.MethodOptions{}
	if hot {
		proto.SetExtension(methodOpts, godashpb.E_Hot, true)
	}
	methods := []*descriptorpb.MethodDescriptorProto{{
		Name:       proto.String("Do"),
		InputType:  proto.String(".test.M"),
		OutputType: proto.String(".test.M"),
		Options:    methodOpts,
	}}
	if streaming {
		methods = append(methods, &descriptorpb.MethodDescriptorProto{
			Name:            proto.String("Watch"),
			InputType:       proto.String(".test.M"),
			OutputType:      proto.String(".test.M"),
			ServerStreaming: proto.Bool(true),
		})
	}
	fd := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test.proto"),
		Package: proto.String("test"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.com/test/pb")},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:   proto.String("v"),
				Number: proto.Int32(1),
				Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:   fieldType.Enum(),
			}},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name:   proto.String("S"),
			Method: methods,
		}},
	}
	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"test.proto"},
		Parameter:      proto.String("module=example.com/test"),
		ProtoFile:      []*descriptorpb.FileDescriptorProto{fd},
	}
}

func generateGoFiles(t *testing.T, req *pluginpb.CodeGeneratorRequest) map[string]string {
	t.Helper()
	gen, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	for _, f := range gen.Files {
		if !f.Generate {
			continue
		}
		generateFile(gen, f, "github.com/nosuta/godash/v2/pb")
	}
	resp := gen.Response()
	if resp.GetError() != "" {
		t.Fatalf("plugin error: %s", resp.GetError())
	}
	out := map[string]string{}
	for _, f := range resp.GetFile() {
		out[f.GetName()] = f.GetContent()
	}
	return out
}

func findSuffix(files map[string]string, suffix string) (string, bool) {
	for name, content := range files {
		if strings.HasSuffix(name, suffix) {
			return content, true
		}
	}
	return "", false
}

func TestGoPluginGeneratesHandlerAndHotFile(t *testing.T) {
	files := generateGoFiles(t, buildGoRequest(t,
		descriptorpb.FieldDescriptorProto_TYPE_INT32, true, true))

	godash, ok := findSuffix(files, ".godash.go")
	if !ok {
		t.Fatalf("missing .godash.go; files: %v", keys(files))
	}
	for _, want := range []string{
		"type SRPCHandler interface",
		"func HandleSRPC(",
	} {
		if !strings.Contains(godash, want) {
			t.Errorf(".godash.go missing %q", want)
		}
	}
	// The stream method must keep the channel-based handler signature.
	if !strings.Contains(godash, "ch chan<- *core.Response") {
		t.Errorf(".godash.go should contain the streaming handler signature:\n%s", godash)
	}

	hot, ok := findSuffix(files, ".hot.go")
	if !ok {
		t.Fatalf("missing .hot.go; files: %v", keys(files))
	}
	for _, want := range []string{
		"//go:build !js",
		"func GodashHot_S_Do(",
		"unsafe.Add(reqPtr, 0)",
		"unsafe.Add(respPtr, 0)",
	} {
		if !strings.Contains(hot, want) {
			t.Errorf(".hot.go missing %q:\n%s", want, hot)
		}
	}
}

func TestGoPluginSkipsIneligibleHotMethod(t *testing.T) {
	// A string field makes M unpackable, so the hot file must not be emitted.
	files := generateGoFiles(t, buildGoRequest(t,
		descriptorpb.FieldDescriptorProto_TYPE_STRING, true, false))
	if _, ok := findSuffix(files, ".hot.go"); ok {
		t.Fatalf("ineligible hot method must not produce a .hot.go; files: %v", keys(files))
	}
	if _, ok := findSuffix(files, ".godash.go"); !ok {
		t.Fatalf("the service handler must still be generated; files: %v", keys(files))
	}
}

func TestGoPluginNoHotWhenOptionAbsent(t *testing.T) {
	files := generateGoFiles(t, buildGoRequest(t,
		descriptorpb.FieldDescriptorProto_TYPE_INT32, false, false))
	if _, ok := findSuffix(files, ".hot.go"); ok {
		t.Fatalf("a scalar method without the option must not produce a .hot.go; files: %v", keys(files))
	}
}

// TestGeneratedHotFileCompiles writes the generated .hot.go next to stub
// message/handler types and runs `go build`, catching syntax/typing errors in
// the generated code (C3).
func TestGeneratedHotFileCompiles(t *testing.T) {
	files := generateGoFiles(t, buildGoRequest(t,
		descriptorpb.FieldDescriptorProto_TYPE_INT32, true, false))
	hot, ok := findSuffix(files, ".hot.go")
	if !ok {
		t.Fatalf("missing .hot.go; files: %v", keys(files))
	}

	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/test\n\ngo 1.26\n")
	write("types.go", `package pb

import "context"

type M struct {
	V int32
}

type SRPCHandler interface {
	Do(ctx context.Context, req *M) (*M, error)
}
`)
	write("hot.go", hot)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GO111MODULE=on", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated .hot.go does not compile: %v\n%s\n---\n%s", err, out, hot)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
