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

### dedup_key:同一次生成被写进两个文件

`event_id` 回答「这条**日志行**见过没有」,拦不住同一次生成被复制成另一条日志行。
Codex 分叉线程(人工 fork 与 subagent)会把父线程整段复制进新 rollout,只改行时间戳,
所以副本的 `event_id` 必然不同。第二把键 `dedup_key` 回答「这次**生成**记过没有」:
`cxg:sha1(turn_id | total_token_usage)`,仅 codex 解析器产出、仅在 `turn_id` 是 UUID 时产出。
**它是 `event_id` 之上的追加约束,不是替代** —— 改 codex 解析器时两把键都要保持稳定。
背景见 `docs/adr/0020-codex-resume-duplicate-events.md`。

### 其余四条

- **offset 仅在上报成功后推进** —— 日志文件本身是重传缓冲,提前推进会在断网时丢数据。
- **权威优先,推断标注** —— 配额取自官方端点,推断值必须显式标注,两者不可混淆。
- **只采元数据** —— 永不读取对话内容;API key 只存哈希指纹,绝不存明文。
- **覆盖已入库的行是白名单制,且全都从不碰计数列** —— 一次请求算给谁,和它被计
  几次,是两回事。分两类,区别在**判据在不在系统之内**:

  | 类别 | 允许的覆盖 | 依据 |
  |---|---|---|
  | 自动(写入/重扫路径,判据是系统可核实的事实) | `source` 的 `proxy → 真实工具` | ADR-0013 |
  | | `device` 的 `observed → self` | ADR-0015 |
  | | `provider` 的重判 | ADR-0018 |
  | 人工发起(管理接口,判据在系统之外) | `device` 的合并 | ADR-0019 |

  自动那三处都是单向的,可以写成永远成立的守卫。设备合并不同:没有任何本地信号
  能证明两个身份是同一台机器,所以它的安全边界不在守卫里,而在「必须由人显式
  发起」这个性质上 —— 采集、解析、ingest 的任何自动路径都不许调用它。

  **唯一一处会删行、会让计数下降的操作是 ADR-0020 的重复生成清理**,它不在上表里,
  因为它回答的正是「被计了几次」而不是「算给谁」。安全边界:只在两行共享同一个
  `dedup_key` 时触发,只删其中一行,且删的是 `ts` 大的那条(副本的时间戳是分叉时的
  刷盘时刻,必然晚于原件)。判据全在库内,与到达顺序无关。

  **新增任何一处覆盖前先写 ADR。**

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

### Agent 开发流程

Agent 执行 non-trivial 功能、跨层重构或 bug 修复时，必须主动完成以下流程，
不得等用户逐次提醒：

1. 开始前运行 `git status`、确认上游基线和现有 worktree，保护用户已有未提交改动。
2. 从最新 `origin/dev` 创建隔离 worktree。人工分支沿用 `feature/<描述>`；
   Codex 客户端创建的分支使用其规定的 `codex/<描述>` 前缀。
3. 复杂改动先提交设计/实现计划；行为变更严格执行测试先行，确认测试因缺少目标行为而失败后再写生产代码。
4. 每个独立可审查阶段在受影响测试通过后形成 focused commit，不把无关修改混入。
5. 声称完成前必须运行本文件规定的完整 gate；涉及 `desktop/` 时额外运行
   `make desktop-check` 和实际 Tauri bundle 构建，涉及 Web 时进行真实浏览器验收。
6. 最后审查 `git diff origin/dev...HEAD`、确认无 secrets/生成垃圾/越界改动，
   推送当前分支并创建目标为 `dev` 的 PR；不得直接提交或推送到 `dev/main`。

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
