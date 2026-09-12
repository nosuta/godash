# godash

Cross-platform bridge core for Flutter + Go/TinyGo applications.

godash lets a Flutter app call Go code (native FFI on macOS/iOS/Android, a Web
Worker on the web) through a single protobuf-enveloped transport, and gives you
typed, code-generated clients, streaming, server push, reverse calls, a SQLite
abstraction, and an opt-in zero-copy "hot path" for latency-critical calls.

This package is published to pub.dev: projects depend on it as
`godash: ^2.2.0` and on the Go module
`github.com/nosuta/godash/v2`. No local source checkout is required.
Local-development and fork workflows can still use a `path:` dependency.

---

## Highlights

- **One API, two transports.** `Bridge` has a native FFI implementation
  (`dart:ffi`) and a web implementation (`dart:js_interop` + Worker), selected
  automatically via a conditional import. Generated Dart clients are identical
  on both.
- **Typed code generation.** `protoc-gen-go-godash` / `protoc-gen-dart-godash`
  turn `service` definitions into a Go handler interface + dispatcher and a Dart
  client (`*RpcClient`), plus push and reverse-service helpers.
- **Layered performance paths.**
  - async envelope (default, general transport),
  - **sync unary** (`CallSync`) — no goroutine/`ReceivePort`/port round trip,
  - **packed hot path** (`option (godash.hot) = true;`) — bypasses protobuf
    entirely for scalar-only unary methods.
- **Stream backpressure.** Opt-in per stream: `dropLatest` (conflate),
  `bufferDrop` (ring buffer), `block` (lossless, with a Go credit signal) and
  `batch`, applied in shared Dart on both transports.
- **Opt-in web shared memory.** With cross-origin isolation, streaming RPCs can
  publish frames through a per-stream `SharedArrayBuffer` ring and signal with a
  bare numeric message (envelope fallback preserved).
- **No code generation step for FFI bindings.** The native ABI is resolved
  dynamically; there is no ffigen/`exported.h` step.
- **Explicit memory ownership** across the Dart↔Go boundary.

## Performance (measured)

From `benchmark/RESULTS.md` (macOS arm64, 1 int64 / 64-byte payload):

| path | round trip |
|---|---|
| hot (packed, int64) | mean ~0.5 µs, p99 ~1 µs |
| sync unary (envelope, 64 B) | p50 ~14–18 µs |
| async unary (envelope, 64 B) | p50 ~40–48 µs |

Run it yourself with `benchmark/native/bench.dart` (see
[Testing & benchmarks](#testing--benchmarks)).

---

## Requirements

Native builds need the Go toolchain plus the platform SDKs; web builds can use
either standard Go or TinyGo. `godash doctor` checks for:

`go`, `flutter`, `dart`, `git`, `protoc`, `npm`, `perl`, `tinygo`

Protobuf code generation also expects the protoc plugins to be installable from
the Go module (`protoc-gen-go`, `protoc-gen-go-lite`) and
`dart pub global activate protoc_plugin`.

Platform support:

| target | native library | web worker |
|---|---|---|
| macOS | yes | — |
| iOS | yes | — |
| Android | yes | — |
| Web (WASM) | — | yes (Go or TinyGo) |
| Windows / Linux native | **unsupported** | — |

---

## Quick start

Install the CLI and scaffold a project:

```sh
go install github.com/nosuta/godash/v2/cmd/godash@latest
godash                      # interactive: creates a project from the template
cd my_app
godash macos run            # or: godash ios / godash android apk / godash web run
```

The template (`cmd/godash/assets/_template/`, embedded in the CLI) contains an example `EchoService` (envelope path) and
a `CalcService.Add` (hot path) so you can see both models in generated code.

### Everyday commands

```sh
godash prepare              # regenerate proto + wiring, flutter create, licenses
godash proto                # regenerate Go + Dart protobuf code only
godash dart-api             # refresh the Dart C API headers
godash macos run            # build the Go lib, then flutter run -d macos
godash web build            # flutter build web --wasm --release
godash android apk          # build .so + APK
godash ios                  # build .a/xcframework + iOS app
godash lib macos            # build only the native library
godash clean                # remove build artifacts
godash reset                # clean + remove platform dirs
godash upgrade              # refresh the godash dependency and regenerate
godash doctor               # verify toolchain
```

---

## How a godash project is structured

godash does **not** vendor its source into the project. Instead the project:

- depends on `godash: ^2.2.0` from pub.dev, and on the `native_internal`
  Flutter plugin materialised at `.godash/native_internal`,
- has `go.mod` requiring `github.com/nosuta/godash/v2` (no `replace`),
- declares messages/services in `proto/*.proto`,
- implements Go handlers in `go/rpc/*_server.go`,
- calls the generated Dart clients from `lib/`.

No local godash checkout is required. On every `godash prepare` / build the CLI
resolves the godash source directory from the project's own Go module graph
(`$GODASH_MODULE_DIR`), then:

- builds the protoc plugins and `gen_marshal_std` from that module,
- reads the shared `godash/options.proto` include from it,
- copies its `packages/native_internal` plugin shell into
  `.godash/native_internal` (a project-local, gitignored build crate that
  `godash lib` writes the platform binaries into).

Projects that consume godash via a `path:` dependency (local development or a
fork) keep working: the CLI provisions the checkout automatically if it is
missing. Set `GODASH_REPO` / `GODASH_REF` to select a fork or a specific
branch, tag or commit, or `GODASH_NO_PROVISION=1` to disable it. A checkout
found at the expected path is always used as-is and never overwritten.

On every `godash prepare` / build, godash regenerates the godash-owned files:

```
go/main*.go               # FFI exports (native) and worker entry (web)
go/rpc/rpc_handler.go     # service dispatcher
go/rpc/hot_handler.go     # packed hot-path dispatcher (native only)
lib/pb/*.pb.dart          # protobuf messages
lib/pb/*.flap.dart        # typed Dart clients / push / reverse handlers
lib/licenses/licenses.g.dart
lib/version/version.dart
web/*                     # worker assets, wasm_exec.js, sqlite wasm
.godash/native_internal/  # native plugin shell + built .so/.a/xcframework
```

Your handlers (`go/rpc/*_server.go`) and `entrypoint.go` are preserved.

See `cmd/godash/assets/_template/README.md` (embedded; also extracted into every new project) for the full project layout.

---

## Architecture

### Bridge and transports

```
Dart app
  │  generated *RpcClient (lib/pb/*.flap.dart)
  ▼
Transport (lib/bridge/transport.dart)
  │  unary / stream / hotRaw
  ▼
Bridge  ── native ──▶  dart:ffi ──▶  Go c-shared/c-archive exports
        └─ web ─────▶  Worker postMessage ──▶ Go/TinyGo wasm worker
```

`Bridge` is a singleton configured once at startup:

```dart
Bridge.configure(
  appEncryptionKey: AppEncryptionKey.key,
  workerUrl: '${Uri.base.origin}/worker.js?v=${GoBuildVersion.version}',
  // Optional (web, requires COOP/COEP cross-origin isolation):
  useSharedMemory: true,
);
await Bridge().ready; // optional: wait for the Init exchange to finish
```

### RPC model (Go side, `rpc/`)

- **Envelope** (`proto/core.proto`): every request/response is a `Request` /
  `Response` oneof. This is the general, backward-compatible transport and the
  only one used on the web.
- **Dispatch**: `rpc.RPC().Call(ctx, req)` runs a handler and streams marshaled
  responses; port-keyed cancellation is supported.
- **Push**: Go → Dart fire-and-forget messages (`pusher.Pusher`).
- **Reverse-RPC**: Go → Dart → Go calls (`ReverseCall` / `sendReverseResponse`).

### Sync unary fast path

For short-lived unary handlers, the native bridge uses a blocking export:

- Go: `//export CallSync` → `rpc.RPC().CallSync(ctx, payload)` calls the handler
  directly (no goroutine, no port).
- Dart: `Bridge.rpcSync` / `Bridge.rpcUnary`; `Transport.unary` uses it
  automatically.

Streaming, reverse calls and cancel stay on the async envelope path.

> Contract: the sync path blocks the platform thread. Only use it for short
> handlers (short DB reads/writes). Move long work to the async path.

### Packed hot path

Mark a unary method whose request and response are fixed-layout scalars:

```proto
import "godash/options.proto";

message AddRequest  { int32 a = 1; int32 b = 2; }
message AddResponse { int32 sum = 1; }

service CalcService {
  rpc Add(AddRequest) returns (AddResponse) {
    option (godash.hot) = true;
  }
}
```

The generators then emit:

- Go (`go/pb/<file>.hot.go`, `//go:build !js`): `GodashHot_CalcService_Add`
  unpacks a packed buffer, calls the handler, packs the response.
- Wiring: `go/rpc/hot_handler.go` dispatcher + a `//export CalcService_Add`
  wrapper in `go/main.go`.
- Dart: a typed client method that packs fields with `ByteData` and calls the
  export via `Transport.hotRaw` on native, or falls back to the normal envelope
  on web (`Transport.supportsHotPath == false`).

**Eligibility** (computed by `internal/hotlayout`): only singular scalar fields
(`bool`, `int32/uint32/int64/uint64`, `sint*/fixed*`, `float`, `double`).
Strings, bytes, messages, repeated/map fields, oneofs, explicit presence and
enums fall back to the envelope automatically — mixed mode is fine.

**ABI**: `int32_t <Service>_<Method>(void* req, void* resp)`; returns `0` on
success, non-zero on error. The packed layout uses natural C alignment in
ascending field-number order, host (little-endian) byte order.

### Stream backpressure

Server-streaming methods accept an optional `BackpressurePolicy` that bounds how
far a fast Go producer can run ahead of a slow Dart consumer. The strategies are
implemented once in `lib/bridge/backpressure.dart`, above the transport, so they
behave identically on native and web:

```dart
final client = EchoRpcClient();
client.serverStream(req, backpressure: const BackpressurePolicy.dropLatest());
```

| strategy | behavior |
|---|---|
| `none` | pass-through (default) |
| `dropLatest` | conflate: keep only the newest item, drop stale pending ones |
| `bufferDrop` | bounded ring buffer; drop the oldest item on overflow |
| `block` | lossless bounded buffer; pauses the producer when full |
| `batch` | coalesce up to `batchMaxSize` or `batchMaxDelay`, then deliver |

`block` uses a credit signal sent over the existing envelope (reserved
`RpcRequest` path `/godash.flow/Credit`, no wire-format change). A Go streaming
handler opts in with:

```go
gate := rpc.FlowFromContext(ctx)
for {
    if gate != nil {
        if err := gate.Acquire(ctx); err != nil {
            return err
        }
    }
    ch <- response
}
```

Handlers that do not call `FlowFromContext` are unaffected; for them `block`
still applies Dart-side buffering and source pausing.

### Web shared memory (opt-in)

On web, streaming frames normally cross the worker boundary as transferable
`ArrayBuffer` envelopes. When the page is cross-origin isolated
(`Cross-Origin-Opener-Policy: same-origin` + `Cross-Origin-Embedder-Policy:
require-corp`) and the caller opts in, each stream gets a `SharedArrayBuffer`
ring instead:

```dart
Bridge.configure(
  appEncryptionKey: AppEncryptionKey.key,
  workerUrl: '${Uri.base.origin}/worker.js?v=${GoBuildVersion.version}',
  useSharedMemory: true,        // effective only when cross-origin isolated
  sharedMemorySlots: 4,         // ring depth per stream
  sharedMemorySlotBytes: 16384, // max frame size per slot
);
```

The worker writes each frame into a slot and signals with a bare numeric
`postMessage`; Dart drains the ring on the signal. Frames that do not fit a slot,
and frames written while the ring is full, fall back to the normal transferable
envelope on the same port, so mixed framing keeps ordering and there is no wire
format change. Unary calls never allocate a ring, and on non-isolated pages the
bridge logs a warning and keeps the envelope.

> Without COOP/COEP, `SharedArrayBuffer` is unavailable and `useSharedMemory` is
> ignored. Static hosts must send the two headers; the scaffold's `web/_headers`
> already does for hosts that honor it (Netlify/Cloudflare Pages).
>
> Dev-mode gotcha: `godash web run` uses `flutter run -d web-server`, which does
> **not** send COOP/COEP, so shared memory is silently inactive while developing.
> Exercise it with `godash web build` served by an isolating host, or put the dev
> server behind a proxy that adds the two headers.

### Memory ownership (native)

godash never frees memory with an allocator other than the one that allocated
it:

| buffer | allocated by | freed by |
|---|---|---|
| request container | Dart (`malloc`/`calloc`) | Dart, right after the synchronous `RPC` export returns |
| response / push container | Go (`C.malloc`/`C.CBytes`) | Dart **through** the Go-exported `FreeBytesContainer` symbol |
| hot-path req/resp buffers | Dart (`malloc`/`calloc`) | Dart (Go only borrows during the call) |

The response export path is zero-copy: Dart parses directly from the
`asTypedList` view over Go's C heap, then frees the container.

### SQLite

`sqlite.Open(path)` returns a `*database/sql.DB`. On native it uses
`modernc.org/sqlite`; on web (`sqlite_js.go`) it uses
`go-wasmsqlite` + OPFS. The abstraction is identical across targets.

### FFI bindings are dynamic

There is no ffigen. `lib/bridge/native_library.dart` resolves the four stable
transport symbols (`InitializeDartAPI`, `RPC`, `CallSync`,
`FreeBytesContainer`) with `DynamicLibrary.lookupFunction`; generated per-method
hot exports are resolved the same way at first use.

---

## Proto & code generation

- `proto/core.proto` — the envelope (`Request`/`Response`/`Push`/…). Its wire
  format is a compatibility boundary: do not change field numbers or semantics.
- `proto/godash/options.proto` — the `(godash.hot)` method option.
- `cmd/godash/assets/_template/proto/echo.proto` — example services for new projects.

Generators:

| tool | output |
|---|---|
| `cmd/protoc-gen-go-godash` | `<file>.flap.go` (handler interface + router), `<file>.hot.go` (packed path) |
| `cmd/protoc-gen-dart-godash` | `<file>.flap.dart` (typed client, push/reverse handlers) |
| `cmd/godash` | `go/main*.go`, `go/rpc/{rpc,hot}_handler.go` |
| `cmd/gen_marshal_std` | `MarshalVT`/`UnmarshalVT` wrappers for standard Go |
| `cmd/gen_go_build_version` | `lib/version/version.dart` |

Generated files are marked `DO NOT EDIT`; change the generator instead.

---

## Testing & benchmarks

Go:

```sh
go vet ./...
go test ./...
```

Dart (run from the repository root, which is the `godash` package):

```sh
flutter analyze lib test benchmark
flutter test
```

The web worker protocol tests are `//go:build js` and run under a browser
harness:

```sh
(cd cmd/go_js_wasm_exec && go build -o /tmp/go_js_wasm_exec .)
GOOS=js GOARCH=wasm go test -exec /tmp/go_js_wasm_exec ./web
```

Native latency benchmark:

```sh
go build -buildmode=c-shared -o benchmark/native/libbench.dylib ./benchmark/native
dart run benchmark/native/bench.dart --n 5000              # async envelope
dart run benchmark/native/bench.dart --n 5000 --sync       # sync unary
dart run benchmark/native/bench.dart --n 20000 --hot       # packed hot path
```

Web benchmark: see `benchmark/web/driver` and `benchmark/RESULTS.md`.

> Note: `go test ./...` rewrites `sqlite/testdb`; restore it with
> `git checkout -- sqlite/testdb` before committing.

---

## Repository layout

```
cmd/
  godash/                    CLI + wiring generation
  protoc-gen-go-godash/      Go protoc plugin
  protoc-gen-dart-godash/    Dart protoc plugin
  gen_marshal_std/           MarshalVT wrappers
  gen_go_build_version/      version.dart generator
  go_js_wasm_exec/           wasmbrowsertest harness for web tests
rpc/                         RPC dispatch, sync dispatch, reverse calls, push
dart_api/                    cgo bridge (Dart_Post, pointer helpers, free)
pb/                          core envelope + generated option types
proto/                       core.proto, godash/options.proto
sqlite/                      DB abstraction (native + web)
web/                         web worker entry point
pusher/ fetch/               push + wasm/native fetch helpers
internal/hotlayout/          shared packed-layout computation
lib/bridge/                  Dart bridge (native + web), transport, bindings
packages/native_internal/    Flutter plugin that embeds the native library
cmd/godash/assets/_template/  embedded project scaffold consumed by `godash new`
benchmark/                   native + web latency harness, RESULTS.md
test/                        Dart tests
```

## Status

The performance work is tracked in `PLAN.md`:

| phase | status |
|---|---|
| P0 test foundation | done |
| P1 response zero-copy + allocator contract | done |
| P2 sync FFI unary path | done |
| P3 typed hot-path C exports | done |
| follow-up: remove ffigen | done |
| P4 request ownership transfer | done |
| P5 stream backpressure | done |
| P6 SharedArrayBuffer (opt-in) | done |
