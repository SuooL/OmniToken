# OmniToken System Redesign

Status: approved for implementation by the user's instruction to execute the
recommended design through to the final result.

Date: 2026-07-30

## 1. Purpose

This redesign addresses three connected product problems:

1. The Web dashboard has solid data coverage and responsive foundations, but
   its visual hierarchy, interaction states, accessibility, and cross-page
   workflows still feel like an engineering dashboard rather than a finished
   monitoring product.
2. The menu-bar popover overweights Claude/Codex quota windows. It already
   receives richer activity, device, session, and speed data, but does not make
   that information legible at a glance.
3. The multi-machine architecture has the correct hub-and-spoke shape, but a
   shared bearer token, client-asserted device identity, weak acknowledgements,
   and the lack of a durable edge outbox limit its safety and reliability as the
   number and network diversity of monitored machines grows.

The outcome is one coherent product:

- a quiet, precise Web instrument for real-time and historical analysis;
- an activity-first menu-bar glance surface;
- lightweight edge agents reporting to one authoritative central aggregator;
- explicit deployment paths for LAN, jump-host, and public-relay networks.

## 2. Scope and delivery boundaries

The work is split into three independently testable packages, delivered on one
feature branch in dependency order.

### Package A: product correctness and Web UX

- Fix authenticated report downloads.
- Preserve settings drafts across local re-renders and route changes.
- Correct cross-device scope labels.
- Standardize loading, empty, partial, stale, unauthorized, and offline states.
- Restore semantic labels, keyboard navigation, and visible focus.
- Improve typography, hierarchy, navigation, responsive behavior, tables, and
  URL-addressable analytical state.

### Package B: menu-bar information architecture

- Make current activity the default hero.
- Promote quota prediction only when it represents an actionable risk.
- Show the leading active sessions with tool, repository, model, device, and
  speed.
- Preserve the last good snapshot during transport degradation and display its
  age.
- Distinguish idle, unobservable, stale, unauthorized, polling, and offline.

### Package C: edge/hub protocol and deployment

- Keep one authoritative central database.
- Give each edge agent a stable identity and an independent ingest credential.
- Add a durable local outbox and strict versioned batch acknowledgement.
- Add heartbeat/health state independent of token activity.
- Support direct LAN/overlay, SSH jump-host tunnels, public ingress mappings,
  and an optional durable broker boundary.

Full multi-primary backends and federated SQLite replication are explicitly out
of scope. An edge may cache pending observations, but it is not a second source
of truth.

## 3. Evidence from the current system

### Web

The current dashboard has nine pages and good low-level foundations:

- real-time-first navigation;
- shared design tokens;
- dark/light variants;
- ECharts for the major analytical charts;
- responsive grids without horizontal page overflow;
- explicit distinctions between authoritative and inferred data.

The main gaps are structural:

- all body copy uses a monospace stack;
- page headings, card headings, navigation, and brand have insufficient scale
  separation;
- nearly every section uses the same glass card and hover lift;
- several pages fail into an empty body with only a small header message;
- form controls and session drill-down lack complete accessible names or
  keyboard semantics;
- mobile navigation is a non-sticky horizontal strip whose active deep link may
  be off-screen;
- analytical filters are not represented in the URL;
- authenticated CSV/JSON exports use plain anchors and therefore cannot attach
  the bearer header.

### Menu bar

The current live payload already includes:

- quotas and projected windows;
- devices and device states;
- inferred recent sessions;
- process-reported open sessions;
- burn rate;
- live generation speed and per-session speed;
- generated timestamp.

The popover renders only aggregate speed, aggregate burn, today, a generation
strip, up to three quota rows, and an aggregate device/process sentence.
Because the lead card requires an authoritative reset boundary, the visual hero
is structurally limited to Claude/Codex subscription quota windows.

### Multi-machine communication

The existing `agent -> ingest -> SQLite` path has important correct properties:

- agents make outbound connections only;
- log offsets advance only after the sink succeeds;
- `event_id` makes retransmission idempotent;
- SSH pull and agent push can coexist;
- process state is separate from immutable usage events.

The main scaling gaps are:

- the shared bearer authenticates a request but not a device;
- payload `device` fields are trusted as self-reported identity;
- any HTTP 200 advances offsets without validating a batch acknowledgement;
- proxy-only precision events have only a bounded in-memory retry ring;
- device online state is inferred from recent token events;
- loopback authentication inference cannot see a public reverse mapping;
- the current stateless relay has no independent TLS, authentication, rate
  limiting, or durable buffering boundary.

## 4. Chosen product direction

### 4.1 Visual language: Quiet Instrument

The dashboard remains recognizably OmniToken rather than becoming a generic
admin template.

- Use the system sans stack for navigation, headings, prose, labels, forms, and
  table headers.
- Use the monospace display stack for quantities, timestamps, model IDs,
  identifiers, and compact machine-readable metadata.
- Keep the live generation lanes as the signature visual element.
- Reserve glass, glow, and stronger depth for Live and Speed. Historical and
  administrative pages use flatter surfaces, separators, and restrained
  shadows.
- Use one brand accent plus semantic status colors. Data series may use the
  existing palette, but chrome and navigation do not compete with chart colors.
- Non-interactive cards do not move on hover.

Typography targets:

| Role | Desktop | Compact/mobile |
|---|---:|---:|
| Page title | 24-28 px | 20-22 px |
| Hero value | 48-64 px | 40-52 px |
| Section heading | 15-16 px | 14-15 px |
| Body | 13-14 px | 13-14 px |
| Metadata | 11-12 px | 11-12 px |

### 4.2 Surface roles

The two user-facing surfaces answer different questions.

#### Menu bar

Answers within two to three seconds:

1. Is anything generating now?
2. Which tool, repository, model, and device are involved?
3. Is a quota expected to hit its limit before reset?
4. How fresh and trustworthy is the displayed data?

It does not contain historical charts, full event tables, reporting, cache
analysis, or administrative configuration.

#### Web dashboard

Answers:

- what is happening now;
- how usage and speed changed;
- which devices, models, repositories, providers, and accounts drove it;
- what individual events or sessions explain an aggregate;
- whether data is complete, stale, inferred, or unavailable.

## 5. Web information architecture

The current nine routes remain, but their visual roles become clearer.

### Global shell

- Sticky desktop rail with connection and monitored-device health summary.
- Sticky compact top bar on narrow screens.
- The active mobile tab is automatically scrolled into view.
- A global status region reports live, polling, stale, unauthorized, partial, or
  offline without replacing page-specific errors.
- Theme selection supports system, light, and dark.
- Page state that changes the analytical answer is encoded in the URL query:
  range, granularity, device, model, source, repository, session, and page.

### Live

Order:

1. aggregate live speed and freshness;
2. concurrent generation lanes;
3. leading active sessions;
4. device health and open process state;
5. burn and quota risk;
6. all quota/window details.

The aggregate lane is labelled "all devices (union)", never "local", unless a
device filter is explicitly active.

When more than eight lanes exist, the page states how many are collapsed.

### Speed

- One primary 60-minute trend.
- Coverage and measurement semantics adjacent to the chart.
- Model comparison and proxy-precision/TTFT follow as secondary sections.
- Unobservable Codex speed is described as unavailable coverage, not idle.

### Overview

- Four period totals remain.
- The 30-day trend is the page's one dominant chart.
- Heatmap and driver breakdown use flatter sections.
- A short source/coverage note is visible without opening documentation.

### Reports and Details

- Authenticated exports use `fetch` and Blob downloads.
- Report session rows deep-link into Details with URL state.
- Details filters and pagination are restorable by refresh/back/forward.
- Session drill-down is a semantic link or button, keyboard reachable.
- Tables use sticky headers; on compact screens, high-priority columns remain
  visible and lower-priority columns move into row details.

### Devices and Models

- A leading summary describes the dominant device/model and change over range.
- Charts and tables are linked: selecting a series filters/highlights the table.
- Long device names and model IDs truncate visually while retaining a title and
  accessible full value.

### Cache

- Lead with hit ratio and saved equivalent cost, clearly distinguishing
  equivalent from real cost.
- TTL and model breakdown follow.
- Empty cache data explains whether the source cannot report cache fields.

### Settings

- Separate pricing, device identity/display names, appearance, and access into
  explicit sections.
- All controls have visible labels and programmatic names.
- Local re-rendering never discards another section's unsaved input.
- Route exit with dirty state preserves an in-memory draft and shows a concise
  unsaved indicator; it does not use a blocking browser dialog.
- Save results use an `aria-live` status region and semantic success/error
  styling.

## 6. Unified UI state model

Every async panel uses the same state vocabulary:

| State | Required presentation |
|---|---|
| Loading | Stable skeleton preserving eventual layout |
| Empty | What is absent, why it may be absent, and the next action |
| Partial | Available data remains visible; missing source is named |
| Stale | Last good value remains visible, dimmed, with age |
| Unauthorized | Credential guidance and direct link to access settings |
| Offline | Last good value remains when safe; reconnect action/status |
| Error | Page-local explanation and retry, not only a global header |

Unknown, zero, idle, stale, and unsupported are separate values and must never
share the same visual output.

## 7. Menu-bar design

### 7.1 Geometry

- Width: 380 px.
- Typical idle height: 280-320 px.
- Typical active height: 350-420 px.
- Hard maximum: 520 px; only explicitly expanded lists scroll.
- Core sections keep stable minimum heights to prevent the window jumping on
  every live update.

### 7.2 Default composition

```text
● Live · 3s ago                              2/3 devices

Generating  68 t/s · 2 sessions
Claude   omni-api   Sonnet   macmini       48 t/s
API      research   GPT-5    workstation   20 t/s
                                      1 more >

Quota   Claude 42% · Codex 18% · reset in 3h12m
Burn    3.1K/min              Today 2.4M

Full dashboard                               Settings
```

The activity row uses the data already available in `speed.sessions` and recent
sessions. It includes source/tool, short repository, model, device, and speed.
Repository/model/device values are truncated independently rather than allowing
one long field to erase the rest of the row.

### 7.3 Risk promotion

When an authoritative window is projected to reach 100% before reset:

- a risk band moves above current activity;
- it shows source, window, current percentage, projected percentage, and
  estimated hit time;
- the existing epistemically honest dotted/solid/dashed timeline is retained;
- normal quota states collapse back to the single summary row.

### 7.4 Transport and data states

- `generated_at` drives a visible data age.
- Live, polling, stale, unauthorized, and offline have separate labels.
- Polling and offline preserve the last good snapshot, dim it, and show age.
- An overview failure renders today's value as unknown, never zero.
- An open Codex process with unavailable speed does not render the entire system
  as idle.
- More sessions or quotas than fit display an explicit "N more" control.

### 7.5 Tray title

Existing Off / Quota / Speed modes remain, but:

- quota mode identifies the source when ambiguity exists;
- missing, stale, idle, and zero remain distinct;
- an optional Activity mode may show `2 active` only if it fits platform limits;
- the tray icon remains a coarse risk/state glyph rather than attempting to
  encode every metric.

## 8. Edge/hub target architecture

```text
edge logs / local API proxy / process table / quota cache
    -> edge collector
    -> durable local outbox
    -> versioned uploader
    -> transport
    -> authenticated central ingest
    -> SQLite authoritative store
    -> query + one coalesced live snapshot broadcaster
    -> Web and menu bar
```

### 8.1 Device identity

- Each agent has a generated stable UUID.
- Enrollment creates an independent ingest-only credential.
- The authenticated principal determines the stored device ID.
- Payload device names are ignored for authority; display name is separate
  mutable metadata.
- Legacy shared-token agents remain temporarily supported and are labelled
  `legacy_unbound`.
- Credentials can be listed, revoked, and rotated per device.

### 8.2 Versioned batch protocol

Request envelope:

```json
{
  "protocol_version": 2,
  "boot_id": "uuid",
  "batch_id": "uuid",
  "sequence": 42,
  "captured_at": 1785319948062,
  "kind": "events",
  "events": []
}
```

Response:

```json
{
  "protocol_version": 2,
  "batch_id": "uuid",
  "ack_sequence": 42,
  "accepted": 1980,
  "duplicates": 20,
  "rejected": [],
  "server_time": 1785319949000
}
```

Rules:

- A status code alone is not an acknowledgement.
- Batch ID and sequence must match before the outbox is deleted.
- Permanent invalid records are returned with a stable error code and moved to
  a diagnosable dead-letter state.
- Timestamp, size, event count, and required identity fields are bounded.
- Replaying an acknowledged batch remains idempotent.

### 8.3 Durable outbox

- Event/proxy observations and quota observations are written to a local SQLite
  outbox before their source cursor is committed.
- Server acknowledgement deletes the batch.
- Process and heartbeat reports are latest-only and coalesced rather than
  replaying stale state.
- The outbox reports queued batches/bytes, oldest age, last successful upload,
  and last error.
- Capacity pressure is visible and does not silently discard oldest events.

The first implementation supports a bounded outbox schema and uploader while
retaining the v1 endpoint for compatibility. It does not introduce Kafka or a
second server database.

### 8.4 Heartbeat

A heartbeat is independent of token events and includes:

- device and agent version;
- boot ID and monotonic sequence;
- server-received timestamp;
- collector capabilities;
- outbox backlog;
- last successful scan/upload;
- collector and clock-skew warnings.

Online/idle/stale status is derived from server receive time. Client timestamps
remain observational metadata.

### 8.5 Quota identity

Quota observations gain account identity/fingerprint separate from device
provenance. Multiple devices observing the same account are deduplicated by
account/source/scope/window, selecting the latest observation.

Remote agents capture the same locally available statusline quota source as the
server collector when configured. Unsupported sources report capability
unavailable instead of a valid empty snapshot.

## 9. Network deployment matrix

### A. Direct LAN or encrypted overlay

Recommended:

- agent pushes directly to the central HTTPS/overlay address;
- per-device credential;
- heartbeat and v2 batches use the same outbound connection policy.

Plain non-loopback HTTP requires explicit `insecure_http: true` and a warning
because bearer credentials otherwise travel in clear text.

### B. SSH jump host

For hosts already addressed through `ProxyJump`:

- run a persistent reverse forward from the central machine to the monitored
  host through the existing SSH alias;
- the remote agent posts to a loopback port on its own machine;
- systemd/launchd owns keepalive and restart;
- SSH provides encryption and authentication.

SSH/rsync pull remains the zero-install fallback. It is explicitly labelled as
lower fidelity: slower, observed attribution, no process truth, and incomplete
remote quota/precision coverage.

### C. Public server relay or mapping

Preferred order:

1. encrypted overlay network;
2. public HTTPS ingress restricted to enroll, ingest, and heartbeat, forwarding
   through WireGuard, FRP, or an SSH reverse tunnel to the central aggregator;
3. central aggregator deployed on the user's public VM if storing the database
   there is acceptable;
4. durable public broker only when the central machine cannot be mapped and must
   remain the authoritative data owner.

Public ingress requirements:

- TLS;
- authentication;
- body and event-count limits;
- rate limiting;
- no exposure of the dashboard/admin API unless separately authenticated;
- upstream health;
- access logs that never include reusable global credentials.

The existing unauthenticated stateless relay is not a public ingress.

### Optional durable broker

Agents push encrypted/versioned envelopes to a persistent queue. The central
aggregator makes an outbound long-poll or stream request, writes each batch, and
acknowledges broker deletion.

This is a separate deployment component and is not needed for LAN or ordinary
SSH/FRP mappings.

## 10. Authentication changes

- Agent ingest credentials are independent and device-scoped.
- Read/admin credentials are not accepted for ingest.
- Web streaming uses a bearer header through a fetch-based SSE reader, or a
  short-lived stream-only token. The reusable read/admin credential is not
  placed in a URL.
- `auth_mode: auto|always` allows a loopback server behind a public reverse
  proxy to enforce its own authentication. `auto` preserves the current local
  zero-configuration behavior.
- The public deployment guide requires the outer proxy to enforce TLS and only
  expose intended routes.

## 11. Error handling and observability

### Agent

- scanner, outbox writer, uploader, heartbeat, process reporter, and optional
  proxy run independently;
- upload retries use exponential backoff with full jitter and respect
  `Retry-After`;
- one 60-second request cannot delay process/heartbeat reporting;
- relay/upstream failure is reflected in health, not only logs;
- state and credential files use restrictive permissions.

### Server

- malformed records are rejected explicitly;
- reattribution and merge mutations trigger live broadcasts even when no new row
  is inserted;
- live payload construction is coalesced once per broadcast interval and the
  same serialized snapshot fans out to subscribers;
- health exposes protocol compatibility and authenticated agent status without
  exposing usage data.

### UI

- errors identify address, authorization, transport, stale data, or unsupported
  capability as distinct remediation paths;
- the last trustworthy snapshot remains visible where misleading zeroing would
  be worse;
- secrets never appear in rendered errors.

## 12. Migration

1. Ship the Web and menu-bar improvements without changing event identity.
2. Add v2 protocol tables and endpoints alongside v1.
3. New agents auto-create local identity/outbox and enroll.
4. Existing agents continue through v1 as `legacy_unbound`.
5. Migrate machines one at a time; bind old hostname labels to the new device
   UUID without rewriting event counts.
6. Add account identity to quota observations and migrate query logic.
7. Disable the global ingest credential only after all active agents migrate.
8. Keep historical `device_origin` uncertainty explicit; do not claim an
   automatic exact attribution migration.

## 13. Testing and acceptance

### Product acceptance

- Within three seconds of opening the menu bar, a user can identify current
  activity, source/repo/model/device, risk, and freshness without scrolling.
- Critical quota risk is always above the fold.
- Normal quota state does not dominate the popover.
- Web Live labels aggregate data as all-device scope.
- All nine Web routes provide complete loading/empty/error/stale states.
- Authenticated CSV and JSON exports work.
- Settings changes never discard unrelated unsaved edits.

### Responsive and accessibility

- Visual matrix: nine routes at 320, 390, 768, 999, 1440, and 1728 px in light
  and dark themes.
- No page-level horizontal overflow.
- Active compact navigation is visible.
- 200% zoom and long identifiers do not clip critical information.
- Keyboard-only operation covers navigation, filters, drill-down, pagination,
  export, and settings.
- All controls have accessible names and visible focus.
- Text meets WCAG AA; non-text controls meet 3:1 contrast.
- Reduced motion and high-contrast behavior are verified.

### Protocol and reliability

- crash before/after outbox commit;
- server commit followed by lost response;
- retry, duplicate, partial rejection, dead-letter, and disk-full behavior;
- file truncation and replacement;
- token revoke/rotate and device impersonation attempts;
- clock skew, PID reuse, agent restart, and duplicate hostnames;
- heartbeat without token activity;
- Windows capability unavailable rather than empty support;
- direct, SSH half-open/restart, jump-host, FRP/public ingress, and broker paths;
- central downtime longer than source-log retention;
- 100-agent synchronized startup with jitter/backpressure;
- v1/v2 migration and rollback;
- reattribution-only live update.

### Required gates

- `make check`
- `make desktop-check`
- JavaScript syntax checks
- focused Go race tests for store, server, agent, collect, and proxy
- browser visual/interaction checks against real local data and fixture states

## 14. Implementation sequence

1. Correctness baseline: authenticated exports, mutation broadcasts, local
   collection boundary, hermetic statusline test, scope labels, and desktop
   token/state inconsistencies.
2. Web state components, semantics, typography, navigation, and responsive
   redesign.
3. Activity-first menu bar and transport-state handling.
4. Device identity, per-device credentials, v2 ingest/ack, heartbeat.
5. Durable outbox and independent uploader scheduling.
6. Remote quota/capability parity and account identity.
7. LAN/jump/public deployment tooling, documentation, and fault tests.
8. Full verification, screenshots, migration evidence, and PR handoff.

Each phase must keep event IDs and token count columns unchanged. Any additional
stored-row overwrite requires a separate ADR and is not authorized by this
design.
