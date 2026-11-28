// Guard against downgrading the web SQLite driver. v0.5.0 is the first
// go-wasmsqlite release that reports failed statements as Go errors without
// recover(); older releases trap the TinyGo -panic:trap release worker on the
// first missing table / constraint violation / stray ROLLBACK. v0.5.1 made the
// SQLite JS module shared per Worker, so reopening the database -- a retry
// after a failed open -- no longer dies inside the module loader. The
// behavioural contract is covered by the browser harness (sqlite/webtest);
// this test makes the versions that provide it part of `go test ./...`.

//go:build !js

package sqlite

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestGoWasmSQLiteDriverVersion(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}",
		"github.com/nosuta/go-wasmsqlite").Output()
	if err != nil {
		t.Fatalf("go list -m go-wasmsqlite: %v", err)
	}
	version := strings.TrimSpace(string(out))
	got, ok := parseSemver(version)
	if !ok {
		t.Fatalf("unexpected go-wasmsqlite version %q", version)
	}
	want := [...]int{0, 5, 1}
	if compareSemver(got, want) < 0 {
		t.Fatalf("go-wasmsqlite %q is too old: need >= v0.5.0 so failed SQL "+
			"statements do not trap the TinyGo -panic=trap worker", version)
	}
}

// parseSemver accepts the vMAJOR.MINOR.PATCH form used by Go module versions
// and ignores any pre-release suffix; module versions here are all releases.
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	if !strings.HasPrefix(v, "v") {
		return out, false
	}
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if i == 2 {
			// Drop a pre-release suffix such as "-rc.1" from the patch field.
			p = strings.SplitN(p, "-", 2)[0]
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func compareSemver(a, b [3]int) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
