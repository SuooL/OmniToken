#!/usr/bin/env python3
"""Generate the app icon master (1024px), from which Tauri derives the rest.

Same gauge as the menubar glyph, so the Finder icon and the tray icon read as
one product rather than two. Where the tray icon must be a colourless template,
this one is free to carry the panel's blue.

Run from the repo root, then hand the result to the Tauri CLI:

    python3 desktop/icons-src/gen-app-icon.py
    cargo tauri icon desktop/icons-src/app-icon-1024.png -o desktop/src-tauri/icons
"""

import math
import os

from PIL import Image, ImageDraw, ImageFilter

N = 1024

# Apple's icon grid: the shape occupies 824 of the 1024 canvas, leaving the
# margin the system expects for shadow and optical alignment. An icon drawn
# edge to edge sits visibly larger than its neighbours in the Dock and Finder.
SHAPE = 824
MARGIN = (N - SHAPE) // 2
CORNER_N = 5.0  # superellipse exponent — the "squircle" macOS actually uses

# The panel's accent (#2a78d6) as the midpoint, opened into a gradient so the
# icon has some depth at large sizes without resorting to gloss.
TOP = (74, 155, 240)
BOTTOM = (26, 84, 163)

# Matches gen-tray-icons.py: opening at the bottom, filling clockwise.
START_DEG = 135.0
SWEEP_DEG = 270.0
# A static icon still has to pick a reading. Around two-thirds reads as a gauge
# in use — 100% would say "you are out", and a bare track would look unfinished.
SHOWN_PCT = 68


def squircle_mask(size, exponent=CORNER_N):
    """Alpha mask for a superellipse, supersampled so the edge is clean."""
    ss = 4
    n = size * ss
    mask = Image.new("L", (n, n), 0)
    d = ImageDraw.Draw(mask)

    pts = []
    steps = 2048
    r = n / 2
    for i in range(steps):
        t = 2 * math.pi * i / steps
        ct, st = math.cos(t), math.sin(t)
        # |x|^n + |y|^n = 1, parameterised so the curve stays smooth.
        x = r * math.copysign(abs(ct) ** (2 / exponent), ct)
        y = r * math.copysign(abs(st) ** (2 / exponent), st)
        pts.append((r + x, r + y))
    d.polygon(pts, fill=255)
    return mask.resize((size, size), Image.LANCZOS)


def vertical_gradient(size, top, bottom):
    grad = Image.new("RGB", (1, size))
    for y in range(size):
        f = y / max(1, size - 1)
        grad.putpixel((0, y), tuple(
            round(top[i] + (bottom[i] - top[i]) * f) for i in range(3)))
    return grad.resize((size, size), Image.BICUBIC)


def gauge_layer(size, pct):
    """The white arc, drawn oversized and downsampled for a clean curve."""
    ss = 4
    n = size * ss
    layer = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)

    stroke = round(0.085 * n)
    radius = 0.29 * n
    c = n / 2
    box = (c - radius, c - radius, c + radius, c + radius)

    d.arc(box, START_DEG, START_DEG + SWEEP_DEG,
          fill=(255, 255, 255, 90), width=stroke)
    d.arc(box, START_DEG, START_DEG + SWEEP_DEG * pct / 100.0,
          fill=(255, 255, 255, 255), width=stroke)
    return layer.resize((size, size), Image.LANCZOS)


def main():
    icon = Image.new("RGBA", (N, N), (0, 0, 0, 0))

    shape = Image.new("RGBA", (SHAPE, SHAPE), (0, 0, 0, 0))
    shape.paste(vertical_gradient(SHAPE, TOP, BOTTOM), (0, 0))
    shape.putalpha(squircle_mask(SHAPE))

    # A soft drop shadow, which is what keeps the icon from looking pasted on
    # in Finder's light mode.
    shadow = Image.new("RGBA", (N, N), (0, 0, 0, 0))
    shadow.paste((0, 0, 0, 90), (MARGIN, MARGIN + 12), squircle_mask(SHAPE))
    icon = Image.alpha_composite(icon, shadow.filter(ImageFilter.GaussianBlur(16)))
    icon.alpha_composite(shape, (MARGIN, MARGIN))
    icon.alpha_composite(gauge_layer(N, SHOWN_PCT))

    out = os.path.join("desktop", "icons-src", "app-icon-1024.png")
    icon.save(out)
    print(out)


if __name__ == "__main__":
    main()
