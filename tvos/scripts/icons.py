"""Generates the tvOS app icon layers and top shelf images.

    python3 tvos/scripts/icons.py
"""
import json, os
from PIL import Image, ImageDraw, ImageFont

ROOT = os.path.join(os.path.dirname(__file__), "..", "urdp", "Assets.xcassets")
BRAND = os.path.join(ROOT, "App Icon & Top Shelf Image.brandassets")
FONT = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"
INFO = {"author": "xcode", "version": 1}


def write_json(path, obj):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f, indent=2)
        f.write("\n")


def gradient(w, h):
    top, bottom = (24, 38, 74), (8, 12, 26)
    img = Image.new("RGB", (w, h))
    d = ImageDraw.Draw(img)
    for y in range(h):
        t = y / max(1, h - 1)
        d.line([(0, y), (w, y)], fill=tuple(round(a + (b - a) * t) for a, b in zip(top, bottom)))
    return img


def monitor(w, h):
    """Front layer: a monitor with a pointer on it, on transparent."""
    img = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    s = h / 240
    mw, mh = 210 * s, 128 * s
    x0, y0 = (w - mw) / 2, (h - mh) / 2 - 18 * s
    lw = max(2, round(9 * s))
    d.rounded_rectangle([x0, y0, x0 + mw, y0 + mh], radius=14 * s, outline=(255, 255, 255, 255), width=lw,
                        fill=(60, 140, 255, 70))
    # stand
    cx = w / 2
    d.polygon([(cx - 18 * s, y0 + mh), (cx + 18 * s, y0 + mh), (cx + 26 * s, y0 + mh + 26 * s), (cx - 26 * s, y0 + mh + 26 * s)],
              fill=(255, 255, 255, 255))
    d.rounded_rectangle([cx - 52 * s, y0 + mh + 24 * s, cx + 52 * s, y0 + mh + 34 * s], radius=4 * s, fill=(255, 255, 255, 255))
    # pointer arrow inside the screen
    px, py, k = x0 + mw * 0.56, y0 + mh * 0.30, 2.2 * s
    arrow = [(0, 0), (0, 28), (7, 21.5), (12, 33), (17, 31), (12, 19.5), (21, 19.5)]
    d.polygon([(px + x * k, py + y * k) for x, y in arrow], fill=(255, 255, 255, 255), outline=(10, 20, 40, 255))
    return img



def imagestack(name, sizes):
    """sizes: list of (scale, w, h)."""
    stack = os.path.join(BRAND, f"{name}.imagestack")
    write_json(os.path.join(stack, "Contents.json"),
               {"info": INFO, "layers": [{"filename": "Front.imagestacklayer"}, {"filename": "Back.imagestacklayer"}]})
    for layer, draw in (("Front", monitor), ("Back", gradient)):
        d = os.path.join(stack, f"{layer}.imagestacklayer")
        write_json(os.path.join(d, "Contents.json"), {"info": INFO})
        images = []
        for scale, w, h in sizes:
            fn = f"{layer.lower()}{'' if scale == 1 else f'@{scale}x'}.png"
            os.makedirs(os.path.join(d, "Content.imageset"), exist_ok=True)
            draw(w, h).save(os.path.join(d, "Content.imageset", fn))
            images.append({"filename": fn, "idiom": "tv", "scale": f"{scale}x"})
        write_json(os.path.join(d, "Content.imageset", "Contents.json"), {"images": images, "info": INFO})


def shelf(name, base_w, base_h):
    d = os.path.join(BRAND, f"{name}.imageset")
    os.makedirs(d, exist_ok=True)
    images = []
    for scale in (1, 2):
        w, h = base_w * scale, base_h * scale
        img = gradient(w, h).convert("RGBA")
        icon = monitor(round(h * 0.9 * 1.1), round(h * 0.9))
        img.alpha_composite(icon, (round(w * 0.22), round(h * 0.05)))
        dr = ImageDraw.Draw(img)
        f1 = ImageFont.truetype(FONT, round(120 * scale))
        f2 = ImageFont.truetype(FONT.replace("-Bold", ""), round(44 * scale))
        tx = round(w * 0.22 + icon.width + 30 * scale)
        dr.text((tx, round(h * 0.30)), "urdp", font=f1, fill=(255, 255, 255))
        dr.text((tx, round(h * 0.30 + 150 * scale)), "remote desktop", font=f2, fill=(170, 190, 230))
        fn = f"shelf{'' if scale == 1 else '@2x'}.png"
        img.convert("RGB").save(os.path.join(d, fn))
        images.append({"filename": fn, "idiom": "tv", "scale": f"{scale}x"})
    write_json(os.path.join(d, "Contents.json"), {"images": images, "info": INFO})


write_json(os.path.join(ROOT, "Contents.json"), {"info": INFO})
write_json(os.path.join(BRAND, "Contents.json"), {
    "assets": [
        {"filename": "App Icon - App Store.imagestack", "idiom": "tv", "role": "primary-app-icon", "size": "1280x768"},
        {"filename": "App Icon.imagestack", "idiom": "tv", "role": "primary-app-icon", "size": "400x240"},
        {"filename": "Top Shelf Image Wide.imageset", "idiom": "tv", "role": "top-shelf-image-wide", "size": "2320x720"},
        {"filename": "Top Shelf Image.imageset", "idiom": "tv", "role": "top-shelf-image", "size": "1920x720"},
    ],
    "info": INFO,
})
imagestack("App Icon", [(1, 400, 240), (2, 800, 480)])
imagestack("App Icon - App Store", [(1, 1280, 768)])
shelf("Top Shelf Image", 1920, 720)
shelf("Top Shelf Image Wide", 2320, 720)
