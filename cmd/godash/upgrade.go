package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	defaultGitAuthor = "Godash"
	defaultGitEmail  = "godash@localhost"
)

// runUpgrade handles `godash upgrade`.
//
// The template no longer owns godash files; everything godash-owned is
// generated or extracted from the godash package on every build. So
// "upgrade" means: pull the latest godash source, refresh Dart/Go
// dependencies, and regenerate derived files.
//
// For path-dep projects (the default template), the godash repo lives
// at the path declared in pubspec.yaml. `godash upgrade` runs
// `git pull` there. For version-pinned projects, it runs
// `flutter pub upgrade godash native_internal` and
// `go get -u github.com/nosuta/godash/v2`.
func runUpgrade(args []string) {
	cwd, err := os.Getwd()
	if err != nil {
		fatalf("%v", err)
	}
	if gitStatusDirty(cwd) {
		fatalf("working tree has uncommitted changes; commit or stash them first")
	}

	depType, depValue, err := detectGodashDep(filepath.Join(cwd, "pubspec.yaml"))
	if err != nil {
		fatalf("could not detect godash dependency: %v", err)
	}

	fmt.Printf("Upgrading godash (dependency: %s %s) ...\n", depType, depValue)
	fmt.Println()

	// Resolve the complete project environment, including GodashPath. Building
	// a bare &projectEnv{Root: cwd} leaves GodashPath empty, which makes the
	// generation scripts resolve $GODASH_PATH/... against the filesystem root.
	env, err := resolveUpgradeEnv(cwd, depType, depValue)
	if err != nil {
		fatalf("could not resolve godash source: %v", err)
	}

	var newCommit string
	switch depType {
	case "path":
		godashDir := env.GodashPath
		if isGitRepo(godashDir) {
			fmt.Printf("Updating godash source in %s ...\n", godashDir)
			if err := pullGodashRepo(godashDir); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not pull godash repo: %v\n", err)
			} else {
				sha, _ := gitRunTrim(godashDir, "rev-parse", "HEAD")
				newCommit = sha
			}
		} else {
			fmt.Fprintf(os.Stderr, "warning: %s is not a git repository; skipping pull\n", godashDir)
		}
	case "version":
		fmt.Println("Running `flutter pub upgrade godash native_internal` ...")
		if err := runShellTask("Upgrade Flutter packages", cwd, "flutter pub upgrade godash native_internal"); err != nil {
			os.Exit(1)
		}
		// The Dart/Go deps resolve from the package caches, but code
		// generation uses the provisioned source checkout: keep it fresh.
		if isGitRepo(env.GodashPath) {
			fmt.Printf("Updating godash source in %s ...\n", env.GodashPath)
			if err := pullGodashRepo(env.GodashPath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not pull godash repo: %v\n", err)
			}
		}
	default:
		fatalf("unknown godash dependency type: %s", depType)
	}

	// Refresh Dart and Go module caches so they pick up the updated godash.
	fmt.Println()
	fmt.Println("Refreshing dependencies ...")
	if err := runShellTask("Refresh dependencies", cwd,
		"flutter pub get\ngo mod -C go download"); err != nil {
		os.Exit(1)
	}

	// Regenerate all godash-owned files (proto, wiring, web assets,
	// licenses) so they match the new godash.
	fmt.Println()
	fmt.Println("Regenerating derived files ...")
	if err := runProtoAndWiring(env); err != nil {
		fatalf("regenerate wiring: %v", err)
	}
	if err := runPrepareRefresh(env); err != nil {
		fatalf("regenerate derived files: %v", err)
	}

	// Record the new version in .godash-template.
	if err := writeTemplateMeta(cwd, &templateMeta{
		Version: depValue,
		Commit:  newCommit,
	}); err != nil {
		fatalf("write .godash-template: %v", err)
	}

	// Commit the regenerated tree if it's a git repo.
	if isGitRepo(cwd) {
		_, _ = gitRun(cwd, "add", "-A")
		commitMsg := fmt.Sprintf("Upgrade godash to %s", depValue)
		if newCommit != "" {
			commitMsg = fmt.Sprintf("Upgrade godash to %s @ %s", depValue, shortSHA(newCommit))
		}
		_ = gitCommitWithUser(cwd, commitMsg)
	}

	fmt.Println()
	if newCommit != "" {
		fmt.Printf("%s✓%s Upgraded godash to %s @ %s\n", colorGreen, colorReset, depValue, shortSHA(newCommit))
	} else {
		fmt.Printf("%s✓%s Upgraded godash to %s\n", colorGreen, colorReset, depValue)
	}
}

// resolveUpgradeEnv loads the project environment used to regenerate derived
// files during an upgrade. For path dependencies it forces the godash path
// declared in pubspec.yaml, so $GODASH_PATH always points at a real checkout.
func resolveUpgradeEnv(cwd, depType, depValue string) (*projectEnv, error) {
	override := ""
	if depType == "path" {
		override = resolveGodashPath(cwd, depValue)
	}
	return loadProjectEnvAt(cwd, override)
}

// runPrepareRefresh re-runs the prepare step (proto, flutter create for
// missing platforms, go-licenses) in env.Root. It is a focused
// subset of runPrepare used by godash upgrade.
func runPrepareRefresh(env *projectEnv) error {
	cwd := env.Root
	licensesLine, cleanupLicenses := licensesTplExport()
	defer cleanupLicenses()
	_ = ensureWebAssets(cwd)
	if err := runProtoAndWiring(env); err != nil {
		return err
	}
	rest := licensesLine + applyGoLicensesScript() + "\n" + flutterCreateBlocks(true)
	return runShellTask("Prepare environment (flutter create, licenses)", cwd, envShell(env)+"\n"+rest)
}

// godashInlineRe matches `  godash: <value>` (inline version constraint).
var godashInlineRe = regexp.MustCompile(`(?m)^\s*godash:\s*(\S+)\s*$`)

// godashBlockRe matches `  godash:` with no inline value (block form).
var godashBlockRe = regexp.MustCompile(`(?m)^\s*godash:\s*$`)

// pathRe matches `    path: <value>` inside a dep block.
var pathRe = regexp.MustCompile(`(?m)^\s*path:\s*(\S+)\s*$`)

// detectGodashDep inspects pubspec.yaml and returns the type and value
// of the godash dependency. depType is "path" or "version".
func detectGodashDep(pubspecPath string) (string, string, error) {
	data, err := os.ReadFile(pubspecPath)
	if err != nil {
		return "", "", err
	}
	text := string(data)
	if m := godashInlineRe.FindStringSubmatch(text); m != nil {
		v := strings.Trim(m[1], `"'`)
		if v != "" {
			return "version", v, nil
		}
	}
	loc := godashBlockRe.FindStringIndex(text)
	if loc == nil {
		return "", "", fmt.Errorf("godash dependency not found in %s", pubspecPath)
	}
	rest := text[loc[1]:]
	// Look at the next few lines for `path:` or a nested version.
	for i, line := range strings.SplitN(rest, "\n", 12) {
		if i == 0 {
			continue
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			return "path", strings.Trim(m[1], `"'`), nil
		}
		// A nested non-indented key means the godash block ended.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.TrimSpace(line) != "" {
			break
		}
		// If a sibling key appears (e.g. `native_internal:`), stop.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.Contains(line, ":") {
			break
		}
	}
	return "", "", fmt.Errorf("godash dependency is neither path nor version in %s", pubspecPath)
}

// resolveGodashPath returns the absolute path to the godash checkout
// referenced by a `path:` dep. dir is the project root; value is the
// raw `path:` value (e.g. "../godash").
func resolveGodashPath(dir, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(dir, value))
}

// pullGodashRepo fetches the latest commits from the godash repo's
// configured remote. It is resilient to repos that have no upstream
// tracking on the current branch (e.g. a fresh clone without
// `git push -u`): it falls back to fetching the remote's default
// branch explicitly.
func pullGodashRepo(godashDir string) error {
	// Fast path: upstream is set.
	if out, err := gitRun(godashDir, "pull", "--ff-only"); err == nil {
		fmt.Print(out)
		return nil
	}
	// Fallback: detect the default branch on the remote and merge it.
	remote := "origin"
	branch, berr := godashDefaultBranch(godashDir, remote)
	if berr != "" {
		// Last resort: just fetch and report; user can merge manually.
		_, _ = gitRun(godashDir, "fetch", remote)
		return fmt.Errorf("no upstream and no default branch detected: %s", berr)
	}
	if _, err := gitRun(godashDir, "fetch", remote, branch); err != nil {
		return err
	}
	if _, err := gitRun(godashDir, "merge", "--ff-only", remote+"/"+branch); err != nil {
		return err
	}
	return nil
}

// godashDefaultBranch returns the remote's default branch name
// (e.g. "main") by inspecting its HEAD symref, or an error message
// if it cannot be determined.
func godashDefaultBranch(godashDir, remote string) (string, string) {
	out, err := gitRun(godashDir, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", err.Error()
	}
	name := strings.TrimPrefix(strings.TrimSpace(out), remote+"/")
	if name == "" || name == "HEAD" {
		return "", "empty HEAD symref"
	}
	return name, ""
}

// runShellTask is defined in shell.go (kept here for the doc reference).

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// suppress unused warnings for the legacy helpers removed from this file.
var _ = (*exec.Cmd)(nil)
var _ = bufio.NewReader
