# PLAN.md — Bridge Performance Plan (nitro-inspired)

## Goal

Incorporate the strengths of [nitro_ecosystem](../../Shreemanarjun/nitro_ecosystem) into the
godash FFI / Web-Worker bridge — **zero-copy buffers** and **minimal call latency** — while:

- keeping **Web support** (Go/TinyGo WASM worker, same envelope, same Dart API),
- keeping **DB support** (`sqlite/` abstraction untouched),
- keeping the existing protobuf envelope wire format (backward compatible).

Inspiration sources:

- nitro's extern "C" ABI per-method bridge, packed `@HybridStruct` payloads, zero-copy
  pinned buffers, and stream backpressure strategies (see its `README.md`, `PERFORMANCE_ANALYSIS.md`).
- nitro's fork branch `feat/golang` (Go backend conforming to the same C ABI) as a reference
  for what a typed Go bridge looks like.

## Non-goals

- Windows/Linux native support (still unsupported, unchanged).
- Changing `proto/core.proto` envelope semantics or wire format.
- Any change to `sqlite/` (modernc.org/sqlite on native, go-wasmsqlite+OPFS on web).
- Replacing the envelope-based RPC/Reverse-RPC/Push model — it stays the general transport;
  the optimizations below *augment* it.

## Current bottleneck map (verified)

| # | Where | What happens | Cost |
|---|---|---|---|
| B1 | `lib/bridge/bridge_native.dart:164-166` + `:270-278` (`_bytesToBytesContainerPointer`) | `writeToBuffer()` allocates, then bytes are copied into malloc'd `BytesContainer` | serialize + 1 memcpy |
| B2 | generated `go/main.go` (`//export RPC`, from `cmd/godash/wiring_gen.go`) + `dart_api/bridge.go` | Go copies the request via `C.GoBytes` | 1 memcpy |
| B3 | `dart_api/bridge.go:37-44` (`BytesToPointerAddress`) | Go marshals response then `C.CBytes` copies it | marshal + 1 memcpy |
| B4 | `lib/bridge/bridge_native.dart:288-301` (`_pointerAddressToBytes`) | Dart makes a **defensive copy** (`Uint8List.fromList`) before parse | 1 memcpy |
| B5 | every call | goroutine spawn + `ReceivePort` + `Completer` + port round-trip (async model, 10s timeout in generated `main.go`) | ~10 µs fixed overhead per call |
| B6 | `lib/bridge/bridge_web.dart:91-111` (`_toTransferableBuffer`) | deliberate re-copy into a JS-owned `ArrayBuffer` before transfer (GC-retention gotcha, documented) | 1 memcpy (keep for now) |
| B7 | `dart_api/bridge.go` vs `lib/bridge/bridge_native.dart:299` | Go `C.malloc`'d memory freed by Dart `malloc.free` (same libc allocator today, but an implicit contract) | fragility |

## Phases

Work top-down; do not start a phase before the previous one is complete.

### P0 — Test foundation (blocker for everything else)

The bridge, rpc, web worker and generators have almost no tests. Zero-copy work changes
memory-ownership contracts; tests must exist first.

- [x] Add Go tests for `rpc/` (dispatch, port-keyed cancel, streaming, Reverse-RPC, push).
- [x] Add Go tests for the FFI export path (request copy-in, response `BytesToPointerAddress`).
- [x] Add Dart tests for `bridge_native.dart` byte conversion helpers (mock lib or extract
      pure helpers to a testable unit). → extracted to `lib/bridge/native_bytes.dart`
- [x] Web: add js/wasm worker protocol tests using the existing `cmd/go_js_wasm_exec`
      (chromedp + OPFS sqlite) harness. → `web/web_js_test.go`
- [x] Add a latency benchmark harness (`benchmark/`): template EchoService, N-iteration
      stopwatch, native + web. Record baseline numbers **before** touching the bridge.
      → `benchmark/RESULTS.md`

Acceptance: `go test ./...` and `flutter test` cover rpc + bridge byte paths; baseline
benchmark numbers committed (in `benchmark/RESULTS.md`).

### P1 — Response zero-copy + allocator contract (low risk, high value)

- [x] `bridge_native.dart:_pointerAddressToBytes`: drop the `Uint8List.fromList` defensive
      copy; parse `Response.fromBuffer` directly from the `asTypedList` view, then free.
      (Safe: protobuf Dart parsing copies scalars/bytes into message objects.)
      → `lib/bridge/native_bytes.dart:pointerAddressToBytes` returns the zero-copy view
- [x] Same change in `_pushListener` and `pointerAddressToString`.
      (both route through `responseFromPointerAddress` / `stringFromPointerAddress`)
- [x] Unify the cross-heap allocator contract: decide one direction of frees
      (recommendation: whoever allocates frees; Go allocations freed by Go via an exported
      `free` symbol, Dart allocations freed by Dart). Update `dart_api/bridge.go`,
      `bridge.c/h`, and the generated `main.go` accordingly.
      → `GoDash_FreeBytesContainer` in `bridge.c/h`, exported as `FreeBytesContainer`
      by the generated `main.go` (and bound in `native_library.g.dart`); the request
      container is now freed by Dart right after the synchronous `RPC` export returns
      (the Go-side copy-in happens on the calling thread).
- [x] Re-run benchmark; expect 1 fewer memcpy per response/push.
      → `benchmark/RESULTS.md` "P1" section (A/B vs pre-P1 in the same session)

Acceptance: no copies of response bytes on the Dart side; tests green; benchmark delta recorded.

### P2 — Sync FFI unary path (the latency core)

> **Removed.** The synchronous unary path (`CallSync` / `rpcSync`) ran handlers
> on the Dart platform (UI) thread and froze the UI whenever a handler was slow
> (network fetches, media decrypt, roster folds). All unary calls now use the
> async envelope. The section below is kept as history; the design is no longer
> in the tree.

- [x] New export in generated `go/main.go` (via `cmd/godash/wiring_gen.go`):
      `Response* CallSync(payload *C.BytesContainer)` — blocking unary call, no goroutine,
      no `ReceivePort`, no port round-trip.
      → `//export CallSync` calling `rpc.RPC().CallSync` (new sync dispatcher in `rpc/rpc.go`)
- [x] Dart side: `bridge_native.dart` gains a `rpcSync` fast path used by unary RPCs
      (streaming / Reverse-RPC / cancel stay async).
      → `Bridge.rpcSync` + `Bridge.rpcUnary`; `Transport.unary` now calls `rpcUnary`
      (web keeps the async path via a compatible `rpcUnary` delegate)
- [x] Document the contract: sync path blocks the platform thread → only for short-lived
      handlers (short DB reads/writes are fine; long work must go async).
      → doc comments on `Bridge.rpcSync`, `rpc.CallSync` and the generated `CallSync` export
- [x] Re-run benchmark; target: sub-10 µs end-to-end for small unary payloads on native
      (was ~10 µs+ fixed overhead + copies).
      → `benchmark/RESULTS.md` "P2" section: p50 14 µs / min 6 µs (from 40 µs async);
      sub-10 µs p50 deferred to P3 (protobuf bypass)

Acceptance: unary EchoService round-trip measurably faster; streaming paths unchanged.

### P3 — Typed hot-path C exports (nitro's ABI model, biggest codegen effort)

For methods whose payloads are scalars / fixed-layout data, bypass protobuf entirely:
per-method C exports taking a packed C struct, exactly like nitro's `@HybridStruct` bridge.

- [x] Define a proto option (e.g. `option (godash.hot) = true;`) or a Dart-side spec
      annotation marking hot-path methods eligible for packed-struct bridging.
      → `proto/godash/options.proto` defines `(godash.hot)` on MethodOptions;
      generated Go extension in `pb/options.pb.go`
- [x] `cmd/protoc-gen-go-godash`: emit `//export <Service>_<Method>` taking/returning a
      packed C struct (mirroring nitro's `NitroOpt*` / packed-struct conventions).
      → emits `<file>.hot.go` (`//go:build !js`) with `GodashHot_<Service>_<Method>`
      unpack/pack functions; `cmd/godash` emits the actual `//export <Service>_<Method>`
      wrappers into package `main` so cgo never enters the web-safe `pb` package.
      ABI note: the packed layout is passed as `void*` buffers (not a C struct value),
      which is portable across ABIs and avoids per-method ffigen declarations.
- [x] `cmd/protoc-gen-dart-godash`: emit typed Dart wrappers calling those exports.
      → typed methods pack/unpack via `ByteData` and call `Transport.hotRaw`
- [x] Regenerate `lib/bridge/native_library.g.dart` via ffigen (`godash prepare`).
      → ffigen removed entirely (follow-up): `native_library.g.dart` was replaced by a
      hand-written `lib/bridge/native_library.dart` that resolves the stable
      transport symbols (`InitializeDartAPI`/`RPC`/`FreeBytesContainer`)
      with `DynamicLibrary.lookupFunction`; generated hot exports were already dynamic.
      `ffigen_config.yaml`, the `godash ffi` command, the `ffigen` dev dependency and
      the `exported.h` build artifact are gone — godash projects never run ffigen.
- [x] **Web fallback**: the generated Dart API is identical; on web the typed wrappers
      serialize to the existing postMessage envelope (`Transport` abstraction already
      isolates this). No worker changes required.
      → `Transport.supportsHotPath` is false on web, so the wrapper calls `unary(...)`
- [x] Fall back to the envelope path for anything that doesn't fit the packed layout
      (strings, nested records) — mixed-mode is fine.
      → eligibility computed by `internal/hotlayout`; a `hot` method with strings
      silently keeps the envelope path (verified by generator run)

Acceptance: a hot-path method measured at <2 µs native round-trip; same call works on web
via the envelope; envelope path still available for everything else.

### P4 — Request ownership transfer (one memcpy less on requests)

- [x] Dart allocates the request `BytesContainer` and **keeps it alive until the response
      (or `Done` for streams) arrives**, then frees it (ownership moves to Go during
      processing; Go views it via `unsafe.Slice`, no `C.GoBytes` copy).
      → Implemented with a shorter lifetime: the `RPC`/`CallSync` exports parse the
      Dart-owned buffer **synchronously** via `unsafe.Slice` and `UnmarshalVT` copies the
      message fields into Go memory, so Go never retains the buffer. Dart therefore frees
      it as soon as the export returns — no `C.GoBytes`, and no lifetime bookkeeping.
- [x] Add a `--js` equivalent: on web, evaluate whether the defensive re-copy (B6) can be
      skipped when the source list is already JS-owned; otherwise keep it (documented).
      → evaluated and **kept** (a transferred buffer must be JS-owned; `bytes.toJS` on a
      Dart list is not independently owned). Documented in `bridge_web.dart`.
- [x] Handle the timeout path: if Go never responds, Dart must still free its buffer
      (tie buffer release to the existing 10s-timeout / cancel logic in `rpc/rpc.go`).
      → Simplifies to immediate release after the export returns, so cancel/timeout can
      never leak the request buffer.

Acceptance: request path has zero copies between serialize and unmarshal (native);
no leaks under cancel/timeout tests from P0. → met

### P5 — Stream backpressure (pure Dart, both transports)

Port nitro's four strategies onto the Dart stream layer (`rpcStream` / push stream):

- [x] `dropLatest`, `bufferDrop` (ring buffer), `block` (pause the Go producer via a
      control push), `batch` (`batchMaxSize` — coalesce items before delivery).
      → `lib/bridge/backpressure.dart`: pure-Dart transformer applied above the
      transport (so identical on native and web). `dropLatest` = conflate (keep the
      newest), `bufferDrop` = bounded ring buffer (drop oldest), `block` = lossless
      bounded buffer that pauses the source, `batch` = flush at `batchMaxSize` or
      `batchMaxDelay`. `none` is the pass-through default.
- [x] Strategy is per-stream opt-in (annotation/parameter on generated stream APIs).
      → generated Dart streaming methods take `{BackpressurePolicy? backpressure}`
      and forward it through `Transport.stream` → `Bridge.rpcStream`
- [x] `block` requires a Go-side consumer signal — implement via a control `Request`
      over the existing envelope (no wire format change).
      → reserved `RpcRequest` path `/godash.flow/Credit` intercepted in `rpc.Call`
      (never reaches the dispatcher); `rpc.FlowGate` + `rpc.FlowFromContext` let a
      streaming handler `Acquire` credits before producing each item. Gates start
      **unbounded** (so handlers may call `Acquire` unconditionally without
      deadlocking); the first credit grant switches the gate to bounded mode and
      becomes its window, and later grants add. Dart grants credits as the consumer
      drains (`Bridge._sendFlowCredit`) on both transports; grants that arrive
      before the stream request are buffered and applied on registration.

Acceptance: backpressure unit tests for all four strategies on both native and web bridges.
→ met: the strategies are transport-agnostic pure Dart (`test/bridge/backpressure_test.dart`
covers all four + `none` + cancellation), and the Go credit gate is covered by
`rpc/flow_test.go` including the end-to-end control-request path.

Note (deviation): nitro implements backpressure on the native producer; godash
implements the buffering at the shared Dart stream layer, with the Go `FlowGate`
as the opt-in lossless `block` signal. Handlers that never call `FlowFromContext`
are unaffected.

### P6 — SharedArrayBuffer on web (opt-in)

True shared-memory zero-copy for streaming hot paths. Requires COOP/COEP
cross-origin isolation headers. Opt-in only.

- [x] Capability probe + opt-in config: `Bridge.configure(useSharedMemory: true,
      sharedMemorySlots:, sharedMemorySlotBytes:)`. Effective only when the page
      is cross-origin isolated and `SharedArrayBuffer` is exposed; otherwise the
      bridge logs a warning and keeps the envelope.
      → `lib/bridge/shared_ring_web.dart:sharedMemorySupported` +
      `lib/bridge/bridge_web.dart` (`_sharedMemoryEnabled`).
- [x] Per-stream shared ring: a `SharedArrayBuffer` with a 16-byte control
      region (an atomic frame counter) and fixed-size slots. Go writes frames
      into slots and signals with a bare numeric postMessage; Dart drains on the
      signal. Frames that do not fit, and frames written while the ring is full,
      fall back to the transferable envelope on the same port, so ordering is
      preserved with mixed SAB/envelope framing and no wire-format change.
      → `web/shared.go` (`sharedRing` / `responseSink`) + `lib/bridge/shared_ring.dart`
      (`SharedRingReader`) + `lib/bridge/shared_ring_web.dart` (`_JsSharedRingMemory`).
- [x] Envelope remains the default; unary `rpcUnsafe` never allocates a ring.
      → only `Bridge.rpcStream` opts in.
- [x] Tests: Go worker ring protocol (in-order drain, full-ring fallback,
      oversized-frame fallback) in `web/web_sab_test.go`; the transport-agnostic
      reader in `test/bridge/shared_ring_test.dart`.
- [x] Benchmark: the web driver now serves COOP/COEP and gains
      `--mode stream|sab`; SAB is ~14–18% faster per 4-frame stream
      (`benchmark/RESULTS.md` "P6").
- [x] TinyGo compatible: the ring only uses `syscall/js` APIs TinyGo supports;
      verified with a `-panic=trap -opt=2` benchmark worker (ring used, zero
      fallbacks, ~12% faster streams than the envelope).

## Target ownership & allocator rules (after P1/P4)

1. Request buffer: allocated by Dart, freed by Dart immediately after the
   synchronous export returns. The export parses it zero-copy via `unsafe.Slice`
   and `UnmarshalVT` copies the fields into Go memory, so Go never retains it.
2. Response buffer: allocated by Go (C.malloc via `BytesToPointerAddress`), freed by Dart
   **immediately after parse** (P1 keeps this, minus the copy).
3. No buffer is ever freed by a different runtime than the one that allocated it
   (replaces today's implicit same-libc assumption, B7).
4. Dart `ReceivePort`s are never closed from Go (existing rule, `wiring_gen.go` comment).

## Risks

| Risk | Mitigation |
|---|---|
| Ownership bugs (use-after-free, leaks) under cancel/timeout | P0 tests incl. cancel/timeout paths; P4 releases the request buffer immediately after the synchronous export, so cancel/timeout cannot leak it |
| Sync path (P2) blocks platform thread on slow handlers | **Resolved (2.4.3):** P2 removed; all unary is async. Do not add a synchronous unary path back |
| P3 codegen complexity (two protoc plugins) | Mixed-mode fallback; ship per-service incrementally |
| TinyGo wasm constraints | P3 typed exports are native-only; P6's ring uses only `syscall/js` APIs TinyGo supports (verified by a CI build) |
| GC retention gotcha on web transfers (documented in `bridge_web.dart`) | Kept and documented (P4 evaluation); P6 references the `SharedArrayBuffer` (never transfers it) and falls back to the envelope on overflow |

## Status

| Phase | Status |
|---|---|
| P0 Test foundation | done (tests + baseline in `benchmark/RESULTS.md`) |
| P1 Response zero-copy + allocator contract | done (zero-copy parse + `FreeBytesContainer` contract; delta in `benchmark/RESULTS.md`) |
| P2 Sync FFI unary path | **removed** (froze the UI on slow handlers; unary is async again) |
| P3 Typed hot-path C exports | done (`godash.hot` + packed `void*` ABI; hot mean ~0.5 µs) |
| follow-up: remove ffigen | done (hand-written dynamic `native_library.dart`) |
| P4 Request ownership transfer | done (zero-copy request parse; no `C.GoBytes`) |
| P5 Stream backpressure | done (pure-Dart strategies + `rpc.FlowGate` credit signal) |
| P6 SharedArrayBuffer (opt-in) | done (per-stream ring + fallback; ~14–18% faster web streams) |
