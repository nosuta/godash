## 2.3.6

* The web host assets are re-extracted into a project on every `godash prepare`
  / `godash web build`, so fixes must live in the embedded templates rather
  than a project's `web/`.
  * `web/index.html` no longer constrains `<body>` (`height: 95vh` + flex
    centering). That made the Flutter view shorter than the visual viewport and
    fed a stale `physicalSize` into the engine's keyboard-inset math, which made
    `WebSocket`/keyboard handling flaky on mobile web. The loading spinner is
    now centered with fixed positioning instead, and the page suppresses the
    upstream Flutter debug assertion `ViewInsets cannot be negative` so it no
    longer spams the console.
  * `web/worker.js` wraps `WebSocket.prototype.close` to clamp close codes the
    browser rejects (a Go client may close a broken connection with 1006) to
    1000 and to truncate the reason. Release workers build with TinyGo
    `-panic=trap`, which cannot recover the JS exception, so an invalid-code
    close previously trapped and killed the whole worker
    (`failed to launch root worker`).

## 2.3.5

* Bump `go-wasmsqlite` to v0.3.2. The web (OPFS) SQLite driver no longer
  prints per-query debug output (`First row: …`, `Query returned …`, `🔍 …`) to
  the browser console in release builds, and is slimmed down to the OO direct
  route that godash uses: the golang-migrate integration, embedded-asset API,
  VFS detection, dump/load helpers and the demo app are removed. This also drops
  the `golang-migrate` and `go-multierror` transitive dependencies.

## 2.3.4

* Fix generated wiring for projects whose Go module path has several path
  segments (e.g. `github.com/acme/app`). The `pb` import alias was built from
  the whole module path (`github.com/acme/apppb`), an invalid Go identifier
  that broke `godash prepare`; the alias is now derived from the last path
  segment (`apppb`).
* `godash prepare` runs the generated `main*.go`, `go/rpc/rpc_handler.go` and
  `go/rpc/hot_handler.go` through `go/format`, so project wiring is gofmt-clean.
* Bump `go-wasmsqlite` to v0.3.1. Parameterless multi-statement `Exec` now goes
  through SQLite's `exec()`, so DDL such as `CREATE TABLE ...; CREATE INDEX ...`
  is no longer silently truncated to its first statement in the web (OPFS)
  driver.

## 2.3.3

* Fix `godash web run`/`godash web build` to use the shared web shell. 2.3.2
  added the cross-origin isolation headers to `buildScriptWebRun`, but `godash
  web run` composed its shell inline, so `flutter run` never received
  `--web-header` and web SQLite still failed with
  `failed to open database: OPFS is not supported`. The real command now sends
  `Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Embedder-Policy: require-corp` (verified on the served page).
* `godash web build` also ensures the sqlite3 web assets are downloaded.

## 2.3.2

* Fix the SQLite demo on web: `godash web run` now starts the dev server with
  the cross-origin isolation headers (`Cross-Origin-Opener-Policy: same-origin`,
  `Cross-Origin-Embedder-Policy: require-corp`) via `--web-header`, so OPFS is
  available and the web database no longer fails with
  `failed to open database: OPFS is not supported`. Production hosting already
  got these from `web/_headers`.

## 2.3.1

* `godash new` now scaffolds a working SQLite demo: a `CounterService`
  (`proto/counter.proto`) persisted through the database opened in
  `go/rpc/entrypoint.go`, with an Increment button in `lib/main.dart`.
* Protobuf generation compiles every `proto/*.proto`, not just `echo.proto`, so
  a project can add services without editing godash-owned scripts.
* `godash web run` / `godash web build` download the sqlite3 web assets
  (`web/sqlite3.js`, `web/sqlite3.wasm`) when missing, so the SQLite demo works
  on web after `godash clean`.
* Fix `godash new` from a dirty dev checkout writing an unresolvable
  `godash: ^X.Y.Z+dirty` pub constraint (build metadata is now stripped).
* Docs: document the SQLite demo in the template README.

## 2.3.0

* Rename the remaining old project-name identifiers to `godash` (breaking for
  existing projects): generated code is now `*.godash.go` / `*.godash.dart`
  (was `*.flap.*`), and the native library is `libgodash` (was `libflap`). The
  `FLAP_TEMPLATE` alias is removed; use `GODASH_TEMPLATE`.
* Parameterise the scaffolded app identity. `godash new` now rewrites the
  template app placeholder to the project's package name in `pubspec.yaml`,
  `go/go.mod`, `go_package` and the Dart/Go imports (the fixed template name
  could not become `godash` without colliding with the dependency).
* Proto generation resolves the module name from `go/go.mod` (`$GO_MODULE`)
  instead of a hardcoded value.

Existing projects: after `godash prepare`, update the generated-client imports
under `lib/`:
`rg -l '\.flap\.dart' lib | xargs sed -i '' 's/\.flap\.dart/.godash.dart/g'`.

## 2.2.11

* Show the real CLI version in `godash -h`: the installed Go module tag
  (`go install ...@vX.Y.Z`) or the `-ldflags` value, instead of always
  `latest`.
* Add `godash --version` (also `-v` and `godash version`) to print the version.

## 2.2.10

* Remove the obsolete `godash dart-api` command. The Dart C API headers are
  vendored inside the godash module (cgo resolves them from there), so nothing
  needs to clone the Dart SDK into the project.
* Document the release procedure in `RELEASING.md` and add tool-neutral agent
  skills under `.agents/skills/`.
* README cleanups: drop `dart-api` and the unused `go/dart_api/` entry, and bump
  the example godash constraint to `^2.2.7`.

## 2.2.9

* `godash upgrade` now reports the resolved godash version (e.g. `2.2.8`)
  instead of the pubspec constraint (e.g. `^2.2.0`), and records that version
  in `.godash-template` and the commit message.
* `godash upgrade` no longer runs protobuf generation twice.

## 2.2.8

* Pin newly scaffolded projects to the CLI's own godash version in both
  `pubspec.yaml` and `go/go.mod`. The Go module supplies
  `.godash/native_internal`, so a new project now gets the matching native
  plugin fixes (e.g. the iOS force-link flags) instead of staying pinned to an
  old template version.

## 2.2.7

* Fix iOS/macOS calls failing with `Failed to lookup symbol
  'FreeBytesContainer'`: the `native_internal` SPM manifests now force-link
  every Go export the Dart bridge resolves (`InitializeDartAPI`, `RPC`,
  `CallSync`, `FreeBytesContainer`), so the Apple linker no longer
  dead-strips them.
* Force-link per-project hot-path exports too: the CLI injects a `-u` flag for
  each discovered hot method into the materialised `Package.swift`.

## 2.2.6

* Fix Android scaffolding: `applyConfig` now renames and moves the generated
  `MainActivity` Kotlin package when the bundle id changes the gradle
  namespace/applicationId. Previously the app crashed on launch with
  `ClassNotFoundException: <bundleId>.MainActivity`.

## 2.2.5

* Fix the macOS starter app keychain failure (`errSecMissingEntitlement`,
  code -34018): `app_encryption_key.dart` now uses the file-based keychain
  (`MacOsOptions(usesDataProtectionKeychain: false)`), so the app encryption
  key can be stored with ad-hoc debug signing and no development team.

## 2.2.4

* Fix the starter app on native platforms: `lib/main.dart` no longer evaluates
  `Uri.base.origin` (a web-only expression) unconditionally, which threw
  `Bad state: Origin is only applicable schemes http and https` on
  macOS/iOS/Android. The worker URL is now only computed on web (`kIsWeb`).

## 2.2.3

* Fix `godash web run`: the generated `main_js_release.go` referenced an
  undefined `rpc.Close()`; it now calls the project entrypoint's `Close()`.
* Fix native debug runs (e.g. `godash macos run`): stop injecting the empty
  `keychain-access-groups` entitlement. It is a restricted entitlement that
  forced a development certificate and broke ad-hoc debug signing, and the
  darwin secure-storage plugin ignores access groups on macOS.

## 2.2.2

* Fix `godash web run`: a plain standard-Go wasm build had no `func main`
  (the release variant was tagged `js && tinygo`). The js variants are now
  complementary (`js && debug` / `js && !debug`), so `-tags`-less builds and
  TinyGo each get exactly one `main`.
* Fix native builds (e.g. `godash macos run`): the materialised
  `.godash/native_internal` is now made writable. `cp -R` had preserved the
  read-only directory modes of the Go module cache, so `mkdir`/`cp` inside
  the plugin failed.

## 2.2.1

* Fix native build output for path-replace projects: resolve the
  `native_internal` plugin directory from `pubspec.yaml` so path-replace
  projects keep building into the checkout, while version-pinned projects use
  the project-local `.godash/native_internal`.

## 2.2.0

* Publish godash to pub.dev so projects consume it as `godash: ^2.2.0` instead
  of a local `path: ../godash` checkout.
* Resolve the godash code-generation tools and `godash/options.proto` from the
  project's own Go module graph (`$GODASH_MODULE_DIR`); the Go module is now
  consumed from the module cache with no `replace` directive.
* Materialise the `native_internal` Flutter plugin into the project-local
  `.godash/native_internal` directory on prepare/build.
* `godash upgrade` for version-pinned projects runs `flutter pub upgrade
  godash native_internal` and `go get -u github.com/nosuta/godash/v2`.
* Keep auto-provisioning of a godash checkout for path-replace projects.
