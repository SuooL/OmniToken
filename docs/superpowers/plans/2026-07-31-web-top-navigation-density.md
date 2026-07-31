# Web Top Navigation and Density Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Overview the Web default and replace the 208 px left rail with a compact right-aligned top navigation while reducing the browser-edge inset.

**Architecture:** Keep the existing semantic `<nav>`, route anchors, page header, and nine view modules. Change only router fallback, navigation order, and shell CSS; responsive behavior continues to use the existing 860 px and 420 px breakpoints.

**Tech Stack:** Go contract tests, vanilla JavaScript hash router, HTML, CSS, in-app browser acceptance.

## Global Constraints

- Bare `/`, empty hash, and unknown hash resolve to `#overview`.
- Desktop body inset is exactly `12px`; shell width is `min(1600px, 100%)`.
- Desktop shell is single-column with the wordmark left and all nine routes right aligned.
- At 860 px and below, the navigation is sticky and horizontally scrollable.
- At 420 px and below, the existing full-bleed mobile shell remains.
- A4 colors, 24 px desktop shell radius, API behavior, and page content do not change.

---

### Task 1: Correct the default route

**Files:**
- Modify: `web/semantic_test.go`
- Modify: `web/app.js`

**Interfaces:**
- Consumes: `parseRouteHash(hash: string)` and the existing `Views` registry.
- Produces: router fallback name `overview` for empty and unknown hashes.

- [ ] **Step 1: Write the failing router contract**

Add a semantic contract that requires both the parser fallback and route fallback to use Overview:

```go
func TestOverviewIsTheDefaultWebRoute(t *testing.T) {
    app := semanticAsset(t, "app.js")
    for _, contract := range []string{
        `const raw = (hash || "#overview")`,
        `const next = Views[parsed.name] ? parsed.name : "overview"`,
    } {
        if !strings.Contains(app, contract) {
            t.Errorf("default overview route missing %q", contract)
        }
    }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./web -run TestOverviewIsTheDefaultWebRoute -count=1
```

Expected: FAIL because both fallbacks currently use `live`.

- [ ] **Step 3: Implement the minimal route correction**

In `web/app.js`, change the two fallbacks and the nearby explanatory comment:

```js
const raw = (hash || "#overview").replace(/^#/, "");
const next = Views[parsed.name] ? parsed.name : "overview";
```

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./web -run TestOverviewIsTheDefaultWebRoute -count=1
```

Expected: PASS.

### Task 2: Replace the left rail with compact top navigation

**Files:**
- Modify: `web/a4_visual_test.go`
- Modify: `web/index.html`
- Modify: `web/style.css`

**Interfaces:**
- Consumes: `#nav a[data-view]`, `.rail`, `.brand`, `.tabs`, `.content`.
- Produces: a one-column shell with a right-aligned horizontal navigation.

- [ ] **Step 1: Write failing shell contracts**

Add a Go test that reads `style.css` and `index.html` and requires:

```go
func TestCompactTopNavigationContract(t *testing.T) {
    css := readA4CSS(t, "web", "style.css")
    html, err := FS.ReadFile("index.html")
    if err != nil {
        t.Fatal(err)
    }
    for _, contract := range []string{
        "padding: 12px;",
        "width: min(1600px, 100%);",
        "grid-template-columns: minmax(0, 1fr);",
        "margin-left: auto;",
    } {
        if !strings.Contains(css, contract) {
            t.Errorf("compact shell missing %q", contract)
        }
    }
    if strings.Index(string(html), `data-view="overview"`) >
        strings.Index(string(html), `data-view="live"`) {
        t.Error("Overview must be the first route")
    }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./web -run TestCompactTopNavigationContract -count=1
```

Expected: FAIL on the 28 px body inset, 1440 px cap, two-column shell, and route order.

- [ ] **Step 3: Reorder the route anchors**

In `web/index.html`, keep the semantic nav but remove visible group labels and order anchors:

```html
<div class="tabs" id="nav">
  <a href="#overview" data-view="overview" data-icon="overview">总览</a>
  <a href="#live" data-view="live" data-icon="live">实时</a>
  <a href="#speed" data-view="speed" data-icon="speed">速度</a>
  <a href="#reports" data-view="reports" data-icon="reports">报表</a>
  <a href="#details" data-view="details" data-icon="details">明细</a>
  <a href="#devices" data-view="devices" data-icon="devices">设备</a>
  <a href="#models" data-view="models" data-icon="models">模型</a>
  <a href="#cache" data-view="cache" data-icon="cache">缓存</a>
  <a href="#settings" data-view="settings" data-icon="settings">设置</a>
</div>
```

- [ ] **Step 4: Implement the desktop shell CSS**

Change the shell rules to:

```css
.viz-root { --gutter: clamp(14px, 1.4vw, 22px); }
body { padding: 12px; }
.viz-root {
  width: min(1600px, 100%);
  min-height: calc(100vh - 24px);
  grid-template-columns: minmax(0, 1fr);
}
.rail {
  position: sticky;
  top: 0;
  z-index: 20;
  height: auto;
  flex-direction: row;
  align-items: center;
  gap: 14px;
  overflow: hidden;
  border-right: 0;
  border-bottom: 1px solid var(--border);
  padding: 10px 14px;
}
.tabs {
  min-width: 0;
  margin-left: auto;
  flex-direction: row;
  overflow-x: auto;
}
```

Update active-route styling from a left border to a bottom edge/pill. At the
860 px breakpoint set `body { padding: 8px; }` and the shell minimum height to
`calc(100vh - 16px)`. Preserve the existing 420 px full-bleed rules.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./web -run 'TestCompactTopNavigationContract|TestA4' -count=1
node --check web/app.js
```

Expected: all focused contracts pass.

### Task 3: Full verification and browser acceptance

**Files:**
- Modify only if verification exposes a defect.

**Interfaces:**
- Consumes: the completed Web shell.
- Produces: verified desktop and narrow-screen behavior.

- [ ] **Step 1: Run the complete repository gates**

```bash
make check
make desktop-check
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 2: Run desktop browser acceptance**

Open the bare server URL without a hash and verify:

```js
({
  hash: location.hash,
  active: document.querySelector("#nav a[aria-current='page']")?.dataset.view,
  overflow: document.body.scrollWidth - document.documentElement.clientWidth,
  navTop: document.querySelector(".rail").getBoundingClientRect().top,
  firstRoute: document.querySelector("#nav a")?.dataset.view
})
```

Expected: active and first route are `overview`, overflow is `0`, and the nav is
at the top of the shell.

- [ ] **Step 3: Run narrow-screen acceptance**

At a viewport at or below 860 px, verify the navigation remains one horizontal
scroll row, the active link is visible, and page-level overflow remains `0`.

- [ ] **Step 4: Commit and update the existing PR**

```bash
git add web/app.js web/index.html web/style.css web/semantic_test.go web/a4_visual_test.go
git commit -m "fix: compact the web navigation shell"
git push
```

Confirm PR #61 includes the new commit.
