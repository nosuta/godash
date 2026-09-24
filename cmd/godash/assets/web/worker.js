"use strict";

// Workaround for a TinyGo release-build crash. Release workers are built with
// TinyGo -panic=trap, which cannot recover JS exceptions; a throw from a
// syscall/js call therefore traps and kills the worker. A Go WebSocket client
// may close a broken connection with status 1006 (abnormal closure), which the
// browser's WebSocket.close() rejects with InvalidAccessError. Do NOT rewrite
// the code (that would actually close a socket the caller meant to leave
// alone, breaking reconnects): swallow the invalid call entirely. The browser
// tears the socket down through its own error/close event, which is exactly
// what happens when the exception propagates on a non-trapping build.
(function () {
    if (typeof WebSocket === "undefined" || !WebSocket.prototype) return;
    const originalClose = WebSocket.prototype.close;
    WebSocket.prototype.close = function (code, reason) {
        if (code !== undefined && code !== null &&
            code !== 1000 && !(code >= 3000 && code <= 4999)) {
            return;
        }
        if (typeof reason === "string" && reason.length > 123) {
            reason = reason.slice(0, 123);
        }
        return originalClose.call(this, code, reason);
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
