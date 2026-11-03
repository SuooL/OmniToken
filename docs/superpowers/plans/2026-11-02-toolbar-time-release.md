# Toolbar parity and patch release

- Keep Claude, Codex, and DeepSeek lanes present even without source data; preserve missing measurement labels.
- Add a blue elapsed-time tick to official 5h/week usage tracks. Derive from reset minus duration, clamp to track, omit unknown/expired bounds, and expose only hover/focus time text.
- Prepare v0.4.2 with Cargo/config/lock versions consistent.
- Add regressions before implementation; run make check, make desktop-check, real browser validation, and make desktop-install.
- Merge via PR to dev, dispatch documented patch Release workflow, verify signed updater assets, and deploy the released Hub binary with checksum, backup, rollback, registry and event invariants, health and authenticated API checks.
- Remove clean merged task worktrees after verifying completed delivery. Preserve existing user files.
