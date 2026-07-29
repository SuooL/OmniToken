#!/usr/bin/env python3
"""Generate the menubar gauge icons.

The tray icon shows how close the tightest quota is to its limit, so it has to
be legible at 16px and carry a reading without being opened. It is a macOS
*template* image: only the alpha channel survives, and the system paints it
black or white to match the menubar. Nothing here may depend on colour —
"filled" is expressed as opacity against a faint track, never as a hue.

Run from the repo root; writes into desktop/src-tauri/icons/tray/.

    python3 desktop/icons-src/gen-tray-icons.py
"""

import math
import os

from PIL import Image, ImageDraw

# Drawn large and downsampled: PIL's arc has no antialiasing of its own, and at
# this size an aliased curve looks like a staircase.
SS = 16

# One asset, at 36px. tray-icon normalises whatever it is given to an 18pt-tall
# NSImage (platform_impl/macos/mod.rs), so 36px is exactly 1:1 on a Retina
# menubar and halves cleanly on a 1x one. A 32px asset would be resampled to 36
# device pixels — a non-integer upscale, and it shows.
TRAY_PX = 36

# Speedometer sweep: starts bottom-left, runs clockwise over the top, ends
# bottom-right. The gap at the bottom is what makes it read as a gauge rather
# than a pie chart or a loading spinner.
START_DEG = 135.0
SWEEP_DEG = 270.0

TRACK_ALPHA = 70  # the unfilled remainder — present, but clearly background
FILL_ALPHA = 255

STROKE_F = 0.145  # stroke width as a fraction of the canvas
INSET_F = 0.01    # breathing room outside the stroke

# Fill buckets. Coarse on purpose: a menubar glyph that changes on every poll is
# noise, and nobody reads 63% off a 16px arc. Values are floored into these.
BUCKETS = (0, 25, 50, 75, 100)


def _geometry(n):
    stroke = max(1, round(STROKE_F * n))
    inset = stroke / 2 + INSET_F * n
    return stroke, (inset, inset, n - inset, n - inset)


def draw_gauge(px, pct):
    """A gauge filled to `pct`, at `px` logical pixels."""
    n = px * SS
    img = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    stroke, box = _geometry(n)

    # Track first, so the fill paints over its head.
    d.arc(box, START_DEG, START_DEG + SWEEP_DEG,
          fill=(0, 0, 0, TRACK_ALPHA), width=stroke)
    if pct > 0:
        d.arc(box, START_DEG, START_DEG + SWEEP_DEG * min(pct, 100) / 100.0,
              fill=(0, 0, 0, FILL_ALPHA), width=stroke)
    return img.resize((px, px), Image.LANCZOS)


def draw_offline(px):
    """The unreachable-server glyph: the same ring, broken into dashes.

    It cannot simply be an unfilled gauge — with no centre dot that is pixel
    for pixel the 0% icon, and "I cannot reach the server" would render as
    "you have used nothing". A broken ring reads as no signal.
    """
    n = px * SS
    img = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    stroke, box = _geometry(n)

    # Long dashes with tight gaps. Short evenly-spaced dashes scatter into
    # something that reads as a loading spinner, which says the wrong thing.
    dashes, ratio = 4, 3.0  # dash is 3x the gap
    dash = SWEEP_DEG / (dashes + (dashes - 1) / ratio)
    gap = dash / ratio
    for i in range(dashes):
        a = START_DEG + i * (dash + gap)
        d.arc(box, a, a + dash, fill=(0, 0, 0, TRACK_ALPHA + 40), width=stroke)
    return img.resize((px, px), Image.LANCZOS)


def main():
    out_dir = os.path.join("desktop", "src-tauri", "icons", "tray")
    os.makedirs(out_dir, exist_ok=True)

    written = []
    for pct in BUCKETS:
        name = f"gauge-{pct}.png"
        draw_gauge(TRAY_PX, pct).save(os.path.join(out_dir, name))
        written.append(name)
    draw_offline(TRAY_PX).save(os.path.join(out_dir, "gauge-offline.png"))
    written.append("gauge-offline.png")

    for name in written:
        print(os.path.join(out_dir, name))


if __name__ == "__main__":
    main()
