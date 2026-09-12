# TEST_PLAN.md — godash test coverage plan

This document inventories where the godash codebase needs tests, what already
exists, and the gaps to close. It complements `PLAN.md` (performance roadmap);
performance deltas are tracked in `benchmark/RESULTS.md`, correctness here.

Priorities:

- **P0** — correctness- or memory-safety-critical, or guards a contract that has
  already caused bugs (allocator ownership, zero-copy, packed layout, wire
  compatibility).
- **P1** — important behavior / regression guard for shipped features.
- **P2** — hardening, platform matrix, nice-to-have.

---

## 1. How to run the current suites

```sh
# Go (module root)
go vet ./...
go test ./...
go test -race ./...                                   # not yet in CI

# Dart (module root is the `godash` package)
flutter analyze lib test benchmark                    # must be 0 issues
flutter test

# Web worker protocol tests (browser harness)
(cd cmd/go_js_wasm_exec && go build -o /tmp/go_js_wasm_exec .)
GOOS=js GOARCH=wasm go test -exec /tmp/go_js_wasm_exec ./web

# Native integration/benchmark backend
go build -buildmode=c-shared -o benchmark/native/libbench.dylib ./benchmark/native
```

Hygiene: `go test ./...` rewrites `sqlite/testdb`; restore with
`git checkout -- sqlite/testdb`.

---

## 2. Current coverage snapshot

| area | test file | what it covers | status |
|---|---|---|---|
| RPC dispatch | `rpc/rpc_test.go` | unary, stream ordering, Init, cancel (port-keyed + pre-cancelled), reverse calls, push, registry | good |
| Sync unary | `rpc/rpc_test.go` | `CallSync` unary/no-handler/no-response/bad-payload | good |
| Flow control | `rpc/flow_test.go` | gate credits, block/cancel/closed, reserved-path interception, pending credits | good |
| FFI byte helpers | `dart_api/bridge_test.go` | `BytesToPointerAddress` round-trip, uniqueness, large payload, `PointerAddr` | partial (C-free only) |
| Hot layout | `internal/hotlayout/hotlayout_test.go` | alignment, ordering, empty, floats, rejection of string/bytes/repeated | good |
| Wiring renderers | `cmd/godash/wiring_gen_test.go` | `HotInvoke`, main.go hot exports, zero-copy request, scan `*.hot.go` | partial |
| SQLite (native) | `sqlite/sqlite_test.go` | open + basic query | partial |
| Web worker protocol | `web/web_js_test.go` | onMessage Init/RPC/stream+Done/bad-proto/non-MessageEvent, webPusher | good (needs harness) |
| Web shared ring (Go) | `web/web_sab_test.go` | in-order drain, full-ring fallback, oversized-frame fallback | good (needs harness) |
| Dart shared ring | `test/bridge/shared_ring_test.dart` | pure reader: order, wrap-around, empty, sequential drains | good |
| Dart SAB web interop | `test/web/shared_ring_web_smoke.dart` | real `SharedArrayBuffer` via `_JsSharedRingMemory` (compiled JS + Node in CI) | good (Node; browser isolation not available to the test runner) |
| Dart byte helpers | `test/bridge/native_bytes_test.dart` | request container, zero-copy response view, free hook, RPC/error/push/string parse | good |
| Dart backpressure | `test/bridge/backpressure_test.dart` | all strategies + cancellation | good |
| Bridge config | `test/bridge_test.dart` | configure-before-use | minimal |
| Flutter plugin stubs | `packages/native_internal/test/*` | default `getPlatformVersion` stubs | not meaningful |

CI is `.github/workflows/test.yml`: `go vet`, `go test -race`, the wasm worker
tests in Chrome, a TinyGo web-worker build, `flutter analyze`, `flutter test`
and the Dart shared-memory Node smoke.

---

## 3. Gaps and proposed tests

### 3.1 Go runtime (`rpc/`, `dart_api/`, `pb/`)

| id | priority | test | notes |
|---|---|---|---|
| G1 | P0 | Run the whole Go suite under `-race` in CI | flow gate, reverse calls and cancel touch shared maps |
| G2 | P0 | `dart_api`: `BytesToContainer`/`BytesToPointerAddress` ownership contract, including `GoDash_FreeBytesContainer` | free path lives in C (`bridge.c`); needs a live-dylib integration test |
| G3 | P1 | `pb`: envelope round-trip for every `Request`/`Response` oneof (std and lite builds) | wire-compat guard |
| G4 | P1 | `pb`: `(godash.hot)` extension is read correctly; `marshal_std_gen` has VT wrappers for every message | catches generator drift |
| G5 | P1 | `rpc`: `CallSync` honours context timeout; `Call` streaming cancel mid-stream | existing cancel test is start-of-stream |
| G6 | P1 | `rpc`: flow gate under concurrent producers/consumers (stress) | races + missed wakeups |
| G7 | P1 | `rpc`: unknown/oversized/negative credit payloads are ignored safely | robustness |
| G8 | P2 | `internal/hotlayout`: reject proto3-optional, map, enum, oneof; field-number gaps | broaden the eligibility matrix |
| G9 | P2 | `fetch`: native path against `httptest`; tinygo path compile-only | |

### 3.2 Dart bridge (`lib/bridge/`)

| id | priority | test | notes |
|---|---|---|---|
| D1 | P0 | `bridge_native` end-to-end against a real test dylib: async unary, `rpcSync`/`rpcUnary`, response free, cancel/timeout, error responses, `Init`/ready | this is the untested core contract; feasible in `flutter test` on macOS via `DynamicLibrary.open` |
| D2 | P0 | `bridge_native.hotRaw`: packed call, cached lookup, non-zero status → exception, missing symbol → error | |
| D3 | P0 | `bridge_native.rpcStream` + `backpressure.block`: credits flow and the Go producer gates | requires a test backend honoring `FlowFromContext` |
| D4 | P0 | Allocator-contract leak check under cancel/timeout (repeated calls, assert stable memory / no double-free) | P1/P4 rely on timing, not a test |
| D5 | P1 | `native_library`: missing/renamed symbol surfaces a clear error; hot symbols resolve dynamically | |
| D6 | P1 | `transport`: unary error mapping, stream decode, envelope fallback when `supportsHotPath == false` | can use a fake `Bridge` once an interface seam exists |
| D7 | P1 | `bridge_web`: browser integration for rpc/rpcStream/push/hot fallback | Chrome via `integration_test` harness |
| D8 | P2 | `backpressure`: high-throughput stress and concurrent pause/resume | |

### 3.3 Code generation (`cmd/protoc-gen-*`, `cmd/gen_*`)

| id | priority | test | notes |
|---|---|---|---|
| C1 | P0 | `protoc-gen-go-godash`: golden output for `.godash.go` and `.hot.go` (handler iface, router, eligibility, empty messages, nil guard, `!js` tag, imports) | currently only manually verified |
| C2 | P0 | `protoc-gen-dart-godash`: golden output for `.godash.dart` (unary/stream/hot, push/reverse handlers, `backpressure` param, options-import skip, conditional imports) | |
| C3 | P0 | Generate into a temp module and compile: `go build` the Go output, `dart analyze` the Dart output | end-to-end generation correctness |
| C4 | P1 | `cmd/gen_marshal_std`: every message in `pb` gets `MarshalVT`/`UnmarshalVT` | G4 overlap |
| C5 | P2 | `cmd/gen_go_build_version`: expected output format | |

### 3.4 CLI and build pipeline (`cmd/godash/`)

| id | priority | test | notes |
|---|---|---|---|
| L1 | P0 | Build scripts contain **no** `ffigen` / `exported.h` references (regression guard for the removal) and keep correct `fmt` arg counts | easy, catches the ffigen-removal regression class |
| L2 | P1 | `projectEnv` resolution: `core.env`, `custom.mk`, godash path auto-detect, defaults | |
| L3 | P1 | `godash new` scaffolding into a temp dir produces the expected file tree (without heavy builds) | |
| L4 | P1 | `prepare`/`upgrade` orchestration order with a mocked shell runner | |
| L5 | P2 | `doctor` reports missing vs present tools; `clean`/`reset` remove the documented artifacts | |
| L6 | P2 | `analyze`/`wiring` idempotence: running twice yields identical files | |

### 3.5 End-to-end / platform

| id | priority | test | notes |
|---|---|---|---|
| E1 | P0 | Scaffold a temp project, run `godash prepare`, build a proto with a hot method + a stream, assert Go compiles and Dart analyzes | the closest thing to a real user project |
| E2 | P1 | Web: run the worker round-trip (RPC, stream, push) via the js harness in CI | extends `web_js_test.go` |
| E3 | P1 | Native lib builds: macOS `.a/xcframework`, Android `.so`, iOS `.a` | platform matrix |
| E4 | P1 | TinyGo web build (`wasmTinyGoScript`) succeeds | distinct from standard Go wasm |
| E5 | P2 | Benchmark harness smoke test: assert echo correctness and emit JSON stats | also guards the harness itself |

### 3.6 Cross-cutting

| id | priority | test | notes |
|---|---|---|---|
| X1 | P0 | Add `.github/workflows/test.yml`: `go vet`, `go test -race`, `flutter analyze`, `flutter test` | currently only release build |
| X2 | P0 | Envelope robustness fuzz/table tests: malformed/truncated/unknown-oneof payloads, null trailing bytes | |
| X3 | P1 | Timeout paths end-to-end: Go never responds → Dart unblocks and frees its request buffer (P4 guarantee) | |
| X4 | P1 | Add the browser harness setup to CI for `web_js_test.go` (Chrome + sqlite wasm assets) | |

---

## 4. Minimum viable next step

If only a small slice can be done, do these in order (all are P0):

1. **X1** — a test CI workflow, so regressions are caught automatically.
2. **D1/D2/D3** — a native integration test harness (build a test dylib from
   `benchmark/native`, load it in `flutter test`, drive async/sync/stream/hot +
   allocator contract). This covers the largest untested surface.
3. **C1/C2/C3** — protoc plugin golden + compile tests, which protect the
   generator contract that P3 and the ffigen removal depend on.
4. **L1** — the ffigen/exported.h regression guard.
5. **G1** — run `-race` on the Go suite.

---

## 5. Notes / constraints

- Some tests need real platform artifacts:
  - Native dylib tests: build `benchmark/native` (or a dedicated
    `test/native` backend) as `c-shared`; load with `DynamicLibrary.open`.
  - Web tests: `cmd/go_js_wasm_exec` + Chrome + the sqlite wasm assets.
- Keep generated-code tests as **golden + compile**, not string-soup
  assertions, so refactors of the templates stay cheap.
- The existing tests intentionally avoid cgo in `_test.go` files
  (`golang/go#4030`); the free path (`GoDash_FreeBytesContainer`) must be
  exercised through a real library or via the Dart side.
- Do not add test-only dependencies without a strong reason; prefer stdlib
  (`httptest`, `testing/fstest`, `os/exec`) and the already-present Flutter test
  tooling.

---

## 6. Implementation status

Implemented in this pass:

| id | where | note |
|---|---|---|
| X1 | `.github/workflows/test.yml` | Go vet + `go test -race` on ubuntu/macOS; Flutter analyze + test |
| G1 | CI + local | `go test -race ./...` is clean (flow gate concurrency fixed below) |
| G3 | `pb/envelope_test.go` | round-trip for every `Request`/`Response` oneof, truncated input |
| G4 | `pb/envelope_test.go` | `MarshalHelper`, `(godash.hot)` extension read |
| G5 | `rpc/rpc_test.go` | `CallSync` honours ctx timeout (streaming cancel-mid-stream still open) |
| G6 | `rpc/flow_test.go` | concurrent `Acquire`; gate rewritten with broadcast waiters + cancel-safe waiter removal |
| G7 | `rpc/flow_test.go` | short/zero/negative credit payloads ignored |
| G8 | `internal/hotlayout/hotlayout_test.go` | reject enum, oneof, proto3-optional, map |
| L1 | `cmd/godash/build_scripts_test.go` | no `ffigen`/`exported.h` in any generated script; `%!` catches fmt-arg drift |
| C1 | `cmd/protoc-gen-go-godash/main_test.go` | `.godash.go` handler/router, `.hot.go` build tag + pack/unpack, ineligible/absent-option fallback |
| C2 | `cmd/protoc-gen-dart-godash/main_test.go` | unary/stream clients, `backpressure` param + import, hot wrapper, push handler, options-import skip |
| D1/D2 | `test/integration/native_bridge_test.dart` | builds the c-shared backend and drives async RPC, `CallSync`, error response, packed hot export, allocator smoke |
| D3 | `benchmark/bench` + `test/integration/native_bridge_test.dart` | block backpressure end-to-end: gated Go producer + credit control request through the real dylib |
| D8 (partial) | `rpc/flow_test.go` | concurrent acquire |
| C3 (Go) | `cmd/protoc-gen-go-godash/main_test.go` | generated `.hot.go` compiled with stub types via `go build` |
| C4 | `pb/envelope_test.go` | every `core.proto` message has `MarshalVT`/`UnmarshalVT` (registry reflection) |
| G9 | `fetch/fetch_test.go` | native fetch 200/204/500 and unreachable host |
| L2 | `cmd/godash/project_test.go` | `readEnvFile` + `loadProjectEnvAt` (core.env/custom.mk/override/missing godash) |
| L6 | `cmd/godash/wiring_gen_test.go` | `writeWiringFiles` output + idempotence with a temp project |
| X2 | `pb/envelope_test.go` | malformed/truncated/adversarial envelope inputs never panic |
| D4 | `test/integration/native_bridge_test.dart` | 2000×64 KiB sync churn with an RSS-growth bound (catches missing request/response frees) |
| D7 / E2 / X4 | `.github/workflows/test.yml` (`web` job) | wasm worker protocol tests in Chrome on CI (harness built from `cmd/go_js_wasm_exec`) |
| L3 / L5 (helpers) | `cmd/godash/cli_unit_test.go` | `toSlug`, `isLocalPath`, `replaceInFile`, `detectGodashDep`, `resolveGodashPath`, `templateMeta` round-trip, `tool.detect` |
| P6 (Go) | `web/web_sab_test.go` | shared-ring producer: in-order drain, full-ring fallback, oversized-frame fallback |
| P6 (Dart logic) | `test/bridge/shared_ring_test.dart` | pure `SharedRingReader`: order, wrap-around, empty, sequential drains |
| P6 (Dart interop) | `test/web/shared_ring_web_smoke.dart` + `dart` CI job | `_JsSharedRingMemory` against a real `SharedArrayBuffer`, compiled to JS and run under Node |
| E4 | `.github/workflows/test.yml` (`tinygo` job) | TinyGo `-panic=trap -opt=2` web-worker build |

Still open (see §3 for ids): D6 (`transport` with a fake Bridge seam — needs a
production seam), C3 (compile the generated **Dart** output), L3/L4 (full
`godash new` scaffold and `prepare`/`upgrade` orchestration, which shell out to
`flutter`/`git`), E1/E3/E5 (end-to-end scaffold + platform build matrix +
benchmark smoke), X3 (timeout end-to-end — covered indirectly by the `CallSync`
timeout and integration error tests). D7 remains partially open: the Go ring and
Dart ring logic/interop are covered, but a full browser integration test of
`bridge_web` (rpc/rpcStream/push) is still missing because Flutter's Chrome test
runner does not serve a cross-origin-isolated page. G2
(`GoDash_FreeBytesContainer`) is exercised through the Dart integration test
(`lib.FreeBytesContainer` drives the Go export → C free).

Notes from implementation:

- The native integration test lives in `test/integration/` and auto-skips when
  Go is unavailable or the host is not macOS/Linux; it builds the dylib in
  `setUpAll`, so it needs the Go toolchain on the test runner (the CI Dart job
  installs it).
- The protoc-plugin tests build a `CodeGeneratorRequest` in memory and call the
  plugin's `generateFile` directly — no `protoc` binary required.
- The `FlowGate` was hardened for multiple concurrent waiters (client code only
  needs a single producer, but the stronger guarantee is cheap and race-tested).
