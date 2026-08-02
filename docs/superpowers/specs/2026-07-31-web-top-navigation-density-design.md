# OmniToken Web Top Navigation and Density Design

**Status:** Approved  
**Date:** 2026-07-31  
**Scope:** Web shell and default route only

## 1. Outcome

The Web application must open on the Overview page and use a compact top
navigation shell. The change reclaims the 208 px previously reserved for the
left rail and reduces the decorative gap between the browser viewport and the
Telemetry Studio surface.

This is a layout refinement of the approved A4 visual system. It does not
change telemetry definitions, API payloads, page content, or the menu bar
popover.

## 2. Default route

- An empty hash, an unknown hash, or the bare server URL resolves to
  `#overview`.
- The Overview navigation item receives `aria-current="page"`.
- Existing explicit routes such as `#live`, `#speed`, and `#reports` retain
  their current behavior.
- Router comments, tests, and fallback logic must agree that Overview is the
  default.

## 3. Desktop shell

At viewport widths above 860 px:

- `.viz-root` uses one content column; no width is reserved for a side rail.
- The shell header is a single horizontal row.
- The OmniToken wordmark remains at the left.
- All nine route links remain directly visible and are aligned to the right:
  Overview, Live, Speed, Reports, Details, Devices, Models, Cache, Settings.
- Navigation group labels are removed from the visible desktop header. Route
  order and icons provide sufficient scanning cues.
- The active route uses the existing A4 accent treatment with a bottom edge or
  compact pill highlight; it must not recreate a vertical rail indicator.
- Hub health and refresh age stay in the page heading, not in the navigation
  row.

## 4. Density

- Desktop `body` inset: 12 px on all sides.
- Shell width: `min(1600px, 100%)`.
- Shell minimum height: `calc(100vh - 24px)`.
- Main content horizontal padding: `clamp(14px, 1.4vw, 22px)`.
- The A4 24 px shell radius, dark palette, ambient gradients, source colors,
  and card hierarchy remain unchanged.

These values are intentionally tighter than the previous 28 px body inset and
32 px maximum content gutter. The result should feel close to the browser edge
without becoming full bleed on desktop.

## 5. Responsive behavior

At 860 px and below:

- The header stays sticky at the top.
- The wordmark remains fixed at the left.
- Route links form one horizontally scrollable row with hidden scrollbars.
- The active route scrolls into view using the existing router behavior.
- Navigation links never wrap into a second row.
- The body inset becomes 8 px.

At 420 px and below:

- The body remains full bleed.
- The shell radius becomes 0 and inline borders are removed, matching the
  existing mobile contract.
- Main content retains 12 px horizontal padding.

## 6. Accessibility and interaction

- The navigation remains a semantic `<nav aria-label="主导航">`.
- Every route remains a direct anchor target and keyboard reachable.
- Focus indicators retain the A4 accent outline.
- The active route continues to use `aria-current="page"`.
- Horizontal scrolling must not create page-level horizontal overflow.

## 7. Verification

Automated contracts must verify:

1. the router fallback is Overview;
2. the Web shell has no two-column rail allocation;
3. desktop navigation is horizontal and right aligned;
4. desktop body inset is 12 px;
5. the shell width cap is 1600 px;
6. mobile navigation remains horizontally scrollable.

Browser acceptance must cover:

- bare `/` opens Overview;
- all nine routes remain directly selectable;
- no horizontal page overflow at desktop and narrow viewport widths;
- the shell remains visually aligned with Telemetry Studio A4.
