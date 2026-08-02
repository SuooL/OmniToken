# HTTP / SSE 接口契约

服务端所有接口挂在 `omnitoken serve` 的监听地址下。凭据按用途隔离:

- `token`:迁移期 v1 ingest;
- `read_token`:非 loopback 的查询、面板和 SSE;
- `admin_token`:enrollment、device revocation 与 settings 写入;
- 每台 v2 agent 的 `device_token`:只代表该 `device_id`,用于 ingest/heartbeat。

旧配置只写 `token` 时仍会回落为 read/admin credential,方便平滑升级;新部署应使用
三个不同的高熵值。读接口要不要凭据由监听地址推导(ADR-0016):

| 监听地址 | 读接口 |
|---|---|
| 仅 loopback(`127.0.0.1` / `localhost` / `[::1]`,默认) | 免鉴权,零配置 |
| 可被其它机器访问 + 配了 `read_token` / legacy fallback | 必带 `Authorization: Bearer <read_token>` |
| 可被其它机器访问 + 缺少任一 required scoped credential | 服务端启动即拒绝(退出码 1) |

空主机名(`":8787"`)算**每一个网络接口**,不算 loopback。读接口的 401 带
`WWW-Authenticate: Bearer realm="omnitoken"`。`GET /api/v1/health` 与面板外壳
(`GET /`)始终免鉴权 —— 浏览器首次导航没法带头,而外壳是空壳,它之后发出的每一次
XHR 都走读鉴权。

## 写入

### POST /api/v1/ingest

Agent(或中继转发)批量上报。幂等:重复 `event_id` 被忽略。

```
Authorization: Bearer <token>        # 服务端配置了 token 时必带
{"events": [Event, ...]}             # Event 见 internal/model/event.go
{"quotas": [QuotaSnapshot, ...]}     # 权威配额观测(ADR-0007)
{"procs":  ProcReport}               # 本机进程状态(ADR-0012)
→ 200 {"received": N, "inserted": M, "quotas": K}
```

三种载荷可分开发送。`procs` 是**快照而非流水**:

```
{"device": "mac", "observed_at": 1785319948062,
 "sessions": [{"pid": 11941, "source": "claude-code", "started_at": 1785319024000}]}
```

服务端按 `(device, pid)` 覆盖写入,该设备上一次上报里没再出现的进程即视为已退出。
`sessions` 为空是**有意义的**——它表示「这台机器上没有会话开着」,与「这台机器不上报」
不同,后者在面板上必须显示为无数据。晚到的旧快照(`observed_at` 更早)被忽略,
否则会让已经退出的进程复活。

### POST /api/v2/enroll

使用 `Authorization: Bearer <admin_token>` 注册或更新一台稳定设备。Hub 必须显式配置
非空 `admin_token`;即使只监听 loopback 也不会开放匿名 enrollment。请求严格 JSON,
上限 64 KiB:

```json
{
  "device_id": "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001",
  "device_token": "<per-device-secret>",
  "display_name": "research-workstation",
  "capabilities": ["events", "quotas", "procs", "heartbeat", "durable_outbox"]
}
```

首次成功返回 `201`;同一 identity + credential 可更新显示名/能力并返回 `200`;ID 或
credential 冲突返回 `409`。响应不包含 plaintext token 或 token hash。推荐通过
`OMNITOKEN_ADMIN_TOKEN=... omnitoken agent enroll ...` 调用,不要把 secret 放进 argv。

### POST /api/v2/ingest

使用该设备自己的 bearer credential。请求是单一严格 JSON envelope,编码后最大
16 MiB;`device_id` 必须与凭据绑定的 principal 及 payload 内每条设备归属一致:

```json
{
  "protocol_version": 2,
  "device_id": "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001",
  "boot_id": "018f2d5a-7b31-7d98-bf8e-3c2f35a1c001",
  "batch_id": "018f2d5a-7b31-7d98-bf8e-3c2f35a1b001",
  "sequence": 42,
  "captured_at": 1785319948062,
  "kind": "events",
  "events": []
}
```

`kind` 为 `events | quotas | procs`,且一次只能携带对应 payload。成功响应的
`protocol_version/device_id/batch_id/ack_sequence` 必须与请求完全匹配:

```json
{
  "protocol_version": 2,
  "device_id": "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001",
  "batch_id": "018f2d5a-7b31-7d98-bf8e-3c2f35a1b001",
  "ack_sequence": 42,
  "accepted": 1,
  "duplicates": 0,
  "rejected": [],
  "server_time": 1785319949000
}
```

agent 只有验证完整 ACK 后才删除本地 outbox 行。相同 batch replay 返回持久化的同一
ACK;相同 `batch_id` 不同 payload 返回 `409`;身份/撤销失败返回 `401` 且不 mutation。

### POST /api/v2/heartbeat

同样使用 device credential,严格 JSON 上限 1 MiB。上报 boot/agent version、能力、
进程快照以及 outbox backlog。客户端的 `sent_at` 只用于诊断;在线/延迟/离线状态只由
Hub 接收请求时写入的 `last_seen_at` 计算,因此错误或恶意客户端时钟不能伪造在线状态。
成功响应包含 `protocol_version/device_id/received_at`。

### POST /api/v2/devices/{device_id}/revoke

使用 admin credential 撤销指定设备。路径中的 `device_id` 必须是 canonical UUID；
成功响应包含 `device_id/status/revoked_at`，其中时间由 Hub 生成。撤销会立即使该设备
credential 的 v2 ingest 与 heartbeat 返回未授权，且不会删除既有历史数据。未知设备
返回 `404`，非法 ID 返回 `400`。该操作不接受 read token、legacy ingest token 或
device credential。

## 查询

### GET /api/v1/overview?days=30

面板主数据,一次返回:`today/week/month/all_time`(Totals)、`daily`(逐日)、
`by_{device,model,repo,provider,source}`(分布)、`costs`(各期 real/equivalent,
ADR-0005)、`model_costs`、`unpriced_models`、`work_by_repo`(union/sum/sessions,
ADR-0006)、`work_matrix`(设备×repo)。

### GET /api/v1/breakdown?by=device|model|source|provider|repo|branch&days=30&limit=100

单维度分布。`by=device` 时每行附 `display_name` —— 设置里的重命名优先,其次是设备
enrollment 时的自报名,两者都没有则**省略字段**(v1 设备的 `key` 本身就是主机名,
没有可补的东西)。`key` 始终是归属标识,筛选与合并都用它,不受重命名影响。

### GET /api/v1/blocks?days=7

Claude 订阅 5 小时计费窗口(F11,算法对齐 ccusage:起点取整到小时,超窗或
空闲 5h 封块;仅统计 source=claude-code 且 provider=anthropic 的事件,
跨设备合并后为**账户级**口径)。返回 `blocks`(近期块)与 `active`
(当前块:已用量、burn rate、窗口余量与外推)。

### GET /api/v1/reports?granularity=daily|weekly|monthly|session&days=30&format=json|csv

报表(F12)。`csv` 时返回附件下载(Content-Disposition);`session` 粒度支持
`limit`(默认 200),行含 session_id/device/first_ts/last_ts。周桶 `%Y-W%W`
(周一起始,与总览周口径一致)。

### GET /api/v1/events?device=&source=&provider=&model=&repo=&session=&days=7&limit=100&offset=0

事件明细(F13)。参数化过滤,limit 上限 500;返回 `{"total": N, "events": [...]}`,
每条事件附 `cost_usd`(无定价时**省略字段**,区分"未定价"与 $0)。

### GET /api/v1/cache?days=30

Cache 分析(F16):按模型命中率与节省金额、cache 写入 1h/5m TTL 结构、
每日命中率;无定价模型列于 `unpriced`,不静默计 0。

### GET /api/v1/speed?days=30

生成速度(F15),三块互不混合的数据:

- `series`:近 60 分钟的滚动曲线,`buckets` 每分钟一个点,含 `tps`、
  `output_tokens`、`active_ms`。**`active_ms = 0` 表示那一分钟没有生成**,
  前端必须画成断点而不是 0 —— 后者等于宣称「跑了但没吐字」。
  跨桶的生成按重叠时长拆分计入两侧,免得长回答变成结束那一刻的尖峰。
- `models`:按模型的并集口径速度(ADR-0009),`tps = Σoutput ÷ |生成区间并集|`。
  并集在「一条会话流」= (设备, session_id) 内取,再跨流相加:流内取并集是因为
  subagent 与父会话共用 session_id、时间重叠;不跨流合并是因为两台机器同时生成
  不会让模型变快一倍。含 `active_ms`、`streams`、`coverage`。
- `exact`:本地代理实测通道,含 TTFT 与逐条中位/P90。

`coverage` = 有生成区间(`gen_ms > 0`)的事件占比。低于 1 的部分补不回来:
Claude Code 30 天后清理日志,更早的事件没有可回填的来源(见 `-rescan`)。

`models` 里**没有逐条中位数**,这是结论不是遗漏:日志推出的区间含等首 token 的
时间,几个 token 的工具决策几乎全是等待,逐条比值会给出没有任何一次响应跑出过的
数(实测 claude-sonnet-5 逐条中位 0.7 vs 整体 57.6)。把延迟与生成分开测是代理
通道的事,所以中位数只出现在 `exact` 里。

**Codex 三块都不在**:其 rollout 文件成批回放历史,70% 的记录时间戳是刷盘时刻,
没有可用的生成区间(ADR-0009)。

### GET /api/v1/telemetry?range=5h|1h|24h

menu bar 与 Web 可视化共用的有界遥测快照(ADR-0017)，包含：

- `today`：本地零点至今的总 tokens，以及所有非零模型的完整构成；
- `rolling_5h`：当前与前一个五小时区间的来源用量；Claude/Codex 行始终
  存在，其他来源归一为 `api` 行并在非零时返回，所有来源行之和等于总量；
- `speed`：按所选范围分桶的已测生成吞吐、来源贡献与覆盖状态；
- `generated_at`、`timezone`：服务端生成时间与日界线时区。

速度字段使用三种明确命名：

```text
aggregate_tps = measured_output_tokens(all)
              / union_ms(all measured generation intervals) * 1000
contribution_tps(group) = measured_output_tokens(group)
                        / union_ms(all measured generation intervals) * 1000
native_tps(group) = measured_output_tokens(group)
                  / union_ms(group generation intervals) * 1000
```

每个 bucket 的未舍入值必须满足
`aggregate_tps = Σ sources[].contribution_tps`。`native_tps` 使用各来源自身分母，
只供下钻比较，不能组成总计。

`measured_sources` 和 `unmeasured_sources` 互斥，区分“至少有一个可靠速度区间”与“完全没有可靠速度区间”。部分覆盖由 `coverage[].measured_events < coverage[].total_events` 表示，不得把来源整体标成 unavailable。
当前 Codex 在后者；它仍出现在 `today` 与 `rolling_5h` 用量中，但不会被伪造成
0 tok/s，也不会静默进入 `aggregate_tps`。

### GET /api/v1/quota

权威配额(ADR-0007):每 (设备, 来源, 限额, 窗口) 的最新快照。
`scope` ∈ five_hour | seven_day | seven_day:<模型> | primary | secondary | limit-hit;
含 `used_percent`、`resets_at`、`window_label`、`expired`。

### GET /api/v1/devices?days=30

用量汇总与设备 registry 的合并视图。注册设备即使尚无 token 用量也会出现,包含
`device_id`、`display_name`、`identity_status`、`connection_state`、
`last_seen_at`、`last_seen_age_ms`、`capabilities`、`queued_batches`、
`queued_bytes`、`oldest_queued_at`。
`connection_state` 为 `online | stale | offline`。`identity_status` 三取一:

| 值 | 含义 |
|---|---|
| `registered` | 走过 v2 注册,有 device_id、token 与心跳 |
| `local` | 运行 hub 的这台机器。服务端直接扫本地日志,没有 agent、token 或心跳,也无从注册 |
| `legacy_unbound` | 仅有历史用量、没有 v2 identity 的设备 |

`local` 与 `legacy_unbound` 的 `connection_state` 均为 `unknown` —— 两者都没有心跳可言,
但原因不同:前者不需要,后者是还没有。

### GET /api/v1/health

`{"status":"ok","auth_required":<bool>}`,中继节点返回 `{"status":"relay"}`。

免鉴权,且不携带任何用量数据 —— 它是客户端用来区分「地址错了」与「令牌错了」的
唯一手段。`auth_required` 即上表的判定结果:面板启动时探一次,服务端要 token 而本
浏览器没有时挂横幅指向**设置 → 访问令牌**。

## 实时

### GET /api/v1/stream (SSE)

契约对齐 token-monitor hub(见 references.md):响应头含
`x-accel-buffering: no`;连接即推 `snapshot` 事件,之后每次入库(本地采集与
HTTP ingest 两条路径都触发)推 `live` 事件(≥1s 合并去抖);每 30s 注释行心跳。

浏览器的 `EventSource` 不能设请求头,所以读接口要鉴权时,此端点额外接受
`?access_token=<token>`。这条较弱的通道**只在这一个端点上被接受**,其余读接口带
query 令牌一律 401(ADR-0016);桌面端的流走 Rust,能设头,不用它。

事件 data:`devices`(每设备最后活跃、今日用量、online 状态:active <2min /
idle / stale >10min,以及 `has_procs` / `running`)、`sessions`(近 10 分钟活跃会话:
设备/repo/模型/增量 tokens)、`processes`(进程地面真值)、`burn`(近 10 分钟
tokens/min)、`block`(当前 5h 窗口摘要)。

`speed.tps` 保留 ADR-0009 的整机并集吞吐。`speed.sessions[].tps` 是会话自身
`native_tps`，分母各不相同；用于贡献列表时必须读取
`speed.sessions[].contribution_tps`。来源、设备和模型贡献同样使用全局并集分母，
所以各自未舍入之和均等于 `speed.tps`。

`sessions` 与 `processes` 回答的是两个问题,不可互相替代:前者由事件推断
(「谁最近花了 token」),后者读进程表(「谁开着」)。一个会话开着但在思考时只出现在
后者,刚关掉但十分钟内有用量时只出现在前者。

`processes` = `{sessions: [{device, source, pid, started_at, observed_at}],
reporters: [{device, observed_at}], ttl_seconds: 90}`。`reporters` 是**有进程数据的
设备**,超过 TTL 未上报即自动消失(离线的机器无法自己清理)。设备不在 `reporters`
里意味着看不见,不等于没有会话 —— SSH 拉取的机器没有 agent,永远属于前者。

### GET /api/v1/live

上面那份 `snapshot` 的单次 GET 版本,字段完全相同(同一个 `livePayload`)。

给**拿不到流**的客户端用。菜单栏应用早期只轮询它(ADR-0008 把 SSE 桥接推到 v1 之后),
ADR-0014 之后它改为在 Rust 侧常驻一条 `/api/v1/stream`,这个端点降为它的断流兜底 ——
流断了就退回轮询,并在界面上标注当前是降级状态。

两者共用一份构造是有意为之:燃烧速率只定义一次,面板与 Live 页不会对同一个
十分钟给出不同的数。网页面板走 SSE,不要用这个端点轮询。
