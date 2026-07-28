# 架构决策记录(ADR)

一事一档,记录"为什么这样设计"。新增重大决策时按编号追加;推翻旧决策时新增
ADR 标注 supersedes,不改写历史。

| 编号 | 决策 | 状态 |
|---|---|---|
| [0001](0001-collection-strategy.md) | 采集策略:日志解析为主、本地代理为辅 | 已采纳 |
| [0002](0002-tech-stack.md) | 技术栈:Go 无 CGO 单二进制 + SQLite + 内嵌前端 | 已采纳 |
| [0003](0003-transport-topology.md) | 传输抽象:URL 目标 + SSH 拉取 + 无状态中继 | 已采纳 |
| [0004](0004-event-identity.md) | 事件身份与幂等:event_id 构造与去重协议 | 已采纳 |
| [0005](0005-pricing.md) | 成本计算:LiteLLM 定价、查询时计算、真实/等效成本分流 | 已采纳 |
| [0006](0006-worktime-semantics.md) | 工作时长语义:事件区间化 + 双指标(投入/代理运转) | 已采纳 |
| [0007](0007-quota-observation.md) | 配额观测:权威限额数据(Codex 5h/周)优先,推断窗口兜底 | 已采纳 |
| [0008](0008-desktop-client.md) | 桌面端形态:Tauri 菜单栏瘦客户端,前端源码 web 与桌面共用 | 已采纳 |
