package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// envShell returns shell lines that export the projectEnv variables, so that
// $VAR references in subsequent shell commands resolve to the resolved values.
func envShell(e *projectEnv) string {
	export := func(k, v string) string {
		return "export " + k + "=" + shellQuote(v)
	}
	materialize := "0"
	if e.MaterializeNative {
		materialize = "1"
	}
	lines := []string{
		export("GODASH_NATIVE_DIR", e.NativeInternalDir),
		export("GODASH_MATERIALIZE_NATIVE", materialize),
		export("LIB_NAME", e.LibName),
		export("NDK_PATH", e.NDKPath),
		export("IOS_DEPLOYMENT_TARGET", e.IOSDeployment),
		export("MACOS_DEPLOYMENT_TARGET", e.MacosDeployment),
		export("MACOS_SDK", e.MacosSDK),
		export("IOS_PLUGIN_DIR", e.IOSPluginDir),
		export("MACOS_PLUGIN_DIR", e.MacosPluginDir),
		export("ANDROID_PLUGIN_DIR", e.AndroidPluginDir),
		export("XCFRAMEWORK_NAME", e.XCFrameworkName),
		export("IOS_FRAMEWORK_DIR", e.IOSFrameworkDir),
		export("MACOS_FRAMEWORK_DIR", e.MacosFrameworkDir),
	}
	return strings.Join(lines, "\n")
}

// shellQuote wraps s in POSIX single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// runWeb handles `godash web [build|run]` (default: build).
func runWeb(args []string) {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	mode := "build"
	if len(args) > 0 {
		mode = args[0]
	}
	_ = ensureWebAssets(env.Root)
	if err := runProtoAndWiring(env); err != nil {
		os.Exit(1)
	}
	switch mode {
	case "build":
		licensesLine, cleanupLicenses := licensesTplExport()
		defer cleanupLicenses()
		// Web build needs proto done (already by runProtoAndWiring), the
		// TinyGo worker, licenses, then flutter build.
		script := envShell(env) + "\n" + licensesLine + wasmTinyGoScript() + "\n" + applyGoLicensesScript() + "\n" + `flutter build web --wasm --release`
		if err := runShellTask("Build for web", env.Root, script); err != nil {
			os.Exit(1)
		}
	case "run":
		script := envShell(env) + "\n" + wasmFullScript() + "\n" + `flutter run -d web-server`
		if err := runShellPipe(env.Root, script); err != nil {
			fmt.Fprintf(os.Stderr, "web run exited with code %d\n", errExit(err))
			os.Exit(errExit(err))
		}
	default:
		fmt.Fprintf(os.Stderr, "godash web: unknown mode %q (expected build or run)\n", mode)
		os.Exit(1)
	}
}

// runAndroid handles `godash android [apk|appbundle]` (default: apk).
func runAndroid(args []string) {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	mode := "apk"
	if len(args) > 0 {
		mode = args[0]
	}
	if err := runProtoAndWiring(env); err != nil {
		os.Exit(1)
	}
	licensesLine, cleanupLicenses := licensesTplExport()
	defer cleanupLicenses()
	var script string
	switch mode {
	case "apk":
		script = envShell(env) + "\n" + licensesLine +
			buildScriptAndroidLibArm64(env) + "\n" +
			buildScriptAndroidLibX86_64(env) + "\n" +
			applyGoLicensesScript() + "\n" +
			`flutter build apk --release --dart-define-from-file=core.env`
	case "appbundle", "aab", "bundle":
		script = envShell(env) + "\n" + licensesLine +
			buildScriptAndroidLibArm64(env) + "\n" +
			buildScriptAndroidLibX86_64(env) + "\n" +
			`flutter build appbundle --release --dart-define-from-file=core.env`
	default:
		fmt.Fprintf(os.Stderr, "godash android: unknown mode %q (expected apk or appbundle)\n", mode)
		os.Exit(1)
	}
	if err := runShellTask("Build Android "+mode, env.Root, script); err != nil {
		os.Exit(1)
	}
}

// runIOS handles `godash ios` (release build).
func runIOS(args []string) {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	if err := runProtoAndWiring(env); err != nil {
		os.Exit(1)
	}
	licensesLine, cleanupLicenses := licensesTplExport()
	defer cleanupLicenses()
	script := envShell(env) + "\n" + licensesLine +
		buildScriptIOSLib(env) + "\n" +
		applyGoLicensesScript() + "\n" +
		`flutter build ios --release --dart-define-from-file=core.env`
	if err := runShellTask("Build iOS", env.Root, script); err != nil {
		os.Exit(1)
	}
}

// runMacos handles `godash macos [build|run]` (default: run).
func runMacos(args []string) {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	mode := "run"
	if len(args) > 0 {
		mode = args[0]
	}
	var script string
	switch mode {
	case "build":
		if err := runProtoAndWiring(env); err != nil {
			os.Exit(1)
		}
		licensesLine, cleanupLicenses := licensesTplExport()
		defer cleanupLicenses()
		script = envShell(env) + "\n" + licensesLine +
			buildScriptMacosLib(env) + "\n" +
			applyGoLicensesScript() + "\n" +
			`flutter build macos --debug --dart-define-from-file=core.env`
		if err := runShellTask("Build macOS", env.Root, script); err != nil {
			os.Exit(1)
		}
	case "run":
		if err := runProtoAndWiring(env); err != nil {
			os.Exit(1)
		}
		script = envShell(env) + "\n" +
			buildScriptMacosLib(env) + "\n" +
			`flutter run -d macos --dart-define-from-file=core.env`
		if err := runShellPipe(env.Root, script); err != nil {
			os.Exit(errExit(err))
		}
	default:
		fmt.Fprintf(os.Stderr, "godash macos: unknown mode %q (expected build or run)\n", mode)
		os.Exit(1)
	}
}

// runLibCmd handles `godash lib <android|ios|macos>`.
func runLibCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: godash lib <android|ios|macos>")
		os.Exit(1)
	}
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	prefix := envShell(env) + "\n" + godashModuleBootstrap() + "\n"
	var script string
	switch args[0] {
	case "android":
		script = prefix + buildScriptAndroidLibArm64(env) + "\n" + buildScriptAndroidLibX86_64(env)
	case "ios":
		script = prefix + buildScriptIOSLib(env)
	case "macos":
		script = prefix + buildScriptMacosLib(env)
	default:
		fmt.Fprintf(os.Stderr, "godash lib: unknown target %q\n", args[0])
		os.Exit(1)
	}
	if err := runShellTask("Build native lib "+args[0], env.Root, script); err != nil {
		os.Exit(1)
	}
}

// errExit extracts an exit code from an exec error, defaulting to 1.
func errExit(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}
