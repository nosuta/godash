"use strict";

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

const baseUrl = self.location.href.substring(0, self.location.href.lastIndexOf('/') + 1);

globalThis.sqlite3InitModuleState = {
    sqlite3Dir: baseUrl
};

try {
    importScripts(baseUrl + "wasm_exec.js");
    console.log("wasm_exec.js loaded");
} catch (e) {
    console.error("Failed to load wasm_exec.js:", e);
    postMessage(undefined);
}

try {
    importScripts(baseUrl + "sqlite3.js");
    console.log("sqlite3.js loaded");
} catch (e) {
    console.error("Failed to load sqlite3.js:", e);
    postMessage(undefined);
}

console.log("worker.js loaded");

if (WebAssembly == null || WebAssembly == undefined) {
    console.error("WebAssembly is not supported");
    postMessage(undefined);
}

if (!WebAssembly.instantiateStreaming) {
    WebAssembly.instantiateStreaming = async (resp, importObject) => {
        const source = await (await resp).arrayBuffer().catch((e) => {
            console.error(e);
            postMessage(undefined);
        });
        return await WebAssembly.instantiate(source, importObject).catch((e) => {
            console.error(e);
            postMessage(undefined);
        });
    };
}

(async () => {
    const go = new self.Go();
    const asset = "worker.wasm";
    const { instance } = await WebAssembly.instantiateStreaming(fetch(asset), go.importObject).catch((e) => {
        console.error(e);
        postMessage(undefined);
    });
    await go.run(instance).catch((e) => {
        console.error(e);
        postMessage(undefined);
    });
})();
