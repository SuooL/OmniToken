# Telemetry Studio A2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a source-reconciled telemetry metric contract, visualization-first redesign of every Web page, and a rebuilt macOS menu-bar application that is installed and running.

**Architecture:** Keep the existing Go/SQLite server, embedded vanilla JavaScript Web client, vendored ECharts runtime, and Rust/Tauri shell. Add one presentation-neutral telemetry endpoint and additive contribution fields to live speed; both clients consume the same server-side calculations, while page modules remain independently loadable and preserve last-good data.

**Tech Stack:** Go 1.26, modernc SQLite, embedded vanilla JavaScript/CSS, ECharts 6.1.0, Rust 1.77.2+ compatible code, Tauri 2.

## Global Constraints

- Work only in `/Users/suool/git/OmniToken/.worktrees/telemetry-studio-a2` on `codex/telemetry-studio-a2`, based on `origin/dev`.
- Preserve the unrelated dirty `desktop/src-tauri/Cargo.toml` in `/Users/suool/git/OmniToken`; never copy or reset it.
- Never change event identity, token-count columns, attribution precedence, authentication boundaries, or loopback defaults.
- Speed totals and source/device/model/session rows use a shared global active-time denominator; native per-group speed is separately named and never summed.
- `aggregate_tps = Σ contribution_tps` before display rounding for every returned window or bucket.
- Unmeasured speed is unknown, not zero. Current Codex replay logs remain unmeasured until a reliable generation interval exists.
- Rolling five-hour usage and today usage still include Claude, Codex, and Other/API even when speed coverage differs.
- Web remains build-free and offline: no npm toolchain, CDN, framework, or runtime dependency is added.
- `web/tokens.css` and `web/format-core.js` remain the source of truth; run `make desktop-sync` after editing them.
- Every new behavior follows red-green-refactor and ends in a focused commit.
- Required final gates: `make check`, `make desktop-check`, `cargo tauri build --bundles app`, browser acceptance at desktop and mobile viewports, installed-app launch verification, and `git diff origin/dev...HEAD` review.

---

### Task 1: Record the metric and Git contracts

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/superpowers/specs/2026-07-30-telemetry-studio-a2-design.md`
- Create: `docs/adr/0017-additive-speed-contributions.md`
- Modify: `docs/API.md`

**Interfaces:**
- Consumes: ADR-0009 generation intervals and union semantics.
- Produces: the names `aggregate_tps`, `contribution_tps`, `native_tps`, `measured_sources`, and `unmeasured_sources` used by every later task.

- [ ] **Step 1: Write ADR-0017**

Record these equations verbatim:

```text
aggregate_tps = measured_output_tokens(all) / union_ms(all measured intervals) * 1000
contribution_tps(group) = measured_output_tokens(group) / union_ms(all measured intervals) * 1000
native_tps(group) = measured_output_tokens(group) / union_ms(group intervals) * 1000
aggregate_tps = Σ contribution_tps(group)
```

Include sequential, fully concurrent, and partially overlapping examples. State that the old `SessionSpeed.tps` is native speed and cannot populate a contributor ranking.

- [ ] **Step 2: Document source coverage**

Specify that Codex usage remains available while Codex speed is unavailable with current replay timestamps. Require explicit `unmeasured_sources` instead of fabricated zeroes or bars.

- [ ] **Step 3: Verify documentation consistency**

Run:

```bash
rg -n "aggregate_tps|contribution_tps|native_tps|unmeasured_sources" \
  docs/adr/0017-additive-speed-contributions.md \
  docs/superpowers/specs/2026-07-30-telemetry-studio-a2-design.md \
  docs/API.md
git diff --check
```

Expected: every term appears in the ADR and API; `git diff --check` exits 0.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/adr/0017-additive-speed-contributions.md \
  docs/API.md docs/superpowers/specs/2026-07-30-telemetry-studio-a2-design.md \
  docs/superpowers/plans/2026-07-30-telemetry-studio-a2.md
git commit -m "docs: define additive telemetry and git workflow"
```

### Task 2: Make live speed decompositions additive

**Files:**
- Modify: `internal/store/livespeed.go`
- Modify: `internal/store/livespeed_test.go`
- Modify: `internal/server/live_test.go`
- Modify: `web/live.js`
- Modify: `web/style.css`

**Interfaces:**
- Consumes: event intervals `[ts-gen_ms, ts]`.
- Produces:

```go
type SpeedContribution struct {
    Key             string  `json:"key"`
    OutputTokens    int64   `json:"output_tokens"`
    ActiveMS        int64   `json:"active_ms"`
    ContributionTPS float64 `json:"contribution_tps"`
    NativeTPS       float64 `json:"native_tps"`
}
```

`SessionSpeed` adds `ContributionTPS`; `TPS` remains native for compatibility. `LiveSpeed` adds `Sources`, `Devices`, and `Models`.

- [ ] **Step 1: Write failing sequential-session test**

Add:

```go
func TestLiveSpeedSequentialSessionsReconcileContributions(t *testing.T) {
    st := speedStore(t)
    now := time.Now()
    evs := []model.Event{
        gen("a", "s1", now.Add(-10*time.Second), 10_000, 1_000),
        gen("b", "s2", now, 10_000, 1_000),
    }
    _, _ = st.InsertEvents(evs, now.UnixMilli())
    got, err := st.LiveSpeedSince(now.Add(-20*time.Second), now, "")
    if err != nil { t.Fatal(err) }
    if got.TPS != 100 { t.Fatalf("aggregate=%v want 100", got.TPS) }
    sum := 0.0
    for _, row := range got.Sessions { sum += row.ContributionTPS }
    if sum != got.TPS { t.Fatalf("sum=%v aggregate=%v", sum, got.TPS) }
    if got.Sessions[0].TPS != 100 || got.Sessions[1].TPS != 100 {
        t.Fatal("native session speed must remain 100")
    }
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./internal/store -run TestLiveSpeedSequentialSessionsReconcileContributions -count=1
```

Expected: compile failure because `ContributionTPS` does not exist.

- [ ] **Step 3: Add common-denominator contributions**

After computing `out.ActiveMS`, set every session contribution from its tokens and `out.ActiveMS`. Fold sessions into canonical source, device, and model maps, keeping per-group merged spans for `NativeTPS`.

- [ ] **Step 4: Add concurrent and partial-overlap tests**

Assert:

```text
fully concurrent: native 100 + 100; contribution 100 + 100; aggregate 200
sequential: native 100 + 100; contribution 50 + 50; aggregate 100
partial overlap: every contribution sum equals aggregate within 1e-9
```

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./internal/store ./internal/server -run 'LiveSpeed|LivePayload' -count=1
```

Expected: PASS.

- [ ] **Step 6: Correct the existing Web live lanes**

Render `session.contribution_tps` as “贡献” and retain `session.tps` only in an accessible detail string labelled “会话自身速度.” Replace the reversed concurrency copy with:

```text
总吞吐按全局活跃时间计算；各行贡献使用同一分母，因此可以相加。
```

- [ ] **Step 7: Commit**

```bash
git add internal/store/livespeed.go internal/store/livespeed_test.go \
  internal/server/live_test.go web/live.js web/style.css
git commit -m "fix: reconcile live speed contributions"
```

### Task 3: Add the bounded telemetry endpoint

**Files:**
- Create: `internal/store/telemetry.go`
- Create: `internal/store/telemetry_test.go`
- Create: `internal/server/telemetry.go`
- Create: `internal/server/telemetry_test.go`
- Modify: `internal/server/server.go`
- Modify: `docs/API.md`

**Interfaces:**
- Produces `GET /api/v1/telemetry?range=5h|1h|24h`.
- The response contains `today`, `rolling_5h`, `speed`, `generated_at`, and `timezone`.

```go
type TelemetryBucket struct {
    StartMS      int64               `json:"start_ms"`
    AggregateTPS float64             `json:"aggregate_tps"`
    ActiveMS     int64               `json:"active_ms"`
    Sources      []SpeedContribution `json:"sources"`
}
```

- [ ] **Step 1: Write failing store tests for today and five-hour totals**

Seed events immediately before and after local midnight, `now-5h`, and
`now-10h`. Assert today model rows sum exactly to today total and current/
previous source windows do not leak boundary events.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/store -run Telemetry -count=1
```

Expected: compile failure because telemetry queries do not exist.

- [ ] **Step 3: Implement usage queries**

Use SQL sums of the canonical token expression already used by overview. Fold
model identifiers through `model.CanonicalModel`, sort descending by tokens
then ascending key, and return every non-zero model.

- [ ] **Step 4: Write failing speed bucket tests**

Test token allocation across bucket boundaries and these invariants:

```go
for _, bucket := range got {
    sum := 0.0
    for _, source := range bucket.Sources {
        sum += source.ContributionTPS
    }
    if math.Abs(sum-bucket.AggregateTPS) > 1e-9 { t.Fatal(...) }
}
```

- [ ] **Step 5: Implement source contribution buckets**

Allocate event tokens proportionally across touched intervals, compute the
global union once per bucket, and divide every source token count by that same
denominator. Return a zero bucket only after a successful query; return Codex
in `unmeasured_sources` when usage exists without measured intervals.

- [ ] **Step 6: Write failing handler contract tests**

Test bearer read authentication, `range` validation, complete model rows,
deterministic source ordering, response size below 512 KiB, and reconciliation.

- [ ] **Step 7: Register and implement the endpoint**

Add:

```go
mux.HandleFunc("GET /api/v1/telemetry", s.readAuth(s.handleTelemetry))
```

Use `s.currentTime()` rather than `time.Now()` so boundary tests are deterministic.

- [ ] **Step 8: Verify GREEN**

Run:

```bash
go test ./internal/store ./internal/server -run Telemetry -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/store/telemetry.go internal/store/telemetry_test.go \
  internal/server/telemetry.go internal/server/telemetry_test.go \
  internal/server/server.go docs/API.md
git commit -m "feat: add reconciled telemetry endpoint"
```

### Task 4: Build the shared Web visual system and shell

**Files:**
- Modify: `web/tokens.css`
- Modify: `web/style.css`
- Modify: `web/index.html`
- Modify: `web/icons.js`
- Create: `web/charts.js`
- Create: `web/telemetry.js`
- Modify: `web/api.js`
- Modify: `web/app.js`
- Modify: `web/semantic_test.go`
- Modify: `web/embed_test.go`

**Interfaces:**
- Produces `TelemetryCache`, `ChartRegistry`, `.instrument-card`,
  `.metric-card`, `.chart-card`, `.composition-strip`, `.data-table-shell`,
  `.state-panel`, and the shared chart palette.

- [ ] **Step 1: Write failing embedded-asset tests**

Require `charts.js` and `telemetry.js` in `web/embed.go`, one `OmniToken`
wordmark in `index.html`, one global health element, a skip link, and
`aria-current` routing.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./web -run 'Embedded|Shell|Semantic' -count=1
```

Expected: FAIL because the new assets and shell contracts are absent.

- [ ] **Step 3: Implement shared data and chart modules**

`TelemetryCache.load(range)` uses generation counters and preserves last-good
data. `ChartRegistry` owns ECharts instances, resize, disposal, shared tooltip,
palette, reduced-motion behavior, and accessible fallback tables.

- [ ] **Step 4: Rebuild the shell**

Keep hash routes stable. Use one responsive rail/top bar, one product wordmark,
page title/subtitle, health/freshness utility, and a constrained content width.
At `max-width: 860px`, navigation becomes horizontally scrollable without
body overflow.

- [ ] **Step 5: Implement the visual tokens**

Use deep graphite/indigo surfaces, warm Claude orange, Codex blue, API violet,
mint health, tabular numerals, AA contrast, visible focus, and reduced motion.
Avoid page-level decorative gradients behind every card.

- [ ] **Step 6: Verify GREEN and sync shared assets**

Run:

```bash
go test ./web -count=1
make desktop-sync
make desktop-sync-check
```

Expected: PASS and “shared ui: in sync”.

- [ ] **Step 7: Commit**

```bash
git add web desktop/ui/tokens.css desktop/ui/format-core.js
git commit -m "feat: establish telemetry visual system"
```

### Task 5: Rebuild the Web Overview and Live pages

**Files:**
- Modify: `web/overview.js`
- Modify: `web/live.js`
- Modify: `web/index.html`
- Modify: `web/style.css`
- Modify: `web/semantic_test.go`

**Interfaces:**
- Consumes `TelemetryCache` plus `/api/v1/stream`.
- Produces the approved A2 overview and source/session live instrument.

- [ ] **Step 1: Write failing semantic behavior tests**

Assert that Overview contains today total, rolling-five-hour total, Claude,
Codex, fleet coverage, separate source lanes, complete daily model composition,
and no standalone source donut. Assert that Live contributor rows use
`contribution_tps`.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./web -run 'Overview|Live|Telemetry' -count=1
```

Expected: FAIL on missing A2 contracts.

- [ ] **Step 3: Implement Overview**

Render:

```text
summary row
source-separated five-hour contribution lanes
current contributor ranking
complete today model composition
device throughput and health
current model contributions
```

All sources included in aggregate speed must have a visible lane. When Codex is
unmeasured, keep its five-hour usage card but render a labelled unavailable
speed lane.

- [ ] **Step 4: Implement Live**

Keep quota and process information but move current aggregate speed and aligned
contribution lanes first. Separate current/near-real-time values from the
ten-minute aggregate label.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./web -run 'Overview|Live|Telemetry' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/overview.js web/live.js web/index.html web/style.css web/semantic_test.go
git commit -m "feat: rebuild overview and live telemetry"
```

### Task 6: Rebuild the analytical Web pages

**Files:**
- Modify: `web/speedview.js`
- Modify: `web/modelsview.js`
- Modify: `web/devicesview.js`
- Modify: `web/cacheview.js`
- Modify: `web/heatmap.js`
- Modify: `web/style.css`
- Modify: `web/semantic_test.go`

**Interfaces:**
- Consumes existing `/speed`, `/models`, `/devices`, `/cache`, `/heatmap` APIs
  plus shared chart primitives.
- Produces five page-specific analytical narratives without changing routes.

- [ ] **Step 1: Write failing page-contract tests**

Require:

```text
Speed: source contribution lanes, measured coverage, native-vs-contribution explanation
Models: complete ranked distribution, cost/token relationship, active contribution
Devices: throughput, health, backlog, trend, registered identity
Cache: hit/creation/read composition, savings, TTL structure, model comparison
Heatmap: calendar plus keyboard-readable day table
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./web -run 'Speed|Models|Devices|Cache|Heatmap' -count=1
```

Expected: FAIL on the new semantic contracts.

- [ ] **Step 3: Rebuild each page using shared chart primitives**

Use one primary visualization, a compact summary row, one explanatory
breakdown, and one exploration table per page. Preserve classified
loading/empty/stale/unauthorized states and async-generation guards.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./web -run 'Speed|Models|Devices|Cache|Heatmap' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/speedview.js web/modelsview.js web/devicesview.js \
  web/cacheview.js web/heatmap.js web/style.css web/semantic_test.go
git commit -m "feat: rebuild analytical web pages"
```

### Task 7: Rebuild the operational Web pages

**Files:**
- Modify: `web/reports.js`
- Modify: `web/details.js`
- Modify: `web/settingsview.js`
- Modify: `web/style.css`
- Modify: `web/embed_test.go`
- Modify: `web/semantic_test.go`

**Interfaces:**
- Preserves report downloads, hash-restorable event filters, settings revision
  guards, read/admin token separation, and device-revoke authorization.

- [ ] **Step 1: Write failing operational page tests**

Require a report trend visualization and export status, a filter summary and
event distribution visualization for Details, and grouped settings sections
with explicit save scope and destructive-action separation.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./web -run 'Reports|Details|Settings' -count=1
```

Expected: FAIL on the new page structure while existing auth tests remain green.

- [ ] **Step 3: Rebuild Reports**

Add a trend chart tied to the current granularity/range, retain authenticated
CSV/JSON buttons, and keep stale results visible during refresh failure.

- [ ] **Step 4: Rebuild Details**

Keep all route-restored filters and pagination. Add compact filter chips,
request/token distributions from the loaded page, and a table optimized for
scanability rather than replacing the event table.

- [ ] **Step 5: Rebuild Settings**

Group connection/auth, pricing, device identities, and preferences. Keep
read-token/admin-token boundaries, revision snapshots, and explicit revoke
confirmation unchanged.

- [ ] **Step 6: Verify GREEN**

Run:

```bash
go test ./web -run 'Reports|Details|Settings' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/reports.js web/details.js web/settingsview.js \
  web/style.css web/embed_test.go web/semantic_test.go
git commit -m "feat: rebuild operational web pages"
```

### Task 8: Add the menu-bar telemetry bridge

**Files:**
- Create: `desktop/src-tauri/src/telemetry.rs`
- Modify: `desktop/src-tauri/src/lib.rs`
- Modify: `desktop/src-tauri/src/live.rs`
- Modify: `desktop/src-tauri/src/settings.rs`

**Interfaces:**
- Produces the Tauri command:

```rust
#[tauri::command]
async fn telemetry_get(app: tauri::AppHandle, range: String)
    -> Result<TelemetryView, String>
```

`TelemetryView` contains raw bounded values, measured/unmeasured sources, and
`generated_at_ms`; it never formats labels or colors.

- [ ] **Step 1: Write failing Rust deserialization tests**

Test complete today model rows, source contribution reconciliation, unknown
Codex speed, malformed payload rejection, endpoint change isolation, and
last-good data freshness.

- [ ] **Step 2: Verify RED**

Run:

```bash
cargo test --manifest-path desktop/src-tauri/Cargo.toml telemetry
```

Expected: compile failure because the telemetry module is absent.

- [ ] **Step 3: Implement typed telemetry parsing and command**

Validate:

```rust
abs(aggregate_tps - sources.iter().map(|s| s.contribution_tps).sum::<f64>())
    <= 1e-6
```

Reject non-finite and negative numeric values. Keep network requests in Rust.

- [ ] **Step 4: Change live session view semantics**

Expose `contribution_rate` for additive lists and optional `native_rate` for
details. Do not derive a total from rounded rows.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
cargo test --manifest-path desktop/src-tauri/Cargo.toml telemetry
cargo test --manifest-path desktop/src-tauri/Cargo.toml live
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add desktop/src-tauri/src/telemetry.rs desktop/src-tauri/src/lib.rs \
  desktop/src-tauri/src/live.rs desktop/src-tauri/src/settings.rs
git commit -m "feat: bridge telemetry into menu bar"
```

### Task 9: Rebuild the macOS menu-bar popover

**Files:**
- Modify: `desktop/ui/index.html`
- Modify: `desktop/ui/app.js`
- Modify: `desktop/ui/style.css`
- Modify: `desktop/src-tauri/tauri.conf.json`
- Modify: `desktop/src-tauri/src/lib.rs`

**Interfaces:**
- Consumes `live` events and `telemetry_get("1h")`.
- Produces the A2 popover with dynamic height up to the visible macOS work area.

- [ ] **Step 1: Write failing desktop UI contract tests**

Add Rust asset-contract tests requiring:

```text
one freshness header
current aggregate speed once
Claude and Codex five-hour cards
one-hour source lanes
ten-minute aggregate, peak, active ratio
today total and every model
additive current contribution rows
full-panel and settings actions
no risk forecast SVG
```

- [ ] **Step 2: Verify RED**

Run:

```bash
cargo test --manifest-path desktop/src-tauri/Cargo.toml ui_contract
```

Expected: FAIL because the old risk-first markup is still present.

- [ ] **Step 3: Replace popover markup and rendering**

Poll telemetry every 30 seconds, keep last-good telemetry with explicit age,
merge it deterministically with the SSE live snapshot, and render unknown
separately from zero. Draw source lanes with SVG/DOM only.

- [ ] **Step 4: Remove the fixed 520 px ceiling**

Set a 420 px target width and compute height from content, bounded by the
current monitor work area with an internal scroll region for long model lists.
Update Tauri's initial size to avoid a visible first-open jump.

- [ ] **Step 5: Preserve menu interactions**

Verify left-click toggle, right-click menu, quit, open full panel, settings,
refresh, autostart, notifications, shortcut, and tray title modes.

- [ ] **Step 6: Verify GREEN**

Run:

```bash
make desktop-sync-check
cargo test --manifest-path desktop/src-tauri/Cargo.toml ui_contract
cargo test --manifest-path desktop/src-tauri/Cargo.toml
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add desktop/ui desktop/src-tauri/tauri.conf.json desktop/src-tauri/src/lib.rs
git commit -m "feat: rebuild telemetry menu bar"
```

### Task 10: Perform automated and real-browser acceptance

**Files:**
- Modify only files required by reproduced acceptance defects.

**Interfaces:**
- Consumes the built server and all nine hash routes.
- Produces screenshot and DOM evidence at desktop and mobile widths.

- [ ] **Step 1: Run the focused suites**

```bash
go test ./internal/store ./internal/server ./web -count=1
make desktop-check
```

Expected: PASS.

- [ ] **Step 2: Build and run an isolated test server**

```bash
go build -o /tmp/omnitoken-telemetry-a2 ./cmd/omnitoken
/tmp/omnitoken-telemetry-a2 serve -listen 127.0.0.1:8877
```

Use a separate temporary data/config location so acceptance cannot modify the
user's production database.

- [ ] **Step 3: Verify every route in a real browser**

Visit:

```text
#overview #live #speed #reports #details #devices #models #cache #settings
```

At 1440×1000 and 390×844 verify no horizontal overflow, no console errors,
correct loading/empty/stale states, keyboard focus, daily model expansion,
source lanes, report buttons, Details filters, and Settings grouping.

- [ ] **Step 4: Verify metric reconciliation in the DOM**

For each rendered current bucket, read the unrounded data object and assert:

```javascript
Math.abs(
  bucket.aggregate_tps -
  bucket.sources.reduce((sum, row) => sum + row.contribution_tps, 0)
) < 1e-6
```

- [ ] **Step 5: Fix only reproduced defects and rerun affected checks**

Every fix begins with a failing automated regression test when the behavior is
machine-testable.

### Task 11: Build, reinstall, and restart the menu-bar application

**Files:**
- Build artifact: `desktop/src-tauri/target/release/bundle/macos/OmniToken.app`
- Install target: `/Applications/OmniToken.app`

**Interfaces:**
- Consumes a green source tree.
- Produces a running installed app with bundle identifier
  `com.suool.omnitoken.desktop`.

- [ ] **Step 1: Build the release app bundle**

```bash
cd desktop
cargo tauri build --bundles app
```

Expected: exit 0 and a fresh `OmniToken.app`.

- [ ] **Step 2: Validate the bundle before installation**

```bash
plutil -lint desktop/src-tauri/target/release/bundle/macos/OmniToken.app/Contents/Info.plist
codesign --verify --deep --strict desktop/src-tauri/target/release/bundle/macos/OmniToken.app
```

Expected: valid plist and accepted local signature.

- [ ] **Step 3: Stop the exact installed process**

Resolve the PID whose executable is exactly:

```text
/Applications/OmniToken.app/Contents/MacOS/omnitoken-desktop
```

Send `TERM`, wait up to ten seconds, and do not target any server, tunnel, or
unrelated process.

- [ ] **Step 4: Install transactionally**

Move the existing `/Applications/OmniToken.app` into a newly created temporary
backup directory, copy the fresh bundle with `ditto`, and restore the backup if
copy or validation fails. Do not recursively delete `/Applications` or use an
unresolved glob.

- [ ] **Step 5: Launch and verify**

```bash
open -a /Applications/OmniToken.app
```

Verify the exact executable is running, its modification time matches the new
bundle, and Tauri can fetch `/api/v1/live` and `/api/v1/telemetry` from the
configured server. Open the popover and inspect the real installed UI.

### Task 12: Whole-system closeout and Git delivery

**Files:**
- Modify: `docs/roadmap.md`
- Modify only other files required by verified closeout defects.

- [ ] **Step 1: Update roadmap status honestly**

Mark only implemented and freshly verified scope complete. Record Codex speed
as unavailable if reliable intervals were not added.

- [ ] **Step 2: Run complete gates**

```bash
make check
make desktop-check
cd desktop && cargo tauri build --bundles app
```

Expected: every command exits 0 with zero test failures or clippy warnings.

- [ ] **Step 3: Review the complete diff**

```bash
git status --short
git diff --check
git diff --stat origin/dev...HEAD
git diff origin/dev...HEAD
```

Check scope, secrets, auth boundaries, copied-asset drift, generated artifacts,
and accidental changes.

- [ ] **Step 4: Commit roadmap/closeout changes**

```bash
git add docs/roadmap.md
git commit -m "docs: record telemetry studio delivery"
```

- [ ] **Step 5: Push and create the PR**

```bash
git push -u origin codex/telemetry-studio-a2
gh pr create --base dev --head codex/telemetry-studio-a2 \
  --title "feat: rebuild OmniToken telemetry experience" \
  --body-file /tmp/omnitoken-telemetry-a2-pr.md
```

The PR body includes metric equations, test/build evidence, installed-app
verification, screenshots, migration notes, and the Codex speed coverage
boundary.

### Task 13: Align production visuals with the approved A4 prototype

**Trigger:** Installed Web and menu bar were functionally correct but visually
diverged from the approved prototype at `http://localhost:56187/`. The prototype
is now explicitly normative in the design spec.

**Files:**
- Modify: `web/tokens.css`
- Modify: `web/style.css`
- Modify only page JS/HTML needed to reproduce A4 component geometry.
- Modify: `desktop/ui/index.html`
- Modify: `desktop/ui/style.css`
- Modify: `desktop/ui/app.js` only for A4 chart geometry/classes.
- Sync: `desktop/ui/tokens.css`

- [ ] **Step 1: Add visual contract tests**

Require the canonical A4 canvas/surface/text/source tokens, ambient background,
gradient source bars, 24px outer surfaces, dark-only color scheme, and shared
Web/menu source palette. Tests must fail against the light A2 production theme.

- [ ] **Step 2: Rebuild the shared A4 tokens and Web shell**

Use the prototype's near-black canvas, indigo ambience, blue-black surfaces,
lavender hairlines, high-contrast telemetry typography, and analytical
12-column layout. Preserve all routes, data contracts, error states, keyboard
behavior, and reduced motion.

- [ ] **Step 3: Align every Web route**

Apply the same A4 card, table, filter, chart, empty/stale, and navigation
treatment across all nine routes. Do not leave light-theme islands or generic
white cards.

- [ ] **Step 4: Align the menu bar**

Reproduce the prototype's single-column instrument surface: borderless hero
divider, two compact 5h cards, gradient source lanes, compact three-stat row,
daily model composition, and contributor list. Retain the verified dynamic
height, scroll, unknown/401 behavior, and installed-app actions.

- [ ] **Step 5: Verify side by side**

Compare the prototype, production Web, and installed popover at the same scale.
The acceptance criterion is recognizable visual-system identity, not merely
similar colors. Run browser route/overflow checks and installed-app AX/screenshot
inspection.

- [ ] **Step 6: Rebuild and reinstall**

Run `make check`, `make desktop-check`, rebuild/sign the Tauri bundle, replace
the installed app recoverably, restart the Hub only if Web assets changed, and
verify `/api/v1/telemetry` plus the installed popover.
