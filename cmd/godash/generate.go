package main

import (
	"fmt"
	"os"
)

// runProto handles `godash proto`.
func runProto() {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	script := envShell(env) + "\n" + protoGoScript() + "\n" + protoDartScript()
	if err := runShellTask("Generate protobuf code", env.Root, script); err != nil {
		os.Exit(1)
	}
	fmt.Println()
	fmt.Printf("%s✓%s Protobuf generated\n", colorGreen, colorReset)
}

// runDartAPI handles `godash dart-api`.
func runDartAPI() {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	script := envShell(env) + "\n" + dartAPIScript()
	if err := runShellTask("Update Dart C API headers", env.Root, script); err != nil {
		os.Exit(1)
	}
	fmt.Println()
	fmt.Printf("%s✓%s Dart C API updated\n", colorGreen, colorReset)
}
