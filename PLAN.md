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

- [ ] Define a proto option (e.g. `option (godash.hot) = true;`) or a Dart-side spec
      annotation marking hot-path methods eligible for packed-struct bridging.
- [ ] `cmd/protoc-gen-go-godash`: emit `//export <Service>_<Method>` taking/returning a
      packed C struct (mirroring nitro's `NitroOpt*` / packed-struct conventions).
- [ ] `cmd/protoc-gen-dart-godash`: emit typed Dart wrappers calling those exports.
- [ ] Regenerate `lib/bridge/native_library.g.dart` via ffigen (`godash prepare`).
- [ ] **Web fallback**: the generated Dart API is identical; on web the typed wrappers
      serialize to the existing postMessage envelope (`Transport` abstraction already
      isolates this). No worker changes required.
- [ ] Fall back to the envelope path for anything that doesn't fit the packed layout
      (strings, nested records) — mixed-mode is fine.

Acceptance: a hot-path method measured at <2 µs native round-trip; same call works on web
via the envelope; envelope path still available for everything else.

### P4 — Request ownership transfer (one memcpy less on requests)

- [ ] Dart allocates the request `BytesContainer` and **keeps it alive until the response
      (or `Done` for streams) arrives**, then frees it (ownership moves to Go during
      processing; Go views it via `unsafe.Slice`, no `C.GoBytes` copy).
- [ ] Add a `--js` equivalent: on web, evaluate whether the defensive re-copy (B6) can be
      skipped when the source list is already JS-owned; otherwise keep it (documented).
- [ ] Handle the timeout path: if Go never responds, Dart must still free its buffer
      (tie buffer release to the existing 10s-timeout / cancel logic in `rpc/rpc.go`).

Acceptance: request path has zero copies between serialize and unmarshal (native);
no leaks under cancel/timeout tests from P0.

### P5 — Stream backpressure (pure Dart, both transports)

Port nitro's four strategies onto the Dart stream layer (`rpcStream` / push stream):

- [ ] `dropLatest`, `bufferDrop` (ring buffer), `block` (pause the Go producer via a
      control push), `batch` (`batchMaxSize` — coalesce items before delivery).
- [ ] Strategy is per-stream opt-in (annotation/parameter on generated stream APIs).
- [ ] `block` requires a Go-side consumer signal — implement via a control `Request`
      over the existing envelope (no wire format change).

Acceptance: backpressure unit tests for all four strategies on both native and web bridges.

### P6 — (optional, later) SharedArrayBuffer on web

True shared-memory zero-copy for streaming hot paths. Requires COOP/COEP cross-origin
isolation headers. Opt-in only; skip until P5 is stable.

## Target ownership & allocator rules (after P1/P4)

1. Request buffer: allocated by Dart, freed by Dart (after response/Done/timeout).
2. Response buffer: allocated by Go (C.malloc via `BytesToPointerAddress`), freed by Dart
   **immediately after parse** (P1 keeps this, minus the copy).
3. No buffer is ever freed by a different runtime than the one that allocated it
   (replaces today's implicit same-libc assumption, B7).
4. Dart `ReceivePort`s are never closed from Go (existing rule, `wiring_gen.go` comment).

## Risks

| Risk | Mitigation |
|---|---|
| Ownership bugs (use-after-free, leaks) under cancel/timeout | P0 tests incl. cancel/timeout paths; P4 frees tied to existing timeout logic |
| Sync path (P2) blocks platform thread on slow handlers | Document contract; keep async path for streams/long work |
| P3 codegen complexity (two protoc plugins + ffigen) | Mixed-mode fallback; ship per-service incrementally |
| TinyGo wasm constraints | Typed exports are native-only; web always uses the envelope (no new wasm ABI surface) |
| GC retention gotcha on web transfers (documented in `bridge_web.dart:91-111`) | Keep the defensive copy on request side until proven unnecessary |

## Status

| Phase | Status |
|---|---|
| P0 Test foundation | done (tests + baseline in `benchmark/RESULTS.md`) |
| P1 Response zero-copy + allocator contract | done (zero-copy parse + `FreeBytesContainer` contract; delta in `benchmark/RESULTS.md`) |
| P2 Sync FFI unary path | done (`CallSync` + `rpcSync`; unary p50 40 → 14 µs) |
| P3 Typed hot-path C exports | not started |
| P4 Request ownership transfer | not started |
| P5 Stream backpressure | not started |
| P6 SharedArrayBuffer (opt-in) | deferred |
