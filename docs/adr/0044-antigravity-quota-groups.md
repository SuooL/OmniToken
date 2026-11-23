# ADR-0044: Antigravity 模型分组滚动配额与估算视图

- **状态**: 已采纳
- **日期**: 2026-11-22
- **关联**: ADR-0007 (配额观测)、ADR-0011 (不读凭据/仅公开通道)、ADR-0025 / ADR-0034 (订阅容量估计)、ADR-0037 (桌面相邻详情)、ADR-0038 (Web 工具界面)、ADR-0042 (Antigravity 采集)

## 1. 背景

Google Antigravity 在产品侧按三个官方模型池分别计量配额（5 小时滚动窗口 + 周上限）：

1. **Gemini Pro / 4**（`gemini-pro`）
2. **Gemini Flash**（`gemini-flash`）
3. **Claude / 第三方**（`third-party`）

但在本地磁盘（`~/.gemini/antigravity/`）实测：
- `transcript.jsonl` 仅记录每个 step 的 `model`、`created_at` 与 `input / output / cache_read` tokens，**没有**官方配额百分比和 `resets_at` 字段；
- `antigravity_state.pbtxt` 仅保存 UI / onboarding 状态，无配额快照；
- 按 ADR-0011 原则，OmniToken 不读取 OAuth/Keychain 凭据，也不探测未公开的本地 daemon RPC。

因此，与 Claude Code（`statusLine` Hook）和 Codex（rollout `rate_limits`）不同，Antigravity 当前没有权威的 `quota_snapshots` 读数。用户希望在「配额与预估」中也能看到 Antigravity 的分组用量汇总（按预估/滚动 token 汇总，点击进入详情查看三个官方分组的具体用量），同时支持可选配置各分组预算上限以估算剩余额度与进度。

## 2. 决策

### 2.1 严格区分官方配额与滚动估算（不伪造官方 `%`）

- 现有 `windows[]` 与 `quotas[]` 的权威语义（Claude Code / Codex 官方已用 `%`、重置倒计时、ADR-0025/0034 容量校准与菜单栏风险告警）保持完全不变。
- `/api/v1/live`（及 `/api/v1/stream` SSE 快照）新增独立的 `quota_groups` 数组字段，显式携带 `basis: "rolling_estimate"`。
- 当未配置分组预算上限时，**不伪造百分比、不画虚构进度条、不伪造重置倒计时**，只展示 5h / 7d（Week）滚动窗口累计已用 tokens 汇总。
- 当用户为任一分组配置了预算上限时，额外返回对应分组及顶层的估算已用百分比、预算总额和估算余量（5h 估算余量同样受同组周余量约束），并在 UI 上显式标注为「估算」以区别于官方读数。

### 2.2 三个官方模型分组（单一后端真相）

Go 后端定义固定顺序的三个官方模型分组，不再额外增加「其他」分组：

| 分组 ID | 显示名 | 匹配规则（去除 `google/`、`vertex_ai/`、`vertex/`、`anthropic/`、`openai/`、`models/` 前缀并转小写） |
|---|---|---|
| `gemini-pro` | `Gemini Pro / 4` | 以 `gemini` 开头且不含 `flash`（覆盖 `gemini-4-*`、`gemini-3*-pro*`、`gemini-2.5-pro*`、`gemini-pro*` 等） |
| `gemini-flash` | `Gemini Flash` | 以 `gemini` 开头且含 `flash`（覆盖 `gemini-*-flash*`、`gemini-flash*` 等） |
| `third-party` | `Claude / 第三方` | 非空且不以 `gemini` 开头的模型（覆盖 `claude-*`、`gpt-*`、`o1*`/`o3*`/`o4*` 等） |

若某条历史事件缺失模型名（`model == ""`），不猜测其归属分组（遵守“不从来源推断模型”原则）：不将其塞入三个分组，也不新增第四个分组，仅计入来源总和并在存在非零值时通过 `unclassified_five_hour_tokens` / `unclassified_weekly_tokens` 字段如实暴露。

### 2.3 可选分组预算配置（热生效，无需重启）

- 支持在 `config.json` 中可选配置 `antigravity_quota_budgets`，键为三个标准分组 ID，值为 `{five_hour_tokens, weekly_tokens}`；
- 同时支持在 SQLite `app_settings`（键 `antigravity_quota_budgets`）中通过 `GET/PUT /api/v1/settings` 与 Web 设置页在线编辑，DB 覆盖配置、无需重启，保存后立即失效 `live` 响应缓存；
- 顶层汇总余量按「已配置预算的各分组剩余额度之和」计算，避免未设上限的分组或某一组超额污染另一独立配额池的余量。

### 2.4 Web 与 macOS 菜单栏联动交互

- **macOS 菜单栏**：在「配额与预估」中于 Claude / Codex 下方展示可点击的 Antigravity 区块（带 `滚动估算` 标识与 `›` 箭头），展示 5h 与 Week 汇总；点击后在现有相邻详情窗口（`#detail-panel`，窄屏回落窗口内详情）中展示三个官方分组的 5h / Week 用量、占比或预算进度条，以及每组内具体模型的 token 与 input/output/cache 构成。
- **Web 端**：总览页与实时页的配额表格同步展示 Antigravity 滚动估算行，点击打开共享 `DetailDrawer` 展示同一份分组与模型明细，并随 SSE / 刷新实时更新；设置页新增可选的「Antigravity 分组预算」配置区。

## 3. 后果与演进

- **零破坏**：旧版客户端可安全忽略 `/api/v1/live` 新增的 `quota_groups` 字段；Claude/Codex 配额告警与容量校准零回归。
- **可平滑升级**：若未来 Antigravity 提供公开的配额日志或 Hook，`quota_snapshots` 表主键 `(device, source, limit_id, scope, window_minutes, observed_at)` 已原生支持 `source = 'antigravity'` + `scope = 'group:<id>'`，可在同一分组视图中无缝切换为权威读数。
