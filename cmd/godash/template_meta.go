package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// templateMetaFile is the name of the project-local file that records which
// godash template/godash version the project was created from. It is committed
// so `godash upgrade` has a known reference; upgrades pull the godash source
// and regenerate derived files rather than merging template changes.
const templateMetaFile = ".godash-template"

// templateMeta holds the contents of .godash-template.
type templateMeta struct {
	Remote  string // custom GODASH_TEMPLATE source; empty for the embedded template
	Version string // ref name: branch (e.g. "main") or tag (e.g. "v1.0.0")
	Commit  string // commit SHA the project was created at
}

// readTemplateMeta loads .godash-template from dir.
func readTemplateMeta(dir string) (*templateMeta, error) {
	m, err := readEnvFile(filepath.Join(dir, templateMetaFile))
	if err != nil {
		return nil, err
	}
	return &templateMeta{
		Remote:  m["remote"],
		Version: m["version"],
		Commit:  m["commit"],
	}, nil
}

// writeTemplateMeta writes .godash-template to dir. Empty fields are
// omitted so the file stays readable (e.g. no trailing "commit=").
func writeTemplateMeta(dir string, m *templateMeta) error {
	var b strings.Builder
	if m.Remote != "" {
		fmt.Fprintf(&b, "remote=%s\n", m.Remote)
	}
	if m.Version != "" {
		fmt.Fprintf(&b, "version=%s\n", m.Version)
	}
	if m.Commit != "" {
		fmt.Fprintf(&b, "commit=%s\n", m.Commit)
	}
	return os.WriteFile(filepath.Join(dir, templateMetaFile), []byte(b.String()), 0644)
}
