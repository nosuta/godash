//go:build tinygo

package fetch

import (
	"context"
	"fmt"
	"syscall/js"
)

// uint8Array is the Uint8Array constructor. Wrapping the fetched ArrayBuffer in
// one lets js.CopyBytesToGo copy raw bytes into Go memory.
var uint8Array = js.Global().Get("Uint8Array")

// Fetch performs a GET with the browser Fetch API and returns the response body
// as raw bytes.
//
// The TinyGo release build has no fetch-backed net/http Transport, so this is
// the worker's network primitive. The body must be read as an ArrayBuffer, not
// with Response.text(): text() decodes the payload as UTF-8 and corrupts binary
// blobs (images, video, AES-GCM ciphertext), so every media fetch returned
// mangled bytes on the release build even though the standard-Go dev build
// (net/http) was correct.
func Fetch(ctx context.Context, url string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	opts := js.Global().Get("Object").New()
	opts.Set("method", "GET")
	// Explicit and matching the standard-Go transport: a cross-origin media
	// request is a CORS request (mode defaults to "cors"), which the page's
	// COEP require-corp permits as long as the host answers with CORS headers.
	opts.Set("credentials", "same-origin")
	abort := js.Global().Get("AbortController")
	if !abort.IsUndefined() {
		abort = abort.New()
		opts.Set("signal", abort.Get("signal"))
	}

	bodyCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	var (
		success, failure         js.Func
		bodySuccess, bodyFailure js.Func
	)

	success = js.FuncOf(func(this js.Value, args []js.Value) any {
		success.Release()
		failure.Release()

		resp := args[0]
		status := resp.Get("status").Int()
		bodySuccess = js.FuncOf(func(this js.Value, args []js.Value) any {
			bodySuccess.Release()
			bodyFailure.Release()

			view := uint8Array.New(args[0])
			body := make([]byte, view.Get("byteLength").Int())
			js.CopyBytesToGo(body, view)
			if status != 200 && status != 204 {
				errCh <- fmt.Errorf("fetch failed: %d", status)
				return nil
			}
			bodyCh <- body
			return nil
		})
		bodyFailure = js.FuncOf(func(this js.Value, args []js.Value) any {
			bodySuccess.Release()
			bodyFailure.Release()
			errCh <- fmt.Errorf("fetch body: %s", jsError(args[0]))
			return nil
		})
		resp.Call("arrayBuffer").Call("then", bodySuccess, bodyFailure)
		return nil
	})
	failure = js.FuncOf(func(this js.Value, args []js.Value) any {
		success.Release()
		failure.Release()
		errCh <- fmt.Errorf("fetch: %s", jsError(args[0]))
		return nil
	})
	js.Global().Call("fetch", url, opts).Call("then", success, failure)

	select {
	case body := <-bodyCh:
		return body, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		if !abort.IsUndefined() {
			abort.Call("abort")
		}
		return nil, ctx.Err()
	}
}

// jsError renders a thrown JS value (usually an Error) as a message, appending
// its cause when present.
func jsError(v js.Value) string {
	if v.Get("toString").IsUndefined() {
		return v.String()
	}
	msg := v.Call("toString").String()
	if cause := v.Get("cause"); !cause.IsUndefined() && !cause.Get("toString").IsUndefined() {
		msg += ": " + cause.Call("toString").String()
	}
	return msg
}
