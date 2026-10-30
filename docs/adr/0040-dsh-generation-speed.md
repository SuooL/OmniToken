# ADR-0040 dsh 生成速度:按流式 chunk 时间戳产出 gen_ms / ttft_ms

状态:已采纳(2026-10-30)

## 背景

ADR-0029 接入 dsh 时没有产出 `gen_ms`,而速度视图只认 `gen_ms > 0` 的行
(`livespeed.go`、`speedseries.go`),所以 dsh 在速度页、实时页、托盘里一直落在
`unmeasured_sources`。ADR-0029「速度页(`speedSourceKey` 给它独立分桶)」那句只对了
分桶 —— 桶一直是空的。

用户在 DSH Desktop 里看到会话级的 `101 tok/s`,希望 OmniToken 也能看到 DeepSeek 的速度。

本机实测 324 个会话文件(v1 `session.jsonl.zstd` 7 个、`session.v3.jsonl.zstd` 34 个、
`session.v4.jsonl.zstd` 283 个),只看字段名与数值,不看内容:

- v3/v4 的 `assistant/message.data.stream[]` 带**最终那次尝试**的每个 chunk 的时间戳:
  单个 chunk 是 `{type:"chunk", time}`,批量 delta 是 `{time0, dt[]}`。
- v1 没有内嵌 stream,同样的 chunk 写成顶层记录:`assistant/chunk`(`time`)与
  `text-chunks` / `reasoning-chunks` / `tool-call-chunks`(`time0`),`data.turn/step`
  指明属于哪一步。
- `step/start` 与 `llm/retry-started` 都带 `turn/step`,标出每次尝试的请求起点。
  失败的尝试在 v3+ 写成 `assistant/attempt`,在 v1 是 `llm/retry-started` 之前的 chunk。
- message 记录时刻与最后一个 chunk 相差 4–26ms。
- 三种格式的 `turn`/`step`/`time`/`time0` 全部是整数。

DSH Desktop 2.0.15 的口径(`dsh-client-ui-chat/lib/client.js`,grep 所见):每步
`decodeMs = completedTime − firstTokenTime`,会话 `tok/s = Σ outputTokens ÷ Σ decodeMs`。
从截图那个会话的日志复现:

| 区间 | tok/s |
|---|---|
| 首次尝试的首个 chunk → message 写入 | 99.3(界面显示 101) |
| **最终尝试的首个 chunk → message 写入** | **122.7** |
| 请求起点 → message 写入(含首 token 等待) | 54.1 |

dsh 界面的数含重试:该会话 25 个重试过的步给分母多加了约 4,500 秒的失败尝试与退避。

## 决定

1. **`gen_ms` = message 记录时刻 − 最终尝试的首个 chunk 时刻**。
   - 首个 chunk:v3+ 取内嵌 stream 里最小的 `time`/`time0`;v1 取该 `(turn, step)` 在
     最近一次 `step/start`/`llm/retry-started` 之后最早的顶层 chunk 记录。
   - 失败尝试与退避不进区间,首 token 等待与工具执行也不进 —— 这是**纯生成**速度,
     比 dsh 界面的数高(那个含重试时间)。
   - 区间 ≤ 0 不产出:未测,不是速度为零。
   - **不套 ADR-0006 的 30 分钟 clamp**:区间由 dsh 逐 chunk 打点测得,不是从日志间隔猜的,
     理由同 ADR-0009 对 Codex 的修订。实测最长的两条 29 分钟,各产出 256,000 token
     (输出上限),chunk 间最大间隔 11.7 秒,是真实的连续生成。
2. **`ttft_ms` = 首个 chunk − 该步最近一次请求起点**(`step/start`;重试过则为最后一次
   `llm/retry-started`)。一步一次请求,所以每条事件各带一个 TTFT,与代理一致,不同于
   Codex 的每 turn 一个。它含 dsh 客户端组装请求的时间 —— 日志里没有更精确的「请求发出」标记。
3. **只读时间戳**,chunk 内容(正文、工具参数)一律不解码(只采元数据)。
4. **计时字段读不出只丢计时、不丢用量**:先整体解码,失败时退回只解 `usage`,与旧行为一致。

## 与幂等铁律的边界

- `event_id` 的生成依据不变,`gen_ms`/`ttft_ms` 是派生列。全量 324 个文件、19,314 条事件,
  新旧解析器除这两列外逐字段相同(含 `event_id`)。
- 回填走既有的 numFill:库内为 0 且新值 > 0 时才写,绝不碰计数列(ADR-0009 决策第 4 条)。
  **一经写入不再覆盖**,所以口径必须在用户重扫之前定死 —— 本 ADR 就是那个定点。

## 回填

- 还在增长的会话文件下一次扫描自动补齐:FullReparse 整文件重解,事件变了的块
  跨扫描去重 key 也变了(ADR-0032),会重新交付。
- 不再增长的文件不会被重读(扫描在 `size == offset` 时直接跳过),历史要靠
  `agent -rescan` / `serve -rescan`。它重置**所有**来源的位点,运行前要先停掉常驻进程,
  见 `docs/configuration.md`。

## 实测

| 范围 | 覆盖(事件 / output token) | Σoutput ÷ Σgen_ms |
|---|---|---|
| 全部 324 个文件 | 100% / 100% | 88.7 tok/s |
| v1 / v3 / v4 | 100% / 100% / 100% | 83.9 / 219.0 / 79.6 |
| 截图那个会话 | 100% | 122.7(界面 101,差额是重试时间) |

Σ÷Σ 是逐步求和,不是面板用的并集;单会话内的步是串行的,两者接近。
TTFT 中位 / P90:v4 2.57s / 13.5s,v3 2.46s / 4.5s,v1 6.86s / 10.9s。
解析代价(最大的文件,17.9MB 压缩):590 → 617 ms/次(+5%),分配 306 → 309 MB。

## 跨来源口径不同,面板必须标注

| 来源 | `gen_ms` 区间 | 含首 token 等待 | 含工具时间 |
|---|---|---|---|
| Claude Code | 上一条 user 记录 → assistant 记录 | 是 | 否 |
| Codex | `duration − ttft`,按 turn | 否 | 是(保守下界) |
| 代理 | 请求全程 | 是 | 否 |
| dsh | 首个 chunk → message 记录 | 否 | 否 |

按模型的速度表用 `CanonicalModel` 跨来源合并,dsh 驱动的 `claude-*`/`gpt-*` 会和
Claude Code/Codex 同行、混入不同的分母。实测 dsh 98.6% 的 output token 来自 DeepSeek
模型,Claude/GPT 约 1.3%,影响可忽略;面板在含 dsh 的行上标出口径。

## 未做

- **v1→v3 迁移造成的重复计数**。同一会话目录里新旧格式文件并存(8 个目录),推断是 dsh
  迁移格式时复制了旧会话且保留旧文件。v3→v4 保留了 `seq`,`event_id` 相同、已被去重;
  v1→v3 重编了 `seq`,221 行、约 62.6M token(dsh 全部用量的 1.70%)被计两次。
  这是计数正确性问题,要动去重键,单独立 ADR。
- 把思考与正文分开测速等进一步细化。

背景:ADR-0009(生成速度语义)、ADR-0029(dsh 解析器)、ADR-0032(跨扫描交付去重)。
