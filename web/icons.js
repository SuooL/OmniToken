"use strict";
// Inline icon set, drawn in the Lucide style (24px grid, 2px stroke, round
// caps). Vendored as paths rather than pulled from a CDN or an npm package:
// the panel ships inside the Go binary and has to render on a headless box
// with no internet, and the project has no build step to bundle one with.
//
// Stroke inherits currentColor, so an icon takes the colour of whatever it
// sits in — that is what lets the nav tint each entry differently without a
// second copy of every glyph.

const ICON_PATHS = {
  // Live: a pulse, because the page is about what is happening right now.
  live: '<path d="M3 12h4l3 8 4-16 3 8h4"/>',
  speed: '<path d="M12 14a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z"/><path d="m13.4 10.6 4.6-4.6"/><path d="M12 3a9 9 0 0 1 7.4 14.1"/><path d="M4.6 17.1A9 9 0 0 1 12 3"/>',
  overview: '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="m7 14 3-4 3 3 4-6"/>',
  reports: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/><path d="M8 13h8"/><path d="M8 17h5"/>',
  details: '<path d="M3 6h18"/><path d="M3 12h18"/><path d="M3 18h12"/>',
  devices: '<rect x="2" y="4" width="14" height="10" rx="2"/><path d="M6 18h8"/><rect x="18" y="9" width="4" height="11" rx="1"/>',
  models: '<path d="M12 2 3 7v10l9 5 9-5V7Z"/><path d="m3 7 9 5 9-5"/><path d="M12 12v10"/>',
  cache: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 9 19.4a1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 4.6 9a1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1Z"/>',

  burn: '<path d="M12 2s4 4.5 4 8a4 4 0 0 1-8 0c0-1.3.6-2.6 1.3-3.7"/><path d="M12 22a6 6 0 0 0 6-6c0-2-1-3.5-2-4.5"/><path d="M12 22a6 6 0 0 1-6-6c0-1 .3-2 .8-2.8"/>',
  quota: '<path d="M12 3a9 9 0 1 0 9 9h-9Z"/><path d="M12 3v9h9A9 9 0 0 0 12 3Z"/>',
  session: '<rect x="3" y="4" width="18" height="14" rx="2"/><path d="m7 9 3 2-3 2"/><path d="M12 13h4"/>',
};

// Returns an <svg> string. `cls` lets callers size or tint it from CSS.
function icon(name, cls) {
  const d = ICON_PATHS[name];
  if (!d) return "";
  return `<svg class="ico ${cls || ""}" viewBox="0 0 24 24" fill="none"
    stroke="currentColor" stroke-width="2" stroke-linecap="round"
    stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
}
