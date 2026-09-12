// Web latency benchmark page script.
//
// Drives the godash web worker exactly like lib/bridge/bridge_web.dart:
// - the worker posts a global Done once it is up,
// - each call uses a fresh MessageChannel and posts [port2, view] with
//   transfer list [port2, ab],
// - the response arrives on port1 as a Uint8Array envelope, ending with Done.
//
// `mode` selects the measured path:
// - unary  (default): one request/one response per iteration (envelope),
// - stream          : one streaming request emitting `count` frames (envelope),
// - sab             : one streaming request emitting `count` frames over a
//                     SharedArrayBuffer ring (PLAN.md P6). Requires the page to
//                     be cross-origin isolated; the driver sets COOP/COEP.
//
// Results are exposed as window.__benchResults (JSON) and rendered into
// #results; the chromedp driver (benchmark/web/driver) reads them.
const params = new URLSearchParams(location.search);
const N = Number(params.get('n') ?? 2000);
const WARMUP = Number(params.get('warmup') ?? 200);
const PAYLOAD_SIZE = Number(params.get('payload') ?? 64);
const MODE = params.get('mode') ?? 'unary';
const COUNT = Number(params.get('count') ?? 4);
const ECHO_PATH = '/bench.EchoService/Echo';
const GATED_STREAM_PATH = '/bench.EchoService/GatedStream';

function makePayload(size) {
  const b = new Uint8Array(size);
  for (let i = 0; i < size; i++) {
    b[i] = i & 0x7F;
  }
  return b;
}

// Envelope request marshaling: this harness hand-encodes the two fields of
// RpcRequest (path=1 string, payload=2 bytes) and Request (rpc_request=10
// message, port=5 varint) to stay dependency-free. Field order follows the
// proto definitions in proto/core.proto.
function encodeVarint(value) {
  const out = [];
  let v = value;
  while (v > 0x7F) {
    out.push((v & 0x7F) | 0x80);
    v = Math.floor(v / 128);
  }
  out.push(v & 0x7F);
  return out;
}

function encodeString(fieldNumber, s) {
  const bytes = Array.from(new TextEncoder().encode(s));
  return [...encodeVarint((fieldNumber << 3) | 2), ...encodeVarint(bytes.length), ...bytes];
}

function encodeBytes(fieldNumber, bytes) {
  return [...encodeVarint((fieldNumber << 3) | 2), ...encodeVarint(bytes.length), ...bytes];
}

function encodeRpcRequest(port, path, payload) {
  const rpcRequest = [...encodeString(1, path), ...encodeBytes(2, payload)];
  return new Uint8Array([
    ...encodeVarint((10 << 3) | 2), ...encodeVarint(rpcRequest.length), ...rpcRequest,
    ...encodeVarint((5 << 3) | 0), ...encodeVarint(port),
  ]);
}

function encodeEchoRequest(port, payload) {
  return encodeRpcRequest(port, ECHO_PATH, payload);
}

// rpcOnce mirrors Bridge.rpcUnsafe: fresh MessageChannel per call, resolve on
// the first response envelope, ignore the trailing Done.
function rpcOnce(worker, requestBytes) {
  return new Promise((resolve, reject) => {
    const ch = new MessageChannel();
    const responses = [];
    ch.port1.onmessage = (message) => {
      const data = message.data;
      if (!data) {
        reject(new Error('rpc response data is null'));
        return;
      }
      responses.push(data);
      // Resolve on the first message; the worker then posts Done which we
      // simply let arrive before closing the ports below.
      resolve(responses[0]);
      setTimeout(() => {
        ch.port2.close();
        ch.port1.close();
      }, 0);
    };
    const ab = new ArrayBuffer(requestBytes.length);
    const view = new Uint8Array(ab);
    view.set(requestBytes);
    worker.postMessage([ch.port2, view], [ch.port2, ab]);
  });
}

// streamOnce mirrors Bridge.rpcStream over the transferable envelope: the
// worker posts one Uint8Array per frame and finishes with Done. We resolve once
// `count` frames arrived and let Done arrive before closing.
function streamOnce(worker, requestBytes, count) {
  return new Promise((resolve, reject) => {
    const ch = new MessageChannel();
    let frames = 0;
    ch.port1.onmessage = (message) => {
      const data = message.data;
      if (!data) {
        reject(new Error('stream response data is null'));
        return;
      }
      frames++;
      if (frames >= count) {
        resolve(frames);
        setTimeout(() => {
          ch.port2.close();
          ch.port1.close();
        }, 0);
      }
    };
    const ab = new ArrayBuffer(requestBytes.length);
    const view = new Uint8Array(ab);
    view.set(requestBytes);
    worker.postMessage([ch.port2, view], [ch.port2, ab]);
  });
}

// streamOnceSab mirrors the opt-in shared-memory path of bridge_web.dart: the
// worker writes each frame into a shared ring and signals with a bare number,
// falling back to an envelope when a frame does not fit. We resolve once
// `count` frames arrived.
let sabRingFrames = 0;
let sabFallbackFrames = 0;

function streamOnceSab(worker, requestBytes, count, slots, slotBytes) {
  const sab = new SharedArrayBuffer(16 + slots * slotBytes);
  const ctrl = new Int32Array(sab, 0, 1);
  const words = new Int32Array(sab);
  const bytes = new Uint8Array(sab);
  let read = 0;
  return new Promise((resolve, reject) => {
    const ch = new MessageChannel();
    let frames = 0;
    const done = () => {
      resolve(frames);
      setTimeout(() => {
        ch.port2.close();
        ch.port1.close();
      }, 0);
    };
    const drain = () => {
      const pending = Atomics.load(ctrl, 0);
      for (let i = 0; i < pending; i++) {
        const base = 16 + read * slotBytes;
        const length = words[base / 4];
        if (length > 0 && base + 4 + length <= bytes.length) {
          frames++;
          sabRingFrames++;
        }
        read = (read + 1) % slots;
        Atomics.sub(ctrl, 0, 1);
      }
      if (frames >= count) {
        done();
      }
    };
    ch.port1.onmessage = (message) => {
      const data = message.data;
      if (typeof data === 'number') {
        drain();
        return;
      }
      if (!data) {
        reject(new Error('stream response data is null'));
        return;
      }
      frames++;
      sabFallbackFrames++;
      if (frames >= count) {
        done();
      }
    };
    const ab = new ArrayBuffer(requestBytes.length);
    const view = new Uint8Array(ab);
    view.set(requestBytes);
    worker.postMessage([ch.port2, view, sab, slots, slotBytes], [ch.port2, ab]);
  });
}

function percentile(sorted, p) {
  const idx = Math.round(p * (sorted.length - 1));
  return sorted[Math.min(Math.max(idx, 0), sorted.length - 1)];
}

async function main() {
  if (MODE === 'sab' && typeof SharedArrayBuffer === 'undefined') {
    throw new Error('SharedArrayBuffer unavailable; serve with COOP/COEP headers');
  }
  const payload = makePayload(PAYLOAD_SIZE);
  const streamPayload = new Uint8Array([COUNT]);
  let nextPort = 1;
  const worker = new Worker('worker.js');
  await new Promise((resolve, reject) => {
    worker.onerror = (e) => reject(new Error('worker error: ' + e.message));
    const timer = setTimeout(() => reject(new Error('worker startup timeout')), 30000);
    worker.onmessage = (e) => {
      clearTimeout(timer);
      resolve(e.data);
    };
  });

  const runOnce = () => {
    switch (MODE) {
      case 'stream':
        return streamOnce(worker, encodeRpcRequest(nextPort++, GATED_STREAM_PATH, streamPayload), COUNT);
      case 'sab':
        return streamOnceSab(worker, encodeRpcRequest(nextPort++, GATED_STREAM_PATH, streamPayload), COUNT, COUNT + 1, 64);
      default:
        return rpcOnce(worker, encodeEchoRequest(nextPort++, payload));
    }
  };

  // Warmup.
  for (let i = 0; i < WARMUP; i++) {
    await runOnce();
  }

  const times = [];
  for (let i = 0; i < N; i++) {
    const t0 = performance.now();
    await runOnce();
    const t1 = performance.now();
    times.push(t1 - t0);
  }

  times.sort((a, b) => a - b);
  const mean = times.reduce((a, b) => a + b, 0) / times.length;
  const stats = {
    transport: 'web',
    mode: MODE,
    frames: MODE === 'unary' ? 1 : COUNT,
    iterations: N,
    warmup: WARMUP,
    payload_bytes: PAYLOAD_SIZE,
    unit: 'ms',
    min: times[0],
    p50: percentile(times, 0.50),
    p90: percentile(times, 0.90),
    p99: percentile(times, 0.99),
    max: times[times.length - 1],
    mean: mean,
  };
  if (MODE === 'sab') {
    stats.sab_ring_frames = sabRingFrames;
    stats.sab_fallback_frames = sabFallbackFrames;
  }
  window.__benchResults = stats;
  document.getElementById('results').textContent = JSON.stringify(stats, null, 2);
  document.getElementById('doneButton').style.display = 'block';
}

main().catch((e) => {
  document.getElementById('results').textContent = 'ERROR: ' + (e && e.message ? e.message : e);
  window.__benchResults = { error: String(e && e.message ? e.message : e) };
  document.getElementById('doneButton').style.display = 'block';
});
