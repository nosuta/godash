// Package assets embeds the web platform assets, patched wasm_exec.js,
// scroll worker placeholder, and licenses template so the godash CLI can
// materialise them in a project without vendoring them in the template tree.
package assets

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed web/*
var webFS embed.FS

// templateFS holds the default project scaffold. It is the canonical source
// for `godash new`; GODASH_TEMPLATE can override it with a local or remote
// alternative. The directory is named `_template` so the Go tool ignores it
// (the scaffold contains its own go.mod, which would otherwise make it a
// nested module and drop it from the embed); `all:` is required so dotfiles
// like .gitignore are included.
//
//go:embed all:_template
var templateFS embed.FS

//go:embed tinygo_wasm_exec.js
var tinygoWasmExecJS []byte

//go:embed scroll_worker.js
var scrollWorkerJS []byte

// ScrollWorkerJS returns the embedded scroll_worker.js content.
func ScrollWorkerJS() []byte { return scrollWorkerJS }

//go:embed licenses_dart.tpl
var licensesTpl []byte

// AppTitlePlaceholder is the template variable replaced in web/index.html
// when godash extracts it into a project.
const AppTitlePlaceholder = "{{APP_TITLE}}"

// ExtractWeb writes the embedded web/* assets into <projectRoot>/web/.
// The index.html title/meta placeholders ({{APP_TITLE}}) are replaced with
// appTitle. Any pre-existing files at the destination are overwritten —
// these are godash-owned files; user customisation lives in the Flutter
// app itself, not the host page.
func ExtractWeb(projectRoot, appTitle string) error {
	dst := filepath.Join(projectRoot, "web")
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := webFS.ReadDir("web")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := extractFSFile(webFS, "web", e.Name(), filepath.Join(dst, e.Name()), appTitle); err != nil {
			return err
		}
	}
	return nil
}

// WriteTinygoWasmExec writes the patched wasm_exec.js (for TinyGo builds)
// to <projectRoot>/web/wasm_exec.js.
func WriteTinygoWasmExec(projectRoot string) error {
	return os.WriteFile(filepath.Join(projectRoot, "web", "wasm_exec.js"), tinygoWasmExecJS, 0644)
}

// ExtractTemplate writes the embedded project template into dst, preserving
// the directory tree and the executable bit of helper scripts. dst is created
// if needed and must not already contain the project files.
func ExtractTemplate(dst string) error {
	return fs.WalkDir(templateFS, "_template", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "_template" {
			return os.MkdirAll(dst, 0755)
		}
		rel := strings.TrimPrefix(path, "_template/")
		// Revert the storage-time renames: the `go` subtree is stored as `_go`
		// (so the Go tool ignores it) and its go.mod as go.mod.tmpl (so go:embed
		// does not treat the scaffold as a nested module and skip it).
		rel = strings.TrimSuffix(rel, ".tmpl")
		switch {
		case rel == "_go":
			rel = "go"
		case strings.HasPrefix(rel, "_go/"):
			rel = "go/" + strings.TrimPrefix(rel, "_go/")
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		// go:embed does not preserve file modes, so restore the executable
		// bit for helper scripts.
		mode := os.FileMode(0644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
}

// WriteScrollWorker writes the scroll_worker.js placeholder to
// <projectRoot>/web/scroll_worker.js.
func WriteScrollWorker(projectRoot string) error {
	return os.WriteFile(filepath.Join(projectRoot, "web", "scroll_worker.js"), scrollWorkerJS, 0644)
}

// WriteLicensesTempFile writes the embedded licenses_dart.tpl to a temp
// file (so the go-licenses tool can read it via --template) and returns the
// path. The caller is responsible for removing the file.
func WriteLicensesTempFile() (string, error) {
	f, err := os.CreateTemp("", "godash-licenses-*.tpl")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(licensesTpl); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// extractFSFile copies a single embedded file to dst, optionally
// substituting appTitle for the {{APP_TITLE}} placeholder.
func extractFSFile(fsys fs.FS, prefix, name, dst, appTitle string) error {
	data, err := fs.ReadFile(fsys, filepath.Join(prefix, name))
	if err != nil {
		return err
	}
	if appTitle != "" && strings.Contains(string(data), AppTitlePlaceholder) {
		data = []byte(strings.ReplaceAll(string(data), AppTitlePlaceholder, appTitle))
	}
	return os.WriteFile(dst, data, 0644)
}

// DumpWebToTemp extracts the web assets to a temp dir (used by tests
// and the upgrade dry-run). Returns the temp dir path.
func DumpWebToTemp() (string, error) {
	dir, err := os.MkdirTemp("", "godash-web-*")
	if err != nil {
		return "", err
	}
	if err := ExtractWeb(dir, "godash test"); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// ensureUnusedImport keeps `fmt`/`io` available for future expansion without
// breaking the embed build. Safe to remove once used.
var _ = fmt.Sprintf
var _ = io.Discard
