# ADR-0029 DeepSeek Harness(dsh)用量采集:被动解析会话日志

状态:已采纳(2026-08-24)

## 背景

用户在本机跑 DeepSeek Harness(dsh,`~/.dsh`,Web UI 在 `127.0.0.1:3080`),这是一个可挂
DeepSeek / OpenAI / Claude 等多家模型的 agent harness。用户希望 OmniToken 统计它的用量,
但**明确不接受代理或改 dsh 配置**这类侵入式做法。

OmniToken 原有两条通用路径:代理(F14,需把 dsh 的 base URL 指向代理——被否)、以及像
Claude Code / Codex 那样**被动读工具自己的日志**。本 ADR 走后者。

实测 `~/.dsh/sessions/<project>/session-<uuid>/session.jsonl.zstd`(zstd 压缩 JSONL):每条
`assistant/message` 带归一化用量 `{inputTokens, outputTokens, cacheReadTokens,
cacheWriteTokens, reasoningTokens}`;`request/context`(或 `request/header.data.header.config`)
给出该 step 的 `provider`/`model`;`session` 行给 `id`/`cwd`。7 个会话 / 4119 行 / 208 条用量
记录里,`seq` 每会话唯一无重复。

## 决定:新增 `dsh` 来源解析器(F27)

新包 `internal/parser/dsh`,`const Source = "dsh"`,对标 codex 解析器。

- **每条 `assistant/message` 出一个 Event**;`assistant/chunk` 里的同值 usage 是副本,不数
  (否则翻倍)。
- **token 映射直取**:实测 `inputTokens` 远小于 `cacheReadTokens`(如 2 vs 13312),说明 dsh 的
  `inputTokens` 已是**非缓存输入**(Anthropic 口径),不像 codex 的 OpenAI 口径含缓存——所以
  `InputTokens=inputTokens` 不做减法,`CacheReadTokens=cacheReadTokens`,
  `CacheCreationTokens=cacheWriteTokens`。`reasoningTokens` 恒 ≤ `outputTokens`,是其子集,
  不另计。
- **event_id = `"dsh:" + sha1(session_id | seq | 四个 token 分量)[:12]`**。`seq` 每会话唯一且
  写入即固定,故重扫幂等(ADR-0004 铁律)。golden 测试 `TestEventIDStable` 钉死具体值。
- **无 dedup_key**:dsh 会话相互独立、不复制历史(sub-agent 是独立会话),沿用 claude-code 那种
  「只有 event_id、没有第二把键」的行为。
- **provider 原样保留**(`anthropic`/`openai`/`deepseek-official` 等用户自取的 ID),缺失落
  `unknown`。**绝不提升为订阅**:dsh 一律经用户配置的 API key 调用,不是第一方 CLI 的订阅通道;
  计费分类由既有 taxonomy 处理(anthropic/openai→unknown,未识别名→relay),这与 ADR-0018
  「判不出落 unknown、不猜」一致。不做 codex 那种 plan-evidence 提升。

## 采集接线

- **压缩文件**:`session.jsonl.zstd`。`listJSONL` 原本只认 `.jsonl`(`filepath.Ext` 看到的是
  `.zstd`),放宽为也接受 `*.jsonl.zstd`。
- **FullReparse**:文件是压缩流,不能按字节增量喂给解析器。沿用 codex 的 `FullReparse` —— 整文件
  重读重解、靠 event_id 去重。`dsh.Parse` 读入全部压缩字节、解压、逐行解析,并把
  `res.Consumed` 设为**压缩字节数**,使 scan 记录的 offset 与磁盘文件大小对齐(只在文件增长时
  重扫)。这是本 ADR 唯一非机械的设计点。
- **依赖**:新增纯 Go 的 `github.com/klauspost/compress/zstd`(无 CGO,保持单二进制交叉编译,N1)。
- 目录默认 `~/.dsh/sessions`(可 `$DSH_HOME` 覆盖),server/agent 配置各加 `dsh_dirs`,
  `LocalSpecs` 增一路,SSH 镜像同步加 `dsh`(rsync 放行 `*.jsonl.zstd`)。
- 覆盖率门禁:`internal/parser/dsh` 生成 event_id,按铁律纳入 `scripts/coverage-gate.sh`。

## 影响与未做

- dsh 用量以 `source=dsh` 进库,自动出现在总量、按模型/设备/项目分布、报表、明细、速度页
  (`speedSourceKey` 给它独立分桶);`telemetry` 的 `extraSources` 机制自动给它一行。会**回填**
  现有 `~/.dsh/sessions` 历史。
- **前端专属卡片/配色留作后续**:总览页的「近 5h 来源卡」目前硬编码 claude-code/codex 两张,
  dsh 暂不单独出卡(但已计入所有聚合)。后续可补 dsh 卡与色板(见 roadmap)。
- 不改 dsh 任何配置、不经代理;数据只来自 dsh 自己写的日志。

背景:ADR-0004(event 身份)、ADR-0018(通道分类)、requirements F27。
