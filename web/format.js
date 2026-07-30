"use strict";
// Panel-side formatting helpers: the ones that need the DOM or ECharts.
//
// The pure number/time formatters moved to format-core.js, which the menubar
// popover shares verbatim (ADR-0014). Nothing here can go there — every
// function below reads computed styles or talks to a chart instance.

function cssVar(name) {
  return getComputedStyle(document.querySelector(".viz-root")).getPropertyValue(name).trim();
}

// ── Chart helpers (ADR-0010) ────────────────────────────────────────────
// ECharts knows nothing about the panel's tokens, so every chart reads its
// colours through these rather than hard-coding hex. One place to change when
// the theme flips, and no chart can drift out of the palette.

const _charts = new WeakMap();

// One instance per element, reused across renders. Disposing and recreating on
// every poll would throw away the animation state and leak canvases.
function echartsFor(el) {
  let c = _charts.get(el);
  if (!c || c.isDisposed()) {
    c = echarts.init(el, null, { renderer: "canvas" });
    _charts.set(el, c);
    // Charts live in a fluid grid, so they have to follow their container
    // rather than the window's breakpoints.
    if (typeof ResizeObserver !== "undefined") {
      new ResizeObserver(() => c.resize()).observe(el);
    }
  }
  return c;
}

// Views are toggled with `hidden`, so a chart built for a view that was not on
// screen measures its container as 0 wide and draws nothing. ECharts only
// re-measures when told to, so every view re-measures its charts on entry.
function resizeChartsIn(root) {
  if (!root || typeof echarts === "undefined") return;
  root.querySelectorAll("div").forEach((el) => {
    const inst = echarts.getInstanceByDom(el);
    if (inst) inst.resize();
  });
}

function chartFont() {
  return getComputedStyle(document.querySelector(".viz-root")).fontFamily;
}

// Vertical fade of one hue: solid where the value is, softer at the base, so a
// stack has depth instead of reading as flat blocks.
function gradientOf(hex) {
  return {
    type: "linear", x: 0, y: 0, x2: 0, y2: 1,
    colorStops: [
      { offset: 0, color: hex },
      { offset: 1, color: mixWithSurface(hex, 0.55) },
    ],
  };
}

// Blends toward transparency; ECharts accepts rgba, so no need to know the
// backdrop colour.
function mixWithSurface(hex, alpha) {
  const h = hex.trim().replace("#", "");
  const n = h.length === 3 ? h.split("").map((c) => c + c).join("") : h;
  const r = parseInt(n.slice(0, 2), 16), g = parseInt(n.slice(2, 4), 16), b = parseInt(n.slice(4, 6), 16);
  return `rgba(${r},${g},${b},${alpha})`;
}

function tooltipStyle() {
  return {
    backgroundColor: cssVar("--page"),
    borderColor: cssVar("--border-strong"),
    borderWidth: 1,
    padding: [8, 10],
    textStyle: { color: cssVar("--text-primary"), fontSize: 11, fontFamily: chartFont() },
    extraCssText: "border-radius:10px;box-shadow:0 12px 32px rgba(0,0,0,0.4);backdrop-filter:blur(12px);",
  };
}
