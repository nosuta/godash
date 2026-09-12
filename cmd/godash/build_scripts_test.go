package main

import (
	"strings"
	"testing"
)

func sampleEnv() *projectEnv {
	return &projectEnv{
		Root:              "/proj",
		GodashPath:        "/godash",
		LibName:           "libflap",
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
		".godash/native_internal",
		`cp -R "$GODASH_MODULE_DIR/packages/native_internal/."`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("godashModuleBootstrap() missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(protoGoScript(), `-I="$GODASH_MODULE_DIR/proto"`) {
		t.Errorf("protoGoScript must include the module proto dir:\n%s", protoGoScript())
	}
}

func TestAndroidScriptsCopySharedLibrary(t *testing.T) {
	e := sampleEnv()
	if s := buildScriptAndroidLibArm64(e); !strings.Contains(s, "cp go/build/android-arm64-v8a/libflap.so") {
		t.Errorf("arm64 script should copy the .so into the plugin:\n%s", s)
	}
	if s := buildScriptAndroidLibX86_64(e); !strings.Contains(s, "cp go/build/android-x86_64/libflap.so") {
		t.Errorf("x86_64 script should copy the .so into the plugin:\n%s", s)
	}
}
