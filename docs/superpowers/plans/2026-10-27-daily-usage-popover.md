# Daily usage and quota forecast in the menubar popover

## Contract

Replace rolling-five-hour usage cards and the separate daily-model panel with one
Today usage section. Claude, Codex, and DeepSeek appear in three equal columns;
each displays calendar-day tokens and all its models. Only a valid change against
yesterday's matching local wall-clock interval is shown; unknown comparisons have
no message or reserved row. Other sources, when present, remain visible separately
so daily totals still reconcile.

A Quota forecast section follows. Each subscription source retains peer 5h/Week
official percentages, independent 70/80/90 text colors, reset countdowns in total
hours/minutes, and estimated remainders. Five-hour usable allowance stays capped
by the remaining weekly allowance. Other popover instruments keep their behavior.

## Data and compatibility

Extend telemetry.today with sources containing tokens, per-model counts, previous
same-time tokens, and optional change_percent. Query source and model together;
never infer a tool from a model name. Use the configured server calendar/timezone,
including calendar-day arithmetic across DST. Retain existing today.models and
rolling_5h API fields for existing web and desktop clients. New desktop clients
accept older payloads without inventing source attribution.

## Validation and delivery

Start with regression tests for day boundaries, DST comparison, source/model
attribution, reconciliation, conditional comparisons, three-column layout, and
existing quota behavior. Run make check and make desktop-check in this worktree,
build/install the actual signed app, inspect native UI when available, and verify
the live telemetry contract after a backed-up server binary update if required.
Submit a focused PR to dev and close the clean temporary checkout after merge.
