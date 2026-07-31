# OmniToken System Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved Quiet Instrument Web UI, activity-first menu bar, and authenticated durable Edge Hub v2 as one reviewed release candidate.

**Architecture:** The three subsystem plans are independently testable and share only stable API/view-model contracts. Execute correctness foundations first, then Web and desktop in parallel, then Edge Hub v2 in protocol-sized commits, ending with whole-system verification.

**Tech Stack:** Go 1.26, modernc SQLite, browser ES modules/CSS/ECharts, Rust/Tauri 2.

## Global Constraints

- Feature work remains on `codex/omnitoken-system-redesign`, based on `origin/dev`.
- Preserve the user's unrelated `desktop/src-tauri/Cargo.toml` edit in the original `dev` worktree.
- Do not weaken event idempotency, attribution precedence, loopback defaults, or authentication.
- Required final gates are `make check`, `make desktop-check`, cross-builds, browser acceptance, and diff review.

---

### Task 1: Web product UX

**Files:**
- Plan: `docs/superpowers/plans/2026-07-30-web-product-ux.md`

- [ ] Execute all Web plan tasks in order, preserving one green commit per accepted task.
- [ ] Review every Web commit against the design sections 4–6 and 13.

### Task 2: Menu-bar redesign

**Files:**
- Plan: `docs/superpowers/plans/2026-07-30-menubar-redesign.md`

- [ ] Execute all menu-bar tasks, coordinating `tokens.css` only through `web/` and `make desktop-sync`.
- [ ] Review every menu-bar commit against design section 7.

### Task 3: Edge Hub v2

**Files:**
- Plan: `docs/superpowers/plans/2026-07-30-edge-hub-v2.md`

- [ ] Execute protocol tasks in order because each published interface is consumed by the next task.
- [ ] Review every protocol commit against design sections 8–12.

### Task 4: Whole-system closeout

**Files:**
- Modify only files required by verified integration defects.

- [ ] Review `git diff origin/dev...HEAD` for secrets, generated artifacts, accidental scope, and source/copy drift.
- [ ] Run `make check`.
- [ ] Run `make desktop-check`.
- [ ] Run `GOOS=linux GOARCH=amd64 go build ./cmd/omnitoken`, `GOOS=darwin GOARCH=arm64 go build ./cmd/omnitoken`, and `GOOS=windows GOARCH=amd64 go build ./cmd/omnitoken`.
- [ ] Run browser and menu-bar visual acceptance from the subsystem plans.
- [ ] Request independent code review and resolve all correctness findings.
- [ ] Push the branch and create a ready PR targeting `dev` with evidence, migration notes, and residual risks.
