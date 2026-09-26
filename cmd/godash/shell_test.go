package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunShellPipeForwardsStdin guards that long-running children (e.g.
// `flutter run`) inherit the parent's stdin. Flutter only enables its
// interactive key commands (r/R/h/d/c/q) when stdin is a terminal; leaving
// cmd.Stdin nil gave the child /dev/null and silently disabled them.
func TestRunShellPipeForwardsStdin(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString("hello\n"); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()

	script := "read line; printf '%s' \"$line\" > " + shellQuote(out)
	if err := runShellPipe(dir, script); err != nil {
		t.Fatalf("runShellPipe: %v", err)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "hello" {
		t.Errorf("child read %q from stdin, want %q", got, "hello")
	}
}
