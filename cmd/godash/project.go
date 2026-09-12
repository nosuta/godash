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
	GodashPath        string // path to the godash repository (may be empty for version deps)
	NativeInternalDir string // resolved native_internal plugin directory
	MaterializeNative bool   // copy native_internal from the godash module tree
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
		XCFrameworkName: "native_internal.xcframework",
	}
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

	// A local checkout is mandatory for path-replace projects (pubspec
	// `path:` + go.mod `replace`), but optional for version-pinned ones:
	// those resolve godash through the package managers and the build scripts
	// locate the source dir via the Go module graph ($GODASH_MODULE_DIR).
	depType, _, depErr := detectGodashDep(filepath.Join(cwd, "pubspec.yaml"))
	if depErr == nil && depType == "path" && !hasGodashModule(env.GodashPath) {
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
		if depErr == nil && depType == "version" {
			// Version-pinned project without a local checkout: not an error.
			// The build scripts resolve the source dir from the Go module
			// graph ($GODASH_MODULE_DIR).
			env.GodashPath = ""
		} else {
			return nil, fmt.Errorf("godash not found at %s (set GODASH_PATH, GODASH_REPO or update custom.mk)", env.GodashPath)
		}
	}

	// Resolve the native_internal plugin directory. Version-pinned projects
	// use the project-local .godash/native_internal (materialised from the
	// godash module); path-replace projects point straight at the checkout
	// declared in pubspec.yaml. Native build outputs (libflap.so, libflap.a,
	// xcframework) are written there and Flutter resolves the plugin via the
	// matching pubspec dependency.
	nativeDir := filepath.Join(cwd, ".godash", "native_internal")
	if p, nerr := nativeInternalPath(filepath.Join(cwd, "pubspec.yaml")); nerr == nil {
		nativeDir = resolveGodashPath(cwd, p)
	}
	env.NativeInternalDir = nativeDir
	checkoutPlugin := ""
	if env.GodashPath != "" {
		checkoutPlugin = filepath.Join(env.GodashPath, "packages", "native_internal")
	}
	env.MaterializeNative = filepath.Clean(nativeDir) != filepath.Clean(checkoutPlugin)
	env.IOSPluginDir = filepath.Join(nativeDir, "ios")
	env.MacosPluginDir = filepath.Join(nativeDir, "macos")
	env.AndroidPluginDir = filepath.Join(nativeDir, "android", "src", "main", "jniLibs")
	env.IOSFrameworkDir = filepath.Join(env.IOSPluginDir, "native_internal", "Frameworks", env.XCFrameworkName)
	env.MacosFrameworkDir = filepath.Join(env.MacosPluginDir, "native_internal", "Frameworks", env.XCFrameworkName)

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
