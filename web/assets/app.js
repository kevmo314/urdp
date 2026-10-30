// urdp web client: receives the stream and forwards input.

const video = document.getElementById('video');
const gate = document.getElementById('gate');
const hud = document.getElementById('hud');
const statsEl = document.getElementById('stats');
const sourcesEl = document.getElementById('sources');
const statusEl = document.getElementById('status');

// "/" streams the desktop, "/display<N>" one monitor, "/<window-id>" one window.
let source = decodeURIComponent(location.pathname.replace(/^\/+|\/+$/g, ''));

const audioEl = new Audio();
let pc, input, move, pads, micSender, camSender;

function toast(msg) {
  statusEl.textContent = msg;
  statusEl.hidden = !msg;
}

async function connect() {
  const conn = pc = new RTCPeerConnection({ bundlePolicy: 'max-bundle' });
  try {
    await negotiate(conn);
  } catch (e) {
    conn.close();
    throw e;
  }
}

async function negotiate(pc) {
  pc.addTransceiver('video', { direction: 'recvonly' });
  pc.addTransceiver('audio', { direction: 'recvonly' });
  // The viewer's mic and camera, sent to the host's virtual devices. They
  // start empty; toggling swaps tracks in without renegotiating.
  micSender = pc.addTransceiver('audio', { direction: 'sendonly' }).sender;
  const camTx = pc.addTransceiver('video', { direction: 'sendonly' });
  const vp8 = RTCRtpSender.getCapabilities?.('video')?.codecs.filter((c) => /vp8|rtx/i.test(c.mimeType));
  if (vp8?.length) camTx.setCodecPreferences(vp8); // the host decodes VP8
  camSender = camTx.sender;
  for (const [sender, track] of [[micSender, micTrack], [camSender, camTrack]]) {
    if (track) sender.replaceTrack(track);
  }
  pads = pc.createDataChannel('gamepad', { ordered: false, maxRetransmits: 0 });
  input = pc.createDataChannel('input', { ordered: true });
  move = pc.createDataChannel('move', { ordered: false, maxRetransmits: 0 });
  move.bufferedAmountLowThreshold = 0;
  move.onbufferedamountlow = flushMove;
  input.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.t === 'ended') toast('Source closed');
  };

  pc.ontrack = (e) => {
    // Render frames as soon as they decode instead of buffering for
    // smoothness; this is the single biggest client-side latency knob.
    if ('jitterBufferTarget' in e.receiver) e.receiver.jitterBufferTarget = 0;
    if ('playoutDelayHint' in e.receiver) e.receiver.playoutDelayHint = 0;
    // Audio plays from its own element so the video element never waits on
    // (or gets re-initialized by) the audio track.
    if (e.track.kind === 'video') {
      video.srcObject = new MediaStream([e.track]);
    } else {
      audioEl.srcObject = new MediaStream([e.track]);
      audioEl.play().catch(() => {});
    }
  };
  pc.onconnectionstatechange = () => {
    const s = pc.connectionState;
    if (s === 'connected') toast('');
    else if (s === 'failed' || s === 'disconnected') toast(`Connection ${s} — reconnecting…`);
    // "disconnected" often heals by itself; give it a moment.
    if (s === 'failed' || s === 'disconnected') {
      setTimeout(() => {
        if (pc === window.urdpPC && ['failed', 'disconnected'].includes(pc.connectionState)) reconnect();
      }, s === 'failed' ? 500 : 4000);
    }
  };
  window.urdpPC = pc;

  await pc.setLocalDescription(await pc.createOffer());
  await gatheringComplete(pc);
  const res = await fetch(`/api/offer?source=${encodeURIComponent(source)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(pc.localDescription),
  });
  if (!res.ok) throw new Error(await res.text());
  await pc.setRemoteDescription(await res.json());
}

function gatheringComplete(pc) {
  if (pc.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    // Host candidates arrive almost immediately; don't wait on slow STUN.
    const t = setTimeout(resolve, 1000);
    pc.addEventListener('icegatheringstatechange', () => {
      if (pc.iceGatheringState === 'complete') { clearTimeout(t); resolve(); }
    });
  });
}

async function reconnect() {
  pc?.close();
  prev = undefined;
  try { await connect(); } catch (e) { toast(`Error: ${e.message}`); setTimeout(reconnect, 2000); }
}

// ---- input ----

function send(ch, msg) {
  if (ch?.readyState === 'open') ch.send(JSON.stringify(msg));
}

// The video's layout box, cached: raw pointer events can arrive at 1kHz
// and reading layout for each is wasted work.
let videoRect = video.getBoundingClientRect();
new ResizeObserver(() => { videoRect = video.getBoundingClientRect(); }).observe(video);

// Normalized [0,1] position within the letterboxed video content.
function norm(e) {
  const r = videoRect;
  const vw = video.videoWidth || r.width, vh = video.videoHeight || r.height;
  const scale = Math.min(r.width / vw, r.height / vh);
  const w = vw * scale, h = vh * scale;
  const x = (e.clientX - r.left - (r.width - w) / 2) / w;
  const y = (e.clientY - r.top - (r.height - h) / 2) / h;
  return { x: Math.min(Math.max(x, 0), 1), y: Math.min(Math.max(y, 0), 1) };
}

// Only the latest position matters for an absolute pointer, so while the
// channel is backed up, keep just the newest one and send it once it drains.
let pendingMove;
function flushMove() {
  // A little slack: a single queued message usually means it's in flight,
  // not that the link is congested.
  if (pendingMove && move?.bufferedAmount < 256) {
    send(move, pendingMove);
    pendingMove = undefined;
  }
}
// pointerrawupdate fires as soon as the OS reports motion; pointermove is
// held back until the next animation frame (up to 16ms later). It's only
// exposed in secure contexts (https, localhost).
const moveEvent = 'onpointerrawupdate' in window ? 'pointerrawupdate' : 'pointermove';
video.addEventListener(moveEvent, (e) => {
  pendingMove = { t: 'm', ...norm(e) };
  flushMove();
});
// Capture keeps moves (and the final release) flowing while dragging
// outside the video.
video.addEventListener('pointerdown', (e) => video.setPointerCapture(e.pointerId));
// mousedown/up rather than pointerdown/up: pointer events only fire for
// the first press and last release, losing chorded buttons.
// Buttons pressed on the video, so releases elsewhere (e.g. after dragging
// out) are forwarded but clicks on the HUD are not.
const held = new Set();
video.addEventListener('mousedown', (e) => {
  video.focus();
  held.add(e.button);
  send(input, { t: 'd', b: e.button, ...norm(e) });
  e.preventDefault();
});
window.addEventListener('mouseup', (e) => {
  if (!held.delete(e.button)) return;
  send(input, { t: 'u', b: e.button, ...norm(e) });
  e.preventDefault();
});
const reset = () => { held.clear(); send(input, { t: 'reset' }); };
video.addEventListener('pointercancel', reset);
video.addEventListener('contextmenu', (e) => e.preventDefault());
video.addEventListener('wheel', (e) => {
  const k = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 800 : 1;
  send(input, { t: 'w', dx: e.deltaX * k, dy: e.deltaY * k });
  e.preventDefault();
}, { passive: false });

window.addEventListener('keydown', (e) => {
  if (!gate.hidden || e.code === 'F11') return;
  send(input, e.repeat ? { t: 'kd', c: e.code, r: true } : { t: 'kd', c: e.code });
  e.preventDefault();
});
window.addEventListener('keyup', (e) => {
  if (!gate.hidden || e.code === 'F11') return;
  send(input, { t: 'ku', c: e.code });
  e.preventDefault();
});
// Don't leave modifiers stuck on the host when focus leaves the page.
window.addEventListener('blur', reset);

document.getElementById('fullscreen').onclick = async () => {
  await document.documentElement.requestFullscreen();
  // Lets Escape, Alt+Tab-adjacent and OS shortcuts reach the host.
  await navigator.keyboard?.lock?.().catch(() => {});
};

// ---- sources ----

async function loadSources() {
  try {
    const [displays, wins] = await Promise.all(['/api/displays', '/api/windows'].map((u) => fetch(u).then((r) => r.json()).catch(() => [])));
    const opts = [
      ['/', 'Entire desktop'],
      ...displays.map((d) => [d.path, `Display ${d.index} — ${d.width}×${d.height}`]),
      ...wins.map((w) => [w.path, `${w.app ? `${w.app}: ` : ''}${w.title || '(untitled)'} — ${w.width}×${w.height}`]),
    ];
    sourcesEl.replaceChildren(...opts.map(([path, label]) => {
      const o = new Option(label, path);
      o.selected = path.replace(/^\//, '') === source;
      return o;
    }));
  } catch { /* window listing is optional */ }
}
sourcesEl.onfocus = loadSources;
sourcesEl.onchange = () => { location.href = sourcesEl.value + location.search; };

// ---- stats ----

let prev;
async function updateStats() {
  if (!pc) return;
  const r = await pc.getStats();
  let v, pair;
  r.forEach((s) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') v = s;
    if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s;
  });
  if (!v) return;
  const lines = [];
  if (prev) {
    const dt = (v.timestamp - prev.timestamp) / 1000;
    const fps = (v.framesDecoded - prev.framesDecoded) / dt;
    const mbps = ((v.bytesReceived - prev.bytesReceived) * 8) / dt / 1e6;
    const dFrames = v.jitterBufferEmittedCount - prev.jitterBufferEmittedCount;
    const jb = dFrames > 0 ? ((v.jitterBufferDelay - prev.jitterBufferDelay) / dFrames) * 1000 : 0;
    const dDec = v.framesDecoded - prev.framesDecoded;
    const dec = dDec > 0 ? ((v.totalDecodeTime - prev.totalDecodeTime) / dDec) * 1000 : 0;
    const rtt = pair?.currentRoundTripTime != null ? pair.currentRoundTripTime * 1000 : NaN;
    lines.push(`${v.frameWidth}×${v.frameHeight} ${fps.toFixed(0)}fps ${mbps.toFixed(1)}Mbps`);
    lines.push(`rtt ${rtt.toFixed(0)}ms  jitbuf ${jb.toFixed(0)}ms  dec ${dec.toFixed(1)}ms  lost ${v.packetsLost}`);
  }
  prev = v;
  if (probe) lines.push(probeStats());
  statsEl.textContent = lines.join('\n');
}

// ---- viewer devices ----

let micTrack, camTrack;

async function toggleDevice(kind) {
  const btn = document.getElementById(kind);
  const sender = kind === 'mic' ? micSender : camSender;
  let track = kind === 'mic' ? micTrack : camTrack;
  if (track) {
    track.stop();
    track = undefined;
  } else {
    try {
      const stream = await navigator.mediaDevices.getUserMedia(kind === 'mic'
        // Keep echo cancellation: the host's audio plays through this
        // page and would otherwise loop back into the host's mic.
        ? { audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } }
        : { video: { width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30 } } });
      track = stream.getTracks()[0];
      if (kind === 'cam') track.contentHint = 'motion';
    } catch (e) {
      toast(`${kind === 'mic' ? 'Microphone' : 'Camera'}: ${e.message}`);
      setTimeout(() => toast(''), 3000);
      return;
    }
  }
  if (kind === 'mic') micTrack = track; else camTrack = track;
  await sender?.replaceTrack(track ?? null);
  btn.classList.toggle('on', !!track);
}
document.getElementById('mic').onclick = () => toggleDevice('mic');
document.getElementById('cam').onclick = () => toggleDevice('cam');

// Gamepads are polled (the Gamepad API has no change events) and each
// changed state is sent whole, so a lost message is fixed by the next one;
// a periodic resend covers a lost final state.
const padState = new Map();
function pollGamepads() {
  requestAnimationFrame(pollGamepads);
  if (pads?.readyState !== 'open') return;
  const seen = new Set();
  const now = performance.now();
  for (const gp of navigator.getGamepads()) {
    if (!gp || gp.mapping !== 'standard') continue;
    seen.add(gp.index);
    const msg = JSON.stringify({
      i: gp.index,
      b: gp.buttons.slice(0, 17).map((b) => +b.value.toFixed(3)),
      a: gp.axes.slice(0, 4).map((a) => +a.toFixed(3)),
    });
    const prev = padState.get(gp.index);
    if (msg !== prev?.msg || now - prev.at > 250) {
      pads.send(msg);
      padState.set(gp.index, { msg, at: now });
    }
  }
  for (const i of padState.keys()) {
    if (!seen.has(i)) {
      pads.send(JSON.stringify({ i, gone: true }));
      padState.delete(i);
    }
  }
}
requestAnimationFrame(pollGamepads);

// ---- source tree ----
// The landing page lists every source as a `tree`-style listing: the
// desktop and its displays, then windows grouped by application.

const treeEl = document.getElementById('tree');

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function row(prefix, { path, name, meta, icon, glyph, dir }) {
  const a = el(path ? 'a' : 'div');
  if (path) {
    a.href = path;
    if (path.replace(/^\//, '') === source) a.classList.add('current');
    a.onclick = (e) => {
      e.preventDefault();
      history.pushState(null, '', path);
      source = path.replace(/^\//, '');
      start();
    };
  }
  a.append(el('span', 'branch', prefix));
  if (icon) {
    const img = el('img', 'icon');
    img.src = icon;
    img.alt = '';
    img.onerror = () => img.replaceWith(el('span', 'glyph', glyph ?? '▫'));
    a.append(img);
  } else {
    a.append(el('span', 'glyph', glyph ?? '▫'));
  }
  a.append(el('span', dir ? 'name dir' : 'name', name));
  if (meta) a.append(el('span', 'meta', meta));
  return a;
}

async function loadTree() {
  const get = (u) => fetch(u).then((r) => (r.ok ? r.json() : [])).catch(() => []);
  let [info, displays, windows] = await Promise.all([get('/api/info'), get('/api/displays'), get('/api/windows')]);
  if (info.hostname) document.getElementById('host').textContent = info.hostname;

  const groups = new Map();
  // Tray stubs and helper windows are rarely worth streaming.
  windows = windows.filter((w) => w.width >= 64 && w.height >= 48);
  for (const w of windows) {
    const app = w.app || '(other)';
    if (!groups.has(app)) groups.set(app, []);
    groups.get(app).push(w);
  }
  const apps = [...groups.keys()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
  const size = (o) => `${o.width}×${o.height}`;
  const iconURL = (w) => `/api/windows/${w.path.slice(1)}/icon?size=${Math.ceil(16 * devicePixelRatio)}`;

  const rows = [];
  const top = 1 + apps.length;
  let i = 0;
  const branch = (last) => (last ? '└── ' : '├── ');
  const indent = (last) => (last ? '    ' : '│   ');

  const desktopLast = ++i === top;
  const bounds = displays.reduce((b, d) => ({
    x0: Math.min(b.x0, d.x), y0: Math.min(b.y0, d.y), x1: Math.max(b.x1, d.x + d.width), y1: Math.max(b.y1, d.y + d.height),
  }), { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity });
  rows.push(row(branch(desktopLast), {
    path: '/', name: 'desktop/', dir: true, glyph: '🖥',
    meta: displays.length ? `${bounds.x1 - bounds.x0}×${bounds.y1 - bounds.y0}` : '',
  }));
  displays.forEach((d, j) => {
    const last = j === displays.length - 1;
    rows.push(row(indent(desktopLast) + branch(last), {
      path: d.path, name: `display${d.index}${d.name ? ` (${d.name})` : ''}${d.primary ? ' *' : ''}`, glyph: '▭', meta: size(d),
    }));
  });

  for (const app of apps) {
    const appLast = ++i === top;
    const wins = groups.get(app);
    rows.push(row(branch(appLast), { name: `${app}/`, dir: true, icon: iconURL(wins[0]), glyph: '▸' }));
    wins.forEach((w, j) => {
      rows.push(row(indent(appLast) + branch(j === wins.length - 1), {
        path: w.path, name: w.title || '(untitled)', icon: iconURL(w), meta: size(w),
      }));
    });
  }
  const n = (k, word) => `${k} ${word}${k === 1 ? '' : 's'}`;
  rows.push(el('div', 'summary', `\n${n(apps.length, 'application')}, ${n(windows.length, 'window')}, ${n(displays.length, 'display')}`));
  treeEl.replaceChildren(...rows);
}

// ---- USB passthrough (experimental; the server needs -usb) ----

import { setupUSB } from './usb.js';
setupUSB(hud, () => pc, toast);

// ---- start ----

let started = false;
// Audio playback needs a user gesture, so connect behind a click.
async function start() {
  gate.hidden = true;
  hud.hidden = false;
  audioEl.play().catch(() => {}); // unlocks audio within the click
  video.focus();
  loadSources();
  if (started) return reconnect();
  started = true;
  setInterval(updateStats, 1000);
  try { await connect(); } catch (e) { toast(`Error: ${e.message}`); setTimeout(reconnect, 2000); }
}
document.getElementById('start').onclick = start;
window.addEventListener('popstate', () => location.reload());

if (source) {
  document.getElementById('start').textContent = `Connect to /${source}`;
  document.getElementById('back').hidden = false;
} else {
  document.getElementById('start').textContent = 'Connect to desktop';
}
loadTree();
const treeTimer = setInterval(() => (gate.hidden ? clearInterval(treeTimer) : loadTree()), 2000);

// ---- latency probe ----
// With ?probe, while viewing this machine's own desktop, the page paints a
// 12-bit millisecond timestamp as black/white blocks, finds those blocks in
// the received video and reports how old the decoded picture is: the
// capture → encode → network → decode latency, excluding display scan-out.

const probe = new URLSearchParams(location.search).has('probe');
const BITS = 12, CELL = 12; // CSS px per block
let probeLatency = [];

if (probe) {
  const c = document.createElement('canvas');
  c.width = BITS * CELL;
  c.height = CELL;
  c.style.cssText = `position:fixed;left:0;top:0;width:${c.width}px;height:${c.height}px;z-index:9;image-rendering:pixelated`;
  document.body.append(c);
  // Not desynchronized: without a GPU that canvas bypasses what screen
  // capture sees, and the probe would read stale timestamps.
  const ctx = c.getContext('2d', { alpha: false });
  const paint = () => {
    const t = Math.round(performance.now()) & 0xfff;
    for (let i = 0; i < BITS; i++) {
      ctx.fillStyle = (t >> (BITS - 1 - i)) & 1 ? '#fff' : '#000';
      ctx.fillRect(i * CELL, 0, CELL, CELL);
    }
    requestAnimationFrame(paint);
  };
  paint();

  const sample = document.createElement('canvas').getContext('2d', { willReadFrequently: true });
  sample.canvas.width = BITS;
  sample.canvas.height = 1;
  const read = () => {
    video.requestVideoFrameCallback(read);
    // Where the probe sits on the physical screen, in video pixels.
    const dpr = devicePixelRatio;
    const x0 = (screenX + (outerWidth - innerWidth) / 2) * dpr;
    const y0 = (screenY + outerHeight - innerHeight) * dpr;
    const cell = CELL * dpr;
    // Sample the center of each block.
    for (let i = 0; i < BITS; i++) {
      sample.drawImage(video, x0 + (i + 0.5) * cell - 1, y0 + cell / 2 - 1, 2, 2, i, 0, 1, 1);
    }
    const px = sample.getImageData(0, 0, BITS, 1).data;
    let t = 0;
    for (let i = 0; i < BITS; i++) t = (t << 1) | (px[i * 4 + 1] > 128 ? 1 : 0);
    const lat = ((Math.round(performance.now()) & 0xfff) - t + 0x1000) & 0xfff;
    if (lat < 1000) probeLatency.push(lat);
  };
  video.requestVideoFrameCallback(read);
}

function probeStats() {
  if (!probeLatency.length) return '';
  const s = probeLatency.sort((a, b) => a - b);
  const p = (q) => s[Math.min(s.length - 1, Math.floor(q * s.length))];
  probeLatency = [];
  return `e2e p50 ${p(0.5)}ms  p90 ${p(0.9)}ms`;
}

// ---- HUD ----
// Keep the HUD out of the render tree unless the pointer is near the top:
// anything drawn over the video stops the browser from scanning it out as
// a hardware overlay, which can cost a compositor frame.
window.addEventListener('pointermove', (e) => {
  if (!gate.hidden) return;
  hud.hidden = e.clientY > 48 && !hud.matches(':hover') && document.activeElement !== sourcesEl;
});
