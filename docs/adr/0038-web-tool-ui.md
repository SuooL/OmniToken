# ADR-0038 Web tool UI and contextual details

Status: accepted and implemented, 2026-10-29. The user authorized applying the
approved desktop tool UI standard to the existing Web pages and parallel work.
The executable plan is `docs/superpowers/plans/2026-10-29-web-tool-ui-implementation.md`.

## Decision

Keep native JavaScript, Go embed, ECharts, all nine routes and existing API
semantics. Add the scoped `tool.css` profile after the original base stylesheet;
shared desktop tokens remain unchanged. Source identity is centralized in
SourceUI (including offline logos), independently from quota risk, chart series,
and billing-channel identity. ToolUI owns stable disclosure/data updates.

Overview prioritizes daily total/source rows and independent official quota
windows, followed by actual daily token history. Live prioritizes measured
speed, official quota, concurrent generation and sessions. Retrospective details,
reports, devices and models are table-first. Secondary diagnosis charts use
native closed details elements. Settings has four task groups and preserves
existing scoped saves, drafts and guarded device administration.

A single native dialog provides contextual source/event/device details, with
Escape/outside close, focus restoration after list refresh, contained scrolling
and a narrow-screen overlay. It does not resize or squeeze the main charts.
Its metadata is escaped and its contents are cleared on close. Route changes
close it. Native dialog focus trapping and modal behavior avoid a second custom
keyboard-interaction implementation.

QuotaUI accepts actual subscription windows and snapshots. Only authoritative
readings produce percent meters; no synthetic token denominator is used.
Missing, explicit null, real zero, exhausted and over-limit states are distinct.
The backend omits a zero used_percent field on authoritative window cards, so
undefined + authoritative is a known zero; explicit null/invalid remains unknown.
Source-less backend placeholders use their known provider key. The stricter
scope retains its identity and only borrows a capacity estimate from its exact
scope. Five-hour usable allowance is bounded by known weekly allowance.
Bars cap at 100%; actual percentage and independent 70/80/90 risk remain visible.
Non-subscription spend never receives a quota meter. Estimates and projections
are labelled separately from official readings.

LiveSnapshotCache deduplicates GET snapshots; the live route owns its existing
single SSE connection and seeds the cache. Older HTTP snapshots cannot overwrite
newer SSE observations. Credential changes invalidate requests/cache and page
snapshots, and authorization failures remove prior protected observations.
Ordinary transient failures preserve last-good observations with explicit stale
labels; auxiliary data freshness does not inherit another section's success.
TelemetryCache retains generation protection and supports identity-wide reset.

Stable shells and ChartRegistry preserve chart instances and disclosure state.
Empty-data transitions dispose canvases before inserting placeholders; opening
analysis details resizes existing charts. ECharts stays bundled offline and has
readable companion tables and reduced-motion support.

## Preserved scope and limitations

No event identity, parser, collection, database or historical accounting change.
No new production endpoint or automatic device-management behavior. Device
context exposes only the dimensions the existing endpoint supplies; unavailable
per-device full model/source histories and scan/upload times are stated as such.
Model `top=100` restricts daily composition, not the by-source ranking result;
the UI labels the two scopes separately.

Web deployment is outside this implementation task. Acceptance uses an actual
isolated Hub, synthetic metadata and Chrome, without production credentials or
usage data. Safari/Firefox are not runtime-tested in this change.
