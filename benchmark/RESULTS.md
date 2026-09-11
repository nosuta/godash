# Benchmark results — bridge latency baseline

Latency of one unary echo round trip (`RpcRequest(path, payload)` →
`RpcResponse(same payload)`) through the envelope bridge, measured **before**
any zero-copy / sync-path optimization work (PLAN.md P1+). The echo semantics
mirror the template `EchoServer.Echo` at the envelope level
(`benchmark/bench/bench.go`).

Baseline recorded: 2026-08-28, commit at P0 completion.
Machine: macOS (darwin/arm64, Apple Silicon), Go 1.27, Dart 3.14 dev / Flutter 3.48 master.
Payload: 64 bytes. Native driver uses a fresh `ReceivePort` per call and the
same allocation/free pattern as `lib/bridge/bridge_native.dart`.

## Native (FFI, c-shared libbench → Go)

Unit: microseconds per round trip (n=5000 after 200 warmup).

| metric | value |
|---|---|
| min | 21 µs |
| p50 | 39 µs |
| p90 | 64 µs |
| p99 | 129 µs |
| max | 1070 µs |
| mean | 45.9 µs |

## Web (Go wasm worker, Chrome, postMessage envelope)

Unit: milliseconds per round trip (n=2000 after 200 warmup). Standard Go
wasm (not TinyGo); production release builds use TinyGo, so absolute numbers
differ — treat these as a Go-wasm baseline.

| metric | value |
|---|---|
| min | 0.00 ms |
| p50 | 0.10 ms |
| p90 | 0.20 ms |
| p99 | 0.50 ms |
| max | 14.3 ms |
| mean | 0.124 ms |

## How to reproduce

Native:

```sh
go build -buildmode=c-shared -o benchmark/native/libbench.dylib ./benchmark/native
dart run benchmark/native/bench.dart --n 5000
# flags: --n, --warmup, --payload (bytes), --lib (path to libbench)
```

Web:

```sh
GOOS=js GOARCH=wasm go build -o benchmark/web/worker.wasm ./benchmark/web/worker
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" benchmark/web/
go run ./benchmark/web/driver --dir benchmark/web --n 2000
# flags: --n, --warmup, --payload (bytes), --dir (page assets dir), --timeout
```

The web page (`benchmark/web/index.html` + `bench.js` + `worker.js`) drives
the real worker protocol: global Done handshake, Init exchange, then one
fresh `MessageChannel` per echo call, timing each round trip with
`performance.now()`.

## P1 — Response zero-copy + allocator contract

Re-measured after P1 (2026-08-29): response parsing now reads the C-heap
`asTypedList` view directly (defensive `Uint8List.fromList` removed, B4) and
response containers are freed through the Go-exported `FreeBytesContainer`
symbol instead of Dart's `malloc.free` (B7 contract fixed). Same machine and
harness as the baseline; A/B against the pre-P1 code stashed and re-run in the
same session to cancel machine-load drift (baseline table above was recorded
on a quieter machine and is not directly comparable).

| payload | pre-P1 (same session) | P1 | delta |
|---|---|---|---|
| 64 B | p50 46.0 µs, mean 52.8 µs | p50 47.0 µs, mean 54.3 µs | ~noise |
| 64 KiB | p50 86.0 µs, mean 101.7 µs | p50 84.0 µs, mean 114.7 µs | p50 −2 µs, tail noisy |

Notes:

- The round trip is dominated by the fixed goroutine + `ReceivePort` overhead
  (B5, ~40–50 µs here), so removing one 64-byte memcpy barely moves the needle
  on small payloads; the copy saving grows with payload size (p50 improved at
  64 KiB). Tail percentiles on this loaded machine are noisy (max 2–3 ms
  outliers in both runs).
- P1 intentionally trades one cgo hop per response (the `FreeBytesContainer`
  export, ~1 µs) for the removed memcpy — the win is the allocator-contract
  correctness plus the zero-copy parse; bigger latency wins land in P2 (sync
  unary path).

## P2 — Sync FFI unary path

Re-measured after P2 (2026-08-29): unary RPCs now use the `CallSync` export
(`Response* CallSync(BytesContainer*)`, no goroutine, no `ReceivePort`, no port
round trip) via `Bridge.rpcSync` / `Bridge.rpcUnary` / `Transport.unary`. The
benchmark gained a `--sync` mode (`benchmark/native/bench.dart`). Same machine
and session as the P1 run.

| payload | async p50 / mean | sync p50 / mean | speedup |
|---|---|---|---|
| 64 B | 40.0 / 46.5 µs | 14.0 / 16.9 µs | 2.9× p50 |
| 64 KiB | 77.0 / 100.0 µs | 36.0 / 50.9 µs | 2.1× p50 |

Sync 64 B (5000 iters): min 6 µs, p50 14 µs, p90 23 µs, p99 ~80 µs, mean
~17 µs.

Notes:

- The sub-10 µs p50 target is not met (p50 14 µs, min 6 µs). The remaining
  cost is the protobuf serialize/parse pair, two malloc/free pairs and two cgo
  transitions (`CallSync` in, `FreeBytesContainer` out). P3 (packed structs,
  bypassing protobuf) is what would push p50 under 10 µs.
- Streaming, Reverse-RPC and cancel stay on the async envelope path; only
  unary calls take the sync fast path.

Reproduce: `dart run benchmark/native/bench.dart --n 5000 --payload 64 --sync`.

## P3 — Typed hot-path C exports (packed struct)

Measured after P3 (2026-08-29): a method marked `option (godash.hot) = true;`
with fixed-layout scalar messages bypasses protobuf, the envelope, the
goroutine/port machinery and `FreeBytesContainer` entirely. The Dart wrapper
packs the fields into a `calloc`'d buffer, calls the `//export <Service>_<Method>`
symbol through `DynamicLibrary.lookupFunction`, and unpacks the response.

Benchmark: `--hot` mode (`BenchHotAdd`, packed int64 in/out, 20000 iters).

| mode | min | p50 | p90 | p99 | mean |
|---|---|---|---|---|---|
| hot (packed int64) | 0 µs | 0 µs | 1 µs | 1 µs | ~0.5 µs |
| sync (envelope, 64 B) | 10 µs | 18 µs | 25 µs | 95 µs | ~22 µs |
| async (envelope, 64 B) | 27 µs | 48 µs | 74 µs | 200 µs | ~57 µs |

Notes:

- The <2 µs acceptance target is met with a wide margin (mean ~0.5 µs, p99 1 µs).
  The measurement includes the Dart `ByteData` pack/unpack, two `malloc`/`calloc`
  calls, the cached symbol lookup and the FFI round trip.
- `p50 0 µs` means below `Stopwatch`'s reported microsecond resolution for many
  iterations; the mean is the reliable figure here.
- Hot methods are native-only by construction: web uses the identical generated
  API but `Transport.supportsHotPath == false`, so it serializes to the protobuf
  envelope. Ineligible (`hot`-marked but string/nested/repeated) methods also stay
  on the envelope.

Reproduce: `dart run benchmark/native/bench.dart --n 20000 --hot`.

## P4 — Request ownership transfer

Re-measured after P4 (2026-08-29): the `RPC` and `CallSync` exports no longer
copy the request through `C.GoBytes`; they parse zero-copy from the Dart-owned
buffer with `unsafe.Slice` (`UnmarshalVT` copies the fields into Go memory).
Dart still frees the request container right after the export returns, so
cancel/timeout cannot leak it.

| payload | path | before P4 (p50/mean) | after P4 (p50/mean) |
|---|---|---|---|
| 64 B | async | 40 / ~47 µs | 40 / ~47 µs |
| 64 B | sync | 14 / ~17 µs | 14 / ~17 µs |
| 64 KiB | async | 77 / ~100 µs | 76 / ~95 µs |
| 64 KiB | sync | 36 / ~51 µs | 34 / ~47 µs |
| hot (int64) | hot | 0–1 / ~0.5 µs | 0–1 / ~0.47 µs |

Notes:

- The removed copy is one `memcpy` of the request payload. It is invisible at
  64 B (the round trip is dominated by the transport) and visible as a few µs at
  64 KiB, where the copy is 64 KiB.
- The main deliverable is correctness: zero copies between serialize and
  unmarshal on native, and no request-buffer lifetime bookkeeping (Dart frees
  unconditionally right after the synchronous export).
- Web keeps the defensive re-copy into a JS-owned `ArrayBuffer` (B6): a
  transferred buffer must already be JS-owned. Evaluated and documented in
  `lib/bridge/bridge_web.dart`.

Reproduce: `dart run benchmark/native/bench.dart --n 5000 [--sync|--hot]`.

## Notes

- The measured path includes protobuf `Request.writeToBuffer()` /
  `Response.fromBuffer()` on the Dart side and `MarshalVT`/`UnmarshalVT` on
  the Go side, the zero-copy request view (`unsafe.Slice`, P4), the goroutine
  spawn and the `ReceivePort` round trip — i.e. everything P1 (response
  zero-copy) and P2 (sync FFI unary path) target.
- Native per-call cost breakdown of interest for later phases: the fixed
  goroutine + port overhead (~10 µs, B5) plus serialize/memcpy (B1–B4).
  Re-run this harness after each phase and append a row here.
