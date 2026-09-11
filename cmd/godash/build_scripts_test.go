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
		IOSPluginDir:      "../godash/packages/native_internal/ios",
		MacosPluginDir:    "../godash/packages/native_internal/macos",
		AndroidPluginDir:  "../godash/packages/native_internal/android/src/main/jniLibs",
		XCFrameworkName:   "native_internal.xcframework",
		IOSFrameworkDir:   "../godash/packages/native_internal/ios/native_internal/Frameworks/native_internal.xcframework",
		MacosFrameworkDir: "../godash/packages/native_internal/macos/native_internal/Frameworks/native_internal.xcframework",
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
		// A missing/extra fmt argument renders as "%!..."; catch arg drift.
		if strings.Contains(s, "%!") {
			t.Errorf("%s: build script has a fmt formatting mismatch:\n%s", name, s)
		}
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
