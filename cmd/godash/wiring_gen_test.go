package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleHot() []hotInfo {
	return []hotInfo{
		{Name: "CalcService_Add", ServerVar: "calcServer"},
		{Name: "EchoService_Ping", ServerVar: "echoServer"},
	}
}

func TestRenderHotHandlerWithMethods(t *testing.T) {
	got := renderHotHandler(moduleInfo{Name: "flap", PbAlias: "flappb"}, sampleHot())
	for _, want := range []string{
		"//go:build !js",
		"func HotInvoke(",
		`case "CalcService_Add":`,
		"flappb.GodashHot_CalcService_Add(ctx, calcServer, reqPtr, respPtr)",
		`case "EchoService_Ping":`,
		"flappb.GodashHot_EchoService_Ping(ctx, echoServer, reqPtr, respPtr)",
		`flappb "flap/pb"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderHotHandler missing %q\n---\n%s", want, got)
		}
	}
}

func TestRenderHotHandlerWithoutMethods(t *testing.T) {
	got := renderHotHandler(moduleInfo{Name: "flap", PbAlias: "flappb"}, nil)
	if !strings.Contains(got, "//go:build !js") {
		t.Errorf("missing build tag:\n%s", got)
	}
	if strings.Contains(got, "flap/pb") {
		t.Errorf("no hot methods should not import the project pb package:\n%s", got)
	}
	if !strings.Contains(got, `return fmt.Errorf("hot method not found: %s", name)`) {
		t.Errorf("missing not-found error:\n%s", got)
	}
}

func TestRenderMainGoIncludesHotExports(t *testing.T) {
	got := renderMainGo(moduleInfo{Name: "flap"}, sampleHot())
	for _, want := range []string{
		"//export CalcService_Add",
		"func CalcService_Add(req unsafe.Pointer, resp unsafe.Pointer) C.int32_t",
		`flaprpc.HotInvoke(context.Background(), "CalcService_Add", req, resp)`,
		"//export EchoService_Ping",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderMainGo missing %q", want)
		}
	}
	plain := renderMainGo(moduleInfo{Name: "flap"}, nil)
	if strings.Contains(plain, "//export CalcService_Add") {
		t.Error("renderMainGo must not emit hot exports without hot methods")
	}
}

func TestRenderMainGoZeroCopyRequest(t *testing.T) {
	got := renderMainGo(moduleInfo{Name: "flap"}, nil)
	if strings.Contains(got, "C.GoBytes(") {
		t.Error("renderMainGo must not copy the request via C.GoBytes (PLAN.md P4)")
	}
	const slice = "unsafe.Slice((*byte)(payload.message)"
	if c := strings.Count(got, slice); c != 2 {
		t.Errorf("expected 2 zero-copy request slices (RPC + CallSync), got %d", c)
	}
}

func TestWriteWiringFilesIdempotent(t *testing.T) {
	root := t.TempDir()
	goDir := filepath.Join(root, "go")
	pbDir := filepath.Join(goDir, "pb")
	if err := os.MkdirAll(pbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module flap\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flap := `package pb

type CalcRPCHandler interface {
	Add(ctx context.Context, req *AddRequest) (*AddResponse, error)
}

func HandleCalcRPC(ctx context.Context, req *core.RpcRequest, ch chan<- *core.Response, handler CalcRPCHandler) bool {
	return false
}
`
	hot := `package pb

import (
	"context"
	"unsafe"
)

func GodashHot_CalcService_Add(ctx context.Context, handler CalcRPCHandler, reqPtr unsafe.Pointer, respPtr unsafe.Pointer) error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(pbDir, "calc.flap.go"), []byte(flap), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pbDir, "calc.hot.go"), []byte(hot), 0o644); err != nil {
		t.Fatal(err)
	}

	generated := []string{
		"go/main.go", "go/main_js.go", "go/main_debug.go", "go/main_release.go",
		"go/main_js_debug.go", "go/main_js_release.go",
		"go/rpc/rpc_handler.go", "go/rpc/hot_handler.go", "go/rpc/entrypoint.go",
	}
	readAll := func() map[string]string {
		out := map[string]string{}
		for _, rel := range generated {
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			out[rel] = string(b)
		}
		return out
	}

	if err := writeWiringFiles(root, ""); err != nil {
		t.Fatalf("writeWiringFiles: %v", err)
	}
	first := readAll()

	mainGo := first["go/main.go"]
	for _, want := range []string{
		"//export CalcService_Add",
		`flaprpc.HotInvoke(context.Background(), "CalcService_Add"`,
		"unsafe.Slice((*byte)(payload.message)",
	} {
		if !strings.Contains(mainGo, want) {
			t.Errorf("main.go missing %q", want)
		}
	}
	if !strings.Contains(first["go/rpc/hot_handler.go"], `case "CalcService_Add":`) {
		t.Errorf("hot_handler.go missing the CalcService_Add case")
	}
	if !strings.Contains(first["go/rpc/rpc_handler.go"], "calcServer") {
		t.Errorf("rpc_handler.go should wire calcServer")
	}

	// Running again must be idempotent (entrypoint.go is user-owned but is only
	// created once).
	if err := writeWiringFiles(root, ""); err != nil {
		t.Fatalf("second writeWiringFiles: %v", err)
	}
	second := readAll()
	for _, rel := range generated {
		if first[rel] != second[rel] {
			t.Errorf("%s changed between runs", rel)
		}
	}
}

// TestRenderMainJsVariantsComplementary guards the js build tags: a plain
// `GOOS=js GOARCH=wasm go build` (no -tags) and a TinyGo build must each get
// exactly one func main, otherwise the linker fails with
// "function main is undeclared in the main package".
func TestRenderMainJsVariantsComplementary(t *testing.T) {
	mod := moduleInfo{Name: "flap"}
	debug := renderMainJsBuildVariant(mod, "debug")
	release := renderMainJsBuildVariant(mod, "release")

	if !strings.Contains(debug, "//go:build js && debug") {
		t.Errorf("debug variant must be tagged `js && debug`:\n%s", debug)
	}
	if !strings.Contains(release, "//go:build js && !debug") {
		t.Errorf("release variant must be tagged `js && !debug` so a plain GOOS=js build has a main:\n%s", release)
	}
	for name, s := range map[string]string{"debug": debug, "release": release} {
		if !strings.Contains(s, "func main()") {
			t.Errorf("%s js variant is missing func main():\n%s", name, s)
		}
		if !strings.Contains(s, "flaprpc.Close()") {
			t.Errorf("%s js variant must call flaprpc.Close() (the project entrypoint):\n%s", name, s)
		}
		if strings.Contains(s, "\trpc.Close()") {
			t.Errorf("%s js variant references undefined rpc.Close():\n%s", name, s)
		}
	}
}

// TestPatchNativeLinkerHotSymbols verifies that discoverd hot-path exports are
// force-linked into the materialised native_internal SPM manifests so the
// Apple linker does not dead-strip them (dlsym would fail otherwise).
func TestPatchNativeLinkerHotSymbols(t *testing.T) {
	root := t.TempDir()
	const src = `linkerSettings: [
                .unsafeFlags(["-Xlinker", "-u", "-Xlinker", "_FreeBytesContainer"])//GODASH_HOT_LINKER_FLAGS
            ]`
	rels := []string{
		filepath.Join(".godash", "native_internal", "ios", "native_internal", "Package.swift"),
		filepath.Join(".godash", "native_internal", "macos", "native_internal", "Package.swift"),
	}
	for _, rel := range rels {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := patchNativeLinkerHotSymbols(root, sampleHot()); err != nil {
		t.Fatalf("patchNativeLinkerHotSymbols: %v", err)
	}
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, godashHotLinkerMarker) {
			t.Errorf("%s: marker should have been replaced", rel)
		}
		for _, sym := range []string{"CalcService_Add", "EchoService_Ping"} {
			if !strings.Contains(s, `"-Xlinker", "_`+sym+`"`) {
				t.Errorf("%s: missing force-link for %s:\n%s", rel, sym, s)
			}
		}
	}
}

func TestPatchNativeLinkerHotSymbolsEmpty(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".godash", "native_internal", "ios", "native_internal", "Package.swift")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`xx//GODASH_HOT_LINKER_FLAGS`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := patchNativeLinkerHotSymbols(root, nil); err != nil {
		t.Fatalf("patchNativeLinkerHotSymbols: %v", err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), godashHotLinkerMarker) {
		t.Error("marker should be removed when there are no hot methods")
	}
}

func TestScanHotMethods(t *testing.T) {
	dir := t.TempDir()
	content := `// Code generated by protoc-gen-go-godash. DO NOT EDIT.

//go:build !js

package pb

import "context"

func GodashHot_CalcService_Add(ctx context.Context, handler CalcRPCHandler, reqPtr unsafe.Pointer, respPtr unsafe.Pointer) error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "calc.hot.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	services := []serviceInfo{
		{Name: "Calc", HandlerIface: "CalcRPCHandler", ServerVar: "calcServer"},
	}
	got, err := scanHotMethods(dir, services)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hot methods, want 1: %+v", len(got), got)
	}
	if got[0].Name != "CalcService_Add" || got[0].ServerVar != "calcServer" {
		t.Fatalf("unexpected hot info: %+v", got[0])
	}
}
