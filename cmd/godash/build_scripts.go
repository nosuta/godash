package main

import (
	"fmt"
	"strings"
)

// This file contains shell scripts that mirror the legacy Makefile targets.
// They are embedded as Go strings and executed via `/bin/sh -c` by the build
// subcommands. Variables in the scripts (e.g. $GODASH_PATH) are exported by
// envShell() in build.go so the scripts can stay close to the original Makefile
// form.

// protoGoScript returns the shell commands that regenerate Go protobuf code.
// It mirrors the PROTO_GO macro from the Makefile.
func protoGoScript() string {
	return `
GOPATH_BIN="$(go env GOPATH)/bin"

go install -C "$GODASH_PATH/cmd/protoc-gen-go-godash"

# 0. Clean
rm -rf go/pb/*
mkdir -p go/pb

# 1. Generate standard Go protobuf (for non-TinyGo)
protoc -I=proto -I="$GODASH_PATH/proto" \
  --plugin protoc-gen-go="$GOPATH_BIN/protoc-gen-go" \
  --go_out=go --go_opt=module=flap proto/echo.proto

# 2. Add build tag to standard Go files
for f in go/pb/*.pb.go; do
  if ! grep -q "go:build" "$f"; then
    if [ "$(uname)" = "Darwin" ]; then
      sed -i '' '1s/^/\/\/go:build !js\n\n/' "$f"
    else
      sed -i '1s/^/\/\/go:build !js\n\n/' "$f"
    fi
  fi
done

# 3. Temporarily move standard files to avoid overwrite
mkdir -p go/pb/tmp_std
mv go/pb/*.pb.go go/pb/tmp_std/

# 4. Generate Lite Go protobuf (for TinyGo) and godash Flap protobuf
protoc -I=proto -I="$GODASH_PATH/proto" \
  --plugin protoc-gen-go-lite="$GOPATH_BIN/protoc-gen-go-lite" \
  --plugin protoc-gen-go-godash="$GOPATH_BIN/protoc-gen-go-godash" \
  --go-lite_out=go --go-lite_opt=module=flap,features=marshal+unmarshal+size+equal+clone \
  --go-godash_out=go --go-godash_opt=module=flap,core_pkg=github.com/nosuta/godash/v2/pb \
  proto/echo.proto

# 5. Rename Lite files and add build tag
for f in go/pb/*.pb.go; do
  if [ "$(uname)" = "Darwin" ]; then
    sed -i '' '1s/^/\/\/go:build js\n\n/' "$f"
  else
    sed -i '1s/^/\/\/go:build js\n\n/' "$f"
  fi
  mv "$f" "${f%.go}_lite.go"
done

# 6. Restore standard files
mv go/pb/tmp_std/*.pb.go go/pb/
rmdir go/pb/tmp_std

# 7. Generate MarshalVT wrappers for standard Go
go run -C "$GODASH_PATH" ./cmd/gen_marshal_std "$PWD/go/pb"
`
}

// protoDartScript returns the shell commands that regenerate Dart protobuf code.
func protoDartScript() string {
	return `
GOPATH_BIN="$(go env GOPATH)/bin"

rm -rf lib/pb/*
mkdir -p lib/pb
go install -C "$GODASH_PATH/cmd/protoc-gen-dart-godash"

protoc -I=proto -I="$GODASH_PATH/proto" \
  --plugin protoc-gen-dart-godash="$GOPATH_BIN/protoc-gen-dart-godash" \
  --dart_out=lib/pb \
  --dart-godash_out=lib/pb \
  proto/echo.proto
`
}

// applyGoLicensesScript runs go-licenses and writes the Dart licenses file.
// The template path is read from $GODASH_LICENSES_TPL, which the Go
// handler exports after writing the embedded template to a temp file.
func applyGoLicensesScript() string {
	return `
mkdir -p lib/licenses
go tool -C go go-licenses report . --template "$GODASH_LICENSES_TPL" > lib/licenses/licenses.g.dart 2> /dev/null
`
}

// updateWebScript is a no-op kept for legacy callers. Web assets are now
// extracted by godash itself (assets.ExtractWeb) instead of being copied
// from a platform_templates/ directory in the project.
func updateWebScript() string {
	return ""
}

// prepareAndroidScript runs `flutter create` for Android and patches the
// AndroidManifest to add INTERNET permission.
func prepareAndroidScript() string {
	return `
flutter create -e --platforms=android .
perl -pi -e 's/(<manifest .*?>)/$1\n    <uses-permission android:name="android.permission.INTERNET" \/>/' android/app/src/main/AndroidManifest.xml
`
}

// prepareIOSScript runs `flutter create` for iOS and patches the entitlements.
func prepareIOSScript() string {
	return `
flutter create -e --platforms=ios .
perl -pi -e 's/(<\/dict>)/<key>com.apple.security.network.client<\/key>\n<true\/>\n<key>keychain-access-groups<\/key>\n<array\/>\n$1/' ios/Runner/DebugProfile.entitlements
perl -pi -e 's/(<\/dict>)/<key>com.apple.security.network.client<\/key>\n<true\/>\n<key>keychain-access-groups<\/key>\n<array\/>\n$1/' ios/Runner/Release.entitlements
`
}

// prepareMacosScript runs `flutter create` for macOS and patches entitlements.
func prepareMacosScript() string {
	return `
flutter create -e --platforms=macos .
perl -pi -e 's/(<\/dict>)/<key>com.apple.security.network.client<\/key>\n<true\/>\n<key>keychain-access-groups<\/key>\n<array\/>\n$1/' macos/Runner/DebugProfile.entitlements
perl -pi -e 's/(<\/dict>)/<key>com.apple.security.network.client<\/key>\n<true\/>\n<key>keychain-access-groups<\/key>\n<array\/>\n$1/' macos/Runner/Release.entitlements
`
}

// prepareWebScript runs `flutter create` for web. The godash web assets
// (index.html, worker.js, flutter_bootstrap.js, _headers, wasm_exec.js,
// scroll_worker.js) are extracted by the Go handler after flutter create
// overwrites the default files.
func prepareWebScript() string {
	return `
flutter create -e --platforms=web .
`
}

// buildScriptWebBuild returns the shell for `godash web build`.
func buildScriptWebBuild(e *projectEnv) string {
	return updateWebScript() + "\n" +
		protoGoScript() + "\n" +
		protoDartScript() + "\n" +
		wasmTinyGoScript() + "\n" +
		applyGoLicensesScript() + "\n" +
		`flutter build web --wasm --release`
}

// buildScriptWebRun returns the shell for `godash web run` (dev mode).
func buildScriptWebRun(e *projectEnv) string {
	return updateWebScript() + "\n" +
		protoGoScript() + "\n" +
		protoDartScript() + "\n" +
		wasmFullScript() + "\n" +
		`flutter run -d web-server`
}

// buildScriptAndroidLibArm64 builds arm64-v8a shared lib.
func buildScriptAndroidLibArm64(e *projectEnv) string {
	ndkToolchain := e.NDKPath + "/toolchains/llvm/prebuilt/darwin-x86_64/bin"
	return fmt.Sprintf(`
mkdir -p go/dart_api
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
CC="%s/aarch64-linux-android21-clang" \
go build -C go -ldflags="-w -s -extldflags=-Wl,-soname=%s" -buildmode=c-shared -tags='android' \
-o build/android-arm64-v8a/%s.so .
mkdir -p %s/arm64-v8a
cp go/build/android-arm64-v8a/%s.so %s/arm64-v8a/
`, ndkToolchain, e.LibName, e.LibName, e.AndroidPluginDir, e.LibName, e.AndroidPluginDir)
}

// buildScriptAndroidLibX86_64 builds x86_64 shared lib.
func buildScriptAndroidLibX86_64(e *projectEnv) string {
	ndkToolchain := e.NDKPath + "/toolchains/llvm/prebuilt/darwin-x86_64/bin"
	return fmt.Sprintf(`
CGO_ENABLED=1 GOOS=android GOARCH=amd64 \
CC="%s/x86_64-linux-android21-clang" \
go build -C go -ldflags="-w -s -extldflags=-Wl,-soname=%s" -buildmode=c-shared -tags='android' \
-o build/android-x86_64/%s.so .
mkdir -p %s/x86_64
cp go/build/android-x86_64/%s.so %s/x86_64/
`, ndkToolchain, e.LibName, e.LibName, e.AndroidPluginDir, e.LibName, e.AndroidPluginDir)
}

// buildScriptIOSLib builds iOS .a and xcframework.
func buildScriptIOSLib(e *projectEnv) string {
	return fmt.Sprintf(`
mkdir -p go/build/ios-arm64 %s/Headers $(dirname %s)
CGO_ENABLED=1 GOOS=ios GOARCH=arm64 CGO_CFLAGS="-fembed-bitcode" DEPLOYMENT_TARGET=%s \
SDK=iphoneos PLATFORM=ios CC="$PWD/clangwrap.sh" \
go build -C go -buildmode=c-archive -trimpath -tags='ios' \
-o build/ios-arm64/%s.a .
cp go/build/ios-arm64/%s.h %s/Headers/
rm -rf %s
xcodebuild -create-xcframework \
  -library go/build/ios-arm64/%s.a -headers %s/Headers \
  -output %s
`, e.IOSPluginDir, e.IOSFrameworkDir, e.IOSDeployment,
		e.LibName, e.LibName, e.IOSPluginDir,
		e.IOSFrameworkDir, e.LibName, e.IOSPluginDir, e.IOSFrameworkDir)
}

// buildScriptMacosLib builds macOS .a and xcframework.
func buildScriptMacosLib(e *projectEnv) string {
	macosCC := "$(xcrun --sdk " + e.MacosSDK + " --find clang)"
	macosSDKPath := "$(xcrun --sdk " + e.MacosSDK + " --show-sdk-path)"
	return fmt.Sprintf(`
mkdir -p go/build/macos-arm64 %s/Headers $(dirname %s)
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 SDK=%s CC=%s \
CGO_CFLAGS="-isysroot %s -mmacosx-version-min=%s" \
CGO_LDFLAGS="-isysroot %s -mmacosx-version-min=%s" \
go build -C go -ldflags='-w -s' -buildmode=c-archive -trimpath -tags debug \
-o build/macos-arm64/%s.a .
cp go/build/macos-arm64/%s.h %s/Headers/
rm -rf %s
xcodebuild -create-xcframework \
  -library go/build/macos-arm64/%s.a -headers %s/Headers \
  -output %s
`, e.MacosPluginDir, e.MacosFrameworkDir, e.MacosSDK, macosCC,
		macosSDKPath, e.MacosDeployment, macosSDKPath, e.MacosDeployment,
		e.LibName, e.LibName, e.MacosPluginDir,
		e.MacosFrameworkDir, e.LibName, e.MacosPluginDir, e.MacosFrameworkDir)
}

// wasmExecScript copies the standard Go wasm_exec.js into web/.
func wasmExecScript() string {
	return `cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/`
}

// wasmExecTinyGoScript copies the patched tinygo_wasm_exec.js into web/.
func wasmExecTinyGoScript() string {
	return `cp tinygo_wasm_exec.js web/wasm_exec.js`
}

// wasmFullScript builds the WASM worker using the standard Go compiler.
func wasmFullScript() string {
	return wasmExecScript() + "\n" + `go mod -C go download
GOOS=js GOARCH=wasm go build -C go -ldflags='-w -s' -o ../web/worker.wasm
` + updateGoBuildVersionScript() + "\n" + updateGoBuildVersionWebScript()
}

// wasmTinyGoScript builds the WASM worker using TinyGo.
func wasmTinyGoScript() string {
	return wasmExecTinyGoScript() + "\n" + `go mod -C go download
GOOS=js GOARCH=wasm tinygo build -C go -no-debug -panic=trap -opt=2 -o ../web/worker.wasm
` + updateGoBuildVersionScript() + "\n" + updateGoBuildVersionWebScript()
}

// updateGoBuildVersionScript regenerates lib/version/version.dart using
// the gen_go_build_version tool shipped inside the godash module.
func updateGoBuildVersionScript() string {
	return `go run github.com/nosuta/godash/v2/cmd/gen_go_build_version lib/version/version.dart`
}

// updateGoBuildVersionWebScript rewrites web/worker.js with a cache-busting
// asset version.
func updateGoBuildVersionWebScript() string {
	return `perl -pi -e "s/const asset.*?$/$1const asset = \"worker.wasm?v=$(date +%s)\";/" web/worker.js`
}

// sqliteDownloadScript downloads the sqlite3 wasm distribution and copies the
// relevant files into web/.
func sqliteDownloadScript() string {
	return `
curl -L -o /tmp/sqlite-wasm.zip https://sqlite.org/2026/sqlite-wasm-3530300.zip
unzip -o /tmp/sqlite-wasm.zip -d /tmp/sqlite-wasm
cp /tmp/sqlite-wasm/sqlite-wasm-3530300/jswasm/sqlite3.js web/sqlite3.js
cp /tmp/sqlite-wasm/sqlite-wasm-3530300/jswasm/sqlite3.wasm web/sqlite3.wasm
cp /tmp/sqlite-wasm/sqlite-wasm-3530300/jswasm/sqlite3-opfs-async-proxy.js web/sqlite3-opfs-async-proxy.js
rm -rf /tmp/sqlite-wasm /tmp/sqlite-wasm.zip
`
}

// prepareWasmTestScript copies sqlite3 / scroll_worker.js to godash and
// installs go_js_wasm_exec.
func prepareWasmTestScript(e *projectEnv) string {
	return fmt.Sprintf(`
cp web/sqlite3.js %s/cmd/go_js_wasm_exec/
cp web/sqlite3.wasm %s/cmd/go_js_wasm_exec/
cp web/sqlite3-opfs-async-proxy.js %s/cmd/go_js_wasm_exec/
cp web/scroll_worker.js %s/cmd/go_js_wasm_exec/
go install -C %s/cmd/go_js_wasm_exec
`, e.GodashPath, e.GodashPath, e.GodashPath, e.GodashPath, e.GodashPath)
}

// dartAPIScript clones the Dart SDK and copies the C API headers into go/dart_api.
func dartAPIScript() string {
	return `
git clone --depth 1 --branch stable https://github.com/dart-lang/sdk /tmp/github.com/dart-lang/sdk
mkdir -p go/dart_api
cp -r /tmp/github.com/dart-lang/sdk/runtime/include/* go/dart_api/
cp /tmp/github.com/dart-lang/sdk/LICENSE go/dart_api/
rm -rf /tmp/github.com/dart-lang/sdk
`
}

// protoShellScript returns the protobuf regeneration shell (Go + Dart).
func protoShellScript() string {
	return protoGoScript() + "\n" + protoDartScript()
}

// flutterCreateBlocks returns the conditional `flutter create` shell that
// creates any platform directory that does not yet exist.
func flutterCreateBlocks(createMissingPlatforms bool) string {
	if !createMissingPlatforms {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
if [ ! -d web ]; then
`)
	b.WriteString(prepareWebScript())
	b.WriteString(`
fi
if [ ! -d android ]; then
`)
	b.WriteString(prepareAndroidScript())
	b.WriteString(`
fi
if [ ! -d ios ]; then
`)
	b.WriteString(prepareIOSScript())
	b.WriteString(`
fi
if [ ! -d macos ]; then
`)
	b.WriteString(prepareMacosScript())
	b.WriteString(`
fi
`)
	return b.String()
}

// goModBootstrap returns the standard `go mod` preparation shell (tidy,
// download, install tools, activate protoc plugin).
func goModBootstrap() string {
	return `
dart pub global activate protoc_plugin
go mod -C go tidy
go -C go mod download
go -C go install tool
`
}

// buildPrepareScript returns the full prepare pipeline (legacy combined
// shell). Prefer protoShellScript + writeWiringFiles + licensesShellScript
// + flutterCreateBlocks for new code paths.
func buildPrepareScript(e *projectEnv, createMissingPlatforms bool) string {
	return goModBootstrap() + "\n" +
		protoShellScript() + "\n" +
		applyGoLicensesScript() + "\n" +
		flutterCreateBlocks(createMissingPlatforms)
}

// buildPrepareWasmTestScript downloads sqlite3 wasm assets (if missing) and
// prepares the go_js_wasm_exec tool.
func buildPrepareWasmTestScript(e *projectEnv) string {
	var b strings.Builder
	b.WriteString(`
# Ensure sqlite3.js exists; download if missing
if [ ! -f web/sqlite3.js ]; then
`)
	b.WriteString(sqliteDownloadScript())
	b.WriteString(`
fi
`)
	b.WriteString(prepareWasmTestScript(e))
	return b.String()
}

// buildCleanScript removes build artifacts and runs `flutter clean`.
func buildCleanScript(e *projectEnv) string {
	return fmt.Sprintf(`
rm -f web/wasm_exec.js
rm -f web/sqlite3.js
rm -f web/sqlite3-opfs-async-proxy.js
rm -f web/sqlite3.wasm
rm -f %s/cmd/go_js_wasm_exec/sqlite3.js
rm -f %s/cmd/go_js_wasm_exec/sqlite3-opfs-async-proxy.js
rm -f %s/cmd/go_js_wasm_exec/sqlite3.wasm
rm -f %s/cmd/go_js_wasm_exec/wasm_exec.js
rm -rf go/build/
rm -rf go/pb/
rm -rf lib/pb/
rm -rf %s/Headers
rm -rf %s/Headers
rm -rf %s
rm -rf %s
rm -f %s/x86_64/%s.so
rm -f %s/arm64-v8a/%s.so
rm -f lib/version/version.dart
flutter clean
`, e.GodashPath, e.GodashPath, e.GodashPath, e.GodashPath,
		e.IOSPluginDir, e.MacosPluginDir, e.IOSFrameworkDir, e.MacosFrameworkDir,
		e.AndroidPluginDir, e.LibName, e.AndroidPluginDir, e.LibName)
}

// buildResetScript removes platform directories and runs clean.
func buildResetScript(e *projectEnv) string {
	return buildCleanScript(e) + `
rm -rf web
rm -rf android
rm -rf ios
rm -rf macos
`
}
