package main

import "strings"

// Version is set at build time via -ldflags "-X main.Version=vX.Y.Z" (the
// release workflow does this for the standalone binaries). When it is left at
// its default, the installed Go module version is used instead (see
// cliVersion), so `go install ...@vX.Y.Z` reports the tag.
var Version = "latest"

// versionString returns the version to display: an explicit -ldflags override,
// else the installed module version (the release tag, e.g. "v2.2.10"), else
// "latest" for unversioned local builds.
func versionString() string {
	if v := strings.TrimSpace(Version); v != "" && v != "latest" {
		return v
	}
	if v := cliVersion(); v != "" {
		return v
	}
	return "latest"
}
