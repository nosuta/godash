package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// projectEnv holds resolved project context used by build scripts.
type projectEnv struct {
	Root              string // project root (working directory)
	GodashPath        string // path to the godash repository
	LibName           string // e.g. libflap
	NDKPath           string // Android NDK path
	IOSDeployment     string // iOS deployment target
	MacosDeployment   string // macOS deployment target
	MacosSDK          string // e.g. macosx
	IOSPluginDir      string // relative to Root
	MacosPluginDir    string // relative to Root
	AndroidPluginDir  string // relative to Root
	XCFrameworkName   string
	IOSFrameworkDir   string
	MacosFrameworkDir string
	Unamr             string // `uname -s` for sed -i '' vs -i
}

// loadProjectEnv resolves the project context from the current working
// directory by reading core.env, custom.mk, and locating godash.
//
// godashPathOverride, if non-empty, is used instead of auto-detection.
func loadProjectEnv(godashPathOverride string) (*projectEnv, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return loadProjectEnvAt(cwd, godashPathOverride)
}

// loadProjectEnvAt resolves the project context rooted at dir, not the
// process's current working directory. Used when scaffolding a new project
// (where cwd is the parent directory, not the freshly created one).
func loadProjectEnvAt(dir, godashPathOverride string) (*projectEnv, error) {
	cwd := dir

	env := &projectEnv{
		Root:            cwd,
		IOSDeployment:   "13.0",
		MacosDeployment: "10.15",
		MacosSDK:        "macosx",
		// The native bridge plugin lives in the godash repo. Native build
		// outputs (libflap.so, libflap.a, xcframework) are dropped here by
		// the godash CLI; Flutter then picks them up via the
		// path: ../godash/packages/native_internal dependency in pubspec.yaml.
		IOSPluginDir:     filepath.Join("..", "godash", "packages", "native_internal", "ios"),
		MacosPluginDir:   filepath.Join("..", "godash", "packages", "native_internal", "macos"),
		AndroidPluginDir: filepath.Join("..", "godash", "packages", "native_internal", "android", "src", "main", "jniLibs"),
		XCFrameworkName:  "native_internal.xcframework",
	}
	env.IOSFrameworkDir = filepath.Join(env.IOSPluginDir, "native_internal", "Frameworks", env.XCFrameworkName)
	env.MacosFrameworkDir = filepath.Join(env.MacosPluginDir, "native_internal", "Frameworks", env.XCFrameworkName)
	env.Unamr = runtime.GOOS

	// core.env
	coreEnv, err := readEnvFile(filepath.Join(cwd, "core.env"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if v, ok := coreEnv["LIB_NAME"]; ok && v != "" {
		env.LibName = v
	} else {
		env.LibName = "libflap"
	}

	// custom.mk
	customMk, err := readEnvFile(filepath.Join(cwd, "custom.mk"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if v, ok := customMk["NDK_PATH"]; ok {
		env.NDKPath = v
	}

	// godash path: override > custom.mk > env > default
	env.GodashPath = godashPathOverride
	if env.GodashPath == "" {
		if v, ok := customMk["GODASH_PATH"]; ok && v != "" {
			env.GodashPath = v
		}
	}
	if env.GodashPath == "" {
		env.GodashPath = os.Getenv("GODASH_PATH")
	}
	if env.GodashPath == "" {
		env.GodashPath = filepath.Join("..", "godash")
	}
	if !filepath.IsAbs(env.GodashPath) {
		env.GodashPath = filepath.Join(cwd, env.GodashPath)
	}
	if !hasGodashModule(env.GodashPath) {
		// Auto-provision a checkout so the user does not have to place godash
		// source manually. Overridable with GODASH_REPO / GODASH_REF and
		// disableable with GODASH_NO_PROVISION=1.
		provisioned, perr := provisionGodash(cwd)
		if perr != nil {
			return nil, fmt.Errorf("godash not found at %s and auto-provision failed: %w", env.GodashPath, perr)
		}
		if provisioned != "" {
			env.GodashPath = provisioned
		}
	}
	if !hasGodashModule(env.GodashPath) {
		return nil, fmt.Errorf("godash not found at %s (set GODASH_PATH, GODASH_REPO or update custom.mk)", env.GodashPath)
	}
	return env, nil
}

// readEnvFile reads a simple KEY=VALUE file, ignoring comments and blank lines.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// accept KEY=VALUE and KEY := VALUE
		if idx := strings.Index(line, ":="); idx >= 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+2:])
			out[k] = v
			continue
		}
		if idx := strings.Index(line, "="); idx >= 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			out[k] = v
		}
	}
	return out, sc.Err()
}

// readAppTitle reads the app's display name from the description field of
// pubspec.yaml. Used to parameterise the embedded web/index.html.
func readAppTitle(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "pubspec.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			v = strings.Trim(v, `"`)
			if v != "" {
				return v
			}
		}
	}
	return ""
}
