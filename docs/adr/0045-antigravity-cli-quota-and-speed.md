# ADR-0045: Antigravity CLI (`agy`) 官方配额采集、纯生成速度与多目录接入

- **状态**: 已采纳
- **日期**: 2026-11-23
- **关联**: ADR-0009 (生成速度)、ADR-0011 (通过 statusLine 捕获官方配额)、ADR-0018 (计费通道)、ADR-0025/0034 (订阅容量估计)、ADR-0040 (纯生成速度口径)、ADR-0042 (Antigravity 接入)、supersedes ADR-0044 (已撤回)

## 1. 背景

Antigravity CLI（`agy`）提供了完整且官方公开的遥测与配额通道，与早期仅使用 Antigravity Desktop App 时的限制不同：

1. **多产品目录**：Antigravity CLI 将会话日志写入 `~/.gemini/antigravity-cli/brain/*/.system_generated/logs/transcript.jsonl`（Antigravity 2.0 写 `~/.gemini/antigravity/brain`，IDE 写 `~/.gemini/antigravity-ide/brain`），原有默认目录漏掉了 `antigravity-cli` 与 `antigravity-ide`。
2. **官方 `statusLine` 配额通道**：`agy` CLI 在 `~/.gemini/antigravity-cli/settings.json` 中官方支持 `statusLine.command`，每次刷新向 `stdin` 传入 JSON，其中直接携带官方权威的 `quota` 桶字典（如 `gemini-5h`、`gemini-weekly`、`gemini-argon-wtu-weekly`、`3p-5h`、`3p-weekly`），包含 `remaining_fraction`、`reset_time` 与 `reset_in_seconds`。无需读取任何 OAuth/Keychain 凭据，完全遵守 ADR-0011。
3. **纯生成计时与 TTFT**：与 `brain/<id>/` 同级的只读对话元数据库 `conversations/<id>.db` 的 `steps.metadata`（`step_type = 15`，即 `PLANNER_RESPONSE`）记录了每步的四个毫秒级 protobuf Timestamp：`f1`（请求创建）、`f6`（首 token 到达）、`f7`/`f8`（生成完成）。据此可直接计算精确的 `ttft_ms = f6 - f1` 与纯生成区间 `gen_ms = f7 - f6`（不含首 token 等待与工具执行，与 ADR-0040 `dsh` 同口径）。

## 2. 决策

### 2.1 多目录发现与订阅通道归类
- `DefaultLocalAntigravityDirs()`（未显式设置 `$ANTIGRAVITY_HOME` 时）同时扫描 `~/.gemini/antigravity/brain`、`~/.gemini/antigravity-cli/brain`、`~/.gemini/antigravity-ide/brain`；SSH 镜像同步同样覆盖三目录。
- Antigravity 仅使用 Google 账户订阅/计划额度，不走按量 API Key，因此 `internal/parser/antigravity` 将 `Provider` 标记为 `model.ProviderGoogleOAuth`（`ChannelSubscription`，rank=3 高于原 `ProviderGoogle` 的 rank=2，重扫可幂等升级），使 Antigravity 事件能正确参与 5h / 周窗口用量统计及 ADR-0025/0034 跨窗口容量学习。

### 2.2 纯生成速度与 TTFT（只读元数据，不读内容）
- `internal/parser/antigravity` 解析 `transcript.jsonl` 时，若同级 `../conversations/<session-id>.db` 存在，则以只读模式（`?mode=ro`）读取 `steps` 表中 `step_type = 15` 的 `(idx, metadata)` 两列，仅解码 protobuf Timestamp 字段 `1/6/7/8`（不接触对话文本、思考过程或工具载荷，满足 N4）。
- `event_id` 构造保持不变；Web 速度页对 `antigravity` 标注 `Antigravity · 纯生成`。

### 2.3 扩展 `omnitoken statusline` 捕获官方 Antigravity 配额
- `omnitoken statusline`（及 `-capture-only`）同时识别 Claude Code 的 `rate_limits` 与 Antigravity CLI 的 `product: "antigravity"` + `quota` 字典，分别原子落盘至 `rate-limits.json` 与 `antigravity-rate-limits.json`，互不覆盖。
- `omnitoken statusline -setup -target antigravity`（及 `-setup-undo`）采用与 Claude Code 相同的无损包装策略：保留用户已有状态栏（如 `agy-powerline`），仅通过独立包装脚本 `statusline-hook-antigravity.sh` 旁路捕获配额并完整转发 `stdin`。
- `collect.StatusQuotaReader` 同时轮询两份文件，将 Antigravity 各配额桶转为权威 `model.QuotaSnapshot`（`source = "antigravity"`，`limit_id = "antigravity-account"`，`window_minutes = 300 | 10080`）。

### 2.4 Hub、Web 与 macOS 菜单栏的官方配额与分桶详情
- `internal/server/windows.go` 将 `antigravity` 纳入订阅窗口卡（仅在存在权威 `antigravity` 快照时产出，避免未用 `agy` 时出现空白占位）；主卡显示最紧的 5h / Week 官方已用百分比、真实重置倒计时、蓝色时间进度刻度及 ADR-0025/0034 预估余量。
- 当某来源存在多个官方配额分桶（例如 Antigravity 的 `gemini-5h`、`gemini-weekly`、`gemini-argon-wtu-weekly`、`3p-5h`、`3p-weekly`）时，Web 配额区与 macOS 菜单栏「配额与预估」均允许点击该来源，分别在 `DetailDrawer` 与相邻详情窗口（`#quota-detail`）查看**每个官方配额桶**的独立已用百分比、蓝色时间刻度与重置倒计时。

## 3. 验收
- 保持零外部凭据读取、零私有 RPC 探测；
- `agy-powerline` 等已有状态栏在 `-setup -target antigravity` 后渲染完全不变，支持 `-setup-undo` 一键还原；
- 实测 `agy` CLI 会话可产出真实 `gen_ms` / `ttft_ms`（约 120–210 tok/s）与真实 5h / Week 官方配额百分比及倒计时。
