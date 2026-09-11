package main

import (
	"fmt"
	"os"
)

// usage is printed for `godash help` and on unknown subcommands.
const usage = `godash %s

Usage:
  godash [new]                       Scaffold a new project from a template (default)
  godash prepare                     Initialize / regenerate project artifacts
  godash proto                       Regenerate Go and Dart protobuf code
  godash dart-api                    Update Dart C API headers
  godash web [build|run]             Build or run the Web target
  godash android [apk|appbundle]     Build an Android target
  godash ios                         Build the iOS target
  godash macos [build|run]           Build or run the macOS target
  godash lib <android|ios|macos>     Build a single Go native library
  godash clean                       Remove build artifacts
  godash reset                       Remove generated platform directories
  godash upgrade                     Pull latest godash and regenerate derived files
  godash doctor                      Check required tools
  godash help                        Show this help

Run "godash help <subcommand>" for details on a specific subcommand.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		runScaffold(os.Args[1:])
		return
	}
	switch args[0] {
	case "new":
		runScaffold(args[1:])
	case "prepare":
		runPrepare()
	case "proto":
		runProto()
	case "dart-api":
		runDartAPI()
	case "web":
		runWeb(args[1:])
	case "android":
		runAndroid(args[1:])
	case "ios":
		runIOS(args[1:])
	case "macos":
		runMacos(args[1:])
	case "lib":
		runLibCmd(args[1:])
	case "clean":
		runClean()
	case "reset":
		runReset()
	case "upgrade":
		runUpgrade(args[1:])
	case "doctor":
		runDoctorCmd()
	case "help", "-h", "--help":
		printHelp(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "godash: unknown command %q\n\n", args[0])
		printHelp(nil)
		os.Exit(1)
	}
}

func printHelp(args []string) {
	if len(args) > 0 {
		if h, ok := helpText[args[0]]; ok {
			fmt.Printf("godash %s - %s\n\n%s\n", args[0], h.summary, h.body)
			return
		}
		fmt.Fprintf(os.Stderr, "godash: no help for %q\n", args[0])
	}
	fmt.Printf(usage, Version)
}

type helpEntry struct {
	summary string
	body    string
}

var helpText = map[string]helpEntry{
	"new": {
		summary: "Scaffold a new project from a template",
		body: `Interactively prompts for app name, directory, and bundle ID, then
clones the godash starter template and runs the initial prepare steps.

Override the template source with the GODASH_TEMPLATE environment variable
(local path or remote Git URL). FLAP_TEMPLATE is also accepted as a
deprecated alias.`,
	},
	"prepare": {
		summary: "Initialize / regenerate project artifacts",
		body: `Runs:
  - dart pub global activate protoc_plugin
  - go mod tidy / download / install tool
  - protobuf generation (Go + Dart)
  - go-licenses → lib/licenses/licenses.g.dart
  - flutter create for any missing platform (web/android/ios/macos)
  - copies platform_templates/web into web/

Idempotent: safe to re-run.`,
	},
	"web": {
		summary: "Build or run the Web target",
		body: `Usage:
  godash web build   # flutter build web --wasm --release
  godash web run     # flutter run -d web-server (dev, full Go wasm)
  godash web         # alias of "godash web build"`,
	},
	"android": {
		summary: "Build an Android target",
		body: `Usage:
  godash android apk        # release APK
  godash android appbundle  # release AAB
  godash android            # alias of "godash android apk"

Requires the Android SDK and NDK (NDK_PATH is read from custom.mk).`,
	},
	"ios": {
		summary: "Build the iOS target",
		body: `Builds the iOS Runner.app in release mode.
Requires Xcode and a configured signing team.`,
	},
	"macos": {
		summary: "Build or run the macOS target",
		body: `Usage:
  godash macos build  # flutter build macos --debug
  godash macos run    # flutter run -d macos
  godash macos        # alias of "godash macos run"`,
	},
	"lib": {
		summary: "Build a single Go native library",
		body: `Usage:
  godash lib android   # build both arm64-v8a and x86_64 .so files
  godash lib ios       # build ios-arm64 .a + xcframework
  godash lib macos     # build macos-arm64 .a + xcframework`,
	},
	"clean": {
		summary: "Remove build artifacts",
		body: `Removes generated Go/Dart proto code, WASM artifacts, native library
build outputs, and licenses file. Runs flutter clean.`,
	},
	"reset": {
		summary: "Remove generated platform directories",
		body: `Same as clean, plus removes web/, android/, ios/, macos/. Useful
to regenerate platform projects from scratch.`,
	},
	"upgrade": {
		summary: "Pull the latest godash source and regenerate derived files",
		body: `The template no longer owns godash files; everything godash-owned
(main*.go, web assets, native bridge plugin, …) is generated or
extracted from the godash package on every build. So "upgrade" means:
refresh the godash dependency, then re-run prepare.

Behaviour by dependency type (detected from pubspec.yaml):
  - "path: ../godash"  runs "git pull" in the godash dir, then regenerates
  - "godash: ^1.2.0"   runs "flutter pub upgrade godash native_internal",
                       then "go get -u github.com/nosuta/godash",
                       then regenerates

After upgrading, the regenerated files are committed automatically if
the project is a git repository.`,
	},
	"doctor": {
		summary: "Check required tools",
		body: `Verifies that go, flutter, dart, git, protoc, npm, perl, tinygo and
Chrome are installed.`,
	},
}
