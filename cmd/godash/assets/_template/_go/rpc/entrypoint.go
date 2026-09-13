// EntryPoint and Close are owned by the application. godash creates a no-op
// default only when this file is missing, so edit it freely.

package rpc

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/nosuta/godash/v2/sqlite"
)

// db is the application database, opened once in EntryPoint and shared by the
// service handlers. dbMu serialises access because the web (OPFS) driver keeps
// a single SQLite connection.
var (
	db   *sql.DB
	dbMu sync.Mutex
)

// EntryPoint is called once when the native library / web worker starts. It
// receives the per-platform database path and the app encryption key.
func EntryPoint(databasePath, appEncryptionKey string) error {
	_ = appEncryptionKey
	conn, err := sqlite.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	// The web (OPFS) driver only supports one connection; keep the pool small
	// so database/sql does not open a second OPFS handle.
	conn.SetMaxOpenConns(1)

	// Create the demo schema. Keep each DDL statement in its own Exec: the web
	// SQLite driver compiles only the first statement of a multi-statement Exec.
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS counters (
  id    INTEGER PRIMARY KEY CHECK (id = 1),
  value INTEGER NOT NULL
)`); err != nil {
		_ = conn.Close()
		return fmt.Errorf("init database: %w", err)
	}
	db = conn
	return nil
}

// Close is called when the runtime shuts down. Release any resources held by
// your app here.
func Close() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		_ = db.Close()
		db = nil
	}
}

// ensureDB reports whether EntryPoint has opened the database. Callers must
// hold dbMu.
func ensureDB() error {
	if db == nil {
		return fmt.Errorf("database not initialized")
	}
	return nil
}
