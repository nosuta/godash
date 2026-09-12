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
  undefined `rpc.Close()`; it now calls the project's `flaprpc.Close()`.
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
