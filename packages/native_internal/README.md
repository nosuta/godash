# native_internal

Flutter plugin that bundles the godash Go native library for Android, iOS, and macOS.

The plugin itself only exposes a `getPlatformVersion` method channel. The real
work is done via the static library / shared library (`libgodash`) that the root
`Makefile` builds from `go/` and copies here.

## How it is built

The root project Makefile builds the Go shared library for each platform and
places it where this plugin expects it:

| Platform | Built artifact location                                  |
|----------|----------------------------------------------------------|
| Android  | `android/src/main/jniLibs/{arm64-v8a,x86_64}/libgodash.so`  |
| iOS      | `ios/native_internal/Frameworks/native_internal.xcframework/` |
| macOS    | `macos/native_internal/Frameworks/native_internal.xcframework/` |

Run `make apk`, `make ios`, or `make macos_run` from the project root to build
and run on the corresponding platform.
