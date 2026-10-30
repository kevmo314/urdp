// urdp web client: receives the stream and forwards input.

const video = document.getElementById('video');
const gate = document.getElementById('gate');
const hud = document.getElementById('hud');
const statsEl = document.getElementById('stats');
const sourcesEl = document.getElementById('sources');
const statusEl = document.getElementById('status');

// "/" streams the desktop, "/<window-id>" a single window.
const source = decodeURIComponent(location.pathname.replace(/^\/+|\/+$/g, ''));

let pc, input, move;

function toast(msg) {
  statusEl.textContent = msg;
  statusEl.hidden = !msg;
}

async function connect() {
  pc = new RTCPeerConnection({ bundlePolicy: 'max-bundle' });
  pc.addTransceiver('video', { direction: 'recvonly' });
  pc.addTransceiver('audio', { direction: 'recvonly' });
  input = pc.createDataChannel('input', { ordered: true });
  move = pc.createDataChannel('move', { ordered: false, maxRetransmits: 0 });
  input.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.t === 'ended') toast('Source closed');
  };

  const stream = new MediaStream();
  pc.ontrack = (e) => {
    // Render frames as soon as they decode instead of buffering for
    // smoothness; this is the single biggest client-side latency knob.
    if ('jitterBufferTarget' in e.receiver) e.receiver.jitterBufferTarget = 0;
    if ('playoutDelayHint' in e.receiver) e.receiver.playoutDelayHint = 0;
    stream.addTrack(e.track);
    video.srcObject = stream;
  };
  pc.onconnectionstatechange = () => {
    const s = pc.connectionState;
    if (s === 'connected') toast('');
    else if (s === 'failed' || s === 'disconnected') toast(`Connection ${s} — reconnecting…`);
    if (s === 'failed') setTimeout(reconnect, 1000);
  };

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
  try { await connect(); } catch (e) { toast(`Error: ${e.message}`); setTimeout(reconnect, 2000); }
}

// ---- input ----

function send(ch, msg) {
  if (ch?.readyState === 'open') ch.send(JSON.stringify(msg));
}

// Normalized [0,1] position within the letterboxed video content.
function norm(e) {
  const r = video.getBoundingClientRect();
  const vw = video.videoWidth || r.width, vh = video.videoHeight || r.height;
  const scale = Math.min(r.width / vw, r.height / vh);
  const w = vw * scale, h = vh * scale;
  const x = (e.clientX - r.left - (r.width - w) / 2) / w;
  const y = (e.clientY - r.top - (r.height - h) / 2) / h;
  return { x: Math.min(Math.max(x, 0), 1), y: Math.min(Math.max(y, 0), 1) };
}

video.addEventListener('pointermove', (e) => {
  // Coalesced events would be sent as separate positions; only the latest
  // matters for an absolute pointer.
  send(move, { t: 'm', ...norm(e) });
});
video.addEventListener('pointerdown', (e) => {
  video.focus();
  video.setPointerCapture(e.pointerId);
  send(input, { t: 'd', b: e.button, ...norm(e) });
  e.preventDefault();
});
video.addEventListener('pointerup', (e) => {
  send(input, { t: 'u', b: e.button, ...norm(e) });
  e.preventDefault();
});
video.addEventListener('contextmenu', (e) => e.preventDefault());
video.addEventListener('wheel', (e) => {
  const k = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 800 : 1;
  send(input, { t: 'w', dx: e.deltaX * k, dy: e.deltaY * k });
  e.preventDefault();
}, { passive: false });

window.addEventListener('keydown', (e) => {
  if (!gate.hidden || e.code === 'F11') return;
  send(input, { t: 'kd', c: e.code });
  e.preventDefault();
});
window.addEventListener('keyup', (e) => {
  if (!gate.hidden || e.code === 'F11') return;
  send(input, { t: 'ku', c: e.code });
  e.preventDefault();
});
// Don't leave modifiers stuck on the host when focus leaves the page.
window.addEventListener('blur', () => send(input, { t: 'reset' }));

document.getElementById('fullscreen').onclick = async () => {
  await document.documentElement.requestFullscreen();
  // Lets Escape, Alt+Tab-adjacent and OS shortcuts reach the host.
  await navigator.keyboard?.lock?.().catch(() => {});
};

// ---- sources ----

async function loadSources() {
  try {
    const wins = await (await fetch('/api/windows')).json();
    const opts = [['/', 'Entire desktop'], ...wins.map((w) => [w.path, `${w.title || '(untitled)'} — ${w.width}×${w.height}`])];
    sourcesEl.replaceChildren(...opts.map(([path, label]) => {
      const o = new Option(label, path);
      o.selected = path.replace(/^\//, '') === source;
      return o;
    }));
  } catch { /* window listing is optional */ }
}
sourcesEl.onfocus = loadSources;
sourcesEl.onchange = () => { location.pathname = sourcesEl.value; };

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
  statsEl.textContent = lines.join('\n');
}

// ---- start ----

// Audio playback needs a user gesture, so connect behind a click.
document.getElementById('start').onclick = async () => {
  gate.hidden = true;
  hud.hidden = false;
  video.muted = false;
  video.focus();
  loadSources();
  setInterval(updateStats, 1000);
  try { await connect(); } catch (e) { toast(`Error: ${e.message}`); setTimeout(reconnect, 2000); }
};
