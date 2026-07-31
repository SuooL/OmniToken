# 计费通道分类、设备身份合并与 Codex 速度 —— 实施计划

> 2026-07-31。用户复盘提出六条调整,查证后其中两条的事实与原判断不符(见下)。
> 本计划把六条收敛为五条工作流 A–E,并给出调度顺序。

## 背景:两条被查证推翻的既有判断

### 1. `provider` 列不可信 —— `bedrock` 是误判

`internal/model/provider.go:19` 判 Bedrock 的依据只有一条字符串规则:model ID 含
`anthropic.claude`。本机实测:

- 无任何 Bedrock 配置(Claude Code 用 Bedrock 必须 `CLAUDE_CODE_USE_BEDROCK=1`,
  settings 与环境变量里都没有);
- 被判为 `bedrock` 的 3,172 条,模型名是 `anthropic.claude-opus-4-8` 这类,
  实为中转商采用 Bedrock 风格命名;
- 中转商**也会用裸名**:`claude-opus-4-6` 有 443/995 条无 `requestId`。

因此按模型名区分官方与第三方在原理上就不成立。

**可用信号:逐事件 `requestId` 有无**(排除 `<synthetic>` 记录)。本机 120 个会话实测:

| 模型 | 带 requestId | 无 requestId |
|---|---|---|
| `claude-opus-4-8` | 23,031 | 0 |
| `claude-opus-5` | 15,579 | 108 |
| `anthropic.claude-opus-4-8` | 0 | 4,218 |
| `anthropic.claude-opus-4-6` | 0 | 402 |

会话级统计:78 个全带、20 个全无、22 个混合;混合中除 1 个是中途换端点
(304/1645),其余都只有 1–4 条 `<synthetic>` 噪声。故判据取**逐事件**,不取会话级。

Codex 侧不受影响:其 provider 取自 rollout 的 `session_meta.model_provider`,
是配置声明值,已经可靠(`openai` / `custom` / `sub2api` / `enjoy` / `aihub`)。

### 2. Codex 速度可做 —— ADR-0009 的排除理由需要修订

ADR-0009 判定 Codex 无法计算生成速度,依据是 rollout 有约 70% 的记录时间戳为
刷盘时刻。该测量本身仍然成立(仅 `token_count` 行:74.7% 为成批回放),但它只考察了
**日志行时间戳**,遗漏了两点:

1. **Codex 自己记录权威计时**。`event_msg/task_complete` 携带 `turn_id`、
   `started_at`、`completed_at`、`duration_ms`、`time_to_first_token_ms`。
   这些值由 Codex 计算并写入,与日志行时间戳无关,回放不影响。
2. **按文件位置归属可绕开回放**。`task_started` / `task_complete` 成对夹住一个
   turn,`token_count` 顺序落在其间;回放是按原顺序整块写入的,**位置序保持不变**。

200 个 rollout 实测两种归属方式:

| | 按时间戳窗口 | **按文件位置** |
|---|---|---|
| 可用 turn 覆盖率 | 38.5% | **84.4%**(944/1119) |
| 并集口径 tokens/s | 222.8(离谱) | **34.7** |
| 逐 turn 中位 / P90 | 56.2 / 2600.4 | **53.4 / 101.1** |
| 每 turn output 中位 | 8,742 | 6,235 |

对照 claude-code 实测 50–70 tok/s,34.7 量级合理。

**须如实标注的口径偏差**:`duration_ms − time_to_first_token_ms` 里含 turn 内的
工具执行时间,因此该值是**保守下界**。扣除工具区间(`patch_apply_end`、
`mcp_tool_call_end` 均带 duration)是后续精化方向,不在本轮。

### 3. 其余查证结论

- **设备重复**:`JasonHudeMacBook-Pro.local` 即本机(`hostname` 返回值),
  `suool-mac` 是 `config.json` 的 `device_name`。影响 1 条事件 + 1,296 条配额快照
  (codex primary 1,288 + claude 8)。源自 `serve`/`agent` 的 hostname 兜底路径。
- **Claude 5h 配额是机会性的**:只在 Claude Code 渲染状态栏时捕获,
  `resets_at` 一过即被 `internal/server/live.go` 过滤掉,所以时有时无。
- **Codex 现在拿不到 5h**:`primary` 在 2026-07-09 前是 300 分钟窗口,
  之后 Codex 改语义为 10080(周),`secondary` 同日停报。

## 五条工作流

| # | 内容 | 依赖 | 规模 |
|---|---|---|---|
| D | 去掉弹窗「近 10m」文案与「N 前」年龄串 | 无 | 小 |
| C | 弹窗配额卡:5h 优先 / 周兜底 / 暂无 | 无 | 中 |
| E | Codex 速度与 TTFT | 修订 ADR-0009 | 大 |
| A | 计费通道三分类(订阅 / 官方 API / 第三方) | 新增 ADR | 大 |
| B | 设备身份合并 | 新增 ADR | 中 |

调度顺序:**D+C 并作一个 PR 先落地** → E → A → B。D/C 当天可见,E 收益最大,
A 最需设计,B 依赖 A 的设置页改动落定。

## 全局约束(每条工作流都适用)

- 每条工作流一个隔离 worktree,从最新 `origin/dev` 切出,分支 `feature/<描述>`。
- **绝不触碰计数列**。A 只改 `provider` 归属列,B 只改 `device` 归属列;
  两者都不得改动 token 计数、事件条数、`event_id`。
- **`event_id` 生成依据不得改变**(ADR-0004 铁律)。E 新增的 `gen_ms`/`ttft_ms`
  是派生列,不参与 ID。
- 行为变更严格测试先行:先确认测试因缺少目标行为而失败,再写生产代码。
- 未测量 ≠ 零。覆盖率不足必须显式标注,不能画成 0。
- Web 保持零构建链:不引入 npm / CDN / 框架。
- `web/tokens.css` 与 `web/format-core.js` 是唯一来源,改后跑 `make desktop-sync`。
- 完成前必跑:`make check`;涉及 `desktop/` 另跑 `make desktop-check` 与
  `cargo tauri build`;涉及 Web 做真实浏览器验收。
- 各 PR 只在 `docs/roadmap.md` 追加**自己那一行**,减少并行冲突。

## 阶段一:设计(并行,三份互不重叠的文件)

- **ADR-0018 计费通道分类**:三类定义、`requestId` 判据与其局限、历史数据重判的
  边界(只改 `provider` 列、单向、可审计)、`<synthetic>` 排除规则。
- **ADR-0019 设备身份合并**:这是 CLAUDE.md 铁律之外的**第三处覆盖**,必须写清
  只并归属列、不碰计数列、合并可审计、`self`↔`self` 的冲突规则。
- **ADR-0009 修订**:追加「后续修订(2026-07-31)」小节,记录 `task_complete`
  权威计时与文件位置归属,修订第 3 条「Codex 仍不纳入速度统计」。

## 阶段二:实现(并行 worktree,按上述顺序合并)

- **W1 = D + C**:`desktop/ui/app.js`、`desktop/ui/index.html`、
  `desktop/src-tauri/src/live.rs`(配额重新进入 `PopoverView`,注意本轮刚删过
  一批无读者字段 —— 这次要**先有读者再加字段**)。
- **W2 = E**:`internal/parser/codex/parser.go`(turn 括起 + `gen_ms`/`ttft_ms`)、
  `internal/store/speed.go`、`web/speedview.js`、ADR-0009 修订、`-rescan` 回填。
- **W3 = A**:`internal/parser/claudecode/parser.go`(采 `requestId`)、
  `internal/model/provider.go`、store 查询、`web/` 分栏、历史重判。
- **W4 = B**:`internal/server`、`internal/store`、`web/settingsview.js`、
  修掉 `serve`/`agent` 的 hostname 兜底。

## 验收

- A:面板三栏分开;本机「官方 API」应接近 0(用户自述只有订阅与第三方),
  若不为 0 需逐条解释。
- B:合并后 `JasonHudeMacBook-Pro.local` 不再单列;计数总量不变。
- C:5h 有值时显示 5h,无值退周,都无显示「暂无」,三态可区分。
- D:弹窗不再出现「近 10m」与「N 前」。
- E:速度页出现 Codex 泳道并标注覆盖率(约 84%)与「含工具时间的保守下界」。
