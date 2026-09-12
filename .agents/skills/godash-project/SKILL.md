---
name: godash-project
description: Use when creating, upgrading, or debugging a godash Flutter project — `godash new`/`prepare`/`upgrade`, the no-checkout consumption model, `.godash/native_internal`, `$GODASH_MODULE_DIR`, template version pinning, or generated wiring. Triggers on "godash new", "godash prepare", "godash upgrade", ".godash", "native_internal", "GODASH_MODULE_DIR".
---

# godash project consumption model

A project does **not** need a local godash checkout:

- `pubspec.yaml`: `godash: ^2.2.7` (pub.dev) + `native_internal: path: .godash/native_internal`.
- `go/go.mod`: `require github.com/nosuta/godash/v2 vX.Y.Z` (no `replace`).
- `.godash/native_internal` is the Flutter plugin shell, materialised by the CLI
  from the resolved Go module (gitignored build crate).

## Where things come from

- The CLI resolves the source dir from the project's own Go module:
  `GODASH_MODULE_DIR="$(go -C go list -m -f '{{.Dir}}' github.com/nosuta/godash/v2)"`.
  This is the module cache for a version dep, or the checkout for a path replace.
- protoc plugins / generators: `go build -C go -o ...` / `go run -C go ...`.
- `godash/options.proto`: `$GODASH_MODULE_DIR/proto`.
- `native_internal`: copied from `$GODASH_MODULE_DIR/packages/native_internal`
  into `.godash/native_internal` (then `chmod -R u+w`).
- Wiring / proto / web assets: regenerated on every `prepare`/build.

## Version pinning

- `godash new` pins a project to the CLI's own version (`cliVersion()` from
  `runtime/debug.ReadBuildInfo`) in both `pubspec.yaml` and `go/go.mod`.
- `.godash/native_internal` comes from the **Go module version in the project's
  go.mod** — not the Dart package or the CLI. To get a native plugin fix in an
  existing project, run `godash upgrade` (or
  `go -C go get github.com/nosuta/godash/v2@vX.Y.Z`) then `godash prepare`.

## Commands

`godash new`, `prepare`, `proto`, `web run|build`, `android apk|appbundle`,
`ios`, `macos run|build`, `lib <android|ios|macos>`, `clean`, `reset`,
`upgrade`, `doctor`.

## Notes

- `godash upgrade` reports the **resolved** version (from `pubspec.lock`) and
  records it in `.godash-template`.
- Path-replace projects (local dev / forks) still work; the CLI auto-provisions
  the declared checkout. `GODASH_REPO` / `GODASH_REF` override the source,
  `GODASH_NO_PROVISION=1` disables it.
- `go_js_wasm_exec` is a nested Go module, so the wasm test harness is skipped
  for version deps (only available with a path-replace checkout).
