# ADR-0042 Google Antigravity、Gemini CLI 与 Gemini 本地代理接入

状态:已采纳(2026-11-22)

## 背景

用户除 Claude Code、Codex、DeepSeek Harness(`dsh`) 之外，开始在日常开发中使用
Google Antigravity(AGY) 与 Gemini CLI，并希望通过可选的本地 API 代理观测直接调用
Gemini API 的吞吐与 TTFT。

此前 [roadmap.md](../roadmap.md) 在 2026-07-28 登记过 `.gemini` 目录无用量日志的观察，
如今情况已发生变化:

1. **Google Antigravity** 在 `~/.gemini/antigravity/brain/<conversation-id>/.system_generated/logs/transcript.jsonl`
   落盘结构化 JSONL 轨迹。本机实测两个会话共 30 条 `PLANNER_RESPONSE` 记录，每条均携带
   `step_index`、`created_at`、`model`、`input_tokens`、`cache_read_tokens`、`output_tokens`。
   相邻步数值递推验证(步 1 `input=20789, cache_read=0` → 步 5 `cache_read=20790, input=4602`
   → 步 10 `cache_read=25393, input=12200` → 步 15 `cache_read=37594, input=21018`)证明:
   **Antigravity 的 `input_tokens` 已是不含缓存的净输入**，与 OmniToken/Anthropic 四分量口径一致，
   无需再减去 `cache_read_tokens`。
2. **Gemini CLI** 在 `~/.gemini/tmp/**/chats/session-*.json` 及 `*.jsonl` 落盘会话用量
   (语义对照本地参考项目 `~/git/references/ccusage` Rust 适配器 `adapter/gemini`):
   `messages[]` 中 `type == "gemini"` 的记录带 `tokens.{input, output, cached, thoughts, tool, total}`，
   另支持单行 JSONL 及 `stats` / `result.stats` 形态。与 OpenAI/dsh 不同，Gemini 的 `thoughts`
   独立于 `output` 之外(`total = input + output + thoughts + tool`)，且按输出单价计费；
   `input` 是否含 `cached` 按 `total` 是否等于含/不含 `cached` 之和判别(`normalizeSessionInput`)。
3. **本地 API 代理(F14)** 目前仅内建 `/anthropic` 与 `/openai` 上游且仅解析 Anthropic / OpenAI
   响应体，无法解析 Gemini `usageMetadata` 或从 `/v1beta/models/<model>:generateContent` 路径提取模型名。

## 决定

### 1. 两个独立的一等来源(`antigravity` / `gemini`)

不合并为单一来源:两者日志形态、会话模型、产品入口与使用场景不同，分别注册为一等来源:

- `internal/parser/antigravity`: `const Source = "antigravity"`
- `internal/parser/gemini`: `const Source = "gemini"`

两者在 Web 九页与 macOS 菜单栏均拥有独立的品牌标签(`Antigravity` / `Gemini`)、SVG 图标与语义配色，
并在 `internal/store/livespeed.go` (`speedSourceKey`) 与 `internal/store/telemetry.go`
(`telemetryDailySources`) 中作为一等来源保留，不再被折叠进 `api`。

### 2. Antigravity 解析器(`internal/parser/antigravity`)

- **默认目录**: `~/.gemini/antigravity/brain`(可通过 `$ANTIGRAVITY_HOME` → `$ANTIGRAVITY_HOME/brain`
  或配置项 `antigravity_dirs` 覆盖)。
- **文件匹配**: 仅扫描位于 `**/.system_generated/logs/transcript.jsonl` 的文件，明确排除同目录的
  `transcript_full.jsonl` 与 `chunks/` 副本，避免重复扫描。
- **增量扫描**: `transcript.jsonl` 为追加写 JSONL，每条 `PLANNER_RESPONSE` 自包含 `step_index`、
  `created_at`、`model` 与 token 分量，走增量字节偏移扫描(`FullReparse: false`)。
- **只采元数据**: 反序列化结构体仅声明 `step_index`、`source`、`type`、`status`、`created_at`、
  `model`、`input_tokens`、`cache_read_tokens`、`cache_creation_tokens`、`output_tokens`，
  绝不解码 `content`/`thinking`/`tool_calls`/`media`(N4)。
- **会话 ID**: 从路径 `<conversation-id>/.system_generated/logs/transcript.jsonl` 提取，
  为此在 `collect.SourceSpec` 上扩展可选的 `Match(path)` 与 `ParsePath(path, r, device, turnStartMS)`，
  保持既有 `Parse` 签名兼容。
- **事件过滤与 token 映射**: 仅处理 `type == "PLANNER_RESPONSE"`、非进行中状态(`RUNNING`/`PENDING`)、
  且四分量总和 > 0 的记录。`InputTokens = input_tokens`、`CacheReadTokens = cache_read_tokens`、
  `CacheCreationTokens = cache_creation_tokens`、`OutputTokens = output_tokens`。
- **`event_id` 铁律**: `"agy:" + sha1(session_id | step_index | created_at | 四分量)[:12]`(24 hex)，
  golden 单测 `TestEventIDStable` 钉死。
- **生成速度**: `transcript.jsonl` 仅记录秒级步创建时刻，不含完成时刻或流式分片时间戳，
  故 `gen_ms = 0`、`ttft_ms = 0`，如实归入未测来源，不编造速度。

### 3. Gemini CLI 解析器(`internal/parser/gemini`)

- **默认目录**: `~/.gemini/tmp`(可通过 `$GEMINI_DATA_DIR` 逗号分隔路径或配置项 `gemini_dirs` 覆盖)。
- **文件匹配与重解**: 扫描目录下 `*.json` 与 `*.jsonl`；因 Gemini CLI 会原地重写整份
  `session-*.json`，采用 `FullReparse: true`，`Consumed` 记为已读完整字节数(配合 ADR-0032
  跨扫描交付去重避免重复投递)。
- **语义对齐 `ccusage`**:
  - 支持整文件会话 JSON(`messages[]` 中 `type == "gemini"`)、单条记录 JSON/JSONL(同文件内按 `id`
    保留最新一条)、以及 `stats` / `result.stats`(分模型 `stats.models.<model>.tokens` 与扁平 `stats`)。
  - `normalizeSessionInput`: 当 `cached > 0` 且 `total == input + output + thoughts + tool`
    且 `total != input + output + thoughts + tool + cached` 时，从 `input` 中扣除 `min(input, cached)`。
  - `stats` 路径按 `subtractCachedOverlap` 扣除 `min(input, cached)`。
  - `InputTokens = inputWithoutCache + tool`，`CacheReadTokens = cached`，
    `OutputTokens = output + thoughts`(与 `ccusage` 成本口径及 `total` 严格对账)。
  - 所有分量均为 0 但 `total > 0` 时回退为 `OutputTokens = total`。
- **`event_id` 铁律**:
  - 带消息 `id` 时: `"gem:" + sha1(session_id | id)[:12]`(24 hex)；
  - 无 `id`(如 `stats` 记录或匿名行)时: `"gem:" + sha1(session_id | ts | ordinal | model | input | output | cache_read)[:12]`。
  - golden 单测 `TestEventIDStable` 钉死。
- **生成速度**: 日志不含可靠生成耗时，`gen_ms = 0`、`ttft_ms = 0`。

### 4. 计费通道分类(`internal/model/provider.go`)

新增 Google 第一方 provider 标签并纳入 `providerChannel` 与 `RankedProviders()`，避免落入默认 `ChannelRelay`:

- `google` → `ChannelUnknown`(`rankEndpoint = 2`，日志解析默认标签:确认为第一方端点但账单方式未明，不猜)
- `google-api` → `ChannelAPI`(`rankBilling = 3`，本地代理检测到 `x-goog-api-key` 或 `?key=`)
- `google-oauth` → `ChannelSubscription`(`rankBilling = 3`，本地代理检测到 Google OAuth Bearer 凭据)

### 5. 本地 API 代理 Gemini 端点(`internal/proxy`)

- 新增默认上游路由: `"google": "https://generativelanguage.googleapis.com"` 与别名 `"gemini"`。
- **模型提取**: 请求体 JSON 无 `model` 时，从路径 `/models/<model>(:<action>)?` 提取模型名；
  响应体/SSE 中 `modelVersion` 作为兜底。
- **用量解析**: `usageObserver` 扩展支持 Gemini `usageMetadata`
  (`promptTokenCount` / `cachedContentTokenCount` / `candidatesTokenCount` / `thoughtsTokenCount` / `toolUsePromptTokenCount`)，
  同时兼容非流式 JSON 与 `?alt=sse` 流式响应，精确记录 `gen_ms` 与 `ttft_ms`。
- **凭据指纹**: 支持对 `x-goog-api-key` 与 `?key=` 计算 SHA-256 前 12 位 hex 作为 `account_label`，绝不落盘明文。

### 6. 定价表、SSH、进程表与覆盖率门禁

- **定价表**: `internal/pricing/pricing.go` 查询链补充 `"gemini/"`、`"vertex_ai/"`、`"google/"` 前缀
  (与 `ccusage` 一致)，命中 LiteLLM 内置 Gemini 价表；未收录的新模型(如 `gemini-4-neon-high`)
  维持未定价诚实披露并支持设置页手动覆盖。
- **SSH 镜像**: `remotePullSets` 增加 `antigravity`(`.gemini/antigravity/brain`，仅同步 `transcript.jsonl`，
  排除 `transcript_full.jsonl` 与 `chunks/`) 与 `gemini`(`.gemini/tmp`，同步 `*.json`/`*.jsonl`)。
- **进程表**: `internal/collect/procs.go` 增加 `gemini` CLI 与 `agy` CLI 交互会话识别。
- **覆盖率门禁**: `internal/parser/antigravity` 与 `internal/parser/gemini` 均生成 `event_id`，
  纳入 `scripts/coverage-gate.sh` 强制门禁(85%)。

## 影响

- 仅新增来源与只读解析，不修改既有 `claude-code`/`codex`/`dsh`/`proxy` 的 `event_id` 与历史数据。
- Web 九页与 macOS 菜单栏同步展示五个一等来源(`Claude`、`Codex`、`DeepSeek`、`Antigravity`、`Gemini`)。
- 涉及 `desktop/` 共享设计令牌与格式化文件时，严格通过 `make desktop-sync` 与 `make desktop-check` 保持字节一致。
