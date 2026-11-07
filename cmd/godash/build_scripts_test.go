package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func sampleEnv() *projectEnv {
	return &projectEnv{
		Root:              "/proj",
		GodashPath:        "/godash",
		LibName:           "libgodash",
		NDKPath:           "/ndk",
		IOSDeployment:     "13.0",
		MacosDeployment:   "10.15",
		MacosSDK:          "macosx",
		IOSPluginDir:      ".godash/native_internal/ios",
		MacosPluginDir:    ".godash/native_internal/macos",
		AndroidPluginDir:  ".godash/native_internal/android/src/main/jniLibs",
		XCFrameworkName:   "native_internal.xcframework",
		IOSFrameworkDir:   ".godash/native_internal/ios/native_internal/Frameworks/native_internal.xcframework",
		MacosFrameworkDir: ".godash/native_internal/macos/native_internal/Frameworks/native_internal.xcframework",
	}
}

// TestScriptsHaveNoFfigenOrExportedHeader is the regression guard for the
// ffigen removal (PLAN.md P3 follow-up): no generated build script may invoke
// ffigen or depend on the cgo `exported.h` artifact.
func TestScriptsHaveNoFfigenOrExportedHeader(t *testing.T) {
	e := sampleEnv()
	scripts := map[string]string{
		"android-arm64":  buildScriptAndroidLibArm64(e),
		"android-x86_64": buildScriptAndroidLibX86_64(e),
		"ios":            buildScriptIOSLib(e),
		"macos":          buildScriptMacosLib(e),
		"web-build":      buildScriptWebBuild(e),
		"web-run":        buildScriptWebRun(e),
		"proto-go":       protoGoScript(),
		"proto-dart":     protoDartScript(),
		"tinygo":         wasmTinyGoScript(),
		"licenses":       applyGoLicensesScript(),
	}
	for name, s := range scripts {
		if strings.Contains(s, "ffigen") {
			t.Errorf("%s: build script must not reference ffigen", name)
		}
		if strings.Contains(s, "exported.h") {
			t.Errorf("%s: build script must not reference exported.h", name)
		}
		if strings.Contains(s, "GODASH_PATH") {
			t.Errorf("%s: build script must resolve godash via $GODASH_MODULE_DIR, not GODASH_PATH", name)
		}
		// A missing/extra fmt argument renders as "%!..."; catch arg drift.
		if strings.Contains(s, "%!") {
			t.Errorf("%s: build script has a fmt formatting mismatch:\n%s", name, s)
		}
	}
}

// TestGodashModuleBootstrap guards the no-checkout build contract: the
// bootstrap must resolve the source dir from the Go module graph and
// materialise the native plugin project-locally.
func TestGodashModuleBootstrap(t *testing.T) {
	s := godashModuleBootstrap()
	for _, want := range []string{
		"go -C go list -m -f '{{.Dir}}' github.com/nosuta/godash/v2",
		"export GODASH_MODULE_DIR",
		`GODASH_MATERIALIZE_NATIVE" = "1"`,
		`cp -R "$GODASH_MODULE_DIR/packages/native_internal/." "$GODASH_NATIVE_DIR/"`,
		`chmod -R u+w "$GODASH_NATIVE_DIR"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("godashModuleBootstrap() missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(protoGoScript(), `-I="$GODASH_MODULE_DIR/proto"`) {
		t.Errorf("protoGoScript must include the module proto dir:\n%s", protoGoScript())
	}
}

// TestWebScriptsProvideSqliteAssets guards the SQLite web contract: the web
// worker importScripts()es sqlite3.js and go-wasmsqlite loads sqlite3.wasm, so
// every web build/run must download them into web/ when missing. It also guards
// that protoc runs over every proto file (not just echo.proto), so a project can
// add services without editing godash-owned scripts.
func TestWebScriptsProvideSqliteAssets(t *testing.T) {
	e := sampleEnv()
	for name, s := range map[string]string{
		"web-build":       buildScriptWebBuild(e),
		"web-run":         buildScriptWebRun(e),
		"web-build-shell": webBuildShell(""),
		"web-run-shell":   webRunShell(""),
	} {
		if !strings.Contains(s, "web/sqlite3.js") {
			t.Errorf("%s must ensure the sqlite3 web assets are present:\n%s", name, s)
		}
		if !strings.Contains(s, "sqlite3.wasm") {
			t.Errorf("%s must download sqlite3.wasm:\n%s", name, s)
		}
	}
	// The dev server must be cross-origin isolated or web SQLite/OPFS fails
	// with "OPFS is not supported".
	if s := webRunShell(""); !strings.Contains(s, "Cross-Origin-Opener-Policy=same-origin") ||
		!strings.Contains(s, "Cross-Origin-Embedder-Policy=require-corp") {
		t.Errorf("web run must send cross-origin isolation headers:\n%s", s)
	}
	for name, s := range map[string]string{
		"proto-go":   protoGoScript(),
		"proto-dart": protoDartScript(),
	} {
		if !strings.Contains(s, "proto/*.proto") {
			t.Errorf("%s must glob all proto files, not just echo.proto:\n%s", name, s)
		}
	}
}

// TestSqliteOpfsDeletePatchString guards the Chromium OPFS quota workaround:
// the patch must replace every SQLite proxy removeEntry() call, insert the
// truncate-then-delete helper once, and be idempotent.
func TestSqliteOpfsDeletePatch(t *testing.T) {
	proxy := `  const closeSyncHandleNoThrow = async (fh)=>{
    try{await closeSyncHandle(fh)}
    catch(e){
      warn("closeSyncHandleNoThrow() ignoring:",e,fh);
    }
  };
  try{ await fh.dirHandle.removeEntry(fh.filenamePart) }
  await hDir.removeEntry(filenamePart, {recursive});
  await hDir.removeEntry(filenamePart);
`
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/web", 0o755); err != nil {
		t.Fatal(err)
	}
	path := dir + "/web/sqlite3-opfs-async-proxy.js"
	if err := os.WriteFile(path, []byte(proxy), 0o644); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		cmd := exec.Command("/bin/sh", "-c", sqliteOpfsDeletePatchScript())
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sqlite OPFS delete patch failed: %v\n%s", err, out)
		}
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)
	for _, want := range []string{
		"// opfs-truncate-before-removeEntry",
		"sah.truncate(0);",
		"await deleteEntry(fh.dirHandle, fh.filenamePart)",
		"await deleteEntry(hDir, filenamePart, recursive)",
		"await deleteEntry(hDir, filenamePart);",
		"return hDir.removeEntry(filenamePart, {recursive});",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patched proxy missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "opfs-truncate-before-removeEntry"); n != 1 {
		t.Errorf("patch marker appears %d times, want 1", n)
	}
	if strings.Contains(got, "await fh.dirHandle.removeEntry") ||
		strings.Contains(got, "await hDir.removeEntry(filenamePart, {recursive})") ||
		strings.Contains(got, "await hDir.removeEntry(filenamePart);") {
		t.Errorf("patch left a direct removeEntry call:\n%s", got)
	}
}

// TestPrepareEntitlementsAvoidRestrictedKeychain guards macOS/iOS debug runs:
// the empty keychain-access-groups entitlement is a restricted entitlement that
// forces a development certificate, and the darwin plugin ignores access groups
// (they are iOS-only), so godash must not inject it.
func TestPrepareEntitlementsAvoidRestrictedKeychain(t *testing.T) {
	for name, s := range map[string]string{
		"ios":   prepareIOSScript(),
		"macos": prepareMacosScript(),
	} {
		if strings.Contains(s, "keychain-access-groups") {
			t.Errorf("%s prepare script must not add keychain-access-groups:\n%s", name, s)
		}
		if !strings.Contains(s, "com.apple.security.network.client") {
			t.Errorf("%s prepare script should still grant network.client:\n%s", name, s)
		}
	}
}

// TestNativeInternalForcesAllDartSymbols guards iOS/macOS static linking: the
// Dart bridge resolves these Go exports at runtime with dlsym, so the SPM
// package must force-link every one of them or the linker dead-strips them
// (e.g. "Failed to lookup symbol 'FreeBytesContainer'").
func TestNativeInternalForcesAllDartSymbols(t *testing.T) {
	symbols := []string{"_InitializeDartAPI", "_RPC", "_FreeBytesContainer"}
	for _, rel := range []string{
		"../../packages/native_internal/ios/native_internal/Package.swift",
		"../../packages/native_internal/macos/native_internal/Package.swift",
	} {
		b, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, sym := range symbols {
			if !strings.Contains(string(b), sym) {
				t.Errorf("%s must force-link %s", rel, sym)
			}
		}
		if !strings.Contains(string(b), godashHotLinkerMarker) {
			t.Errorf("%s must keep the %s injection marker", rel, godashHotLinkerMarker)
		}
	}
}

func TestAndroidScriptsCopySharedLibrary(t *testing.T) {
	e := sampleEnv()
	if s := buildScriptAndroidLibArm64(e); !strings.Contains(s, "cp go/build/android-arm64-v8a/libgodash.so") {
		t.Errorf("arm64 script should copy the .so into the plugin:\n%s", s)
	}
	if s := buildScriptAndroidLibX86_64(e); !strings.Contains(s, "cp go/build/android-x86_64/libgodash.so") {
		t.Errorf("x86_64 script should copy the .so into the plugin:\n%s", s)
	}
}

// TestWebScriptsForwardDartDefines guards that `godash web build`/`run` forward
// --dart-define flags to flutter, so a project can enable a build-time constant
// such as --dart-define=CONCORD_SAB_RING=true.
func TestWebScriptsForwardDartDefines(t *testing.T) {
	if got := dartDefineArgs([]string{"build", "--dart-define=A=1", "--other"}); got != " '--dart-define=A=1'" {
		t.Fatalf("dartDefineArgs = %q, want the single quoted define", got)
	}
	if got := dartDefineArgs(nil); got != "" {
		t.Fatalf("dartDefineArgs(nil) = %q, want empty", got)
	}
	defines := dartDefineArgs([]string{"--dart-define=CONCORD_SAB_RING=true"})
	for name, s := range map[string]string{
		"web-build": webBuildShell(defines),
		"web-run":   webRunShell(defines),
	} {
		if !strings.Contains(s, "--dart-define=CONCORD_SAB_RING=true") {
			t.Errorf("%s must forward the dart-define:\n%s", name, s)
		}
	}
}
