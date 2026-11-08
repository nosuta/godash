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
		export("GO_MODULE", e.GoModule),
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

// splitPassthrough returns the arguments Flutter should receive. A literal
// "--" ends godash's own flags, so `godash web run -- --web-port 9999` is a
// future-proof spelling; without it every argument after the mode is forwarded.
func splitPassthrough(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return args
}

// forwardArgs shell-quotes every argument for the underlying `flutter
// build`/`run`, so a project can pass any Flutter flag through godash, e.g.
// `godash web run --web-port 9999 --web-hostname 0.0.0.0`. Quoting each argv
// element keeps values with spaces or quotes intact because the generated
// script is run by a shell.
func forwardArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return " " + strings.Join(quoted, " ")
}

// rejectDeviceArgs refuses -d/--device-id for `godash web run`: the web flow
// needs -d web-server for the COOP/COEP headers and the Go wasm worker, and
// Flutter's -d is a multi-option, so a second device would start a second
// browser target.
func rejectDeviceArgs(args []string) error {
	for _, arg := range args {
		switch {
		case arg == "-d" || arg == "--device-id":
			return fmt.Errorf("the web target always uses -d web-server; remove %s", arg)
		case strings.HasPrefix(arg, "-d=") || strings.HasPrefix(arg, "--device-id="):
			return fmt.Errorf("the web target always uses -d web-server; remove %s", arg)
		}
	}
	return nil
}

// runWeb handles `godash web [build|run]` (default: build). Every argument after
// the mode is forwarded to Flutter; see forwardArgs.
func runWeb(args []string) {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	mode := "build"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode = args[0]
		rest = args[1:]
	}
	flutterArgs := splitPassthrough(rest)
	forwarded := forwardArgs(flutterArgs)
	if mode == "run" {
		if err := rejectDeviceArgs(flutterArgs); err != nil {
			fatalf("godash web run: %v", err)
		}
	}
	_ = ensureWebAssets(env.Root)
	if err := runProtoAndWiring(env); err != nil {
		os.Exit(1)
	}
	switch mode {
	case "build":
		licensesLine, cleanupLicenses := licensesTplExport()
		defer cleanupLicenses()
		// Proto + wiring are already done by runProtoAndWiring; webBuildShell
		// adds the sqlite assets, the TinyGo worker, licenses and the bundle.
		script := envShell(env) + "\n" + licensesLine + webBuildShell(forwarded)
		if err := runShellTask("Build for web", env.Root, script); err != nil {
			os.Exit(1)
		}
	case "run":
		script := envShell(env) + "\n" + webRunShell(forwarded)
		if err := runShellPipe(env.Root, script); err != nil {
			fmt.Fprintf(os.Stderr, "web run exited with code %d\n", errExit(err))
			os.Exit(errExit(err))
		}
	default:
		fmt.Fprintf(os.Stderr, "godash web: unknown mode %q (expected build or run)\n", mode)
		os.Exit(1)
	}
}

// androidOptions is the parsed form of the `godash android` arguments.
type androidOptions struct {
	mode      string   // apk | appbundle
	split     bool     // --split-per-abi
	platforms []string // flutter --target-platform values, empty = default
}

// supportedAndroidPlatforms are the ABIs godash can build a Go shared library
// for. Flutter also knows android-arm, but godash has no 32-bit ARM library.
var supportedAndroidPlatforms = []string{"android-arm64", "android-x64"}

// parseAndroidArgs parses `godash android [apk|appbundle] [flags]`. Flags may
// come before or after the mode, and --target-platform takes a comma-separated
// list either as the next argument or after "=".
func parseAndroidArgs(args []string) (androidOptions, error) {
	opts := androidOptions{mode: "apk"}
	modeSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--split-per-abi":
			opts.split = true
		case arg == "--target-platform" || strings.HasPrefix(arg, "--target-platform="):
			raw := strings.TrimPrefix(arg, "--target-platform=")
			if raw == arg { // no "=" form: consume the next argument
				if i+1 >= len(args) {
					return opts, fmt.Errorf("--target-platform needs a comma-separated value")
				}
				i++
				raw = args[i]
			}
			platforms, err := parseAndroidPlatforms(raw)
			if err != nil {
				return opts, err
			}
			opts.platforms = platforms
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unknown flag %q", arg)
		default:
			if modeSet {
				return opts, fmt.Errorf("unexpected argument %q", arg)
			}
			modeSet = true
			opts.mode = arg
		}
	}
	switch opts.mode {
	case "apk":
	case "appbundle", "aab", "bundle":
		opts.mode = "appbundle"
		if opts.split {
			return opts, fmt.Errorf("--split-per-abi is only supported for apk builds")
		}
	default:
		return opts, fmt.Errorf("unknown mode %q (expected apk or appbundle)", opts.mode)
	}
	// Flutter's split default includes 32-bit ARM, which godash cannot build.
	if opts.split && len(opts.platforms) == 0 {
		opts.platforms = append([]string(nil), supportedAndroidPlatforms...)
	}
	return opts, nil
}

// parseAndroidPlatforms validates and normalises a Flutter --target-platform
// list to the ABIs godash supports.
func parseAndroidPlatforms(raw string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		platform := strings.TrimSpace(part)
		if platform == "" {
			continue
		}
		switch platform {
		case "android-arm64", "android-x64":
		case "android-arm":
			return nil, fmt.Errorf("android-arm is not supported: godash builds native libraries for arm64-v8a and x86_64 only")
		default:
			return nil, fmt.Errorf("unsupported target platform %q (expected android-arm64 or android-x64)", platform)
		}
		if !seen[platform] {
			seen[platform] = true
			out = append(out, platform)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--target-platform needs at least one of android-arm64, android-x64")
	}
	return out, nil
}

// androidLibScripts builds the Go shared libraries for the requested Flutter
// target platforms. An empty list means every ABI godash supports.
func androidLibScripts(e *projectEnv, platforms []string) string {
	if len(platforms) == 0 {
		return buildScriptAndroidLibArm64(e) + "\n" + buildScriptAndroidLibX86_64(e)
	}
	scripts := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		switch platform {
		case "android-arm64":
			scripts = append(scripts, buildScriptAndroidLibArm64(e))
		case "android-x64":
			scripts = append(scripts, buildScriptAndroidLibX86_64(e))
		}
	}
	return strings.Join(scripts, "\n")
}

// androidFlutterCommand is the `flutter build` invocation for the parsed flags.
func androidFlutterCommand(opts androidOptions) string {
	cmd := "flutter build " + opts.mode +
		" --release --dart-define-from-file=core.env"
	if opts.split {
		cmd += " --split-per-abi"
	}
	if len(opts.platforms) > 0 {
		cmd += " --target-platform " + strings.Join(opts.platforms, ",")
	}
	return cmd
}

// buildVersionPrelude regenerates lib/version/version.dart before a release
// build. `flutter build` neither runs the generator nor reads the git tag, so
// without this the app reports the previous tag in Settings and the MCP
// handshake until the next web build happens to refresh the file.
func buildVersionPrelude() string {
	return updateGoBuildVersionScript() + "\n"
}

// androidBuildScript is the full shell for an Android release build.
func androidBuildScript(e *projectEnv, licensesLine string, opts androidOptions) string {
	return envShell(e) + "\n" + licensesLine +
		buildVersionPrelude() +
		androidLibScripts(e, opts.platforms) + "\n" +
		applyGoLicensesScript() + "\n" +
		androidFlutterCommand(opts)
}

// iosBuildScript is the full shell for an iOS release build.
func iosBuildScript(e *projectEnv, licensesLine string) string {
	return envShell(e) + "\n" + licensesLine +
		buildVersionPrelude() +
		buildScriptIOSLib(e) + "\n" +
		applyGoLicensesScript() + "\n" +
		`flutter build ios --release --dart-define-from-file=core.env`
}

// macosBuildScript is the full shell for a macOS build.
func macosBuildScript(e *projectEnv, licensesLine string) string {
	return envShell(e) + "\n" + licensesLine +
		buildVersionPrelude() +
		buildScriptMacosLib(e) + "\n" +
		applyGoLicensesScript() + "\n" +
		`flutter build macos --debug --dart-define-from-file=core.env`
}

// runAndroid handles `godash android [apk|appbundle]` (default: apk).
func runAndroid(args []string) {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	opts, err := parseAndroidArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "godash android: %v\n", err)
		os.Exit(1)
	}
	if err := runProtoAndWiring(env); err != nil {
		os.Exit(1)
	}
	licensesLine, cleanupLicenses := licensesTplExport()
	defer cleanupLicenses()
	script := androidBuildScript(env, licensesLine, opts)
	if err := runShellTask("Build Android "+opts.mode, env.Root, script); err != nil {
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
	script := iosBuildScript(env, licensesLine)
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
		script = macosBuildScript(env, licensesLine)
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
