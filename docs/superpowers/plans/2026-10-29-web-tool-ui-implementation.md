# Web tool UI implementation plan

Status: implementation and local acceptance complete, 2026-10-29; remote delivery pending. Baseline origin/dev 4bc1281.
The user approved applying the desktop tool UI standard to the Web and requested
parallel implementation. This plan makes the audit specification executable.

## Delivery boundary

Implement all nine existing routes using native JS, Go embed and ECharts.
Preserve API/calculation/authentication semantics, metadata-only collection,
last-good states, pagination/export and guarded device administration. No Hub
production deployment or new release is implied by this Web implementation task.
Work in the isolated feature/web-tool-ui checkout; primary checkout is read-only.

## Shared contracts (root agent)

- SourceUI.label(source), color(source), brand(source): canonical identity,
  escaped HTML brand with offline logo and CSS source class.
- QuotaUI.render(windows, quotas): compact official readings (finite incl. zero),
  estimates and countdowns; 5h/week independent 70/80/90 risk; real zero/full/
  overshoot/unknown distinct; metered spend separate, no invented denominator.
- DetailDrawer.open({key,title,subtitle,html,trigger}), update(key,html), close():
  one accessible dialog, close/Escape/outside, focus restore, narrow overlay;
  refreshing matching content preserves scroll and focus, route changes close.
- ToolUI.analysis(id,title,html): native details (closed by default) with stable
  id; collapsed charts resize on disclosure using ChartRegistry.
- tool.css loaded after style.css: scoped tool profile, restrained section
  surfaces, shared tables/rows/toolbar/drawer/settings groups, 420/768/1440.
- Existing CSS/DOM hooks remain when they represent supported behavior. Static
  tests enforcing superseded decoration are rewritten as behavior contracts.

## Parallel ownership and order

1. Root: shared helpers/assets/styles, main shell/router, test integration,
   requirements/ADR, real-browser synthetic Hub acceptance and final PR.
2. Operational pages agent: overview.js, live.js, operational-ui.test.cjs.
   Total + source rows + quota first; historical tokens trend from actual
   overview/report payload; advanced charts behind disclosure; stable shells.
3. Workflow pages agent: details.js, reports.js, settingsview.js,
   workflow-ui.test.cjs. Table first, route-safe filters/pagination, event
   drawer; scoped settings groups and guarded administration stay intact.
4. Analysis pages agent: devicesview.js, modelsview.js, cacheview.js,
   speedview.js, analysis-ui.test.cjs. Tables first, secondary charts disclosed,
   stable source identity, valid domains/empty denominators, device drawer.

Agents use gpt-6.1-sol/high for independent page work; root handles cross-cutting
state/risk integration and review. Each owns only its listed files, writes and
runs failing tests before behavior changes, then reports evidence and limitations.
No agent commits or changes shared files; root creates focused commits after review.

## Acceptance and checkpoints

A. Commit approved design/plan and requirements before implementation.
B. Pure regression tests first: official 0/null/100/over, zero remaining, source
   consistency, complete route parsing, stable model domain and cache denominator.
C. Implement shared UI then integrate page groups. Preserve table companion data
   and hidden chart resizing; no fixed detail height or stale-as-live values.
D. Run meaningful node tests through make check, full make check, and real
   browser acceptance with isolated synthetic Hub data. Exercise nine routes,
   420/768/1440, source/event/device drawer, focus/Escape/back, refresh state,
   pagination/filter URL/export, 401/403/stale/missing, long names and many rows.
E. Review full diff, focused commits, push feature branch, PR targeting dev;
   verify remote Go + macOS desktop gates (shared assets remain unchanged).
F. Only after merged CI and review evidence, remove clean merged worktree and
   branch. Report exact shipped scope and remaining runtime validation limits.

## Recorded acceptance

- `make check`: passed, including 47 Node tool-UI regressions and existing Web
  semantic/DOM tests. Critical parser coverage gates remain unchanged.
- `NODE_PATH=<playwright modules> node scripts/web-tool-browser.cjs`: 14 groups
  passed against an isolated actual Hub and 2700 synthetic events; 27 route /
  viewport screenshots plus three contextual drawer screenshots; no pageerror
  or global horizontal overflow. 420/768/1440 widths, Escape/focus after refresh,
  disclosure/chart instance retention, filter/page Back, settings draft, scoped
  CSV export, quota boundaries, stale data, 401/403 and 25 long model names.
- Artifact directory for the final pre-PR run:
  `/var/folders/38/vh8w9pbj46s11269vqvzdyp00000gn/T/omnitoken-web-tool-browser-qENs3x`.
- Independent shared-layer review found scope/placeholder/auth-generation/focus
  edge cases; failing tests were added and all cases repaired before delivery.
- Shared token copies and desktop application behavior remain untouched.
