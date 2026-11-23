# ADR-0044: Antigravity 模型分组滚动配额视图（已撤回）

- **状态**: 已撤回（2026-11-22）
- **关联**: ADR-0007 (配额观测)、ADR-0011 (不读凭据/仅公开通道)、ADR-0042 (Antigravity 采集)

## 结论与撤回原因

1. **Antigravity Desktop App** 内部展示的 5h / Week 配额百分比与重置倒计时仅存在于进程内存 UI 中，不落盘至本地日志文件，也未提供官方公开的导出或 `statusLine` 接口；
2. 遵守 ADR-0011 安全边界，OmniToken **不读取其他应用的 OAuth / Keychain 凭据，也不探测未公开的本地 daemon RPC 接口**；
3. 在拿不到桌面端官方 5h / Week 余量读数的前提下，在「配额与预估」中仅展示本地滚动累计 token 汇总容易与「今日使用」重叠且无法反映真实剩余额度，因此将「配额与预估 / 官方配额」中的 Antigravity 分组展示关闭撤回（Antigravity 的 token 用量、模型构成、历史趋势、明细与报表统计仍完整保留，见 ADR-0042）。

> 后续若 Antigravity Desktop App 开放类似 Antigravity CLI (`agy`) 的官方 `statusLine` JSON 通道（携带 `quota.<bucket>.remaining_fraction` 与 `reset_time`），再通过公开 `statusLine` 接入 `quota_snapshots` 与 ADR-0025/0034 容量学习。
