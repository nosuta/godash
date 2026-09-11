package main

import (
	"fmt"
	"os"
)

// runPrepare handles `godash prepare`.
func runPrepare() {
	env, err := loadProjectEnv("")
	if err != nil {
		fatalf("%v", err)
	}
	licensesLine, cleanupLicenses := licensesTplExport()
	defer cleanupLicenses()
	_ = ensureWebAssets(env.Root)
	if err := runProtoAndWiring(env); err != nil {
		os.Exit(1)
	}
	rest := licensesLine + applyGoLicensesScript() + "\n" + flutterCreateBlocks(true)
	if err := runShellTask("Prepare environment (flutter create, licenses)", env.Root, envShell(env)+"\n"+rest); err != nil {
		os.Exit(1)
	}
	fmt.Println()
	fmt.Printf("%s✓%s Project prepared\n", colorGreen, colorReset)
}
