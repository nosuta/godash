package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// serviceInfo describes one gRPC-style service discovered in a generated
// .flap.go file. It carries enough metadata to generate the wiring
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
	// Name is the module name from go.mod (e.g. "flap").
	Name string
	// PbAlias is the import alias used in generated code for "<module>/pb"
	// (e.g. "flappb" or just "flap" when unambiguous).
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

// scanServices parses go/pb/*.flap.go and returns the discovered services.
// A .flap.go file (produced by protoc-gen-go-godash) declares a
// "<Name>RPCHandler" interface and a "Handle<Name>RPC" function. We
// extract both and derive the conventional server struct / var names.
func scanServices(pbDir string) ([]serviceInfo, error) {
	matches, err := filepath.Glob(filepath.Join(pbDir, "*.flap.go"))
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
	mod := moduleInfo{
		Name:    modName,
		PbAlias: modName + "pb",
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
		{"main.go", renderMainGo(mod)},
		{"main_js.go", renderMainJsGo(mod)},
		{"main_debug.go", renderMainBuildVariant("debug")},
		{"main_release.go", renderMainBuildVariant("release")},
		{"main_js_debug.go", renderMainJsBuildVariant(mod, "debug")},
		{"main_js_release.go", renderMainJsBuildVariant(mod, "release")},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(goDir, f.name), []byte(f.content), 0644); err != nil {
			return err
		}
	}

	// go/rpc/rpc_handler.go — generated dispatch.
	rpcDir := filepath.Join(goDir, "rpc")
	if err := os.MkdirAll(rpcDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(rpcDir, "rpc_handler.go"), []byte(renderRPCHandler(mod, services)), 0644); err != nil {
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

	_ = licensesTplPath // currently unused; reserved for future embed wiring
	return nil
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

	"github.com/nosuta/godash/pb"
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

// renderMainGo generates go/main.go (native, !js build tag).
func renderMainGo(mod moduleInfo) string {
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

	"github.com/nosuta/godash/dart_api"
	"github.com/nosuta/godash/pb"
	"github.com/nosuta/godash/rpc"
	flaprpc %q
)

func init() {
	rpc.SetEntryPoint(flaprpc.EntryPoint)
	rpc.SetHandleRPC(flaprpc.HandleRPCImpl)
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
	// The request container is allocated by Dart and freed by Dart right
	// after this export returns (the C.GoBytes copy below happens
	// synchronously on the calling thread, before the goroutine starts).
	// Never free it here — cross-runtime frees are forbidden (see
	// dart_api/bridge.h for the allocator contract).
	b := C.GoBytes(payload.message, payload.size)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10000)
		defer cancel()
		req := &pb.Request{}
		if err := req.UnmarshalVT(b); err != nil {
			resp := &pb.Response{
				Responses: &pb.Response_Error{
					Error: &pb.Error{
						Message: err.Error(),
					},
				},
			}
			e, err := resp.MarshalVT()
			if err != nil {
				slog.Error("MUST FIX, failed to marshal error response", "error", err.Error())
			}
			addr := dart_api.BytesToPointerAddress(e)
			if err := dart_api.SendPointerAddress(int64(port), addr); err != nil {
				slog.Warn("dart_api.SendPointerAddress failed", "error", err.Error())
			}
			return
		}

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

//export CallSync
func CallSync(payload *C.BytesContainer) *C.BytesContainer {
	// Synchronous unary fast path (PLAN.md P2): blocks the calling Dart
	// platform thread until the unary handler returns, avoiding the
	// goroutine + ReceivePort + port round trip of RPC. The request
	// container is Dart-owned and freed by Dart right after this returns.
	//
	// Contract: only short-lived handlers (e.g. short DB reads/writes) may
	// be called through CallSync; long-running work must stay on the async
	// RPC path or it will freeze the platform thread.
	b := C.GoBytes(payload.message, payload.size)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10000)
	defer cancel()

	rb, err := rpc.RPC().CallSync(ctx, b)
	if err != nil {
		e, merr := (&pb.Response{
			Responses: &pb.Response_Error{
				Error: &pb.Error{Code: 500, Message: err.Error()},
			},
		}).MarshalVT()
		if merr != nil {
			slog.Error("MUST FIX, failed to marshal sync error response", "error", merr.Error())
			return nil
		}
		rb = e
	}
	return (*C.BytesContainer)(dart_api.BytesToContainer(rb))
}
`, mod.Name+"/rpc")
}

// renderMainJsGo generates go/main_js.go (js build tag, web worker).
func renderMainJsGo(mod moduleInfo) string {
	return fmt.Sprintf(`// Code generated by godash. DO NOT EDIT.

//go:build js

package main

import (
	"github.com/nosuta/godash/rpc"
	"github.com/nosuta/godash/web"
	flaprpc %q
)

func init() {
	rpc.SetEntryPoint(flaprpc.EntryPoint)
	rpc.SetHandleRPC(flaprpc.HandleRPCImpl)
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
		buildTag = "js && tinygo"
	}
	return fmt.Sprintf(`// Code generated by godash. DO NOT EDIT.

//go:build %s

package main

import (
	"log/slog"

	flaprpc %q
)

// main as a web worker
func main() {
	slog.SetLogLoggerLevel(%s)
	defer func() {
		rpc.Close()
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
func renderMainJsDebugGo(_ moduleInfo) string  { return "" } // unused
func renderMainJsReleaseGo(_ moduleInfo) string { return "" } // unused
