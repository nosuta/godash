package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nosuta/godash/v2/cmd/godash/assets"
)

func TestPackageName(t *testing.T) {
	cases := map[string]string{
		"My App":        "myapp",
		"9lives":        "app9lives",
		"":              "app",
		"Hello World 1": "helloworld1",
	}
	for in, want := range cases {
		if got := packageName(in); got != want {
			t.Errorf("packageName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParameterizeTemplate guards that the embedded template's app identity
// placeholder is rewritten to the project package name (pubspec name, Go
// module, proto go_package, Go/Dart imports).
func TestParameterizeTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := assets.ExtractTemplate(dir); err != nil {
		t.Fatalf("ExtractTemplate: %v", err)
	}
	if err := parameterizeTemplate(dir, "myapp", "My App"); err != nil {
		t.Fatalf("parameterizeTemplate: %v", err)
	}
	checks := map[string]string{
		"go/go.mod":                "module myapp",
		"proto/echo.proto":         `go_package = "myapp/pb"`,
		"proto/counter.proto":      `go_package = "myapp/pb"`,
		"go/rpc/echo_server.go":    `myapp "myapp/pb"`,
		"go/rpc/calc_server.go":    `myapp "myapp/pb"`,
		"go/rpc/counter_server.go": `myapp "myapp/pb"`,
		"lib/main.dart":            `package:myapp/`,
	}
	for rel, want := range checks {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing %q:\n%s", rel, want, string(b))
		}
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "pubspec.yaml"))
	if !strings.Contains(string(pub), "name: myapp") {
		t.Errorf("pubspec name not set: %q", string(pub))
	}
	if !strings.Contains(string(pub), `description: "My App"`) {
		t.Errorf("pubspec description not set: %q", string(pub))
	}
	// The generated-client extension must stay `.godash.dart`, not inherit the
	// app package name.
	main, _ := os.ReadFile(filepath.Join(dir, "lib", "main.dart"))
	if !strings.Contains(string(main), "pb/echo.godash.dart") {
		t.Errorf("generated-client import was mangled: %q", string(main))
	}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), appPackagePlaceholder) {
			t.Errorf("%s still contains the app placeholder", p)
		}
		return nil
	})
}

func TestToSlug(t *testing.T) {
	cases := map[string]string{
		"My App":        "myapp",
		"Hello World 1": "helloworld1",
		"already_slug":  "alreadyslug",
		"UPPER-case!":   "uppercase",
		"":              "",
	}
	for in, want := range cases {
		if got := toSlug(in); got != want {
			t.Errorf("toSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLocalPath(t *testing.T) {
	cases := map[string]bool{
		"/abs/path":              true,
		"../godash":              true,
		"owner/repo":             true,
		".":                      true,
		"..":                     true,
		"https://github.com/x/y": false,
		"justaname":              false,
	}
	for in, want := range cases {
		if got := isLocalPath(in); got != want {
			t.Errorf("isLocalPath(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestRenameAndroidPackage guards Android scaffolding: applyConfig changes the
// gradle namespace/applicationId to the user's bundle id, so the generated
// MainActivity package must be renamed/moved or the app crashes on launch with
// ClassNotFoundException: ...MainActivity.
func TestRenameAndroidPackage(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "android", "app", "src", "main", "kotlin", "com", "example", "godashapp", "MainActivity.kt")
	if err := os.MkdirAll(filepath.Dir(oldFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldFile, []byte("package com.example.godashapp\n\nclass MainActivity\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := renameAndroidPackage(dir, "com.example.godashapp", "com.acme.good"); err != nil {
		t.Fatalf("renameAndroidPackage: %v", err)
	}
	newFile := filepath.Join(dir, "android", "app", "src", "main", "kotlin", "com", "acme", "good", "MainActivity.kt")
	b, err := os.ReadFile(newFile)
	if err != nil {
		t.Fatalf("expected the Kotlin file at the new package path: %v", err)
	}
	if !strings.Contains(string(b), "package com.acme.good") {
		t.Errorf("package declaration not rewritten: %q", string(b))
	}
	if _, err := os.Stat(oldFile); err == nil {
		t.Error("old package path must be gone after the rename")
	}
}

// TestRewriteGodashVersion guards that a scaffolded project is pinned to the
// CLI's godash version in both pubspec.yaml and go/go.mod, so the Go module
// (which supplies .godash/native_internal) matches the CLI.
func TestRewriteGodashVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go", "go.mod"), []byte("module godashapp\n\nrequire github.com/nosuta/godash/v2 v2.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pubspec.yaml"), []byte("dependencies:\n  godash: ^2.2.0\n  fixnum: ^1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := rewriteGodashVersion(dir, "v2.2.8"); err != nil {
		t.Fatalf("rewriteGodashVersion: %v", err)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "go", "go.mod"))
	if !strings.Contains(string(gomod), "github.com/nosuta/godash/v2 v2.2.8") {
		t.Errorf("go.mod not pinned: %q", string(gomod))
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "pubspec.yaml"))
	if !strings.Contains(string(pub), "godash: ^2.2.8") {
		t.Errorf("pubspec not pinned: %q", string(pub))
	}
	if !strings.Contains(string(pub), "fixnum: ^1.1.1") {
		t.Errorf("pubspec unrelated deps must be untouched: %q", string(pub))
	}

	// A dev build reports build metadata ("+dirty"); it must be stripped before
	// it becomes a pub constraint / Go module version.
	if err := rewriteGodashVersion(dir, "v2.2.9+dirty"); err != nil {
		t.Fatalf("rewriteGodashVersion(+dirty): %v", err)
	}
	gomod, _ = os.ReadFile(filepath.Join(dir, "go", "go.mod"))
	if !strings.Contains(string(gomod), "github.com/nosuta/godash/v2 v2.2.9\n") {
		t.Errorf("go.mod build metadata not stripped: %q", string(gomod))
	}
	pub, _ = os.ReadFile(filepath.Join(dir, "pubspec.yaml"))
	if !strings.Contains(string(pub), "godash: ^2.2.9\n") {
		t.Errorf("pubspec build metadata not stripped: %q", string(pub))
	}
}

func TestReadDartLockVersion(t *testing.T) {
	dir := t.TempDir()
	lock := `# Generated by pub
packages:
  fixnum:
    dependency: "direct main"
    description:
      name: fixnum
      url: "https://pub.dev"
    source: hosted
    version: "1.1.1"
  godash:
    dependency: "direct main"
    description:
      name: godash
      sha256: "abc"
      url: "https://pub.dev"
    source: hosted
    version: "2.2.8"
sdks:
  dart: ">=3.12.0"
`
	p := filepath.Join(dir, "pubspec.lock")
	if err := os.WriteFile(p, []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDartLockVersion(p, "godash"); got != "2.2.8" {
		t.Errorf("readDartLockVersion(godash) = %q, want 2.2.8", got)
	}
	if got := readDartLockVersion(p, "fixnum"); got != "1.1.1" {
		t.Errorf("readDartLockVersion(fixnum) = %q, want 1.1.1", got)
	}
	if got := readDartLockVersion(p, "missing"); got != "" {
		t.Errorf("readDartLockVersion(missing) = %q, want empty", got)
	}
}

// TestVersionString guards the CLI version resolution: an explicit -ldflags
// value wins; otherwise the installed module version (tag) is used, falling
// back to "latest" for unversioned local builds.
func TestVersionString(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	Version = "v9.9.9"
	if got := versionString(); got != "v9.9.9" {
		t.Errorf("explicit version = %q, want v9.9.9", got)
	}

	Version = "latest"
	if got := versionString(); got == "" {
		t.Error("versionString() must never be empty")
	}
}

// TestApplyConfig guards the bundle-id / display-name rewrite against a
// flutter-create tree for a project whose package name is parameterised.
func TestApplyConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("android/app/build.gradle.kts", "namespace = \"com.example.myapp\"\napplicationId = \"com.example.myapp\"\n")
	write("android/app/src/main/AndroidManifest.xml", "android:label=\"myapp\"\n")
	write("android/app/src/main/kotlin/com/example/myapp/MainActivity.kt", "package com.example.myapp\n\nclass MainActivity\n")
	write("ios/Runner.xcodeproj/project.pbxproj", "PRODUCT_BUNDLE_IDENTIFIER = com.example.myapp;\nPRODUCT_BUNDLE_IDENTIFIER = com.example.myapp.RunnerTests;\n")
	write("ios/Runner/Info.plist", "<key>CFBundleDisplayName</key>\n<string>Myapp</string>\n")
	write("macos/Runner/Configs/AppInfo.xcconfig", "PRODUCT_NAME = myapp\nPRODUCT_BUNDLE_IDENTIFIER = com.example.myapp\n")

	cfg := scaffoldConfig{dir: dir, appName: "Good App", pkg: "myapp", bundleID: "com.acme.good"}
	if err := applyConfig(cfg); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	if s := read("android/app/build.gradle.kts"); !strings.Contains(s, `namespace = "com.acme.good"`) || !strings.Contains(s, `applicationId = "com.acme.good"`) {
		t.Errorf("gradle not updated: %q", s)
	}
	if s := read("android/app/src/main/AndroidManifest.xml"); !strings.Contains(s, `android:label="Good App"`) {
		t.Errorf("manifest label not updated: %q", s)
	}
	if s := read("android/app/src/main/kotlin/com/acme/good/MainActivity.kt"); !strings.Contains(s, "package com.acme.good") {
		t.Errorf("MainActivity not moved/rewritten: %q", s)
	}
	if s := read("ios/Runner.xcodeproj/project.pbxproj"); !strings.Contains(s, "PRODUCT_BUNDLE_IDENTIFIER = com.acme.good;") || !strings.Contains(s, "PRODUCT_BUNDLE_IDENTIFIER = com.acme.good.RunnerTests;") {
		t.Errorf("ios bundle ids not updated: %q", s)
	}
	if s := read("ios/Runner/Info.plist"); !strings.Contains(s, "<string>Good App</string>") {
		t.Errorf("ios display name not updated: %q", s)
	}
	if s := read("macos/Runner/Configs/AppInfo.xcconfig"); !strings.Contains(s, "PRODUCT_NAME = Good App") || !strings.Contains(s, "PRODUCT_BUNDLE_IDENTIFIER = com.acme.good") {
		t.Errorf("macos config not updated: %q", s)
	}
}

func TestReplaceInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("name: TODO\nid: TODO\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceInFile(path, "TODO", "myapp"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "name: myapp\nid: myapp\n" {
		t.Fatalf("unexpected content: %q", b)
	}
}

func TestDetectGodashDep(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	inline := write("inline.yaml", "dependencies:\n  godash: ^1.2.3\n  fixnum: ^1.1.1\n")
	typ, val, err := detectGodashDep(inline)
	if err != nil || typ != "version" || val != "^1.2.3" {
		t.Fatalf("inline: got (%q,%q,%v)", typ, val, err)
	}

	block := write("block.yaml", "dependencies:\n  godash:\n    path: ../godash\n  native_internal:\n    path: ../godash/packages/native_internal\n")
	typ, val, err = detectGodashDep(block)
	if err != nil || typ != "path" || val != "../godash" {
		t.Fatalf("block: got (%q,%q,%v)", typ, val, err)
	}

	missing := write("missing.yaml", "dependencies:\n  fixnum: ^1.1.1\n")
	if _, _, err := detectGodashDep(missing); err == nil {
		t.Fatal("expected an error when godash is absent")
	}
}

func TestResolveGodashPath(t *testing.T) {
	if got := resolveGodashPath("/proj", "../godash"); got != "/godash" {
		t.Errorf("relative = %q, want /godash", got)
	}
	if got := resolveGodashPath("/proj", "/abs/godash"); got != "/abs/godash" {
		t.Errorf("absolute = %q, want /abs/godash", got)
	}
}

func TestToolDetect(t *testing.T) {
	// `go` is necessarily present (we are running go test).
	if !(tool{name: "go", check: []string{"version"}}).detect() {
		t.Error("expected the go toolchain to be detected")
	}
	if (tool{name: "godash-no-such-binary-xyz", check: []string{"--version"}}).detect() {
		t.Error("a nonexistent binary must not be detected")
	}
	// checkChrome must not panic regardless of the host.
	_ = checkChrome()
}

func TestTemplateMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &templateMeta{Remote: "https://example.com/tpl", Version: "v1.2.0", Commit: "abc123"}
	if err := writeTemplateMeta(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := readTemplateMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if *out != *in {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}

	// Empty fields are omitted.
	partial := t.TempDir()
	if err := writeTemplateMeta(partial, &templateMeta{Version: "main"}); err != nil {
		t.Fatal(err)
	}
	got, err := readTemplateMeta(partial)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "main" || got.Remote != "" || got.Commit != "" {
		t.Fatalf("unexpected partial meta: %+v", got)
	}
}

func TestTemplateSourceDefaultsToEmbedded(t *testing.T) {
	t.Setenv("GODASH_TEMPLATE", "")
	if got := templateSource(); got != embeddedTemplateSource {
		t.Fatalf("templateSource() = %q, want the embedded template", got)
	}
	t.Setenv("GODASH_TEMPLATE", "/tmp/custom-template")
	if got := templateSource(); got != "/tmp/custom-template" {
		t.Fatalf("templateSource() = %q, want the GODASH_TEMPLATE override", got)
	}
}

// TestCloneEmbeddedTemplate covers the default `godash new` source: cloneTemplate
// must materialise the embedded scaffold (with the go module file restored and
// dotfiles present) without touching the network.
func TestCloneEmbeddedTemplate(t *testing.T) {
	t.Setenv("GODASH_TEMPLATE", "")
	dir := filepath.Join(t.TempDir(), "proj")
	if err := cloneTemplate(scaffoldConfig{dir: dir}); err != nil {
		t.Fatalf("cloneTemplate: %v", err)
	}
	for _, rel := range []string{
		".gitignore",
		"pubspec.yaml",
		"go/go.mod",
		"go/rpc/echo_server.go",
		"go/rpc/counter_server.go",
		"go/rpc/entrypoint.go",
		"proto/counter.proto",
		"lib/main.dart",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing embedded template file %s: %v", rel, err)
		}
	}
}
