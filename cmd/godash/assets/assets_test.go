package assets

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractTemplate guards the embedded project template (the default
// `godash new` source): it must include dotfiles, keep the executable bit on
// helper scripts, and must not carry the old nested .git.
func TestExtractTemplate(t *testing.T) {
	dst := t.TempDir()
	if err := ExtractTemplate(dst); err != nil {
		t.Fatalf("ExtractTemplate: %v", err)
	}

	for _, rel := range []string{
		".gitignore",
		"README.md",
		"analysis_options.yaml",
		"clangwrap.sh",
		"core.env",
		"pubspec.yaml",
		"go/go.mod",
		"go/rpc/echo_server.go",
		"lib/main.dart",
		"lib/bridge/bridge.dart",
		"lib/version/version.dart",
		"lib/licenses/licenses.g.dart",
		"proto/echo.proto",
	} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing embedded template file %s: %v", rel, err)
		}
	}

	info, err := os.Stat(filepath.Join(dst, "clangwrap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("clangwrap.sh should stay executable, mode = %v", info.Mode())
	}

	// go.mod is stored as go.mod.tmpl (so go:embed does not treat the scaffold
	// as a nested module) and must be restored under its real name.
	gomod, err := os.ReadFile(filepath.Join(dst, "go", "go.mod"))
	if err != nil {
		t.Fatalf("read go/go.mod: %v", err)
	}
	if !strings.Contains(string(gomod), "module godashapp") {
		t.Errorf("go/go.mod was not restored correctly: %q", string(gomod))
	}
	if _, err := os.Stat(filepath.Join(dst, "go", "go.mod.tmpl")); err == nil {
		t.Error("go.mod.tmpl must not be extracted")
	}

	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Error(".git must not be part of the embedded template")
	}
	if _, err := os.Stat(filepath.Join(dst, ".dart_tool")); err == nil {
		t.Error(".dart_tool must not be part of the embedded template")
	}
}

// TestWriteManagedLifecycle covers the provenance contract: create, leave
// alone, upgrade, preserve a customised file and adopt the reference back.
func TestWriteManagedLifecycle(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "index.html")
	first := []byte("<!DOCTYPE html>\n<title>one</title>\n")

	result, err := WriteManaged(dst, first, StampHTML)
	if err != nil || result != ManagedCreated {
		t.Fatalf("create: result=%v err=%v", result, err)
	}
	stamped, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	body, stamp := splitStamp(stamped, StampHTML)
	if !bytes.Equal(body, first) || stamp != sha256Hex(first) {
		t.Fatalf("stamp mismatch: stamp=%q body=%q", stamp, body)
	}
	if !strings.HasPrefix(string(stamped), "<!-- godash:template sha256:") {
		t.Fatalf("missing stamp line: %q", stamped)
	}

	// Same template, untouched: no write at all.
	result, err = WriteManaged(dst, first, StampHTML)
	if err != nil || result != ManagedUnchanged {
		t.Fatalf("unchanged: result=%v err=%v", result, err)
	}
	if again, _ := os.ReadFile(dst); !bytes.Equal(again, stamped) {
		t.Fatal("an unchanged file must not be rewritten")
	}

	// New template, untouched: upgraded in place.
	second := []byte("<!DOCTYPE html>\n<title>two</title>\n")
	result, err = WriteManaged(dst, second, StampHTML)
	if err != nil || result != ManagedUpgraded {
		t.Fatalf("upgrade: result=%v err=%v", result, err)
	}
	body, stamp = splitStamp(mustRead(t, dst), StampHTML)
	if !bytes.Equal(body, second) || stamp != sha256Hex(second) {
		t.Fatalf("upgrade content = %q", body)
	}

	// Customised by the project, template moved on: preserved plus a reference.
	customised := append(append([]byte{}, mustRead(t, dst)...), []byte("<!-- app -->\n")...)
	if err := os.WriteFile(dst, customised, 0644); err != nil {
		t.Fatal(err)
	}
	third := []byte("<!DOCTYPE html>\n<title>three</title>\n")
	result, err = WriteManaged(dst, third, StampHTML)
	if err != nil || result != ManagedPreserved {
		t.Fatalf("preserve: result=%v err=%v", result, err)
	}
	if got := mustRead(t, dst); !bytes.Equal(got, customised) {
		t.Fatal("a customised file must never be overwritten")
	}
	if ref := mustRead(t, dst+ManagedRefSuffix); !bytes.Equal(ref, third) {
		t.Fatalf("reference = %q", ref)
	}

	// Copying the reference over the file adopts it and clears the reference.
	if err := os.WriteFile(dst, third, 0644); err != nil {
		t.Fatal(err)
	}
	result, err = WriteManaged(dst, third, StampHTML)
	if err != nil || result != ManagedUpgraded {
		t.Fatalf("adopt: result=%v err=%v", result, err)
	}
	body, stamp = splitStamp(mustRead(t, dst), StampHTML)
	if !bytes.Equal(body, third) || stamp != sha256Hex(third) {
		t.Fatalf("adopted content = %q", body)
	}
	if _, err := os.Stat(dst + ManagedRefSuffix); !os.IsNotExist(err) {
		t.Error("the stale reference must be removed after adoption")
	}
}

// TestExtractWebStampsHostFiles checks the first extraction, the idempotent
// second run and that the pure build output carries no stamp.
func TestExtractWebStampsHostFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := ExtractWeb(dir, "Demo"); err != nil {
		t.Fatalf("ExtractWeb: %v", err)
	}

	index := mustRead(t, filepath.Join(dir, "web", "index.html"))
	if !bytes.HasPrefix(index, []byte("<!-- godash:template sha256:")) {
		t.Fatalf("index.html has no stamp: %q", index[:60])
	}
	if !bytes.Contains(index, []byte("<title>Demo</title>")) {
		t.Error("the app title was not substituted")
	}
	headers := mustRead(t, filepath.Join(dir, "web", "_headers"))
	if !bytes.HasPrefix(headers, []byte("# godash:template sha256:")) {
		t.Errorf("_headers has no stamp: %q", headers[:40])
	}
	worker := mustRead(t, filepath.Join(dir, "web", "worker.js"))
	if bytes.HasPrefix(worker, []byte("// godash:template")) {
		t.Error("worker.js is a build output and must not be a managed file")
	}

	preserved, err := ExtractWeb(dir, "Demo")
	if err != nil || len(preserved) != 0 {
		t.Fatalf("second extract: preserved=%v err=%v", preserved, err)
	}
	if again := mustRead(t, filepath.Join(dir, "web", "index.html")); !bytes.Equal(again, index) {
		t.Error("a second extraction must not rewrite an untouched file")
	}
}

// TestExtractWebPreservesCustomisedFile simulates an older template (a stamp
// that no longer matches) and checks that the project's file survives.
func TestExtractWebPreservesCustomisedFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ExtractWeb(dir, "Demo"); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, "web", "index.html")
	old := mustRead(t, indexPath)
	_, stamp := splitStamp(old, StampHTML)
	// Pretend it came from an older godash release with an extra local edit.
	edited := bytes.Replace(
		old,
		[]byte(stamp),
		[]byte(strings.Repeat("0", 64)),
		1,
	)
	edited = append(edited, []byte("<!-- mine -->\n")...)
	if err := os.WriteFile(indexPath, edited, 0644); err != nil {
		t.Fatal(err)
	}

	preserved, err := ExtractWeb(dir, "Demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(preserved) != 1 || preserved[0] != filepath.Join("web", "index.html") {
		t.Fatalf("preserved = %v", preserved)
	}
	if got := mustRead(t, indexPath); !bytes.Equal(got, edited) {
		t.Fatal("the customised index.html was overwritten")
	}
	ref := mustRead(t, indexPath+ManagedRefSuffix)
	if bytes.HasPrefix(ref, []byte("<!-- godash:template")) {
		t.Error("the reference must be the bare template")
	}
	if !bytes.Contains(ref, []byte("<title>Demo</title>")) {
		t.Error("the reference lost the app title")
	}
}

// TestWriteScrollWorkerPreservesCustomisedPlaceholder covers the file whose own
// comments invite the app to replace it.
func TestWriteScrollWorkerPreservesCustomisedPlaceholder(t *testing.T) {
	dir := t.TempDir()
	result, err := WriteScrollWorker(dir)
	if err != nil || result != ManagedCreated {
		t.Fatalf("first write: result=%v err=%v", result, err)
	}
	path := filepath.Join(dir, "web", "scroll_worker.js")
	generated := mustRead(t, path)
	_, stamp := splitStamp(generated, StampSlash)
	// An edit plus an older stamp: the same file, but from a previous release.
	customised := bytes.Replace(generated, []byte(stamp), []byte(strings.Repeat("0", 64)), 1)
	customised = append(customised, []byte("// mine\n")...)
	if err := os.WriteFile(path, customised, 0644); err != nil {
		t.Fatal(err)
	}
	result, err = WriteScrollWorker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result != ManagedPreserved {
		t.Fatalf("customised scroll worker: result=%v", result)
	}
	if got := mustRead(t, path); !bytes.Equal(got, customised) {
		t.Error("the customised scroll worker was overwritten")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
