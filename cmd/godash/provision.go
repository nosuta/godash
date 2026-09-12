package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// defaultGodashRepo is the canonical godash source used to auto-provision a
// checkout when a project does not have one. Override with GODASH_REPO.
const defaultGodashRepo = "https://github.com/nosuta/godash.git"

// godashRepo returns the git URL used for auto-provisioning.
func godashRepo() string {
	if v := strings.TrimSpace(os.Getenv("GODASH_REPO")); v != "" {
		return v
	}
	return defaultGodashRepo
}

// godashRef returns the optional ref (branch, tag or commit SHA) to check out
// when auto-provisioning. Empty means the remote's default branch.
func godashRef() string {
	return strings.TrimSpace(os.Getenv("GODASH_REF"))
}

// godashProvisionDisabled reports whether auto-provisioning was opted out of.
func godashProvisionDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GODASH_NO_PROVISION"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// godashCacheRoot returns the per-user cache directory for godash checkouts
// used by version-pinned projects. Override with GODASH_CACHE_DIR.
func godashCacheRoot() string {
	if dir := strings.TrimSpace(os.Getenv("GODASH_CACHE_DIR")); dir != "" {
		return dir
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "godash")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "godash")
	}
	return filepath.Join(os.TempDir(), "godash-cache")
}

var (
	gitSHAShortRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	refUnsafeRe   = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// sanitizeRef turns a git ref into a safe single path segment.
func sanitizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "default"
	}
	out := strings.Trim(refUnsafeRe.ReplaceAllString(ref, "_"), "._-")
	if out == "" {
		return "default"
	}
	return out
}

// hasGodashModule reports whether dir looks like a godash checkout.
func hasGodashModule(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// ensureGodashCheckout materialises a godash checkout at target if one is not
// already present, then returns target. A missing target is cloned from repo,
// optionally checking out ref.
func ensureGodashCheckout(target, repo, ref string) (string, error) {
	if hasGodashModule(target) {
		return target, nil
	}
	// Never clobber an existing directory that is not a godash checkout.
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return "", fmt.Errorf("%s exists and is not a godash checkout", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	// A commit SHA cannot be targeted by a shallow branch clone.
	shallow := ref == "" || !gitSHAShortRe.MatchString(ref)
	if shallow {
		args := []string{"clone", "--depth", "1"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		args = append(args, repo, target)
		if out, err := gitRun("", args...); err != nil {
			return "", fmt.Errorf("clone %s: %v: %s", repo, err, strings.TrimSpace(out))
		}
		return target, nil
	}

	if out, err := gitRun("", "clone", repo, target); err != nil {
		return "", fmt.Errorf("clone %s: %v: %s", repo, err, strings.TrimSpace(out))
	}
	if out, err := gitRun(target, "checkout", ref); err != nil {
		return "", fmt.Errorf("checkout %s: %v: %s", ref, err, strings.TrimSpace(out))
	}
	return target, nil
}

// provisionGodash auto-creates a godash source checkout for the project so the
// user does not have to place one manually. It returns the effective godash
// path, or ("", nil) when the project has no godash dependency.
//
// For path dependencies the checkout goes to the path declared in pubspec.yaml
// so the pubspec `path:` and go.mod `replace` both resolve. For version-pinned
// projects the checkout goes to the per-user cache: the Dart/Go dependencies
// resolve from the package caches, but code generation still needs the tools
// and `godash/options.proto` from a source tree.
func provisionGodash(projectDir string) (string, error) {
	pubspecPath := filepath.Join(projectDir, "pubspec.yaml")
	if _, err := os.Stat(pubspecPath); err != nil {
		return "", nil // not a godash project
	}
	depType, depValue, err := detectGodashDep(pubspecPath)
	if err != nil {
		return "", nil // no godash dependency to provision
	}
	if godashProvisionDisabled() {
		return "", nil
	}

	repo := godashRepo()
	ref := godashRef()

	if depType == "path" {
		target := resolveGodashPath(projectDir, depValue)
		if hasGodashModule(target) {
			return target, nil
		}
		fmt.Printf("godash source not found at %s; cloning %s ...\n", target, repo)
		return ensureGodashCheckout(target, repo, ref)
	}

	target := filepath.Join(godashCacheRoot(), sanitizeRef(ref))
	if hasGodashModule(target) {
		return target, nil
	}
	fmt.Printf("Fetching godash source into %s ...\n", target)
	return ensureGodashCheckout(target, repo, ref)
}
