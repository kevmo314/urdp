#!/usr/bin/env python3
"""Check a TestLibavD3D11Moving dump: decode it and compare each frame with
the pattern the test drew (gray levels varying per 16-row and 64-column
band, shifting every frame), so swapped rows or blocks, stale frames or
wrong colors fail.

  scripts/check-moving.py <dump> <h264|hevc> [WxH]
"""
import subprocess, sys

path, fmt = sys.argv[1], sys.argv[2]
w, h = map(int, (sys.argv[3] if len(sys.argv) > 3 else "1920x1080").split("x"))
raw = subprocess.run(["ffmpeg", "-v", "error", "-f", fmt, "-i", path, "-f", "rawvideo", "-pix_fmt", "gray", "-"],
                     capture_output=True, check=True).stdout
n = len(raw) // (w * h)
worst = 0.0
for i in range(n):
    f = raw[i * w * h:(i + 1) * w * h]
    errs = [abs(f[y * w + x] - (32 + ((y // 16) * 37 + (x // 64) * 11 + i * 13) % 192))
            for y in range(8, h, 16) for x in range(32, w, 64)]
    worst = max(worst, sum(errs) / len(errs))
# Compression leaves ~7 levels of error at band edges; a layout bug is ~85.
ok = n == 60 and worst < 15
print(f"{path}: {n} frames, worst mean error {worst:.1f}: {'ok' if ok else 'FAIL'}")
sys.exit(0 if ok else 1)
