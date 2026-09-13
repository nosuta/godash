package rpc

import (
	"context"
	"fmt"

	godashapp "godashapp/pb"
)

// CounterServer implements CounterRPCHandler. It persists a single integer in
// SQLite (opened in EntryPoint), so the value survives restarts.
type CounterServer struct{}

func (s *CounterServer) Increment(ctx context.Context, req *godashapp.IncrementRequest) (*godashapp.CounterResponse, error) {
	delta := req.GetDelta()
	if delta == 0 {
		delta = 1
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if err := ensureDB(); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO counters (id, value) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET value = value + excluded.value`, delta); err != nil {
		return nil, fmt.Errorf("increment counter: %w", err)
	}
	return readCounter(ctx)
}

func (s *CounterServer) Get(ctx context.Context, _ *godashapp.GetCounterRequest) (*godashapp.CounterResponse, error) {
	dbMu.Lock()
	defer dbMu.Unlock()
	if err := ensureDB(); err != nil {
		return nil, err
	}
	return readCounter(ctx)
}

// readCounter loads the stored value, defaulting to 0 when no row exists yet.
// Callers must hold dbMu.
func readCounter(ctx context.Context) (*godashapp.CounterResponse, error) {
	var value int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM counters WHERE id = 1), 0)`).Scan(&value); err != nil {
		return nil, fmt.Errorf("read counter: %w", err)
	}
	return &godashapp.CounterResponse{Value: value}, nil
}
