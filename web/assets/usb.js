// USB passthrough (experimental): forwards a WebUSB device from this
// browser to the host, which attaches it as a real USB device via USB/IP.
// Each device gets its own reliable, ordered "usb" data channel. The
// server translates USB/IP into the simple operations below and this file
// only executes them; see internal/usbip/wire.go for the byte layout.
//
// Browsers refuse to claim some interface classes (HID, mass storage,
// audio, video, smart card, wireless) and the OS must not have a driver
// bound to the device (on Windows it must use WinUSB), so in practice this
// works for vendor-specific and CDC devices.

const MSG = { HELLO: 0x01, DONE: 0x02, STATUS: 0x80, CONTROL: 0x10, TRANSFER: 0x11, ISO: 0x12, SETCONF: 0x13, SETALT: 0x14, CLEAR: 0x15, RESET: 0x16 };
const STATUS = { READY: 0, ATTACHED: 1, ERROR: 2 };
const WIRE_VERSION = 1;
const CHUNK = 16 << 10;
const ZLP = 0x01;
// Negative Linux errno values, as URB status.
const E = { OK: 0, ENODEV: -19, EPIPE: -32, EPROTO: -71, EOVERFLOW: -75 };

export function setupUSB(hud, getPC, toast) {
  if (!navigator.usb) return; // needs a secure context (HTTPS or localhost) and Chromium
  const btn = document.createElement('button');
  btn.textContent = 'USB';
  btn.title = 'Attach a USB device from this computer to the remote host (experimental; server needs -usb)';
  hud.insertBefore(btn, hud.querySelector('#fullscreen'));
  btn.onclick = () => {
    const pc = getPC();
    if (!pc || pc.connectionState !== 'connected') return toast('USB: not connected');
    attach(pc, toast).catch((e) => toast(`USB: ${e.message}`));
  };
}

// Framing: the channel is a byte stream of [u32 LE length][message].
class Link {
  constructor(ch) {
    this.ch = ch;
    this.head = new Uint8Array(4);
    this.headLen = 0;
    this.body = null;
    this.bodyLen = 0;
    this.queue = Promise.resolve();
    this.onmessage = () => {};
    ch.binaryType = 'arraybuffer';
    ch.bufferedAmountLowThreshold = 1 << 20;
    ch.onmessage = (e) => this.feed(new Uint8Array(e.data));
  }

  feed(b) {
    let i = 0;
    while (i < b.length) {
      if (!this.body) {
        const n = Math.min(4 - this.headLen, b.length - i);
        this.head.set(b.subarray(i, i + n), this.headLen);
        this.headLen += n;
        i += n;
        if (this.headLen < 4) return;
        this.body = new Uint8Array(new DataView(this.head.buffer).getUint32(0, true));
        this.bodyLen = 0;
      }
      const n = Math.min(this.body.length - this.bodyLen, b.length - i);
      this.body.set(b.subarray(i, i + n), this.bodyLen);
      this.bodyLen += n;
      i += n;
      if (this.bodyLen === this.body.length) {
        const msg = this.body;
        this.body = null;
        this.headLen = 0;
        this.onmessage(msg);
      }
    }
  }

  // Sends one message; chunks of different messages never interleave,
  // and a full send buffer (Chrome closes the channel past 16 MiB) waits.
  send(msg) {
    this.queue = this.queue.then(async () => {
      const framed = new Uint8Array(4 + msg.length);
      new DataView(framed.buffer).setUint32(0, msg.length, true);
      framed.set(msg, 4);
      for (let off = 0; off < framed.length; off += CHUNK) {
        if (this.ch.readyState !== 'open') return;
        if (this.ch.bufferedAmount > 8 << 20) {
          await new Promise((r) => this.ch.addEventListener('bufferedamountlow', r, { once: true }));
        }
        this.ch.send(framed.subarray(off, off + CHUNK));
      }
    });
    return this.queue;
  }
}

// Little-endian message builder.
class Writer {
  constructor() { this.parts = []; this.len = 0; }
  bytes(b) { const u = b instanceof Uint8Array ? b : new Uint8Array(b.buffer, b.byteOffset, b.byteLength); this.parts.push(u); this.len += u.length; return this; }
  u8(v) { return this.bytes(Uint8Array.of(v)); }
  u16(v) { const b = new Uint8Array(2); new DataView(b.buffer).setUint16(0, v, true); return this.bytes(b); }
  u32(v) { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, v >>> 0, true); return this.bytes(b); }
  done() { const out = new Uint8Array(this.len); let o = 0; for (const p of this.parts) { out.set(p, o); o += p.length; } return out; }
}

function waitOpen(ch) {
  if (ch.readyState === 'open') return Promise.resolve();
  return new Promise((resolve, reject) => {
    ch.addEventListener('open', resolve, { once: true });
    ch.addEventListener('close', () => reject(new Error('channel closed')), { once: true });
  });
}

async function attach(pc, toast) {
  // Ask the server before showing the chooser, so a server without -usb
  // says so instead of after the user picked a device. This takes one
  // round trip, well within the click's user activation.
  const ch = pc.createDataChannel('usb', { ordered: true });
  const link = new Link(ch);
  let statusWaiter;
  const nextStatus = () => new Promise((resolve) => { statusWaiter = resolve; });
  let first = nextStatus();
  let session;
  link.onmessage = (m) => {
    if (m[0] === MSG.STATUS) {
      const st = { code: m[1], text: new TextDecoder().decode(m.subarray(2)) };
      if (statusWaiter) { const w = statusWaiter; statusWaiter = null; w(st); return; }
      if (st.code === STATUS.ERROR) toast(`USB: ${st.text}`);
      return;
    }
    session?.handle(m);
  };
  ch.onclose = () => session?.close();
  await waitOpen(ch);
  const ready = await Promise.race([first, new Promise((r) => setTimeout(() => r({ code: STATUS.ERROR, text: 'no answer from server' }), 3000))]);
  if (ready.code !== STATUS.READY) { ch.close(); throw new Error(ready.text); }

  let device;
  try {
    device = await navigator.usb.requestDevice({ filters: [] });
  } catch (e) {
    ch.close();
    if (e.name === 'NotFoundError') return; // chooser cancelled
    throw e;
  }
  try {
    session = new Session(device, link, ch);
    const warnings = await session.open();
    const attached = nextStatus();
    await link.send(await session.hello());
    const st = await attached;
    if (st.code !== STATUS.ATTACHED) throw new Error(st.text);
    toast(`USB: ${device.productName || 'device'} attached (${st.text})${warnings}`);
    setTimeout(() => toast(''), 4000);
  } catch (e) {
    ch.close();
    session?.close();
    throw e;
  }
}

class Session {
  constructor(device, link, ch) {
    this.device = device;
    this.link = link;
    this.ch = ch;
    this.closed = false;
    this.onDisconnect = (e) => { if (e.device === device) ch.close(); };
    navigator.usb.addEventListener('disconnect', this.onDisconnect);
  }

  async open() {
    const d = this.device;
    await d.open();
    if (!d.configuration) await d.selectConfiguration(d.configurations[0].configurationValue);
    const failed = await this.claimAll();
    return failed.length ? `; the browser refused interface ${failed.join(', ')}, which won't work on the host` : '';
  }

  // Claims every interface of the active configuration; returns those
  // the browser refused (protected classes, or held by an OS driver).
  async claimAll() {
    const failed = [];
    for (const iface of this.device.configuration?.interfaces ?? []) {
      if (iface.claimed) continue;
      try {
        await this.device.claimInterface(iface.interfaceNumber);
      } catch (e) {
        const cls = iface.alternate?.interfaceClass;
        failed.push(`${iface.interfaceNumber} (class 0x${(cls ?? 0).toString(16).padStart(2, '0')})`);
        console.warn('usb: claimInterface', iface.interfaceNumber, e);
      }
    }
    return failed;
  }

  getDescriptor(type, index, length) {
    return this.device.controlTransferIn({ requestType: 'standard', recipient: 'device', request: 6, value: (type << 8) | index, index: 0 }, length);
  }

  async hello() {
    const d = this.device;
    const dev = await this.getDescriptor(1, 0, 18);
    if (dev.status !== 'ok') throw new Error('could not read the device descriptor');
    const configs = [];
    for (let i = 0; i < d.configurations.length; i++) {
      const head = await this.getDescriptor(2, i, 9);
      if (head.status !== 'ok' || head.data.byteLength < 4) throw new Error(`could not read configuration ${i}`);
      const full = await this.getDescriptor(2, i, head.data.getUint16(2, true));
      configs.push(full.data);
    }
    const name = new TextEncoder().encode(d.productName || '').subarray(0, 200);
    const w = new Writer().u8(MSG.HELLO).u8(WIRE_VERSION).u8(d.configuration?.configurationValue ?? 0).u8(configs.length);
    w.u16(dev.data.byteLength).bytes(dev.data);
    for (const c of configs) w.u16(c.byteLength).bytes(c);
    return w.u16(name.length).bytes(name).done();
  }

  // Runs one server operation and replies with its completion. Operations
  // are started in arrival order (WebUSB queues transfers per endpoint),
  // and complete in whatever order the device finishes them.
  handle(m) {
    const v = new DataView(m.buffer, m.byteOffset, m.byteLength);
    const type = m[0];
    const id = v.getUint32(1, true);
    this.run(type, v, m)
      .catch((e) => ({ status: errno(e, this.device) }))
      .then((r) => {
        if (this.closed) return;
        const w = new Writer().u8(MSG.DONE).u32(id).u32(r.status ?? 0).u32(r.actual ?? 0);
        w.u32(r.packets?.length ?? 0);
        for (const p of r.packets ?? []) w.u32(p.status).u32(p.actual);
        if (r.data) w.bytes(r.data);
        this.link.send(w.done());
      });
  }

  async run(type, v, m) {
    const d = this.device;
    switch (type) {
      case MSG.CONTROL: {
        const bm = m[5];
        const setup = {
          requestType: ['standard', 'class', 'vendor', 'reserved'][(bm >> 5) & 3],
          recipient: ['device', 'interface', 'endpoint', 'other'][bm & 0x1f] ?? 'other',
          request: m[6],
          value: v.getUint16(7, true),
          index: v.getUint16(9, true),
        };
        if (setup.requestType === 'reserved') return { status: E.EPIPE };
        if (bm & 0x80) return inResult(await d.controlTransferIn(setup, v.getUint16(11, true)));
        const data = m.subarray(13);
        return outResult(await d.controlTransferOut(setup, data.length ? data : undefined));
      }
      case MSG.TRANSFER: {
        const ep = m[5], flags = m[6];
        if (ep & 0x80) return inResult(await d.transferIn(ep & 0x0f, v.getUint32(7, true)));
        const r = outResult(await d.transferOut(ep & 0x0f, m.subarray(11)));
        if (flags & ZLP && r.status === E.OK) await d.transferOut(ep & 0x0f, new Uint8Array(0));
        return r;
      }
      case MSG.ISO: {
        const ep = m[5];
        const n = v.getUint32(6, true);
        const lengths = [];
        for (let i = 0; i < n; i++) lengths.push(v.getUint32(10 + 4 * i, true));
        if (ep & 0x80) {
          const r = await d.isochronousTransferIn(ep & 0x0f, lengths);
          const w = new Writer();
          const packets = r.packets.map((p) => {
            const len = p.data?.byteLength ?? 0;
            if (len) w.bytes(p.data);
            return { status: statusOf(p.status), actual: len };
          });
          return { packets, data: w.done() };
        }
        const r = await d.isochronousTransferOut(ep & 0x0f, m.subarray(10 + 4 * n), lengths);
        return { packets: r.packets.map((p) => ({ status: statusOf(p.status), actual: p.bytesWritten })) };
      }
      case MSG.SETCONF:
        await d.selectConfiguration(m[5]);
        await this.claimAll(); // a new configuration releases all claims
        return {};
      case MSG.SETALT:
        await d.selectAlternateInterface(m[5], m[6]);
        return {};
      case MSG.CLEAR:
        await d.clearHalt(m[5] & 0x80 ? 'in' : 'out', m[5] & 0x0f);
        return {};
      case MSG.RESET:
        await d.reset();
        await this.claimAll();
        return {};
    }
    return { status: E.EPROTO };
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    navigator.usb.removeEventListener('disconnect', this.onDisconnect);
    this.device.close().catch(() => {});
  }
}

function statusOf(s) {
  return s === 'ok' ? E.OK : s === 'stall' ? E.EPIPE : s === 'babble' ? E.EOVERFLOW : E.EPROTO;
}

function inResult(r) {
  const data = r.data ? new Uint8Array(r.data.buffer, r.data.byteOffset, r.data.byteLength) : new Uint8Array(0);
  return { status: statusOf(r.status), actual: data.length, data };
}

function outResult(r) {
  return { status: statusOf(r.status), actual: r.bytesWritten ?? 0 };
}

// Maps a WebUSB exception onto a URB status.
function errno(e, device) {
  if (e?.name === 'NotFoundError') return device.opened ? E.EPIPE : E.ENODEV; // unclaimed endpoint, or unplugged
  if (e?.name === 'SecurityError') return E.EPIPE; // request blocked by the browser: look like a stall
  console.warn('usb:', e);
  return E.EPROTO;
}
