# Web Product UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Web panel correct under authentication, resilient across every async state, accessible on keyboard and mobile, and visually coherent as a Quiet Instrument.

**Architecture:** Keep the dependency-free ES-module frontend and existing Go JSON APIs. Centralize request/download and view-state primitives in `web/api.js`, then migrate every route to those primitives before applying the shared visual system.

**Tech Stack:** Go 1.26 embedded filesystem, browser ES modules, CSS custom properties, ECharts.

## Global Constraints

- Preserve the current route URLs and JSON response compatibility.
- `web/tokens.css` is the source of truth for shared tokens; run `make desktop-sync` after changing it.
- Body copy uses the repository's current Chinese convention; identifiers and tests use English.
- Every async view distinguishes loading, empty, unauthorized, error, stale, and ready.
- Tables remain usable at 390 px without page-level horizontal overflow.

---

### Task 1: Authenticated requests and report downloads

**Files:**
- Modify: `web/api.js`
- Modify: `web/reports.js`
- Create: `web/embed_test.go`

**Interfaces:**
- Produces: `apiFetch(path, init)`, `downloadAPI(path, filename)`, and `APIError.status`.
- Consumes: the existing `omnitoken_token` local-storage key.

- [ ] Add a failing embedded-asset contract test that requires reports to call `downloadAPI` and forbids direct `/api/v1/reports` anchors.
- [ ] Run `go test ./web`; expect the new assertion to fail.
- [ ] Implement `downloadAPI` with authenticated `fetch`, `Blob`, object-URL download, filename cleanup, URL revocation, and `APIError` propagation.
- [ ] Replace the report anchors with buttons that call `downloadAPI`.
- [ ] Run `go test ./web && make check`; expect both to pass.
- [ ] Commit with `fix(web): authenticate report downloads`.

### Task 2: Unified view-state and draft-safe settings

**Files:**
- Modify: `web/api.js`
- Modify: `web/app.js`
- Modify: `web/settingsview.js`
- Modify: `web/overview.js`
- Modify: `web/speedview.js`
- Modify: `web/cacheview.js`
- Modify: `web/devicesview.js`
- Modify: `web/modelsview.js`
- Test: `web/embed_test.go`

**Interfaces:**
- Produces: `renderState(container, {kind, title, detail, action})` and `classifyAPIError(error)`.
- Consumes: `APIError.status` from Task 1.

- [ ] Add failing asset tests for an `aria-live` state region and settings draft listeners that do not depend on rerender.
- [ ] Run `go test ./web`; expect failure.
- [ ] Add reusable state rendering, including explicit 401 copy and retry actions.
- [ ] Migrate first-load and refresh failures on all listed routes so old data becomes visibly stale and an empty body never remains.
- [ ] Keep settings form values in a module draft object, update it on `input`, and clear it only after a successful save.
- [ ] After token save, immediately refresh health/auth state and remove a stale banner.
- [ ] Run `go test ./web && make check`; expect pass.
- [ ] Commit with `fix(web): unify async states and preserve drafts`.

### Task 3: Semantic correctness and accessible interaction

**Files:**
- Modify: `web/app.js`
- Modify: `web/live.js`
- Modify: `web/details.js`
- Modify: `web/heatmap.js`
- Modify: `web/index.html`
- Test: `web/embed_test.go`

**Interfaces:**
- Produces: route links with real `href`, labeled controls, ECharts `aria.enabled = true`.

- [ ] Add failing assertions for removal of `这台机器` from fleet aggregates, real detail links, named filters, and ECharts aria.
- [ ] Run `go test ./web`; expect failure.
- [ ] Rename fleet-wide totals to `全部设备` and use `当前设备` only where a device filter actually exists.
- [ ] Turn session drill-down controls into real hash links that support keyboard activation and open-in-new-tab semantics.
- [ ] Add `<label>` or `aria-label` to every filter/input and expose daily heatmap details without hover.
- [ ] Show `另有 N 个会话` after the eight visible live lanes.
- [ ] Run `go test ./web && make check`; expect pass.
- [ ] Commit with `fix(web): correct fleet semantics and accessibility`.

### Task 4: Quiet Instrument visual system and responsive navigation

**Files:**
- Modify: `web/tokens.css`
- Modify: `web/style.css`
- Modify: `web/index.html`
- Modify: `desktop/ui/tokens.css` via `make desktop-sync`
- Test: `web/embed_test.go`

**Interfaces:**
- Produces: system-sans text tokens, mono numeric tokens, sticky scrollable navigation, analytical and live surface variants.

- [ ] Add asset tests that require font-role tokens, sticky mobile navigation, focus-visible styles, and reduced-motion handling.
- [ ] Run `go test ./web`; expect failure.
- [ ] Replace global monospace typography with system sans; retain mono for values, IDs, code, and timestamps.
- [ ] Reserve glass/elevation for Live and Speed; make Overview, Reports, Details, Devices, Models, Cache, and Settings flatter and denser.
- [ ] Add strong page-title/section/label/value hierarchy and visible keyboard focus.
- [ ] Make the mobile nav sticky, auto-scroll the active item into view, and adapt tables/cards for 390 px.
- [ ] Run `make desktop-sync && go test ./web && make desktop-check`; expect pass.
- [ ] Commit with `feat(web): apply quiet instrument design system`.

### Task 5: Web visual acceptance

**Files:**
- Modify only files required by defects found during verification.

- [ ] Start a disposable server with a temporary config/database.
- [ ] Inspect `/`, `#/speed`, `#/overview`, `#/reports`, `#/details`, `#/devices`, `#/models`, `#/cache`, and `#/settings` at 1440×1000 and 390×844.
- [ ] Verify keyboard navigation, active-nav visibility, no page overflow, error/retry states, settings draft survival, and authenticated CSV/JSON download.
- [ ] Fix concrete defects with a regression assertion in `web/embed_test.go`.
- [ ] Run `make check && make desktop-check`; expect pass.
- [ ] Commit with `test(web): close visual acceptance findings` only if verification required changes.
