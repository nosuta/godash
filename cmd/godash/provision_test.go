package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireGit skips the test when git is not installed.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// makeFakeGodashRemote creates a local git repo that looks like a godash
// checkout, for use as a clone source in tests. It returns the repo path.
func makeFakeGodashRemote(t *testing.T, base string) string {
	t.Helper()
	requireGit(t)
	repo := filepath.Join(base, "godash-remote")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/nosuta/godash/v2\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitRun(repo, "init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if _, err := gitRun(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := gitCommitWithUser(repo, "init"); err != nil {
		t.Fatal(err)
	}
	return repo
}

func writePubspecBody(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "pubspec.yaml"), []byte("name: app\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProvisionGodashPathDep verifies that a path-dep project with a missing
// `../godash` gets the checkout cloned into the declared path.
func TestProvisionGodashPathDep(t *testing.T) {
	base := t.TempDir()
	remote := makeFakeGodashRemote(t, base)

	project := filepath.Join(base, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writePubspecBody(t, project, "dependencies:\n  godash:\n    path: ../godash\n")

	t.Setenv("GODASH_REPO", remote)
	t.Setenv("GODASH_REF", "")
	t.Setenv("GODASH_PATH", "")
	t.Setenv("GODASH_NO_PROVISION", "")

	env, err := loadProjectEnvAt(project, "")
	if err != nil {
		t.Fatalf("loadProjectEnvAt: %v", err)
	}
	want := filepath.Join(base, "godash")
	if env.GodashPath != want {
		t.Errorf("GodashPath = %q, want %q", env.GodashPath, want)
	}
	if !hasGodashModule(want) {
		t.Errorf("expected a godash checkout at %s", want)
	}
}

// TestVersionDepNeedsNoCheckout verifies that a version-pinned project resolves
// godash through the package managers: no source checkout is required, none is
// created, and GodashPath is left empty.
func TestVersionDepNeedsNoCheckout(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writePubspecBody(t, project, "dependencies:\n  godash: ^2.1.0\n")

	t.Setenv("GODASH_REPO", filepath.Join(base, "does-not-exist"))
	t.Setenv("GODASH_REF", "")
	t.Setenv("GODASH_PATH", "")

	env, err := loadProjectEnvAt(project, "")
	if err != nil {
		t.Fatalf("loadProjectEnvAt: %v", err)
	}
	if env.GodashPath != "" {
		t.Errorf("GodashPath = %q, want empty (no checkout needed)", env.GodashPath)
	}
	if _, err := os.Stat(filepath.Join(base, "godash")); err == nil {
		t.Error("a checkout must not be created for a version dep")
	}
}

// TestEnsureGodashCheckoutSHA verifies that an explicit commit ref is checked
// out (the non-shallow clone path).
func TestEnsureGodashCheckoutSHA(t *testing.T) {
	base := t.TempDir()
	remote := makeFakeGodashRemote(t, base)
	sha, err := gitRunTrim(remote, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(base, "checkout")
	got, err := ensureGodashCheckout(target, remote, sha)
	if err != nil {
		t.Fatalf("ensureGodashCheckout: %v", err)
	}
	if got != target {
		t.Errorf("got %q, want %q", got, target)
	}
	if !hasGodashModule(target) {
		t.Errorf("expected a godash checkout at %s", target)
	}
	head, err := gitRunTrim(target, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if head != sha {
		t.Errorf("HEAD = %s, want %s", head, sha)
	}
}

// TestProvisionGodashDisabled verifies the GODASH_NO_PROVISION opt-out.
func TestProvisionGodashDisabled(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writePubspecBody(t, project, "dependencies:\n  godash:\n    path: ../godash\n")

	t.Setenv("GODASH_NO_PROVISION", "1")
	t.Setenv("GODASH_REPO", filepath.Join(base, "does-not-exist"))
	t.Setenv("GODASH_PATH", "")

	if _, err := loadProjectEnvAt(project, ""); err == nil {
		t.Fatal("expected an error when provisioning is disabled")
	}
}

// TestProvisionGodashNoClobber ensures an existing non-godash directory at the
// target is never overwritten.
func TestProvisionGodashNoClobber(t *testing.T) {
	base := t.TempDir()
	remote := makeFakeGodashRemote(t, base)

	project := filepath.Join(base, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writePubspecBody(t, project, "dependencies:\n  godash:\n    path: ../godash\n")

	// Pre-create `../godash` with unrelated content.
	target := filepath.Join(base, "godash")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GODASH_REPO", remote)
	t.Setenv("GODASH_REF", "")
	t.Setenv("GODASH_PATH", "")

	if _, err := loadProjectEnvAt(project, ""); err == nil {
		t.Fatal("expected an error rather than clobbering a non-godash directory")
	}
	if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
		t.Errorf("existing content was removed: %v", err)
	}
}
