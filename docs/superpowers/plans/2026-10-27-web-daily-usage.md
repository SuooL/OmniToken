# Web daily usage and readable device names

- Replace Overview rolling-five-hour totals/source cards with calendar-day totals and source cards. Compare only against yesterday's same local-clock interval when available.
- Add a compatible `range=today` telemetry range: server timezone midnight to now, five-minute speed buckets. Retain existing ranges and rolling fields for installed clients.
- Return `device_display_name` on each event using the shared operator-label-first resolver. The UI must not wait for filter options before displaying names.
- Clarify missing model labels as logs not recording a model. Preserve unknown usage and raw events; do not infer actual models from tool names or pricing fallbacks.
- Audit model/pricing refresh policy and document current manual snapshot behavior.

Validation: regression tests first (timezone/DST/midnight range, event identity/name precedence, rendered daily source data and asynchronous details loading), full `make check`, then user visual review. Do not automate the user's computer UI.
