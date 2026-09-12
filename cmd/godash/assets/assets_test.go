package assets

import (
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
	if !strings.Contains(string(gomod), "module flap") {
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
