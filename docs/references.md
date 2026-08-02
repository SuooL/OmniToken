# 参考项目调研笔记

本地参考代码(已 clone):`~/git/references/ccusage`、`~/git/references/token-monitor`。
**原则:解析语义、去重规则、定价口径必须对照参考实现验证后再写,不凭记忆。**

## ccusage(github.com/ccusage/ccusage)

单机 CLI 统计工具,用户长期使用,数字口径的对账基准。

**能力**:15+ CLI 工具解析;daily/weekly/monthly/session/blocks(5 小时计费窗口);
LiteLLM 定价;statusline。**缺口**(我们的价值):无服务端/多机、事后单次统计、无速度指标。

**已吸收的实现知识**:

| 主题 | 位置(rust/crates/ccusage/src/) | 结论 |
|---|---|---|
| Claude Code 去重 | adapter/claude/ | `message.id + requestId` 联合键;跳过 `<synthetic>` 模型 |
| Codex 重复事件 | adapter/codex/parser.rs | token_count 会重复出现(限流刷新等):**仅当 total_token_usage 前进时才计数** |
| Codex 用量兜底 | 同上 | 无 last_token_usage 时用相邻 total 差值恢复;cached 与 input 取 min 钳制 |
| Codex 语义 | 同上 | OpenAI 口径 input 含 cached;reasoning 计入 output |
| 无定价模型 | adapter/codex/codex-auto-review-fallbacks.json | `codex-auto-review` 等按日期区间映射到真实模型计价 |
| 速度统计 | adapter/codex/speed.rs | service_tier(standard/priority)影响计价与速度口径,M3 参考 |
| 定价 | — | LiteLLM `model_prices_and_context_window.json`;cache_read 缺失时按 input 价计 cached |

## token-monitor(github.com/Javis603/token-monitor)

Electron 桌面应用,多机 hub + SSE 实时。**与本项目差异**:桌面形态(服务器错配)、
hub 仅同步摘要级、无事件级中心库、归档仅覆盖"观察过的日期"、无速度/API 直调。

**值得借鉴**:hub→SSE 秒级推送的体验目标(F10);活动热力图、按工具/模型堆叠历史、
K 线视图等面板形态。

> 两处**已评估后决定不借鉴**,理由均见 roadmap 的「明确不做」小节:
> 28+ 工具路径清单(对应长尾工具覆盖 F17)、多币种展示。

## 架构级研究(2026-07-27,读源码)

### ccusage 的框架启示

- **Adapter 分层**(rust/crates/ccusage/src/adapter/&lt;tool&gt;/):每工具一目录,内部再分
  `paths / loader / parser / aggregate / report`。我们的 `internal/parser/<tool>` 对应其
  parser 层;paths 逻辑在 config,aggregate/report 在 store/server——分层等价,
  但当解析器增多时可效仿其"每 adapter 自带 README + fixtures + snapshots"的自文档做法。
- **blocks 算法**(blocks.rs `identify_session_blocks`):条目按时间排序;
  块起点 = **首条目时间戳向下取整到小时**(UTC);当 `距块起点 > 5h` **或**
  `距上一条目 > 5h`(空闲)即封块,空闲还插入 gap 块;活跃块 = now 在窗口内且
  最近有活动,计算 burn rate 与投影。我们多设备合并后的 blocks 才是真实的
  **账户级**窗口(ccusage 单机视角反而不完整)。
- 双实现(TS 主 + Rust 加速)与 snapshot 测试是其质量手段;我们规模尚小,用
  Go 单测锁定关键语义即可。

### token-monitor 的框架启示(src/hub/server.js、src/agent/agent.js 全文精读)

- **SSE 契约**(照抄级参考):`GET /api/stats/stream` 响应头带
  `x-accel-buffering: no`(防 nginx 缓冲)+ no-cache;连接即推 `snapshot` 事件,
  之后 ingest 触发 `stats` 事件(带 reason);30s 注释行心跳;close/error 双清理;
  **无订阅者时跳过聚合计算**(`if size===0 return`)。
- **传输无关内核**:`ingest()` 同时被 HTTP handler 和同进程 widget 调用——印证我们
  的 Sink 抽象;推论:**SSE 广播必须挂在入库层**,本地采集器与 HTTP ingest 两条
  路径都要触发,不能只挂 HTTP。
- **安全姿态**:未设 secret 时强制只绑 loopback、拒绝局域网(保护账户身份);
  我们采纳其精神,且读接口比警告更硬:未设 token 时写接口仍只是启动打醒目警告,
  但「监听地址可被其它机器访问 + 未设 token」直接拒绝启动(ADR-0016)——
  与其"拒绝局域网"是同一条逻辑,只是把选择权留给了配 token 的人。
- **设备失活判定**:`staleAfterMs`(默认 10min)标记设备离线——Live 页设备
  在线状态采用同款语义(活跃 <2min / 陈旧 >10min)。
- agent 的 watch + debounce(1.5s)模式留作我们 fsnotify 化(M3)参考。

### token-monitor 的工程组织(重点参考对象,与本项目重叠最多)

其顶层布局:`src/{agent, hub, electron, shared, worker}`(按**运行时角色**分包)+
`tests/`(与 src 目录**一一镜像**)+ `docs/`(按主题一文一档:API.md、
configuration.md、privacy.md、export.md、各平台 setup 指南)+ `scripts/`(构建/签名
工具)+ 多语言 README + AGENTS.md/CLAUDE.md。`src/shared` 是几十个单一职责领域小
模块(currency、exchangeRates、history、deviceState、各 provider 的 limits 探测……),
前端 renderer 把"渲染策略"从 app.js 拆成独立模块(breakdownRenderPolicy 等)。

**对照采纳**(Go 习惯下的等价物):
- 运行时角色分包 ↔ 我们的 cmd/omnitoken + internal/{server,agent,collect,…} 已等价;
  测试用 Go 惯例同包 `_test.go`(等价于其 tests 镜像);
- **采纳**:docs/ 增加 API.md(HTTP/SSE 接口契约)与 configuration.md(全配置项
  一览),对齐其"按主题一文一档";
- **采纳**:前端随页面增多按视图拆模块(format/api/overview/live),对齐其
  renderer 的策略拆分,避免 app.js 单体膨胀;
- 其 `src/shared/*Limits.js` 每 provider 一件套,是未来 F18(额度检测)的现成地图。

## ccstatusline(github.com/sirmalloc/ccstatusline)★ 配额数据的关键来源

Claude Code 状态栏工具,**实时显示的 5h/周配额之所以准确,是因为它根本不推断**:
直接调 Anthropic 官方端点 `GET https://api.anthropic.com/api/oauth/usage`
(`Authorization: Bearer <OAuth token>`、`anthropic-beta: oauth-2025-04-20`,5s 超时),
返回账户级 `five_hour`/`seven_day`/`seven_day_opus`/`seven_day_sonnet` 桶
(`utilization` + `resets_at`),新账号则用 `limits[]` 数组
(`kind`、`percent`、`scope.model.display_name` 表示分模型周配额)。
token 取自 `~/.claude/.credentials.json` 的 `claudeAiOauth.accessToken`,
macOS 回落 keychain 服务名 `Claude Code-credentials`。
细节:`utilization=0 且 resets_at=null` 是占位符须丢弃(其 issue #343);
429 依 `Retry-After` 退避;扁平桶优先于 limits[](issue #503)。

源码定位:`src/utils/usage-fetch.ts`(端点/凭据/schema)、`usage-prefetch.ts`(节流)。
**OmniToken 已采纳此通道**,见 [ADR-0007](adr/0007-quota-observation.md)。

## cc-statusline(github.com/chongdashu/cc-statusline)

Claude Code 状态栏生成器:它生成一段 bash,由 Claude Code 每次渲染时调用。
**不解析日志**,消费 Claude Code 传给 statusline 钩子的 stdin JSON:
`.cost.total_cost_usd`、`.cost.total_duration_ms`、
`.context_window.total_input_tokens/total_output_tokens`;
$/h = cost×3600000/duration_ms,tok/min = tokens×60000/duration_ms。
5 小时窗口它自己不算——shell 调 `ccusage blocks --json`(5s 超时)取 `isActive`
块的 `usageLimitResetTime // endTime` 算剩余时间与进度条。

**结论**:单会话实时视角,无跨设备、无周用量;其价值参考点是"$/h 与 tok/min
的算法"和"剩余配额用颜色分级(≤10% 粉、≤25% 黄、否则绿)"。
真正的启发是 `usageLimitResetTime` 的来源(见下)。

**ccusage 如何取 Claude 的限额重置时刻**(`adapter/claude/mod.rs:623`):
只在 `isApiErrorMessage=true` 的行里找字面量 `Claude AI usage limit reached|<unix秒>`,
取管道符后的数字。即 Claude Code **仅在触限时**泄露权威重置时刻,平时无限额数据。
详见 [ADR-0007](adr/0007-quota-observation.md)。

## 日志格式实测记录(本机真实样本,2026-07)

**Claude Code** `~/.claude/projects/<encoded-cwd>/<session>.jsonl`,行自包含:
`type=assistant` 行含 `message.{id,model,usage}`、`requestId`、`uuid`、`timestamp`、
`cwd`、`gitBranch`、`sessionId`、`version`;usage 含 `cache_creation.{ephemeral_1h,5m}`、
`service_tier`。子代理在 `<session>/subagents/*.jsonl`,同构。

**Codex** `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`(另有
`archived_sessions/`),行**不自包含**(模型/cwd 依赖前文,因此采集用整文件重解析):
`session_meta`(session_id、rollout id、cwd、cli_version、model_provider)→
`turn_context`(model、cwd,可逐 turn 变化)→ `event_msg{token_count}`
(`info.last_token_usage` 为单次增量、`total_token_usage` 为累计)。
模型名实测分布:gpt-5.6-sol / gpt-5.5 / codex-auto-review / gpt-5.6-terra。
