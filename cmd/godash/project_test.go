package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.mk")
	content := "# comment\n\nLIB_NAME=libcustom\nNDK_PATH := /opt/ndk\nEMPTY=\n  GODASH_PATH = ../godash  \n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"LIB_NAME":    "libcustom",
		"NDK_PATH":    "/opt/ndk",
		"EMPTY":       "",
		"GODASH_PATH": "../godash",
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadProjectEnvAt(t *testing.T) {
	dir := t.TempDir()

	// A fake godash checkout with a go.mod (the resolver validates it exists).
	godash := filepath.Join(dir, "godash")
	if err := os.MkdirAll(godash, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(godash, "go.mod"), []byte("module github.com/nosuta/godash/v2\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "core.env"), []byte("LIB_NAME=libcustom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom.mk"), []byte("NDK_PATH := /opt/ndk\nLIB_NAME=ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env, err := loadProjectEnvAt(dir, godash)
	if err != nil {
		t.Fatalf("loadProjectEnvAt: %v", err)
	}
	if env.Root != dir {
		t.Errorf("Root = %q, want %q", env.Root, dir)
	}
	if env.LibName != "libcustom" {
		t.Errorf("LibName = %q, want libcustom (core.env wins)", env.LibName)
	}
	if env.NDKPath != "/opt/ndk" {
		t.Errorf("NDKPath = %q, want /opt/ndk", env.NDKPath)
	}
	if env.GodashPath != godash {
		t.Errorf("GodashPath = %q, want %q", env.GodashPath, godash)
	}
	if env.IOSDeployment != "13.0" || env.MacosSDK != "macosx" {
		t.Errorf("defaults not applied: %+v", env)
	}
}

func TestLoadProjectEnvMissingGodash(t *testing.T) {
	dir := t.TempDir()
	_, err := loadProjectEnvAt(dir, filepath.Join(dir, "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error when the godash checkout is missing")
	}
}
