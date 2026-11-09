#!/usr/bin/env python3
"""Check a TestLibavD3D11Moving dump: decode it and compare each frame with
the pattern the test drew (gray levels varying per 16-row and 64-column
band, shifting every frame), so swapped rows or blocks, stale frames or
wrong colors fail.

  scripts/check-moving.py <dump> <h264|hevc> [WxH] [scale]

WxH is the size the pattern was drawn at; with scale (2 or 4), the dump
was encoded at 1/scale of that (from a mip level).
"""
import subprocess, sys

path, fmt = sys.argv[1], sys.argv[2]
W, H = map(int, (sys.argv[3] if len(sys.argv) > 3 else "1920x1080").split("x"))
scale = int(sys.argv[4]) if len(sys.argv) > 4 else 1
w, h = W // scale, H // scale
raw = subprocess.run(["ffmpeg", "-v", "error", "-f", fmt, "-i", path, "-f", "rawvideo", "-pix_fmt", "gray", "-"],
                     capture_output=True, check=True).stdout
n = len(raw) // (w * h)
worst = 0.0
for i in range(n):
    f = raw[i * w * h:(i + 1) * w * h]
    # Sample the middle of each pattern band, in encoded coordinates.
    errs = [abs(f[(Y // scale) * w + X // scale] - (32 + ((Y // 16) * 37 + (X // 64) * 11 + i * 13) % 192))
            for Y in range(8, H - 8, 16) for X in range(32, W - 32, 64)]
    worst = max(worst, sum(errs) / len(errs))
# Compression leaves ~7 levels of error at band edges; a layout bug is ~85.
ok = n == 60 and worst < 15
print(f"{path}: {n} frames, worst mean error {worst:.1f}: {'ok' if ok else 'FAIL'}")
sys.exit(0 if ok else 1)
