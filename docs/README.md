# OmniToken 文档索引

| 文档 | 内容 | 何时读/改 |
|---|---|---|
| [requirements.md](requirements.md) | 需求规格:背景、目标、F1–F18 功能表、非功能约束、验收基准 | 需求变更时先改这里 |
| [architecture.md](architecture.md) | 架构设计:分层、设计模式、不变量、数据模型、待实现部分设计 | 动结构性代码前 |
| [adr/](adr/README.md) | 架构决策记录:每个重大决策的背景/决策/后果 | 做/推翻重大决策时 |
| [references.md](references.md) | 参考项目调研(ccusage、token-monitor)与日志格式实测记录 | 写/改解析器前必读 |
| [API.md](API.md) | HTTP/SSE 接口契约 | 增改接口时 |
| [configuration.md](configuration.md) | 服务端与 agent 全配置项 | 增改配置时 |
| [roadmap.md](roadmap.md) | 里程碑计划与当前状态 | 每次迭代收尾更新 |

工作流约定(design-first):新功能先更新 requirements/architecture(必要时立 ADR),
再实现;解析语义对照 `~/git/references/` 下的参考项目验证;每里程碑以
"端到端可用 + 真实数据验证"为完成标准。
