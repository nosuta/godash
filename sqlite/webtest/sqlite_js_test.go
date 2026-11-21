// Regression coverage for the web (OPFS) SQLite driver. A failed statement
// must surface as a Go error and leave the connection usable: the TinyGo
// release worker is built with -panic=trap, so a trapped SQLite exception
// would kill the whole worker and hang every in-flight RPC. This test runs in
// the browser harness (`GOOS=js GOARCH=wasm go test -exec go_js_wasm_exec
// ./sqlite/webtest`), where the OPFS VFS is available.
//
// This lives in its own package on purpose: sqlite3InitModule() consumes a
// global init state and can only run once per worker, so a second sql.Open in
// the same wasm binary fails. A separate package gets its own test binary and
// therefore its own worker, leaving sqlite/sqlite_test.go untouched.

//go:build js

package webtest

import (
	"strings"
	"testing"

	"github.com/nosuta/godash/v2/sqlite"
)

func TestWebSQLiteErrorsAreReturned(t *testing.T) {
	db, err := sqlite.Open("godash-sqlite-error-regression.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// OPFS supports a single connection; database/sql must not open a second.
	db.SetMaxOpenConns(1)

	const table = "godash_sqlite_error_regression"
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS " + table +
		" (id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO "+table+" (id, name) VALUES (?, ?)", 1, "first"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// These are ordinary SQLite failures, not programmer bugs in the driver.
	// Each must come back as a Go error. Two go through the prepare/bind/step
	// path (parameterised INSERT, QueryRow), one through the OO exec path.
	if _, err := db.Exec("DELETE FROM godash_no_such_table"); err == nil {
		t.Error("DELETE from a missing table should fail")
	}
	if _, err := db.Exec("INSERT INTO "+table+" (id, name) VALUES (?, ?)", 2, "first"); err == nil {
		t.Error("duplicate UNIQUE value should fail")
	}
	if _, err := db.Exec("ROLLBACK"); err == nil {
		t.Error("ROLLBACK outside a transaction should fail")
	}

	var name string
	err = db.QueryRow("SELECT name FROM godash_no_such_table").Scan(&name)
	if err == nil {
		t.Error("SELECT from a missing table should fail")
	} else if !strings.Contains(err.Error(), "no such table") {
		t.Errorf("SELECT from a missing table: got %q, want a \"no such table\" error", err)
	}

	// The module must still be alive and serving queries after all failures.
	if err := db.QueryRow("SELECT name FROM "+table+" WHERE id = ?", 1).Scan(&name); err != nil {
		t.Fatalf("connection unusable after SQLite errors: %v", err)
	}
	if name != "first" {
		t.Fatalf("got name %q, want %q", name, "first")
	}
}
