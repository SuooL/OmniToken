# Scheduled pricing implementation

1. Add bounded upstream parsing/fetch and validated atomic disk snapshots in pricing.
2. Add opt-out and refresh interval config, startup cache loading, serialized refresh/reload, retry scheduling and settings API status.
3. Write failing regression tests before implementation; verify all gates and targeted races.
4. PR to dev; after CI, deploy approved daily Web plus refresh mechanism to the Hub, retaining verified backups. Verify one real refresh, cache, status, device names and calendar-day telemetry. Keep desktop installation untouched.
