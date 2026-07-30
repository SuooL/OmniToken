# Menu-Bar Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the menu-bar popover into an activity-first instrument that preserves trustworthy last-known data and promotes quota risk only when action is required.

**Architecture:** Rust remains the authenticated transport and snapshot cache; the webview remains a pure renderer. Extend the transport state sent to JavaScript, then reorganize the existing overview/live payload into hero, active-session, quota-risk, device, and action sections.

**Tech Stack:** Tauri 2, Rust, dependency-free browser JavaScript/CSS.

## Global Constraints

- Default width is 380 px; normal content height stays between 280 and 420 px and never exceeds 520 px.
- Never render a failed `/overview` request as zero usage.
- Preserve last good data with age and one of live, polling, stale, unauthorized, or offline.
- Do not display secrets back to JavaScript after settings are saved.

---

### Task 1: Transport freshness and last-good snapshot

**Files:**
- Modify: `desktop/src-tauri/src/live.rs`
- Modify: `desktop/src-tauri/src/lib.rs`
- Test: `desktop/src-tauri/src/live.rs`

**Interfaces:**
- Produces: `ConnectionState { kind, last_success_at_ms, error }` and a snapshot that survives transient failures.

- [ ] Add Rust tests proving a failed refresh retains the prior payload, increments age, and distinguishes unauthorized from offline.
- [ ] Run `cargo test live::tests`; expect failure.
- [ ] Replace destructive clear-on-error with explicit connection state and last-success timestamp.
- [ ] Make tray-title unknown/offline behavior use the same state rather than synthesized zeroes.
- [ ] Run `cargo test live::tests`; expect pass.
- [ ] Commit with `fix(desktop): preserve last good snapshot`.

### Task 2: Settings credential validation

**Files:**
- Modify: `desktop/src-tauri/src/settings.rs`
- Modify: `desktop/src-tauri/src/lib.rs`
- Modify: `desktop/ui/app.js`
- Test: `desktop/src-tauri/src/settings.rs`

**Interfaces:**
- Produces: authenticated server probe against `/api/v1/overview?days=1`; settings response exposes `has_token`, never the token.

- [ ] Add tests for valid, invalid, and missing bearer credentials and for redacted serialized settings.
- [ ] Run the focused Rust tests; expect failure.
- [ ] Validate address and token together before replacing persisted settings.
- [ ] Return `has_token` to the webview and keep an existing credential when the token field is blank.
- [ ] Run focused tests and `make desktop-check`; expect pass.
- [ ] Commit with `fix(desktop): validate and redact credentials`.

### Task 3: Activity-first composition

**Files:**
- Modify: `desktop/ui/index.html`
- Modify: `desktop/ui/app.js`
- Modify: `desktop/ui/style.css`
- Modify: `desktop/src-tauri/src/live.rs`
- Test: `desktop/src-tauri/src/live.rs`

**Interfaces:**
- Produces: hero text `Generating {rate} · {count} sessions`, top-session rows, risk promotion, and truthful device summaries.

- [ ] Add Rust fixture tests for active, idle, risk, stale, and unknown view models.
- [ ] Run focused tests; expect failure.
- [ ] Derive a deterministic popover view model from overview/live payloads and generated timestamps.
- [ ] Render hero, up to three top sessions with tool/repo/model/device/rate, condensed quota, risk timeline, devices, and actions.
- [ ] Promote quota above sessions only when projected use exceeds 100% or reset is urgent.
- [ ] Show `N more` for truncated sessions/devices.
- [ ] Run `make desktop-check`; expect pass.
- [ ] Commit with `feat(desktop): make popover activity first`.

### Task 4: Geometry, accessibility, and visual verification

**Files:**
- Modify: `desktop/ui/style.css`
- Modify: `desktop/ui/app.js`
- Modify: `desktop/src-tauri/tauri.conf.json` if window constraints require it.

- [ ] Apply 380 px geometry, 13 px minimum metadata, system-sans text, mono values, visible focus, reduced motion, and contrast-safe state colors.
- [ ] Verify the popover in live, idle, stale, unauthorized, offline, quota-risk, long-device-name, and more-than-three-session fixtures.
- [ ] Confirm every action is keyboard reachable and the popover never clips above 520 px.
- [ ] Run `make desktop-check`; expect pass.
- [ ] Commit with `feat(desktop): polish popover layout and states`.
