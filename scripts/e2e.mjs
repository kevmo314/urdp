// End-to-end check: launches Chrome on $DISPLAY viewing a running urdp
// server's own desktop with the latency probe, then reports the probe's
// p50 and fails if no video arrives or latency exceeds a ceiling.
//
//   node scripts/e2e.mjs [url] [seconds] [max-p50-ms]
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const [url = 'http://localhost:8000/?probe', secs = '10', maxP50 = '200'] = process.argv.slice(2);
const chromeBin = process.env.CHROME || 'google-chrome';
const profile = mkdtempSync(join(tmpdir(), 'urdp-e2e-'));
const port = 9333;
const chrome = spawn(chromeBin, ['--kiosk', '--no-first-run', '--no-default-browser-check',
  '--autoplay-policy=no-user-gesture-required',
  `--user-data-dir=${profile}`, `--remote-debugging-port=${port}`, url], { stdio: 'ignore' });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const done = (code) => { chrome.kill(); rmSync(profile, { recursive: true, force: true }); process.exit(code); };

let ws;
for (let i = 0; i < 100 && !ws; i++) {
  await sleep(200);
  try {
    const page = (await (await fetch(`http://127.0.0.1:${port}/json`)).json()).find((t) => t.type === 'page');
    if (page) ws = new WebSocket(page.webSocketDebuggerUrl);
  } catch { /* not up yet */ }
}
if (!ws) { console.error('chrome did not start'); done(1); }
await new Promise((r) => ws.addEventListener('open', r));
let id = 0;
const pending = new Map();
ws.addEventListener('message', (e) => { const m = JSON.parse(e.data); pending.get(m.id)?.(m); });
const evaluate = (expression) => new Promise((r) => {
  const i = ++id;
  pending.set(i, (m) => r(m.result?.result?.value));
  ws.send(JSON.stringify({ id: i, method: 'Runtime.evaluate', params: { expression, awaitPromise: true, returnByValue: true } }));
});

await sleep(1500);
await evaluate("document.getElementById('start').click()");
const p50s = [];
for (let i = 0; i < +secs; i++) {
  await sleep(1000);
  const stats = await evaluate("document.getElementById('stats').textContent");
  console.log(stats?.replace(/\n/g, ' | '));
  const m = /e2e p50 (\d+)ms/.exec(stats ?? '');
  if (m) p50s.push(+m[1]);
}
const width = await evaluate("document.getElementById('video').videoWidth");
if (!width) { console.error('no video decoded'); done(1); }
if (!p50s.length) { console.error('latency probe produced no samples'); done(1); }
const median = p50s.sort((a, b) => a - b)[p50s.length >> 1];
console.log(`median e2e p50: ${median}ms (ceiling ${maxP50}ms)`);
done(median <= +maxP50 ? 0 : 1);
