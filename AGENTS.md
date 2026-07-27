# AGENTS.md

给在本仓库工作的 AI 编码助手(Claude Code / Codex / Cursor / Copilot 等)的项目约定。
**协作流程见 [CONTRIBUTING.md](CONTRIBUTING.md)** —— 分支、PR、合入规则都在那里,本文件只讲写代码时必须知道的事。

## 项目速览

跨设备 LLM 用量监控:Go 单二进制(`serve` / `agent` 两个子命令)+ SQLite + 内嵌网页面板。
纯 Go 无 CGO,可交叉编译。设计文档见 [docs/](docs/README.md)。

```sh
make build      # 构建
make check      # 提交前必跑:vet + 测试 + 覆盖率门禁 + 构建
make release    # 交叉编译五平台到 dist/
```

## 架构与依赖方向

```
internal/model        事件模型
   ↓
internal/parser/*     每个工具一个包,Strategy 插件
   ↓
internal/collect      增量扫描 / offset / SSH 镜像
   ↓
internal/store        SQLite
   ↓
internal/server       API + 采集调度 + 内嵌 web
internal/agent        推送 + 中继
```

**依赖单向向下,不得反向。** 例如 `parser` 不能引用 `store`。
`server` 与 `agent` 复用 `collect` 层,靠注入不同的 sink 区分「入库」还是「上报」。

## 正确性铁律

这些不是风格偏好,是数据正确性的基石。违反会造成**不可逆**的后果 —— 污染的是已入库的历史数据,回滚代码救不回来。

### 1. event_id 幂等去重

同一条日志无论从几条通道到达(本地扫描、agent 推送、SSH 拉取、重扫、断网重传)都只能计一次。
背景见 [docs/adr/0004-event-identity.md](docs/adr/0004-event-identity.md)。

**改解析器时必须保证:对同一条日志产出完全相同的 event_id。** 否则历史数据会重复计数。

生成点共三处,改动其中任何一处都要格外小心:

| 位置 | 标识依据 |
|------|---------|
| `internal/parser/claudecode/parser.go` | `message.id` + `requestId`(社区验证过的去重键) |
| `internal/parser/codex/parser.go` | `token_count` 行不带 message id,用 rollout + 时间戳 + 序号 |
| `internal/agent/proxy.go` | 设备 + 前缀 + 起始纳秒 + 序号 |

这三个包设有覆盖率硬门禁,见下文。

### 2. offset 仅在上报成功后推进

日志文件本身就是重传缓冲。提前推进 offset 会在断网时丢数据。

### 3. 权威优先,推断标注

配额取自官方端点;推断值必须显式标注,两者不可混淆。

### 4. 只采元数据

**永不读取对话内容。** API key 只存哈希指纹,绝不存明文。

## 改解析器前必读

解析器的语义**必须对照本地参考项目验证,不要凭记忆**:

- `~/git/references/ccusage`
- `~/git/references/token-monitor`

对照要点见 [docs/references.md](docs/references.md)。这两个项目对日志格式的处理是经过实际数据验证的,
凭印象写出来的字段语义经常是错的。

## 覆盖率门禁

`make check` 会对**生成 event_id 的三个包**强制覆盖率下限:

| 包 | 下限 |
|----|------|
| `internal/parser/codex` | 90% |
| `internal/parser/claudecode` | 80% |
| `internal/agent` | 64% |

其余包不设覆盖率要求,只要 `go vet` 和测试通过即可 —— `internal/server`、`collect` 的 SSH/git 路径、
`cmd` 这类 I/O 密集代码,补 mock 成本高、抓到的 bug 少,不值得为覆盖率数字买单。

阈值定义在 [`scripts/coverage-gate.sh`](scripts/coverage-gate.sh),**只有那一处**。
不要在 CI 配置或别处复制这些数字。

## 设计先行

- 动手前先读并更新 [docs/](docs/README.md)。
- **重大技术决策写 ADR**(`docs/adr/`),不要只留在对话记录里。
- 解析器有基于真实样本结构的单测,改动时一并更新。

## 提交前

```sh
make check      # 必须绿
```

然后按 [CONTRIBUTING.md](CONTRIBUTING.md) 开 PR 到 `dev`。**不要直接推 `dev` 或 `main`。**
