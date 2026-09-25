---
name: godash-native-debug
description: Use when a godash app builds but crashes, hangs, or returns no response on a real iOS/macOS/Android device — missing dlsym symbols, keychain errors (-34018), entitlements/signing, MainActivity not found, or when capturing device logs. Triggers on "Send Echo no response", "Failed to lookup symbol", "ClassNotFoundException", "errSecMissingEntitlement", "entitlement", "device logs", "logcat", "idevicesyslog".
---

# Debugging godash on native platforms

The Go code is linked natively (Android `.so`, iOS/macOS static archive), and
the Dart bridge resolves exports at runtime with `dlsym`. Most "no response"
bugs are one of the following.

## iOS/macOS: `Failed to lookup symbol '...'`

Apple's linker dead-strips `//export` symbols that are only resolved at runtime.
Every symbol the Dart bridge dlsyms must be force-linked in
`packages/native_internal/{ios,macos}/native_internal/Package.swift`:
`_InitializeDartAPI`, `_RPC`, `_FreeBytesContainer`. Per-project
hot-path exports (`Service_Method`) are injected by the CLI into the
materialised `.godash/native_internal/*/Package.swift` at the
`//GODASH_HOT_LINKER_FLAGS` marker.

Fix for an existing project: update the module (`go -C go get
github.com/nosuta/godash/v2@latest`) then `godash prepare`, then rebuild.

## macOS: `errSecMissingEntitlement` (-34018)

`flutter_secure_storage` uses the data-protection keychain by default, which
needs the `keychain-access-groups` entitlement (and a dev team). The template
uses the file-based keychain instead:
`FlutterSecureStorage(mOptions: MacOsOptions(usesDataProtectionKeychain: false))`.

## macOS/iOS: signing

Don't inject the empty `keychain-access-groups` entitlement — it is a restricted
entitlement that forces a development certificate and breaks ad-hoc debug builds.

## Android: `ClassNotFoundException: <bundleId>.MainActivity`

`applyConfig` renames the gradle `namespace`/`applicationId`; the generated
`MainActivity.kt` package must be renamed/moved too. Existing projects: move
`android/app/src/main/kotlin/com/example/flap/MainActivity.kt` to the bundle-id
path and update `package ...`; or set `namespace` back to `com.example.flap`
(applicationId may differ from namespace).

## Reactivity: web worker has no `main`

Standard `GOOS=js` builds have no `-tags`; the JS `main()` variants must be
complementary (`js && debug` / `js && !debug`). The template worker calls
`flaprpc.Close()`, not `rpc.Close()`.

## Capturing logs

- Android: `adb logcat AndroidRuntime:E libc:F DEBUG:F GoLog:V Flutter:V '*:S'`
  (wireless: `adb pair <ip:port>` then `adb connect <ip:port>`; or an emulator).
- iOS/macOS: `flutter run -d <device>`; Xcode → Window → Devices and Simulators →
  Open Console; or `idevicesyslog -u <udid>`.
- Filter Console by the app process name (e.g. `Runner`) — SpringBoard/UIKit
  lines are noise. Look for `flutter:`, `Failed to lookup symbol`, `Fatal
  signal`, `ClassNotFoundException`, `Native bridge instantiate`.
