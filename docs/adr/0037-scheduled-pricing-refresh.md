# ADR-0037 Scheduled model pricing refresh

Status: Accepted (2026-10-28)

## Decision

The Hub checks LiteLLM's model catalog at startup when its last successful snapshot is stale, then every 24 hours. `pricing_auto_update: false` disables network updates; `pricing_refresh_hours` defaults to 24 (1–720). This updates upstream model IDs and their five existing USD token-price fields, not event facts, canonical display rules or inferred Codex model identities.

Fetch only the fixed HTTPS LiteLLM source with a timeout and response-size bound. Reject empty/truncated catalogs, invalid numeric cost fields and implausibly small catalogs. Parse and validate the complete candidate before atomically persisting it beside the usage DB and swapping the live price table. Keep embedded prices for first boot and the last valid disk snapshot for restart/offline use. A failed download, parse or persistence must leave the live table and valid cache intact. Retry failures after at most one hour.

Serialize refresh and settings reloads; settings overrides outrank config overrides, which outrank downloaded prices. Existing query caches are invalidated on a successful price change. Expose enabled state, source, cadence, last attempt/success and errors through the authenticated settings API. No secrets travel to upstream. Prices continue to be query-time valuations of stored tokens, so historical cost views reflect the current rates (ADR-0005).

## Validation

Test HTTP failure/oversize/malformed catalogs, validation and atomic cache behavior; restart cache loading, override precedence, concurrent reload/refresh, scheduling from last success and read-cache invalidation. Run full `make check` and targeted race tests, then deploy to the approved Hub with binary/config/database backup and verify live update status without disclosing credentials.

If GitHub Raw is unreachable, the updater retries through the official GitHub Contents API
for the same LiteLLM repository file (`Accept: application/vnd.github.raw+json`), within a
30-second overall deadline. Both sources use the same validation and size bounds.
