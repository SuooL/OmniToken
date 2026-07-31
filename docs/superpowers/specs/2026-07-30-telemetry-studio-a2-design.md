# Telemetry Studio A2 Design

**Status:** Approved visual direction
**Date:** 2026-07-30
**Scope:** macOS menu bar popover and Web overview/telemetry surfaces

## 1. Product decision

OmniToken will replace the menu bar quota-forecast line with a visualization-first
telemetry experience. The primary question is no longer “will a quota projection
cross a ceiling?” but:

1. What is generating now?
2. How has generation speed changed recently?
3. How much did Claude and Codex consume in the rolling five-hour window?
4. What is today’s cumulative total and how is it distributed across models?
5. Which sessions, devices, and models caused the observed speed?

The selected visual direction is **Telemetry Studio A2**: a dark, high-contrast
instrument panel with restrained gradients, compact analytical cards, temporal
speed bars, and source-specific colors.

## 2. Goals

- Show rolling five-hour **actual token usage** separately for Claude and Codex.
- Show today’s cumulative total and every model with usage since local midnight.
- Make current and recent generation speed the main menu bar content.
- Replace the quota forecast line with a useful temporal speed visualization.
- Use graphics to explain trend, composition, and contribution instead of
  repeating the same numbers in text.
- Give menu bar and Web distinct responsibilities while sharing one metric
  contract and visual language.
- Preserve trustworthy freshness, offline, authorization, and unknown states.
- Remain readable during long daily use rather than maximizing decorative
  effects.

## 3. Non-goals

- The menu bar will not attempt to reproduce every Web report.
- Official subscription quota windows are not the basis of the new five-hour
  cards.
- No animated decorative waveform will be synthesized from the duration that
  the popover happens to be open.
- The existing detailed reports, model analysis, device administration, and
  historical calendar remain separate Web views.
- API-paid traffic may appear in current contributors and the Web analysis, but
  it is not merged into either the Claude or Codex five-hour card.

## 4. Metric semantics

### 4.1 Rolling five-hour usage

The five-hour window is `[now - 5h, now]`, not a provider reset window. It uses
the same canonical `total_tokens` calculation already used by OmniToken totals
and reports.

Source mapping:

- `claude-code` → Claude
- `codex` → Codex
- other sources → Other/API, excluded from the two menu bar source cards

Each source summary contains:

- tokens in the current rolling five hours;
- tokens in the immediately preceding five-hour interval;
- percentage change when the preceding interval is non-zero;
- an explicit unavailable comparison when the denominator is zero.

### 4.2 Current speed

Current speed remains derived from observed generation intervals, not request
count or token timestamps alone.

The headline keeps ADR-0009's concurrency-aware machine throughput:

```text
aggregate_tps = measured_output_tokens
              / union_ms(all measured generation intervals) * 1000
```

Every visible source, device, model, and session decomposition uses that same
global union denominator:

```text
contribution_tps(group) = measured_output_tokens(group)
                        / union_ms(all measured generation intervals) * 1000
```

This creates a strict reconciliation contract:

```text
aggregate_tps
  = Σ source contribution_tps
  = Σ device contribution_tps
  = Σ model contribution_tps
  = Σ session contribution_tps
```

A group's native generation speed may still be useful in a drill-down:

```text
native_tps(group) = measured_output_tokens(group)
                  / union_ms(group generation intervals) * 1000
```

`native_tps` is not additive because each row has a different denominator. It
must never be shown in an additive contributor ranking or beside a total that
invites summation.

The menu bar hero shows aggregate `tok/s`, active session count, and the count
of devices currently contributing.

Unknown speed must render as unknown. It must never become zero merely because
the transport or the telemetry query failed.

The current code does not produce trustworthy `gen_ms` for Codex replay logs
(ADR-0009). Rolling five-hour and daily Codex **usage** are still complete, but
Codex **speed** remains unavailable until a reliable measured interval channel
exists. Neither aggregate speed nor its decomposition may silently include
unmeasured Codex output. The UI exposes measured-source coverage beside the
speed visualization.

### 4.3 Recent speed series

The menu bar uses 60 one-minute buckets covering the last hour. Each bucket
contains aggregate speed, global active duration, and a contribution value for
every measured source. The visualization uses aligned source lanes:

- Claude: warm orange
- Codex: clear blue
- Other/API: muted violet when non-zero
- cross-source peak emphasis: magenta

Claude and Codex remain the primary labeled lanes. Other/API cannot be omitted
when it contributes to the aggregate headline; the lane appears dynamically
when non-zero. If Codex has no reliable measured interval, its lane displays an
explicit unavailable state rather than synthetic bars.

For every bucket, `aggregate_tps` equals the sum of all returned source
`contribution_tps` values before display rounding.

The Web overview uses five-minute buckets over the rolling five hours by
default. It may offer one-hour and 24-hour ranges without changing metric
definitions.

### 4.4 Derived speed statistics

- **10m aggregate:** generated tokens divided by the global union of active
  generation time in the last ten minutes.
- **1h peak:** maximum one-minute aggregate speed bucket and its timestamp.
- **Active ratio:** union of active generation duration divided by the selected
  wall-clock interval.
- **Current contributors:** current sessions ordered by additive
  `contribution_tps`, with source, model, repository, device, and `tok/s`.

All derived totals use unrounded values. Clients round only for presentation,
so a displayed one-decimal total may differ by at most the expected rounding
residue from the sum of displayed rows.

### 4.5 Today cumulative model usage

“Today” is `[local midnight, now]` using the Hub’s configured/local timezone and
the same day boundary as the existing overview totals. It is not a rolling
24-hour interval.

The response contains:

- total tokens across all models today;
- every normalized model with non-zero usage today;
- per-model tokens and percentage of today’s total;
- deterministic descending token order with model name as the tie-breaker.

This is distinct from “current model speed.” A model may have substantial daily
usage while contributing no current `tok/s`.

## 5. Information architecture

### 5.1 Menu bar popover

The popover is an operational glance surface. It may grow beyond the previous
520 px cap; its target content height is approximately 680–760 px, constrained
by the available macOS screen.

Order:

1. **Freshness header**
   - transport state and data age;
   - fleet online count;
   - no repeated OmniToken wordmark.
2. **Current speed hero**
   - current aggregate `tok/s`;
   - active sessions and contributing devices.
3. **Rolling five-hour cards**
   - Claude actual tokens and comparison;
   - Codex actual tokens and comparison.
4. **60-minute source-lane speed heatmap**
   - source-specific lanes;
   - peak value and timestamp;
   - axes at `-60m`, `-30m`, and now.
5. **Compact derived statistics**
   - ten-minute average;
   - one-hour peak;
   - active-time ratio.
6. **Today cumulative model usage**
   - today’s total;
   - a compact composition strip;
   - every model with usage in a vertically scrollable list;
   - no implicit top-three truncation.
7. **Current contribution ranking**
   - only the fastest useful rows;
   - additional rows represented by a count, not an unbounded list.
8. **Footer**
   - data age when degraded;
   - full panel and settings actions.

The quota forecast line, dotted historical segment, ceiling, and projected
breach segment are removed from the primary popover.

### 5.2 Web overview

The Web overview is an analytical explanation surface.

Order:

1. One global navigation wordmark: `OmniToken`.
2. A separate Hub health indicator; it is not attached to the wordmark.
3. Five non-redundant summary cards:
   - today’s cumulative total and model count;
   - rolling five-hour total with a compact source composition strip;
   - Claude rolling five-hour usage;
   - Codex rolling five-hour usage;
   - fleet coverage and backlog state.
4. A primary five-hour source-separated speed chart:
   - current aggregate speed is part of the chart header, not another KPI card;
   - measured sources use vertically aligned lanes with a shared time axis;
   - each source has its own zero baseline, so one source never visually shifts
     the other;
   - each lane plots its additive contribution, not source-native speed;
   - aggregate speed is the exact sum of the visible lanes and is available in
     the header and tooltip rather than as a third repeated lane;
   - Other/API appears whenever non-zero and cannot be hidden while remaining
     inside the headline total;
   - unavailable source measurement is labeled separately from zero.
5. A current-contributor panel explaining the chart’s latest value.
6. A full-width today model composition chart showing every model with usage;
   the chart may collapse visually behind an explicit expand control on narrow
   screens, but the data must remain accessible.
7. Device throughput and health.
8. Current model throughput.

The previous standalone source donut is removed because it repeated the Claude
and Codex summary cards. Usage composition is encoded once in the total card;
source speed contributions remain separate, non-stacked lanes.

## 6. Anti-repetition rules

Each fact receives one primary display location:

| Fact | Primary location |
|---|---|
| Product identity | Web global navigation only; menu bar icon supplies app identity |
| Transport freshness | Menu bar header |
| Hub health | Web navigation utility area |
| Current speed | Menu hero; Web speed-chart header |
| Claude/Codex five-hour totals | Source cards |
| Today total and model composition | Today summary plus daily model chart |
| Source composition | Five-hour total strip |
| Source speed trend | Aligned Claude/Codex lanes |
| Current cause | Contributor ranking |
| Fleet liveness | Fleet summary/device visualization |

Numbers answer **what is true now**. Charts answer **how it changed**. Rankings
answer **what caused it**. State indicators answer **whether it can be trusted**.

## 7. Data architecture

### 7.1 New telemetry endpoint

Add an authenticated read endpoint:

```text
GET /api/v1/telemetry
```

It returns a bounded, presentation-neutral snapshot:

```json
{
  "generated_at": 1785460000000,
  "today": {
    "start_ms": 1785427200000,
    "end_ms": 1785460000000,
    "total_tokens": 6920000,
    "models": [
      {
        "model": "claude-sonnet",
        "tokens": 3040000,
        "share": 0.4393
      },
      {
        "model": "gpt-5",
        "tokens": 2010000,
        "share": 0.2905
      }
    ]
  },
  "rolling_5h": {
    "start_ms": 1785442000000,
    "end_ms": 1785460000000,
    "total_tokens": 4510000,
    "sources": [
      {
        "source": "claude-code",
        "tokens": 2840000,
        "previous_tokens": 2410000,
        "change_percent": 17.84
      },
      {
        "source": "codex",
        "tokens": 1370000,
        "previous_tokens": 1280000,
        "change_percent": 7.03
      }
    ]
  },
  "speed_60m": {
    "bucket_ms": 60000,
    "measured_sources": ["claude-code", "api"],
    "unmeasured_sources": ["codex"],
    "series": [
      {
        "start_ms": 1785456400000,
        "aggregate_tps": 60.2,
        "sources": [
          {
            "source": "claude-code",
            "contribution_tps": 48.2,
            "native_tps": 54.6,
            "output_tokens": 2024
          },
          {
            "source": "api",
            "contribution_tps": 12.0,
            "native_tps": 18.5,
            "output_tokens": 504
          }
        ],
        "active_ms": 42000
      }
    ],
    "aggregate_10m_tps": 92.1,
    "peak_tps": 124.0,
    "peak_at": 1785459720000,
    "active_ratio": 0.71
  }
}
```

The endpoint does not return preformatted labels, colors, or chart geometry.
The `today.models` array contains all normalized models with non-zero usage; it
is not a top-N query. The overall endpoint remains body-size bounded and signals
an explicit error rather than silently truncating model data.

### 7.2 Live data

`/api/v1/live` remains the current-state/SSE contract for:

- current aggregate speed;
- active sessions;
- process state;
- device liveness;
- transport freshness.

The one-hour historical series is not added to every SSE frame. This avoids
recomputing and retransmitting a large series for each subscriber every 30
seconds.

### 7.3 Client refresh behavior

- Desktop Rust bridge continues to consume `/api/v1/stream`.
- Desktop independently refreshes `/api/v1/telemetry` every 30 seconds and
  keeps the last-good telemetry snapshot with an explicit age.
- Web overview refreshes telemetry every 30 seconds while visible.
- Current speed may update more frequently through SSE.
- A telemetry refresh failure preserves the previous graph but marks it stale;
  it never clears the graph to a misleading empty state.

## 8. Component boundaries

### Server

- Store query for rolling window totals by normalized source.
- Store query for bounded source-specific speed buckets.
- Store calculation for additive source/session contributions using the global
  bucket/window union denominator.
- Pure telemetry response builder.
- Authenticated HTTP handler with existing read-token semantics.

### Desktop Rust

- Typed telemetry wire structures.
- Deterministic merge of current live state and last-good telemetry state.
- View model containing raw bounded chart values and explicit freshness.

### Menu bar WebView

- SVG/DOM renderer for measured source speed lanes and unavailable states.
- No business-metric recomputation beyond geometry and formatting.
- Responsive height calculation capped to available display work area.

### Web

- Shared telemetry client/cache.
- ECharts aligned additive source-contribution lanes with synchronized tooltip,
  reconciliation detail, and range control.
- Reusable contributor, device-throughput, and model-throughput components.
- Reusable complete daily-model composition component with an accessible list.

## 9. Visual system

- Base: deep indigo/graphite surfaces with clear border separation.
- Claude: warm orange.
- Codex: clear blue.
- Current/healthy status: restrained mint.
- Exceptional peak or error: magenta/red only when semantically meaningful.
- Typography: system sans for labels, tabular display numerals for telemetry.
- Gradients communicate magnitude or grouping; they are not decorative page
  backgrounds inside every card.
- Motion is limited to data transitions and respects `prefers-reduced-motion`.
- No chart uses color as its only source distinction; labels and accessible
  descriptions accompany it.
- Source temporal bars are never stacked; their independent zero baselines and
  labels must remain visible.

## 10. Error and boundary states

- **No events in five hours:** show zero only after a successful response.
- **No events today:** show a successful zero total and an explicit empty model
  state.
- **No previous-window denominator:** show “无可比基线,” not `∞%`.
- **Unauthorized:** hide numeric telemetry and expose the settings recovery
  path.
- **Offline with last-good data:** keep charts, dim them, show age.
- **No source contribution:** retain the lane baseline with an explicit zero.
- **Unmeasured source speed:** show “unavailable / not measured,” never zero and
  never silently include its tokens in an aggregate speed.
- **Future client timestamps:** never affect server-side window boundaries.
- **Revoked/offline device:** cannot improve its state from cached client data.

## 11. Accessibility

- Menu lane chart has a concise text description and a hidden tabular summary.
- Web ECharts enables accessibility metadata and has an exploration table.
- All contrast targets meet WCAG AA for text and essential chart marks.
- Keyboard focus remains visible.
- Reduced-motion mode disables interpolated chart animation.
- Tooltip-only information must also be reachable by keyboard or visible in a
  companion summary.

## 12. Testing and acceptance

### Server

- Rolling five-hour boundary and preceding-window comparison.
- Claude/Codex normalization and Other exclusion from source cards.
- Correct active-time union, peak bucket, and empty windows.
- Aggregate speed equals the sum of source, device, model, and session
  contribution speeds for shared fixtures.
- Native per-group speed remains separately named and is never used in an
  additive ranking.
- Partially overlapping and sequential sessions reconcile without double
  counting or summing incompatible denominators.
- Unmeasured Codex intervals produce an explicit coverage state.
- Authorization and bounded response shape.

### Desktop

- Live and telemetry snapshots merge without freshness confusion.
- Last-good telemetry survives a failed refresh and becomes stale.
- Unknown is distinct from zero.
- Chart values are bounded and sorted.
- Window height respects available display bounds.

### Web

- Current speed appears once in the overview.
- Wordmark appears once per Web surface.
- Source totals and chart buckets share the same metric fixtures.
- Source speed lanes share one time domain while retaining independent visual
  baselines.
- The chart headline equals the unrounded sum of every visible source lane;
  non-zero Other/API contribution cannot be hidden.
- Contributor rows use `contribution_tps`, not `native_tps`.
- Today total equals the sum of every returned model row.
- Daily model composition never silently drops rows or defaults to a top-three
  list.
- Rapid refreshes cannot let an older telemetry response overwrite a newer one.
- Desktop/mobile layout, keyboard operation, reduced motion, and horizontal
  overflow are verified in a real browser.

### Visual acceptance

- Menu bar contains no quota forecast dotted line.
- Menu bar visibly separates Claude and Codex rolling five-hour usage.
- Speed totals can be audited by summing the visible source/session
  contributions.
- Menu bar shows today’s total and provides access to every model with usage.
- A user can identify current speed, one-hour trend, and leading contributor
  within three seconds.
- The Web overview contains no standalone source donut or duplicate current
  speed KPI.
- The Web overview shows the complete today model composition separately from
  current model speed.
- The implementation matches the approved A2 hierarchy at common macOS and Web
  viewport sizes.
