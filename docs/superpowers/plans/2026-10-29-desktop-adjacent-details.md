# Desktop adjacent-details implementation and release

The approved prototype replaces per-source cards with aligned usage rows and
independent official quota windows. Source logos, selected-row tint and model
bars use source identity; quota percentages and meters use independent risk
colors (70/80/90%). Missing readings remain distinct from zero and overshoot
keeps its actual percentage while the meter caps at 100%.

The 420-point main panel retains current measured speed, one aggregate trend,
daily source totals, both quota windows, and a device summary. Source model
details, speed contributions/source trends, and devices open in a separate
320-point adjacent window, preferring left with right fallback. Its natural
height is capped by the current screen, with scrolling only when needed.
The main panel geometry and scroll position remain stable during disclosure.
On displays too narrow for adjacency, details replace the main content with a
Back control. Settings remain in the main panel and retain the existing
credential-redaction and authenticated-save behavior.

1. Commit this plan and update the F24 requirements.
2. Add failing geometry/risk/disclosure regression tests, then implement native
   panel-group focus/dismissal and the production HTML/CSS/JavaScript.
3. Exercise production UI in a real browser with a deterministic IPC fixture:
   source switching, missing/zero/over-quota, model expansion, long strings,
   stale state, keyboard, settings, and 320/420-point layouts.
4. Run make check, make desktop-check, and a real signed Tauri bundle build.
   Do not install or replace /Applications/OmniToken.app.
5. Review the final diff, create a PR to dev, verify remote CI, merge, and
   trigger the minor Release workflow. Verify the public latest.json,
   archive signature, sealed app bundle and version before claiming delivery.
6. Audit the current Web pages and document page-specific redesigns,
   shared UI primitives, state/interaction contracts, migration phases and
   acceptance criteria. This task does not authorize deploying a Web redesign.

The existing uncommitted runbook changes and prototype in the primary checkout
are outside this worktree and must remain untouched.
