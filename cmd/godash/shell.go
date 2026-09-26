package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/nosuta/godash/v2/cmd/godash/assets"
)

// runShellTask runs a shell script in cwd with a spinner.
// On success: "✓ label". On failure: prints buffered output and returns err.
func runShellTask(label, cwd, script string) error {
	var buf bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = cwd
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	frame := 0
	for {
		select {
		case err := <-done:
			clearLine()
			if err != nil {
				if buf.Len() > 0 {
					fmt.Fprint(os.Stderr, buf.String())
				}
				fmt.Fprintf(os.Stderr, colorRed+"✗"+colorReset+" %s\n", label)
				return err
			}
			fmt.Printf(colorGreen+"✓"+colorReset+" %s\n", label)
			return nil
		case <-time.After(80 * time.Millisecond):
			clearLine()
			fmt.Printf("  %s %s", spinnerFrames[frame%len(spinnerFrames)], label)
			frame++
		}
	}
}

// runShellPipe runs a shell script streaming stdin/stdout/stderr to the parent
// process. Used for long-running commands (e.g. `flutter run`).
//
// Stdin must be inherited: `flutter run -d web-server` only enables its
// interactive key commands (r/R/h/d/c/q) when stdin is a terminal.
func runShellPipe(cwd, script string) error {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runShellSimple runs a shell script and returns combined output. Used for
// quick probes (e.g. `uname`).
func runShellSimple(cwd, script string) (string, error) {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensureWebAssets materialises the godash-owned web platform files in
// projectRoot/web/ using the embedded assets. The app title (read from
// pubspec.yaml) is substituted into index.html.
//
// This is idempotent and safe to call from any build entry point
// (godash prepare, godash web, etc.).
func ensureWebAssets(projectRoot string) error {
	title := readAppTitle(projectRoot)
	if title == "" {
		title = "godash app"
	}
	if err := assets.ExtractWeb(projectRoot, title); err != nil {
		return fmt.Errorf("extract web assets: %w", err)
	}
	if err := assets.WriteTinygoWasmExec(projectRoot); err != nil {
		return fmt.Errorf("write wasm_exec.js: %w", err)
	}
	if err := assets.WriteScrollWorker(projectRoot); err != nil {
		return fmt.Errorf("write scroll_worker.js: %w", err)
	}
	return nil
}

// licensesTplExport writes the embedded licenses template to a temp file
// and returns an "export GODASH_LICENSES_TPL=..." line plus a cleanup func.
// The temp file is what go-licenses reads via --template.
func licensesTplExport() (string, func()) {
	p, err := assets.WriteLicensesTempFile()
	if err != nil {
		// Fall back to a path that will make go-licenses error out clearly.
		return `export GODASH_LICENSES_TPL=""` + "\n", func() {}
	}
	return "export GODASH_LICENSES_TPL=" + shellQuote(p) + "\n", func() { _ = os.Remove(p) }
}

// runProtoAndWiring runs the go-mod bootstrap + protobuf generation, then
// writes the godash-owned Go wiring files (main*.go, rpc_handler.go,
// entrypoint.go) into the project. It is the shared first step of every
// build target (prepare, web, android, ios, macos) and is idempotent.
func runProtoAndWiring(env *projectEnv) error {
	script := envShell(env) + "\n" + goModBootstrap() + "\n" + protoShellScript()
	if err := runShellTask("Generate protobuf", env.Root, script); err != nil {
		return err
	}
	if err := writeWiringFiles(env.Root, ""); err != nil {
		return fmt.Errorf("write wiring files: %w", err)
	}
	return nil
}
