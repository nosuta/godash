package main

import (
	"bufio"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// serviceInfo describes one gRPC-style service discovered in a generated
// .godash.go file. It carries enough metadata to generate the wiring
// (rpc_handler.go, main*.go) without further AST analysis.
type serviceInfo struct {
	// Name is the short service name (e.g. "Echo"). It equals the type
	// name portion of the handler interface, i.e. "<Name>RPCHandler".
	Name string
	// HandleFunc is the generated dispatch function name, e.g. "HandleEchoRPC".
	HandleFunc string
	// HandlerIface is the generated interface name, e.g. "EchoRPCHandler".
	HandlerIface string
	// ServerStruct is the conventional server type the user implements,
	// e.g. "EchoServer". Documented in the generated rpc_handler.go.
	ServerStruct string
	// ServerVar is the conventional instance variable name, e.g. "echoServer".
	ServerVar string
}

// moduleInfo describes a Go module's name and its go/pb package alias.
type moduleInfo struct {
	// Name is the module name from go.mod (e.g. "godash").
	Name string
	// PbAlias is the import alias used in generated code for "<module>/pb"
	// (e.g. "godashpb" or just "godash" when unambiguous).
	PbAlias string
}

// readModuleName reads the `module` line from go/go.mod.
func readModuleName(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go", "go.mod"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module line in go.mod")
}

// pbAlias derives a valid Go import alias for "<module>/pb" from the module
// name. Multi-segment module paths collapse to their last segment, so
// "github.com/acme/app" yields "apppb" rather than the invalid identifier
// "github.com/acme/apppb".
func pbAlias(modName string) string {
	seg := modName
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	seg = regexp.MustCompile(`[^A-Za-z0-9_]+`).ReplaceAllString(seg, "")
	if seg == "" || (seg[0] >= '0' && seg[0] <= '9') {
		seg = "app" + seg
	}
	return seg + "pb"
}

// scanServices parses go/pb/*.godash.go and returns the discovered services.
// A .godash.go file (produced by protoc-gen-go-godash) declares a
// "<Name>RPCHandler" interface and a "Handle<Name>RPC" function. We
// extract both and derive the conventional server struct / var names.
func scanServices(pbDir string) ([]serviceInfo, error) {
	matches, err := filepath.Glob(filepath.Join(pbDir, "*.godash.go"))
	if err != nil {
		return nil, err
	}
	ifaceRe := regexp.MustCompile(`type\s+(\w+RPCHandler)\s+interface`)
	funcRe := regexp.MustCompile(`func\s+Handle(\w+)RPC\s*\(`)
	seen := map[string]bool{}
	var services []serviceInfo
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		text := string(data)
		// Find all "<Name>RPCHandler" interfaces in the file.
		for _, im := range ifaceRe.FindAllStringSubmatch(text, -1) {
			iface := im[1] // e.g. "EchoRPCHandler"
			name := strings.TrimSuffix(iface, "RPCHandler")
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			// Verify the matching Handle<Name>RPC function exists in the
			// same file. If not, skip — incomplete protoc output.
			handleRe := regexp.MustCompile(`func\s+Handle` + regexp.QuoteMeta(name) + `RPC\s*\(`)
			if !handleRe.MatchString(text) {
				continue
			}
			services = append(services, serviceInfo{
				Name:         name,
				HandleFunc:   "Handle" + name + "RPC",
				HandlerIface: iface,
				ServerStruct: name + "Server",
				ServerVar:    lowerFirst(name) + "Server",
			})
		}
		// (The funcRe scan is unused; we anchor on the interface instead
		// because the interface name uniquely identifies the service.
		// funcRe kept for reference / future per-method generation.)
		_ = funcRe
	}
	return services, nil
}

// hotInfo describes one generated hot-path method found in go/pb/*.hot.go.
type hotInfo struct {
	// Name is the C export symbol and dispatch key, e.g. "EchoService_Add".
	Name string
	// ServerVar is the conventional server instance in package rpc.
	ServerVar string
}

// hotFuncRe matches the generated GodashHot_<Service>_<Method> functions:
//
//	func GodashHot_EchoService_Add(ctx context.Context, handler EchoRPCHandler, reqPtr unsafe.Pointer, respPtr unsafe.Pointer) error
var hotFuncRe = regexp.MustCompile(`func\s+GodashHot_(\w+)\(ctx context\.Context,\s*handler\s+(\w+RPCHandler),`)

// scanHotMethods reads go/pb/*.hot.go and returns the hot methods in a stable
// order, resolved against the discovered services.
func scanHotMethods(pbDir string, services []serviceInfo) ([]hotInfo, error) {
	matches, err := filepath.Glob(filepath.Join(pbDir, "*.hot.go"))
	if err != nil {
		return nil, err
	}
	byIface := map[string]serviceInfo{}
	for _, s := range services {
		byIface[s.HandlerIface] = s
	}
	seen := map[string]bool{}
	var out []hotInfo
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		for _, m := range hotFuncRe.FindAllStringSubmatch(string(data), -1) {
			name, iface := m[1], m[2]
			if seen[name] {
				continue
			}
			svc, ok := byIface[iface]
			if !ok {
				continue
			}
			seen[name] = true
			out = append(out, hotInfo{Name: name, ServerVar: svc.ServerVar})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// writeWiringFiles generates all godash-owned Go files (main*.go, the
// RPC dispatch, the default entrypoint) into <projectRoot>/go and
// <projectRoot>/go/rpc. It is idempotent.
//
// The function is safe to call before go/pb exists: it will return an
// empty service list and generate a dispatch that only returns 404.
func writeWiringFiles(projectRoot, licensesTplPath string) error {
	modName, err := readModuleName(projectRoot)
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	pbDir := filepath.Join(projectRoot, "go", "pb")
	services, err := scanServices(pbDir)
	if err != nil {
		return fmt.Errorf("scan services: %w", err)
	}
	hot, err := scanHotMethods(pbDir, services)
	if err != nil {
		return fmt.Errorf("scan hot methods: %w", err)
	}
	mod := moduleInfo{
		Name:    modName,
		PbAlias: pbAlias(modName),
	}

	// go/*.go — entry points (native + web).
	goDir := filepath.Join(projectRoot, "go")
	if err := os.MkdirAll(goDir, 0755); err != nil {
		return err
	}
	files := []struct {
		name    string
		content string
	}{
		{"main.go", renderMainGo(mod, hot)},
		{"main_js.go", renderMainJsGo(mod)},
		{"main_debug.go", renderMainBuildVariant("debug")},
		{"main_release.go", renderMainBuildVariant("release")},
		{"main_js_debug.go", renderMainJsBuildVariant(mod, "debug")},
		{"main_js_release.go", renderMainJsBuildVariant(mod, "release")},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(goDir, f.name), []byte(gofmtContent(f.content)), 0644); err != nil {
			return err
		}
	}

	// go/rpc/rpc_handler.go — generated dispatch.
	rpcDir := filepath.Join(goDir, "rpc")
	if err := os.MkdirAll(rpcDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(rpcDir, "rpc_handler.go"), []byte(gofmtContent(renderRPCHandler(mod, services))), 0644); err != nil {
		return err
	}
	// go/rpc/hot_handler.go — native-only packed-struct dispatcher.
	if err := os.WriteFile(filepath.Join(rpcDir, "hot_handler.go"), []byte(gofmtContent(renderHotHandler(mod, hot))), 0644); err != nil {
		return err
	}

	// go/rpc/entrypoint.go — default no-op. Created only if missing so
	// user customisations survive subsequent godash prepare runs.
	entryPath := filepath.Join(rpcDir, "entrypoint.go")
	if _, err := os.Stat(entryPath); os.IsNotExist(err) {
		if err := os.WriteFile(entryPath, []byte(renderEntrypoint()), 0644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Native (iOS/macOS) hot-path exports are dynamically named and are looked
	// up at runtime with dlsym, so the materialised SPM manifests must
	// force-link each of them or the linker dead-strips them.
	if err := patchNativeLinkerHotSymbols(projectRoot, hot); err != nil {
		return fmt.Errorf("patch native linker hot symbols: %w", err)
	}

	_ = licensesTplPath // currently unused; reserved for future embed wiring
	return nil
}

// godashHotLinkerMarker is a marker comment in the native_internal
// Package.swift linkerSettings. godash replaces it with a `-u _<symbol>`
// linker flag for every discovered hot-path export.
const godashHotLinkerMarker = "//GODASH_HOT_LINKER_FLAGS"

// patchNativeLinkerHotSymbols injects force-link flags for the project's
// hot-path Go exports into the materialised native_internal SPM manifests
// (.godash/native_internal/{ios,macos}/.../Package.swift). It is best-effort:
// platform dirs that were never materialised are skipped.
func patchNativeLinkerHotSymbols(projectRoot string, hot []hotInfo) error {
	var entries []string
	for _, h := range hot {
		entries = append(entries, fmt.Sprintf(`                .unsafeFlags(["-Xlinker", "-u", "-Xlinker", "_%s"])`, h.Name))
	}
	repl := ""
	if len(entries) > 0 {
		repl = ",\n" + strings.Join(entries, ",\n")
	}
	for _, rel := range []string{
		filepath.Join(".godash", "native_internal", "ios", "native_internal", "Package.swift"),
		filepath.Join(".godash", "native_internal", "macos", "native_internal", "Package.swift"),
	} {
		path := filepath.Join(projectRoot, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			continue // not materialised
		}
		s := string(data)
		if !strings.Contains(s, godashHotLinkerMarker) {
			continue
		}
		s = strings.Replace(s, godashHotLinkerMarker, repl, 1)
		if err := os.WriteFile(path, []byte(s), 0644); err != nil {
			return err
		}
	}
	return nil
}

// gofmtContent formats generated Go source. It is best-effort: on a parse
// error the original content is returned unchanged so prepare never fails
// because of formatting.
func gofmtContent(content string) string {
	src, err := format.Source([]byte(content))
	if err != nil {
		return content
	}
	return string(src)
}

// lowerFirst lowercases the first ASCII letter of s, leaving the rest
// of the string unchanged. Used to derive the conventional server
// variable name (e.g. "Echo" -> "echoServer").
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] = r[0] + ('a' - 'A')
	}
	return string(r)
}

// renderRPCHandler generates the go/rpc/rpc_handler.go content.
func renderRPCHandler(mod moduleInfo, services []serviceInfo) string {
	var b strings.Builder
	b.WriteString(`// Code generated by godash. DO NOT EDIT.
//
// To register a new service, write a struct named "<Service>Server"
// in go/rpc (e.g. EchoServer for the Echo service) that implements
// the "<Service>RPCHandler" interface from the generated pb package.
// godash regenerates this file on every prepare; your server structs
// are preserved.

package rpc

import (
	"context"

	"github.com/nosuta/godash/v2/pb"
	`)
	b.WriteString(mod.PbAlias)
	b.WriteString(" \"")
	b.WriteString(mod.Name)
	b.WriteString("/pb\"\n)\n\n")
	if len(services) == 0 {
		b.WriteString("// no services discovered in proto/*.proto\n")
	} else {
		b.WriteString("var (\n")
		for _, s := range services {
			fmt.Fprintf(&b, "\t%s = &%s{}\n", s.ServerVar, s.ServerStruct)
		}
		b.WriteString(")\n\n")
	}
	b.WriteString("func HandleRPCImpl(ctx context.Context, req *pb.RpcRequest, ch chan<- *pb.Response) {\n")
	if len(services) == 0 {
		b.WriteString("\t_ = ctx // unused\n")
	} else {
		for _, s := range services {
			fmt.Fprintf(&b, "\tif %s.%s(ctx, req, ch, %s) {\n\t\treturn\n\t}\n",
				mod.PbAlias, s.HandleFunc, s.ServerVar)
		}
	}
	b.WriteString(`	ch <- &pb.Response{
		Responses: &pb.Response_Error{
			Error: &pb.Error{
				Code:    404,
				Message: "RPC path not found",
			},
		},
	}
}
`)
	return b.String()
}

// renderEntrypoint generates the default go/rpc/entrypoint.go content
// (no-op init and close). Created only when the file is missing.
func renderEntrypoint() string {
	return `// Code generated by godash (default no-op). Edit to add your app
// initialisation. godash will not overwrite this file once it exists.

package rpc

// EntryPoint is called once when the native library / web worker starts.
// databasePath is the on-disk database location; appEncryptionKey is the
// per-install secret used by godash's secure storage.
func EntryPoint(databasePath, appEncryptionKey string) error {
	_ = databasePath
	_ = appEncryptionKey
	return nil
}

// Close is called when the runtime shuts down. Release any resources held
// by your app here.
func Close() {}
`
}

// renderHotHandler generates go/rpc/hot_handler.go, the native-only
// (`//go:build !js`) dispatcher for packed-struct hot methods.
func renderHotHandler(mod moduleInfo, hot []hotInfo) string {
	var b strings.Builder
	b.WriteString(`// Code generated by godash. DO NOT EDIT.
//
// Native-only packed-struct dispatcher for methods marked with the
// (godash.hot) proto option. Web builds use the protobuf envelope.

//go:build !js

package rpc

import (
	"context"
	"fmt"
	"unsafe"
`)
	if len(hot) > 0 {
		b.WriteString("\t\"time\"\n\n")
		fmt.Fprintf(&b, "\t%s \"%s/pb\"\n", mod.PbAlias, mod.Name)
	}
	b.WriteString(")\n\n")
	b.WriteString(`// HotInvoke dispatches a packed-struct hot method by name. reqPtr and respPtr
// are packed buffers borrowed from the Dart caller; this function borrows them
// for the duration of the call only.
func HotInvoke(ctx context.Context, name string, reqPtr unsafe.Pointer, respPtr unsafe.Pointer) error {
`)
	if len(hot) == 0 {
		b.WriteString("\t_, _, _ = ctx, reqPtr, respPtr\n")
		b.WriteString("\treturn fmt.Errorf(\"hot method not found: %s\", name)\n")
		b.WriteString("}\n")
		return b.String()
	}
	b.WriteString("\tctx, cancel := context.WithTimeout(ctx, time.Second*10)\n")
	b.WriteString("\tdefer cancel()\n")
	b.WriteString("\tswitch name {\n")
	for _, h := range hot {
		fmt.Fprintf(&b, "\tcase %q:\n\t\treturn %s.GodashHot_%s(ctx, %s, reqPtr, respPtr)\n",
			h.Name, mod.PbAlias, h.Name, h.ServerVar)
	}
	b.WriteString("\t}\n")
	b.WriteString("\treturn fmt.Errorf(\"hot method not found: %s\", name)\n")
	b.WriteString("}\n")
	return b.String()
}

// renderMainGo generates go/main.go (native, !js build tag).
func renderMainGo(mod moduleInfo, hot []hotInfo) string {
	return fmt.Sprintf(`// Code generated by godash. DO NOT EDIT.

//go:build !js

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct bytesContainer
{
    void *message;
    int size;
} BytesContainer;

extern void GoDash_FreeBytesContainer(void *ptr);
*/
import "C"
import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"unsafe"

	"github.com/nosuta/godash/v2/dart_api"
	"github.com/nosuta/godash/v2/pb"
	"github.com/nosuta/godash/v2/rpc"
	godashrpc %q
)

func init() {
	rpc.SetEntryPoint(godashrpc.EntryPoint)
	rpc.SetHandleRPC(godashrpc.HandleRPCImpl)
}

// main as exported functions
func main() {}

//export InitializeDartAPI
func InitializeDartAPI(api unsafe.Pointer) C.int64_t {
	slog.SetLogLoggerLevel(slogLevel)

	rpc.RPC().SetPusher(pusher)
	return C.int64_t(dart_api.InitializeDartAPI(api))
}

func pusher(push *pb.Push, port int64) error {
	if port == 0 {
		return fmt.Errorf("push port is not initialized")
	}
	resp := &pb.Response{
		Responses: &pb.Response_Push{
			Push: push,
		},
	}
	b, err := resp.MarshalVT()
	if err != nil {
		return err
	}
	addr := dart_api.BytesToPointerAddress(b)
	if err := dart_api.SendPointerAddress(port, addr); err != nil {
		slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
	}
	return nil
}

//export RPC
func RPC(port C.int64_t, payload *C.BytesContainer) {
	// Zero-copy request parse (PLAN.md P4): read the Dart-owned C buffer
	// directly with unsafe.Slice instead of copying it through C.GoBytes.
	// UnmarshalVT copies the fields into Go memory, so Dart frees the
	// container as soon as this export returns. Never free it from Go.
	req := &pb.Request{}
	if err := req.UnmarshalVT(unsafe.Slice((*byte)(payload.message), int(payload.size))); err != nil {
		resp := &pb.Response{
			Responses: &pb.Response_Error{
				Error: &pb.Error{
					Message: err.Error(),
				},
			},
		}
		e, merr := resp.MarshalVT()
		if merr != nil {
			slog.Error("MUST FIX, failed to marshal error response", "error", merr.Error())
			return
		}
		addr := dart_api.BytesToPointerAddress(e)
		if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
			slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
		}
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10000)
		defer cancel()
		for ret := range rpc.RPC().Call(ctx, req) {
			addr := dart_api.BytesToPointerAddress(ret)
			if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
				slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
				break
			}
		}
		resp := &pb.Response{
			Responses: &pb.Response_Done{
				Done: &pb.Done{},
			},
		}
		done, err := resp.MarshalVT()
		if err != nil {
			slog.Error("MUST FIX, failed to marshal done response", "err", err.Error())
			return
		}
		addr := dart_api.BytesToPointerAddress(done)
		if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
			slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
		}

		// ! Following code crashes the app when the port is created in Dart.
		// So we must close the port in Dart in this case.
		// dart_api.ClosePort(int64(port))

	}()
}

//export FreeBytesContainer
func FreeBytesContainer(payload *C.BytesContainer) {
	// Frees a response/push container that was allocated on the Go side
	// (dart_api.BytesToPointerAddress). Dart must call this instead of
	// malloc.free so the allocating C allocator also performs the free.
	C.GoDash_FreeBytesContainer(unsafe.Pointer(payload))
}
`, mod.Name+"/rpc") + renderHotExports(hot)
}

// renderHotExports emits the per-method packed-struct C exports appended to
// go/main.go. Each export returns 0 on success and a non-zero status on
// failure; the Dart wrapper turns a non-zero status into an exception.
func renderHotExports(hot []hotInfo) string {
	if len(hot) == 0 {
		return ""
	}
	var b strings.Builder
	for _, h := range hot {
		fmt.Fprintf(&b, `
//export %s
func %s(req unsafe.Pointer, resp unsafe.Pointer) C.int32_t {
	if err := godashrpc.HotInvoke(context.Background(), %q, req, resp); err != nil {
		return 1
	}
	return 0
}
`, h.Name, h.Name, h.Name)
	}
	return b.String()
}

// renderMainJsGo generates go/main_js.go (js build tag, web worker).
func renderMainJsGo(mod moduleInfo) string {
	return fmt.Sprintf(`// Code generated by godash. DO NOT EDIT.

//go:build js

package main

import (
	"github.com/nosuta/godash/v2/rpc"
	"github.com/nosuta/godash/v2/web"
	godashrpc %q
)

func init() {
	rpc.SetEntryPoint(godashrpc.EntryPoint)
	rpc.SetHandleRPC(godashrpc.HandleRPCImpl)
}

func webWorker() {
	web.RunWebWorker()
}
`, mod.Name+"/rpc")
}

// renderMainBuildVariant generates main_{debug,release}.go (slog level).
func renderMainBuildVariant(level string) string {
	imports := `import "log/slog"`
	levelConst := "slog.LevelDebug"
	if level == "release" {
		levelConst = "slog.LevelInfo"
	}
	buildTag := "!js && " + level
	if level == "release" {
		buildTag = "!js && !debug"
	}
	return fmt.Sprintf(`// Code generated by godash. DO NOT EDIT.

//go:build %s

package main

%s

const slogLevel = %s
`, buildTag, imports, levelConst)
}

// renderMainJsBuildVariant generates main_js_{debug,release}.go.
func renderMainJsBuildVariant(mod moduleInfo, level string) string {
	levelConst := "slog.LevelDebug"
	buildTag := "js && " + level
	if level == "release" {
		levelConst = "slog.LevelInfo"
		// Complementary to the debug variant so every js build (standard Go
		// wasm without -tags, and TinyGo) has exactly one func main.
		buildTag = "js && !debug"
	}
	return fmt.Sprintf(`// Code generated by godash. DO NOT EDIT.

//go:build %s

package main

import (
	"log/slog"

	godashrpc %q
)

// main as a web worker
func main() {
	slog.SetLogLoggerLevel(%s)
	defer func() {
		godashrpc.Close()
		if r := recover(); r != nil {
			slog.Error("main recovered from panic", "message", r)
		}
	}()

	webWorker()
	select {}
}
`, buildTag, mod.Name+"/rpc", levelConst)
}

// renderMainJsDebugGo and renderMainJsReleaseGo are kept for symmetry but
// the work is done by renderMainJsBuildVariant.
func renderMainJsDebugGo(_ moduleInfo) string   { return "" } // unused
func renderMainJsReleaseGo(_ moduleInfo) string { return "" } // unused
