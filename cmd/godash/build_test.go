package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAndroidArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		mode    string
		split   bool
		plats   []string
		wantErr string
	}{
		{name: "default", mode: "apk"},
		{name: "appbundle", args: []string{"appbundle"}, mode: "appbundle"},
		{name: "appbundle alias aab", args: []string{"aab"}, mode: "appbundle"},
		{name: "appbundle alias bundle", args: []string{"bundle"}, mode: "appbundle"},
		{
			name:  "split defaults to supported ABIs",
			args:  []string{"apk", "--split-per-abi"},
			mode:  "apk",
			split: true,
			plats: []string{"android-arm64", "android-x64"},
		},
		{
			name:  "split before the mode",
			args:  []string{"--split-per-abi"},
			mode:  "apk",
			split: true,
			plats: []string{"android-arm64", "android-x64"},
		},
		{
			name:  "target-platform as the next argument",
			args:  []string{"--target-platform", "android-x64", "apk"},
			mode:  "apk",
			plats: []string{"android-x64"},
		},
		{
			name:  "target-platform with equals and duplicates",
			args:  []string{"apk", "--target-platform=android-arm64,android-x64,android-arm64"},
			mode:  "apk",
			plats: []string{"android-arm64", "android-x64"},
		},
		{
			name:    "32-bit ARM is rejected",
			args:    []string{"apk", "--target-platform", "android-arm"},
			mode:    "apk",
			wantErr: "android-arm is not supported",
		},
		{
			name:    "unknown platform",
			args:    []string{"apk", "--target-platform", "android-mips"},
			mode:    "apk",
			wantErr: "unsupported target platform",
		},
		{
			name:    "missing target-platform value",
			args:    []string{"apk", "--target-platform"},
			mode:    "apk",
			wantErr: "--target-platform needs",
		},
		{name: "unknown flag", args: []string{"apk", "--bogus"}, mode: "apk", wantErr: "unknown flag"},
		{
			name:    "split is apk only",
			args:    []string{"appbundle", "--split-per-abi"},
			mode:    "appbundle",
			wantErr: "only supported for apk",
		},
		{name: "unknown mode", args: []string{"foo"}, mode: "apk", wantErr: "unknown mode"},
		{
			name:    "second mode",
			args:    []string{"apk", "appbundle"},
			mode:    "apk",
			wantErr: "unexpected argument",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseAndroidArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.mode != tt.mode || opts.split != tt.split ||
				!reflect.DeepEqual(opts.platforms, tt.plats) {
				t.Fatalf("opts = %+v, want mode=%q split=%v platforms=%v",
					opts, tt.mode, tt.split, tt.plats)
			}
		})
	}
}

// TestAndroidBuildScriptUsesOnlyRequestedABIs guards the split workflow: a
// split build for one ABI must not compile the Go library for the other, and
// the Flutter flags must reach the command line.
func TestAndroidBuildScriptUsesOnlyRequestedABIs(t *testing.T) {
	e := sampleEnv()
	opts, err := parseAndroidArgs([]string{"apk", "--split-per-abi", "--target-platform", "android-arm64"})
	if err != nil {
		t.Fatal(err)
	}
	script := androidBuildScript(e, "", opts)
	for _, want := range []string{
		"gen_go_build_version",
		"build/android-arm64-v8a/libgodash.so",
		"flutter build apk --release --dart-define-from-file=core.env" +
			" --split-per-abi --target-platform android-arm64",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("androidBuildScript missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "build/android-x86_64") {
		t.Errorf("androidBuildScript must not build x86_64 for an arm64-only target:\n%s", script)
	}

	// Without a target list, the universal build keeps both supported ABIs.
	universal := androidBuildScript(e, "", androidOptions{mode: "apk"})
	for _, want := range []string{"build/android-arm64-v8a", "build/android-x86_64"} {
		if !strings.Contains(universal, want) {
			t.Errorf("universal androidBuildScript missing %q:\n%s", want, universal)
		}
	}

	// The app bundle cannot be split, but it can still restrict ABIs.
	appbundle := androidBuildScript(e, "", androidOptions{
		mode:      "appbundle",
		platforms: []string{"android-x64"},
	})
	if !strings.Contains(appbundle, "flutter build appbundle") ||
		!strings.Contains(appbundle, "--target-platform android-x64") {
		t.Errorf("appbundle command is wrong:\n%s", appbundle)
	}
	if strings.Contains(appbundle, "build/android-arm64-v8a") {
		t.Errorf("appbundle must not build arm64 for an x64-only target:\n%s", appbundle)
	}
}

// TestReleaseBuildsRegenerateVersion guards the release version: the in-app
// version comes from git describe, and only the web build used to regenerate
// it, so Android/iOS/macOS releases reported the previous tag.
func TestReleaseBuildsRegenerateVersion(t *testing.T) {
	e := sampleEnv()
	scripts := map[string]string{
		"android": androidBuildScript(e, "", androidOptions{mode: "apk"}),
		"ios":     iosBuildScript(e, ""),
		"macos":   macosBuildScript(e, ""),
		"web":     buildScriptWebBuild(e),
	}
	for name, script := range scripts {
		if !strings.Contains(script, "gen_go_build_version") {
			t.Errorf("%s build script must regenerate lib/version/version.dart:\n%s", name, script)
		}
	}
}
