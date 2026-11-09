// Cursor propagation check against a Linux server capturing $DISPLAY.
// DISPLAY=:99 node scripts/e2e-cursor.mjs [http://localhost:8000/]
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import assert from 'node:assert/strict';

const [url = 'http://localhost:8000/'] = process.argv.slice(2);
const profile = mkdtempSync(join(tmpdir(), 'urdp-cursor-e2e-'));
const port = 19335;
const chrome = spawn(process.env.CHROME || 'google-chrome', [
  '--disable-dev-shm-usage', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--autoplay-policy=no-user-gesture-required',
  '--window-size=1200,800', `--user-data-dir=${profile}`, `--remote-debugging-port=${port}`, url,
], { stdio: 'ignore' });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let ws;
try {
  for (let i = 0; i < 100 && !ws; i++) {
    await sleep(200);
    try {
      const page = (await (await fetch(`http://127.0.0.1:${port}/json`)).json()).find((t) => t.type === 'page' && t.url.startsWith(url));
      if (page) ws = new WebSocket(page.webSocketDebuggerUrl);
    } catch { /* Chrome is still starting. */ }
  }
  assert.ok(ws, 'Chrome did not start');
  await new Promise((resolve) => ws.addEventListener('open', resolve));
  let id = 0;
  const pending = new Map();
  ws.addEventListener('message', (e) => {
    const m = JSON.parse(e.data);
    pending.get(m.id)?.(m);
    pending.delete(m.id);
  });
  const evaluate = (expression) => new Promise((resolve, reject) => {
    const i = ++id;
    const timeout = setTimeout(() => reject(new Error('CDP evaluation timed out')), 10000);
    pending.set(i, (m) => {
      clearTimeout(timeout);
      if (m.error || m.result?.exceptionDetails) reject(new Error(JSON.stringify(m)));
      else resolve(m.result?.result?.value);
    });
    ws.send(JSON.stringify({ id: i, method: 'Runtime.evaluate', params: { expression, awaitPromise: true, returnByValue: true } }));
  });
  for (let i = 0; i < 50; i++) {
    if (await evaluate("document.readyState === 'complete' && !!document.getElementById('start')")) break;
    await sleep(100);
  }
  await evaluate("document.getElementById('start').click()");
  for (let i = 0; i < 100; i++) {
    if (await evaluate("document.getElementById('video').videoWidth > 0 && window.urdpPC.connectionState === 'connected'")) break;
    await sleep(200);
  }
  assert.ok(await evaluate("document.getElementById('video').videoWidth"), 'no video decoded');
  // Move outside this Chrome window so the root cursor is the host cursor.
  execFileSync('xdotool', ['mousemove', '1800', '1000']);
  for (const [name, css] of [['xterm', 'text'], ['sb_h_double_arrow', 'ew-resize'], ['left_ptr', 'default'], ['xterm', 'text']]) {
    execFileSync('xsetroot', ['-cursor_name', name]);
    let actual;
    for (let i = 0; i < 50; i++) {
      actual = await evaluate("document.getElementById('video').style.cursor");
      if (actual === css) break;
      await sleep(100);
    }
    assert.equal(actual, css, `${name} cursor`);
    console.log(`${name} -> video.style.cursor = ${actual}`);
  }
  // A bitmap cursor has no standard name, so it must arrive as an image.
  const xbm = join(profile, 'cursor.xbm');
  writeFileSync(xbm, '#define c_width 16\n#define c_height 16\n#define c_x_hot 3\n#define c_y_hot 5\nstatic unsigned char c_bits[] = {\n' +
    Array(32).fill('0xff').join(', ') + ' };\n');
  execFileSync('xsetroot', ['-cursor', xbm, xbm]);
  let actual;
  for (let i = 0; i < 50; i++) {
    actual = await evaluate("document.getElementById('video').style.cursor");
    if (actual.startsWith('url(')) break;
    await sleep(100);
  }
  assert.match(actual, /^url\("data:image\/png;base64,.+"\) 3 5, auto$/, 'bitmap cursor');
  console.log(`bitmap -> video.style.cursor = ${actual.slice(0, 40)}... ${actual.slice(-10)}`);
} finally {
  ws?.close();
  chrome.kill();
  if (chrome.exitCode === null) await new Promise((resolve) => chrome.once('exit', resolve));
  rmSync(profile, { recursive: true, force: true, maxRetries: 5 });
}
