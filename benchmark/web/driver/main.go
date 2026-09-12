// Web latency benchmark driver.
//
// Serves the static benchmark page (benchmark/web) and drives it in headless
// Chrome via chromedp, then prints the collected latency stats as JSON.
//
// Build the web worker and fetch wasm_exec.js first (run from the repository
// root):
//
//	GOOS=js GOARCH=wasm go build -o benchmark/web/worker.wasm ./benchmark/web/worker
//	cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" benchmark/web/
//	go run ./benchmark/web/driver --n 2000
//	go run ./benchmark/web/driver --n 2000 --mode stream   # envelope streaming
//	go run ./benchmark/web/driver --n 2000 --mode sab      # SharedArrayBuffer ring (P6)
//
// ignore: no comments needed
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
)

// crossOriginIsolated wraps h with the COOP/COEP headers required to expose
// SharedArrayBuffer to the benchmark page (PLAN.md P6).
func crossOriginIsolated(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		h.ServeHTTP(w, r)
	})
}

func main() {
	dir := flag.String("dir", ".", "directory with index.html, bench.js, worker.js, worker.wasm, wasm_exec.js")
	n := flag.Int("n", 2000, "timed iterations")
	warmup := flag.Int("warmup", 200, "warmup iterations")
	payload := flag.Int("payload", 64, "payload size in bytes")
	mode := flag.String("mode", "unary", "measured path: unary | stream | sab")
	count := flag.Int("count", 4, "frames per stream for stream|sab modes")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall timeout")
	flag.Parse()

	if err := run(*dir, *n, *warmup, *payload, *mode, *count, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir string, n, warmup, payload int, mode string, count int, timeout time.Duration) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, f := range []string{"index.html", "bench.js", "worker.js", "worker.wasm", "wasm_exec.js"} {
		if _, err := os.Stat(filepath.Join(abs, f)); err != nil {
			return fmt.Errorf("missing %s in %s (see file header for build steps): %w", f, abs, err)
		}
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	// Cross-origin isolation makes SharedArrayBuffer available for the opt-in
	// shared-memory path (PLAN.md P6). It is required even on localhost.
	mux.Handle("/", crossOriginIsolated(http.FileServer(http.Dir(abs))))
	go func() { _ = http.Serve(l, mux) }()
	url := fmt.Sprintf(
		"http://127.0.0.1:%d/?n=%d&warmup=%d&payload=%d&mode=%s&count=%d",
		l.Addr().(*net.TCPAddr).Port, n, warmup, payload, mode, count,
	)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, chromedp.DefaultExecAllocatorOptions[:]...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	var raw string
	err = chromedp.Run(browserCtx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#doneButton`, chromedp.ByID),
		chromedp.Evaluate(`JSON.stringify(window.__benchResults)`, &raw),
	)
	if err != nil {
		return err
	}

	var results map[string]any
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return fmt.Errorf("parse results %q: %w", raw, err)
	}
	if errMsg, ok := results["error"]; ok {
		return fmt.Errorf("page reported error: %v", errMsg)
	}

	out, err := json.Marshal(results)
	if err != nil {
		return err
	}
	log.Printf("web results: %s", out)
	fmt.Println(string(out))
	return nil
}
