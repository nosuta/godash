// Package assets embeds the web platform assets, patched wasm_exec.js,
// scroll worker placeholder, and licenses template so the godash CLI can
// materialise them in a project without vendoring them in the template tree.
package assets

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
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

// StampStyle selects the comment syntax of a managed file's provenance line.
type StampStyle int

const (
	// StampHTML is `<!-- godash:template sha256:... -->`.
	StampHTML StampStyle = iota
	// StampHash is `# godash:template sha256:...`.
	StampHash
	// StampSlash is `// godash:template sha256:...`.
	StampSlash
)

// stampMarker prefixes the provenance line of a managed file.
const stampMarker = "godash:template sha256:"

// ManagedRefSuffix names the file that carries the current godash template
// next to a customised one, for a manual merge.
const ManagedRefSuffix = ".godash"

// ManagedResult reports what WriteManaged did with a file.
type ManagedResult int

const (
	// ManagedUnchanged means the file already matched; nothing was written.
	ManagedUnchanged ManagedResult = iota
	// ManagedCreated means the file did not exist and was written.
	ManagedCreated
	// ManagedUpgraded means the file was untouched by the project and was
	// replaced with the current template (or adopted it after a copy).
	ManagedUpgraded
	// ManagedPreserved means the project customised the file: it was left
	// alone and the current template was written to <file>.godash.
	ManagedPreserved
)

// ExtractWeb writes the embedded web/* assets into <projectRoot>/web/.
//
// The index.html title/meta placeholders ({{APP_TITLE}}) are replaced with
// appTitle. worker.js is a pure build output and is always refreshed; the
// other host files (index.html, _headers, flutter_bootstrap.js) are managed:
// a project may customise them, and their provenance stamp decides whether an
// upgrade may replace them (see WriteManaged).
//
// It returns the project-relative paths that were preserved because the
// project customised them.
func ExtractWeb(projectRoot, appTitle string) ([]string, error) {
	dst := filepath.Join(projectRoot, "web")
	if err := os.MkdirAll(dst, 0755); err != nil {
		return nil, err
	}
	entries, err := webFS.ReadDir("web")
	if err != nil {
		return nil, err
	}
	var preserved []string
	for _, e := range entries {
		render, err := renderWebAsset(e.Name(), appTitle)
		if err != nil {
			return preserved, err
		}
		target := filepath.Join(dst, e.Name())
		if e.Name() == "worker.js" {
			// A generated build output, never customised.
			if err := writeIfChanged(target, render, 0644); err != nil {
				return preserved, err
			}
			continue
		}
		result, err := WriteManaged(target, render, webAssetStyle(e.Name()))
		if err != nil {
			return preserved, err
		}
		if result == ManagedPreserved {
			preserved = append(preserved, filepath.Join("web", e.Name()))
		}
	}
	return preserved, nil
}

// renderWebAsset reads one embedded web file and substitutes the app title.
func renderWebAsset(name, appTitle string) ([]byte, error) {
	data, err := fs.ReadFile(webFS, filepath.Join("web", name))
	if err != nil {
		return nil, err
	}
	if appTitle != "" && bytes.Contains(data, []byte(AppTitlePlaceholder)) {
		data = []byte(strings.ReplaceAll(string(data), AppTitlePlaceholder, appTitle))
	}
	return data, nil
}

// webAssetStyle selects the comment syntax of an embedded web asset.
func webAssetStyle(name string) StampStyle {
	switch name {
	case "index.html":
		return StampHTML
	case "_headers":
		return StampHash
	default:
		return StampSlash
	}
}

// WriteManaged writes a godash-owned text asset that a project may customise.
//
// The first line records the sha256 of the template it was generated from, so
// a later run can tell an untouched file (upgrade it) from a customised one
// (leave it alone and offer the new template as <dst>.godash).
func WriteManaged(dst string, render []byte, style StampStyle) (ManagedResult, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return ManagedUnchanged, err
	}
	hash := sha256Hex(render)
	existing, err := os.ReadFile(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return ManagedCreated, writeStamped(dst, render, style, hash)
	}
	if err != nil {
		return ManagedUnchanged, err
	}
	body, base := splitStamp(existing, style)
	untouched := base != "" && base == sha256Hex(body)
	// A project can adopt a new template by copying the .godash reference
	// over the file: identical content re-establishes the provenance stamp.
	adopts := bytes.Equal(body, render)

	switch {
	case untouched && base == hash:
		return ManagedUnchanged, nil
	case untouched || adopts:
		if err := writeStamped(dst, render, style, hash); err != nil {
			return ManagedUpgraded, err
		}
		_ = os.Remove(dst + ManagedRefSuffix)
		return ManagedUpgraded, nil
	case base == hash:
		// Customised, but the template has not moved since: nothing to do.
		return ManagedUnchanged, nil
	default:
		if err := os.WriteFile(dst+ManagedRefSuffix, render, 0644); err != nil {
			return ManagedPreserved, err
		}
		return ManagedPreserved, nil
	}
}

// writeIfChanged writes data unless the file already has exactly that content.
func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

func writeStamped(dst string, render []byte, style StampStyle, hash string) error {
	return os.WriteFile(dst, stampText(render, style, hash), 0644)
}

// stampText prefixes render with the provenance line for its style.
func stampText(render []byte, style StampStyle, hash string) []byte {
	var prefix, suffix string
	switch style {
	case StampHTML:
		prefix, suffix = "<!-- ", " -->"
	case StampHash:
		prefix = "# "
	default:
		prefix = "// "
	}
	var out bytes.Buffer
	out.WriteString(prefix)
	out.WriteString(stampMarker)
	out.WriteString(hash)
	out.WriteString(suffix)
	out.WriteByte('\n')
	out.Write(render)
	return out.Bytes()
}

// splitStamp returns the content without its provenance line and the recorded
// hash ("" when the file carries no valid stamp).
func splitStamp(data []byte, style StampStyle) ([]byte, string) {
	var prefix, suffix string
	switch style {
	case StampHTML:
		prefix, suffix = "<!-- ", " -->"
	case StampHash:
		prefix = "# "
	default:
		prefix = "// "
	}
	if !bytes.HasPrefix(data, []byte(prefix+stampMarker)) {
		return data, ""
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return data, ""
	}
	line := strings.TrimSpace(string(data[len(prefix)+len(stampMarker) : nl]))
	line = strings.TrimSpace(strings.TrimSuffix(line, suffix))
	if !isSHA256Hex(line) {
		return data, ""
	}
	return data[nl+1:], line
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// WriteTinygoWasmExec writes the patched wasm_exec.js (for TinyGo builds)
// to <projectRoot>/web/wasm_exec.js.
func WriteTinygoWasmExec(projectRoot string) error {
	return writeIfChanged(filepath.Join(projectRoot, "web", "wasm_exec.js"), tinygoWasmExecJS, 0644)
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
// <projectRoot>/web/scroll_worker.js. It is a managed file: the template says
// an app may replace it, so a customised copy is preserved.
func WriteScrollWorker(projectRoot string) (ManagedResult, error) {
	return WriteManaged(
		filepath.Join(projectRoot, "web", "scroll_worker.js"),
		scrollWorkerJS,
		StampSlash,
	)
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

// DumpWebToTemp extracts the web assets to a temp dir (used by tests
// and the upgrade dry-run). Returns the temp dir path.
func DumpWebToTemp() (string, error) {
	dir, err := os.MkdirTemp("", "godash-web-*")
	if err != nil {
		return "", err
	}
	if _, err := ExtractWeb(dir, "godash test"); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
