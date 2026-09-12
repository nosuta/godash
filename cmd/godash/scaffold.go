package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nosuta/godash/v2/cmd/godash/assets"
)

// embeddedTemplateSource marks the project template embedded in the CLI. It is
// the default `godash new` source; GODASH_TEMPLATE (or the deprecated
// FLAP_TEMPLATE) can override it with a local path or remote Git URL.
const embeddedTemplateSource = "embedded"

// scaffoldConfig captures the user-provided inputs for a new project.
type scaffoldConfig struct {
	dir      string // directory / project name
	appName  string // display name e.g. "My App"
	bundleID string // e.g. com.example.myapp
}

var reBundleID = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*){2,}$`)

func runScaffold(args []string) {
	fmt.Printf("godash %s\n", Version)
	fmt.Println()

	// 1. dependency check
	fmt.Println("Checking dependencies...")
	if !checkDeps() {
		os.Exit(1)
	}
	fmt.Printf("%s✓%s All dependencies found\n", colorGreen, colorReset)
	fmt.Println()

	// set version (optional first arg)
	if len(args) > 0 {
		v := args[0]
		if v == "latest" {
			Version = "latest"
		} else {
			if err := checkRemoteTag(v); err != nil {
				fatalf("%v", err)
			}
			Version = v
		}
	}
	fmt.Printf("Using version %s\n", Version)
	fmt.Println()

	// 2. interactive prompts
	cfg := promptScaffoldConfig()
	fmt.Println()

	cleanup := func() {
		if _, err := os.Stat(cfg.dir); err == nil {
			fmt.Fprintf(os.Stderr, "Cleaning up ./%s ...\n", cfg.dir)
			os.RemoveAll(cfg.dir)
		}
	}

	// 3. materialise the template (embedded by default, or GODASH_TEMPLATE)
	if err := cloneTemplate(cfg); err != nil {
		fatalf("Failed to materialise template: %v", err)
	}

	// Resolve to an absolute path so downstream helpers always see an absolute
	// project root (avoids "../godash" collapsing to "godash" when cfg.dir is
	// relative). The original (display) name is kept separately.
	displayDir := cfg.dir
	absDir, err := filepath.Abs(cfg.dir)
	if err != nil {
		cleanup()
		fatalf("Failed to resolve project path: %v", err)
	}
	cfg.dir = absDir

	// 4. custom.mk (NDK auto-detection)
	if err := setupCustomMk(cfg.dir); err != nil {
		cleanup()
		fatalf("Failed to create custom.mk: %v", err)
	}

	// 5. git tracking: init repo, record the godash dependency/version
	if err := setupTemplateTracking(cfg.dir); err != nil {
		cleanup()
		fatalf("Failed to set up godash tracking: %v", err)
	}

	// 6. prepare (creates platform directories, generates code)
	if err := scaffoldPrepare(cfg.dir); err != nil {
		cleanup()
		fatalf("Setup failed: %v", err)
	}

	// 7. apply config (replaces bundle ID / app name in generated files)
	if err := applyConfig(cfg); err != nil {
		cleanup()
		fatalf("Failed to apply config: %v", err)
	}

	// 8. commit generated platform dirs and applied config so the project
	//    starts as a clean git working tree (lets `godash upgrade` see a
	//    proper merge base).
	if err := commitScaffoldState(cfg.dir, cfg.appName); err != nil {
		cleanup()
		fatalf("Failed to commit initial project state: %v", err)
	}

	// 9. extract godash-owned web assets (now that the real app title is in
	//    pubspec.yaml) and commit the result.
	if err := ensureWebAssets(cfg.dir); err != nil {
		cleanup()
		fatalf("Failed to extract web assets: %v", err)
	}
	_, _ = gitRun(cfg.dir, "add", "-A")
	_ = gitCommitWithUser(cfg.dir, "Extract godash web platform assets")

	// 10. prepare Go wasm test runner
	if err := scaffoldWasmTest(cfg.dir); err != nil {
		cleanup()
		fatalf("Wasm test setup failed: %v", err)
	}

	fmt.Println()
	fmt.Printf("%s✓%s Project created at ./%s\n", colorGreen, colorReset, displayDir)
	fmt.Println()
	fmt.Printf("  cd %s\n", displayDir)
	fmt.Println("  godash doctor                      # verify required tools")
	fmt.Println("  godash web run                     # run in browser")
	fmt.Println("  godash web build                   # build for web release")
	fmt.Println("  godash android apk                 # build Android APK")
	fmt.Println("  godash ios                         # build iOS")
	fmt.Println("  godash macos run                   # run on macOS (requires Xcode)")
	fmt.Println("  godash upgrade                     # pull in new godash template changes")
}

func promptScaffoldConfig() scaffoldConfig {
	r := bufio.NewReader(os.Stdin)
	appName := askScaffold(r, "App name", "My App", func(s string) bool { return s != "" })
	dir := askScaffold(r, "Directory name", toSlug(appName), func(s string) bool { return s != "" })
	bundleID := askScaffold(r, "Bundle ID (e.g. com.example.myapp)", "com.example."+toSlug(appName), func(s string) bool {
		if !reBundleID.MatchString(s) {
			fmt.Println("  Must be lowercase reverse-domain format, e.g. com.example.myapp")
			return false
		}
		return true
	})
	return scaffoldConfig{dir: dir, appName: appName, bundleID: bundleID}
}

func askScaffold(r *bufio.Reader, label, defaultVal string, validate func(string) bool) string {
	for {
		fmt.Printf("%s [%s]: ", label, defaultVal)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = defaultVal
		}
		if validate(line) {
			return line
		}
	}
}

func toSlug(s string) string {
	s = strings.ToLower(s)
	return regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "")
}

// templateSource returns the effective template source: the GODASH_TEMPLATE
// override (or the deprecated FLAP_TEMPLATE alias) if set, otherwise the
// built-in template embedded in the CLI.
func templateSource() string {
	if s := os.Getenv("GODASH_TEMPLATE"); s != "" {
		return s
	}
	if s := os.Getenv("FLAP_TEMPLATE"); s != "" {
		return s
	}
	return embeddedTemplateSource
}

// isLocalPath reports whether src looks like a local filesystem path.
func isLocalPath(src string) bool {
	return !strings.Contains(src, "://") && (filepath.IsAbs(src) || strings.Contains(src, string(filepath.Separator)) || src == "." || src == "..")
}

// checkRemoteTag verifies that the given tag exists on the remote template repo.
func checkRemoteTag(tag string) error {
	src := templateSource()
	if src == embeddedTemplateSource {
		return fmt.Errorf("version %q requires GODASH_TEMPLATE; the built-in template ships with this CLI", tag)
	}
	if isLocalPath(src) {
		return nil
	}
	out, err := exec.Command("git", "ls-remote", "--tags", src, "refs/tags/"+tag).Output()
	if err != nil {
		return fmt.Errorf("failed to reach remote: %w", err)
	}
	if !strings.Contains(string(out), "refs/tags/"+tag) {
		return fmt.Errorf("version %q not found in %s", tag, src)
	}
	return nil
}

func cloneTemplate(cfg scaffoldConfig) error {
	if _, err := os.Stat(cfg.dir); err == nil {
		return fmt.Errorf("directory %q already exists", cfg.dir)
	}
	src := templateSource()
	if src == embeddedTemplateSource {
		// Extract the template embedded in the CLI binary. There is no origin
		// remote; setupTemplateTracking initialises git and records the godash
		// dependency instead.
		return taskFn("Extract built-in template", func() error {
			return assets.ExtractTemplate(cfg.dir)
		})
	}
	if isLocalPath(src) {
		return copyLocalTemplate(src, cfg.dir)
	}
	args := []string{"clone", "--depth=1"}
	if Version != "latest" {
		args = append(args, "--branch", Version)
	}
	args = append(args, src, cfg.dir)
	if err := task("Clone template", ".", "git", args...); err != nil {
		return err
	}
	return task("Set upstream remote", cfg.dir, "git", "remote", "rename", "origin", "upstream")
}

func copyLocalTemplate(src, dst string) error {
	return taskFn("Copy local template", func() error {
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("template path %q does not exist: %w", src, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("template path %q is not a directory", src)
		}
		return copyDir(src, dst)
	})
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, info.Mode())
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

// scaffoldPrepare runs the initial prepare step inside the freshly cloned
// project. It runs the proto bootstrap + generation, then writes the
// godash-owned Go wiring files (main*.go, rpc_handler.go, entrypoint.go),
// then runs the remaining prepare shell (flutter create for missing
// platforms, licenses).
//
// Web assets are NOT extracted here because the app title (used in
// index.html) is set later by applyConfig. The caller (runScaffold) is
// responsible for calling ensureWebAssets after applyConfig.
func scaffoldPrepare(dir string) error {
	env, err := loadProjectEnvAt(dir, "")
	if err != nil {
		return err
	}
	if err := runProtoAndWiring(env); err != nil {
		return err
	}
	licensesLine, cleanupLicenses := licensesTplExport()
	defer cleanupLicenses()
	rest := envShell(env) + "\n" + licensesLine +
		applyGoLicensesScript() + "\n" +
		flutterCreateBlocks(true)
	return runShellTask("Prepare environment (flutter create, licenses)", dir, rest)
}

// scaffoldWasmTest copies sqlite3 assets and scroll_worker.js to godash's
// go_js_wasm_exec and installs it. The scroll_worker.js content comes
// from the embedded godash asset, not from the project tree.
func scaffoldWasmTest(dir string) error {
	env, err := loadProjectEnvAt(dir, "")
	if err != nil {
		return err
	}
	// Materialise the scroll worker placeholder so the wasm test runner
	// has it to serve.
	if err := os.WriteFile(filepath.Join(dir, "web", "scroll_worker.js"), assets.ScrollWorkerJS(), 0644); err != nil {
		return err
	}
	script := envShell(env) + "\n" + buildPrepareWasmTestScript()
	return runShellTask("Prepare Go wasm test", dir, script)
}

func applyConfig(cfg scaffoldConfig) error {
	return taskFn("Apply project configuration", func() error {
		if err := replaceInFile(
			filepath.Join(cfg.dir, "pubspec.yaml"),
			`description: "flap"`, `description: "`+cfg.appName+`"`,
		); err != nil {
			return err
		}
		_ = replaceInFile(
			filepath.Join(cfg.dir, "android", "app", "build.gradle.kts"),
			`namespace = "com.example.flap"`, `namespace = "`+cfg.bundleID+`"`,
		)
		_ = replaceInFile(
			filepath.Join(cfg.dir, "android", "app", "build.gradle.kts"),
			`applicationId = "com.example.flap"`, `applicationId = "`+cfg.bundleID+`"`,
		)
		_ = replaceInFile(
			filepath.Join(cfg.dir, "android", "app", "src", "main", "AndroidManifest.xml"),
			`android:label="flap"`, `android:label="`+cfg.appName+`"`,
		)
		_ = replaceInFile(
			filepath.Join(cfg.dir, "ios", "Runner.xcodeproj", "project.pbxproj"),
			`PRODUCT_BUNDLE_IDENTIFIER = com.example.flap;`, `PRODUCT_BUNDLE_IDENTIFIER = `+cfg.bundleID+`;`,
		)
		_ = replaceInFile(
			filepath.Join(cfg.dir, "ios", "Runner", "Info.plist"),
			`<string>Flap</string>`, `<string>`+cfg.appName+`</string>`,
		)
		_ = replaceInFile(
			filepath.Join(cfg.dir, "macos", "Runner", "Configs", "AppInfo.xcconfig"),
			`PRODUCT_NAME = flap`, `PRODUCT_NAME = `+cfg.appName,
		)
		_ = replaceInFile(
			filepath.Join(cfg.dir, "macos", "Runner", "Configs", "AppInfo.xcconfig"),
			`PRODUCT_BUNDLE_IDENTIFIER = com.example.flap`, `PRODUCT_BUNDLE_IDENTIFIER = `+cfg.bundleID,
		)
		return nil
	})
}

func replaceInFile(path, old, new string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated := strings.ReplaceAll(string(b), old, new)
	return os.WriteFile(path, []byte(updated), 0644)
}

func setupCustomMk(dir string) error {
	return taskFn("Configure custom.mk", func() error {
		ndkPath := findNDK()
		content := "NDK_PATH=" + ndkPath + "\n"
		return os.WriteFile(filepath.Join(dir, "custom.mk"), []byte(content), 0644)
	})
}

// findNDK looks for the Android NDK in common SDK locations.
func findNDK() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, "Library", "Android", "sdk", "ndk"),
		filepath.Join(home, "Android", "Sdk", "ndk"),
		filepath.Join(home, "AppData", "Local", "Android", "Sdk", "ndk"),
	}
	for _, base := range candidates {
		entries, err := os.ReadDir(base)
		if err != nil || len(entries) == 0 {
			continue
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].IsDir() {
				return filepath.Join(base, entries[i].Name())
			}
		}
	}
	return ""
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, colorRed+"✗"+colorReset+" "+format+"\n", args...)
	os.Exit(1)
}

// setupTemplateTracking initialises a git working tree inside the newly
// copied project and records the godash dependency in .godash-template.
//
// The template no longer needs a `godash` git remote: godash is consumed
// as a path/version dep via pubspec.yaml and go.mod. `godash upgrade`
// just pulls godash and re-runs prepare; there is nothing to merge from
// a "template" remote.
func setupTemplateTracking(dir string) error {
	// 1. Ensure the directory is a git repo.
	if !isGitRepo(dir) {
		_, err := gitRun(dir, "init", "-b", "main")
		if err != nil {
			return fmt.Errorf("git init: %w", err)
		}
	}

	// 2. Inspect pubspec.yaml to record the godash dependency.
	depType, depValue, err := detectGodashDep(filepath.Join(dir, "pubspec.yaml"))
	if err != nil {
		return fmt.Errorf("detect godash dep: %w", err)
	}
	version := depValue
	if depType == "path" {
		// Provision the godash checkout if the user did not place one, so
		// the recorded version has a real commit and prepare can run.
		if _, err := resolveUpgradeEnv(dir, depType, depValue); err != nil {
			return fmt.Errorf("provision godash: %w", err)
		}
		// Resolve to absolute and record the godash HEAD commit so
		// future `godash upgrade` runs have a known reference.
		godashDir := resolveGodashPath(dir, depValue)
		if sha, err := gitRunTrim(godashDir, "rev-parse", "HEAD"); err == nil {
			version = godashDir + "@" + shortSHA(sha)
		} else {
			version = godashDir
		}
	}

	// 3. Write .godash-template (untracked at this point).
	if err := writeTemplateMeta(dir, &templateMeta{
		Version: version,
		Commit:  "",
	}); err != nil {
		return err
	}

	// 4. Commit (template files + .godash-template + custom.mk).
	_, _ = gitRun(dir, "add", "-A")
	if err := gitCommitWithUser(dir, fmt.Sprintf("Initialize godash project (godash %s)", version)); err != nil {
		return fmt.Errorf("initial commit: %w", err)
	}
	return nil
}

// commitScaffoldState stages the platform directories produced by
// scaffoldPrepare and the user-config changes from applyConfig, then
// commits them. The repo state is clean after this so a `godash upgrade`
// can run on the next invocation.
func commitScaffoldState(dir, appName string) error {
	if !isGitRepo(dir) {
		return nil
	}
	_, _ = gitRun(dir, "add", "-A")
	if err := gitCommitWithUser(dir, fmt.Sprintf("Apply project configuration for %q", appName)); err != nil {
		return err
	}
	return nil
}
