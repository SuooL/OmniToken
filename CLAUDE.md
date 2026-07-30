# OmniToken 开发约定

## 命令

```sh
make build      # 构建
make check      # 提交前必跑:vet + 测试 + 覆盖率门禁 + 构建
make release    # 交叉编译五平台到 dist/
```

调试时可单跑 `make vet` / `make test` / `make cover`。

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

依赖单向向下,不得反向 —— `parser` 不能引用 `store`。
`server` 与 `agent` 复用 `collect`,靠注入不同 sink 区分入库与上报。

## 正确性铁律

违反会污染已入库的历史数据,回滚代码救不回来。

### event_id 幂等去重

同一条日志无论从哪条通道到达(本地扫描、agent 推送、SSH 拉取、重扫、断网重传)都只能计一次。
**改解析器时必须保证对同一条日志产出相同的 event_id**,否则历史数据会重复计数。

生成点三处:

| 文件 | 标识依据 |
|------|---------|
| `internal/parser/claudecode/parser.go` | `message.id` + `requestId` |
| `internal/parser/codex/parser.go` | rollout + 时间戳 + 序号(`token_count` 行无 message id) |
| `internal/proxy/proxy.go` | 能认出同一次请求时复用日志的 id(`message.id` + `request-id`,ADR-0013);认不出时用 设备 + 前缀 + 起始纳秒 + 序号 |

背景见 `docs/adr/0004-event-identity.md`。

### 其余三条

- **offset 仅在上报成功后推进** —— 日志文件本身是重传缓冲,提前推进会在断网时丢数据。
- **权威优先,推断标注** —— 配额取自官方端点,推断值必须显式标注,两者不可混淆。
- **只采元数据** —— 永不读取对话内容;API key 只存哈希指纹,绝不存明文。

## 改解析器前

语义必须对照本地参考项目验证,不要凭记忆:`~/git/references/ccusage`、
`~/git/references/token-monitor`。对照要点见 `docs/references.md`。
凭印象写出的字段语义经常是错的。

解析器有基于真实样本结构的单测,改动时一并更新。

## 覆盖率门禁

`make check` 只对生成 event_id 的三个包(`parser/codex`、`parser/claudecode`、`proxy`)
强制覆盖率下限。其余包不卡覆盖率 —— 改 HTTP handler、网页面板、配置解析不会被覆盖率拦下。

阈值定义在 `scripts/coverage-gate.sh`,只有那一处,不要复制到 CI 配置或文档里。
需要调整某个下限时,在 PR 里说明理由。

## 分支与 PR

| 分支 | 角色 |
|------|------|
| `main` | 发布分支,里程碑时由 `dev` 合入并打 tag。 |
| `dev` | 集成分支,**PR 目标始终是 `dev`**。 |
| `feature/<描述>` | 从最新 `dev` 切出。热修同样用 `feature/*`,无独立 hotfix 分支。 |

**不要直接推 `dev` 或 `main`**,两条分支都有保护。

```sh
git switch dev && git pull
git switch -c feature/你的改动
make check                  # 必须绿,与 CI 跑的是同一条命令
git push -u origin feature/你的改动
gh pr create --base dev
```

CI 跑 `make check`,绿了自动合并并删除分支。

## 什么算做完

- [ ] 实现了 issue / PR 描述里说的行为
- [ ] `make check` 通过
- [ ] 新行为有测试覆盖
- [ ] PR 目标是 `dev`
- [ ] 只改了这次改动需要的文件
- [ ] 若本次完成、放弃或改变了某个功能项,同一个 PR 内改掉
      `docs/roadmap.md` 的状态列;放弃时删除条目并在「明确不做」小节写清理由

## 代码风格

`gofmt` 干净、`go vet` 无告警。没有额外的 lint 硬性要求。

## 提交信息

`类型: 描述`,类型取 `feat` / `fix` / `docs` / `refactor` / `test` / `chore`。
发版 changelog 由提交信息生成,写清楚「改了什么」比「怎么改的」有用。

## 设计先行

动手前先读 `docs/`。重大技术决策写 ADR 到 `docs/adr/`,不要只留在对话记录里。

改变功能范围(新增或去掉一个功能)先改 `docs/requirements.md` 再写代码 ——
它是功能清单的权威来源,`docs/roadmap.md` 只记实现进度。
