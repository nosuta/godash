//go:build js

// Web benchmark worker: the same web worker entry point used by the real
// bridge (web.RunWebWorker), wired to the benchmark echo dispatch. Build with:
//
//	GOOS=js GOARCH=wasm go build -o benchmark/web/worker.wasm ./benchmark/web/worker
package main

import (
	"log/slog"

	"github.com/nosuta/godash/v2/benchmark/bench"
	"github.com/nosuta/godash/v2/web"
)

func init() {
	bench.Install()
}

func main() {
	// Suppress per-request info logging so it does not distort the numbers.
	slog.SetLogLoggerLevel(slog.LevelError)
	web.RunWebWorker()
}
