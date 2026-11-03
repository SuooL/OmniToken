# Web source visibility, DeepSeek pricing, and quota time marker

## Scope

- Keep Claude, Codex, and DeepSeek speed lanes visible in overview, live, and speed views, in that order. Missing measurements remain gaps, never fabricated zeroes. Preserve additional observed sources.
- Resolve the reported `DeepSeek-V4.1-Flash` (and pinned `-1017`) ID to the first-party `deepseek-flash` pricing entry only after exact matching. Preserve explicit overrides and provider-specific prices. No event data changes.
- Keep quota usage percentages and fill. Add a blue elapsed-time tick on the same track, with a hover/focus label only. Calculate from the official reset and window duration; do not use rolling look-back bounds. Support 5h and weekly windows, clamp the marker, and omit it for missing/expired readings.

The user selected the shared-track design and asked to remove persistent comparison text.

## Evidence

- Production metadata on 2026-11-02: reported model IDs include `DeepSeek-V4.1-Flash` and `deepseek-flash`. The downloaded catalog contains `deepseek-flash`, but no bare `deepseek-v4.1-flash` entry.
- First-party model table at https://api-docs.deepseek.com/quick_start/pricing identifies `deepseek-flash` as DeepSeek-V4.1-Flash-1017. Preserve catalog rates rather than hardcoding a new rate.
- Speed view filters out sources without positive contributions; live/overview construct lanes dynamically.

## Validation

1. Add regression tests and observe failure before implementing behavior.
2. Verify fixed source ordering and missing-value gaps; exact price/override precedence and no cross-provider fallback; time-marker positions, boundaries, missing data, expired resets, and both window lengths.
3. Run `make check` in this worktree and inspect real browser at desktop/mobile sizes.
4. Review diff, push, and open a PR to `dev` with Codex co-author attribution. Production deployment remains a separate user-approved step.
