"use strict";

// Fatal reporting channel. A release worker builds with TinyGo -panic=trap,
// whose wasm target cannot recover a panic or a syscall/js exception (see
// WEB_WASM_NOTES.md §2). When that happens the Go main goroutine stops and no
// RPC is ever answered again: the frontend would otherwise hang silently until
// the user reloads. The reportFatal() calls below post a plain string on a
// reserved prefix so the bridge can turn the death into a normal error, fail
// every in-flight RPC, and surface a reload prompt instead of a frozen UI.
const GODASH_FATAL_PREFIX = "__godash_worker_fatal__ ";

function reportFatal(kind, detail) {
    const text = GODASH_FATAL_PREFIX + kind + (detail ? ": " + detail : "");
    try {
        console.error(text);
    } catch (_) { /* ignore */ }
    try {
        postMessage(text);
    } catch (_) { /* ignore */ }
}

// NOTE: self.onerror / self.onunhandledrejection are deliberately NOT hooked
// here. They fire for non-fatal conditions in normal operation (for example a
// relay fetch promise the Go side settles on its own), and treating those as
// worker death would fail unrelated RPCs and stall loading. The two terminal
// signals below are the definitive ones: the Go main goroutine stopping
// (go.run resolves) and an unrecovered exception/trap (go.run rejects).

// Workaround for a WASM crash and a worker-wide deadlock.
//
// The browser rejects WebSocket.close() codes other than 1000 and 3000-4999
// with an InvalidAccessError, but Go WebSocket clients do close with 1006
// (abnormal closure), 1011 (internal error) and others. A syscall/js call that
// throws traps and kills a TinyGo -panic=trap release worker, so close() must
// never throw.
//
// We cannot simply drop such a call either. coder/websocket calls Close() from
// inside its own "error" event listener and then waits, still in that listener,
// for the resulting "close" event. Blocking inside a syscall/js callback freezes
// the worker's event loop, so the "close" event can never be delivered and the
// waiter never returns: one failed relay hangs the whole worker, and no other
// relay or RPC can make progress. Deliver the close event synchronously to
// unblock the Go side, then close for real with an accepted code.
(function () {
    if (typeof WebSocket === "undefined" || !WebSocket.prototype) return;
    const originalClose = WebSocket.prototype.close;
    WebSocket.prototype.close = function (code, reason) {
        if (typeof reason === "string" && reason.length > 123) {
            reason = reason.slice(0, 123);
        }
        if (code === undefined || code === null ||
            code === 1000 || (code >= 3000 && code <= 4999)) {
            return originalClose.call(this, code, reason);
        }
        try {
            this.dispatchEvent(new CloseEvent("close", {
                code: 1006,
                reason: typeof reason === "string" ? reason : "",
                wasClean: false,
            }));
        } catch (e) { /* ignore */ }
        try {
            return originalClose.call(this, 1000, reason);
        } catch (e) {
            return undefined;
        }
    };
})();

// NOTE: WebSocket.prototype.send is deliberately NOT wrapped. Wrapping every
// relay frame on the hot path is not needed to detect a dead worker (the
// go.run signals below cover it) and risks perturbing relay throughput.

const baseUrl = self.location.href.substring(0, self.location.href.lastIndexOf('/') + 1);

globalThis.sqlite3InitModuleState = {
    sqlite3Dir: baseUrl
};

try {
    importScripts(baseUrl + "wasm_exec.js");
    console.log("wasm_exec.js loaded");
} catch (e) {
    console.error("Failed to load wasm_exec.js:", e);
    reportFatal("wasm_exec.js", e && e.message);
    postMessage(undefined);
}

try {
    importScripts(baseUrl + "sqlite3.js");
    console.log("sqlite3.js loaded");
} catch (e) {
    console.error("Failed to load sqlite3.js:", e);
    reportFatal("sqlite3.js", e && e.message);
    postMessage(undefined);
}

console.log("worker.js loaded");

if (WebAssembly == null || WebAssembly == undefined) {
    console.error("WebAssembly is not supported");
    reportFatal("no-webassembly");
    postMessage(undefined);
}

if (!WebAssembly.instantiateStreaming) {
    WebAssembly.instantiateStreaming = async (resp, importObject) => {
        const source = await (await resp).arrayBuffer().catch((e) => {
            console.error(e);
            reportFatal("arrayBuffer", e && e.message);
            postMessage(undefined);
        });
        return await WebAssembly.instantiate(source, importObject).catch((e) => {
            console.error(e);
            reportFatal("instantiate", e && e.message);
            postMessage(undefined);
        });
    };
}

(async () => {
    const go = new self.Go();
    const asset = "worker.wasm";
    const { instance } = await WebAssembly.instantiateStreaming(fetch(asset), go.importObject).catch((e) => {
        console.error(e);
        reportFatal("instantiateStreaming", e && e.message);
        postMessage(undefined);
    });
    await go.run(instance).catch((e) => {
        console.error(e);
        reportFatal("go.run threw", e && e.message);
        postMessage(undefined);
    });
    // The Go program only resolves go.run when its main goroutine stops, which
    // here means an unrecovered panic triggered runtime.wasmExit (standard Go)
    // or the wasm trapped (TinyGo -panic=trap). The worker can no longer serve
    // RPCs, so make the death loud instead of silent.
    reportFatal("go-exited", "Go main stopped; see the panic/fatal stack above");
})();
