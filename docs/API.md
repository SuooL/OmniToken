# HTTP / SSE 接口契约

服务端所有接口挂在 `omnitoken serve` 的监听地址下。写接口(ingest)受 bearer token
保护(配置 `token`;未配置时不鉴权并在启动时警告);读接口面向内网/组网。

## 写入

### POST /api/v1/ingest

Agent(或中继转发)批量上报。幂等:重复 `event_id` 被忽略。

```
Authorization: Bearer <token>        # 服务端配置了 token 时必带
{"events": [Event, ...]}             # Event 见 internal/model/event.go
→ 200 {"received": N, "inserted": M}
```

## 查询

### GET /api/v1/overview?days=30

面板主数据,一次返回:`today/week/month/all_time`(Totals)、`daily`(逐日)、
`by_{device,model,repo,provider,source}`(分布)、`costs`(各期 real/equivalent,
ADR-0005)、`model_costs`、`unpriced_models`、`work_by_repo`(union/sum/sessions,
ADR-0006)、`work_matrix`(设备×repo)。

### GET /api/v1/breakdown?by=device|model|source|provider|repo|branch&days=30&limit=100

单维度分布。

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

生成速度(F15):`approx`(**仅 Claude Code**,日志间隔推算)与 `exact`
(本地代理实测,含 TTFT)分列。Codex 不在近似通道——其 token_count 日志在一轮
结束后才写,间隔中位仅 30ms,不反映生成耗时(ADR-0006)。

### GET /api/v1/quota

权威配额(ADR-0007):每 (设备, 来源, 限额, 窗口) 的最新快照。
`scope` ∈ five_hour | seven_day | seven_day:<模型> | primary | secondary | limit-hit;
含 `used_percent`、`resets_at`、`window_label`、`expired`。

### GET /api/v1/health

`{"status":"ok"}`,中继节点返回 `{"status":"relay"}`。

## 实时

### GET /api/v1/stream (SSE)

契约对齐 token-monitor hub(见 references.md):响应头含
`x-accel-buffering: no`;连接即推 `snapshot` 事件,之后每次入库(本地采集与
HTTP ingest 两条路径都触发)推 `live` 事件(≥1s 合并去抖);每 30s 注释行心跳。

事件 data:`devices`(每设备最后活跃、今日用量、online 状态:active <2min /
idle / stale >10min)、`sessions`(近 10 分钟活跃会话:设备/repo/模型/增量
tokens)、`burn`(近 10 分钟 tokens/min)、`block`(当前 5h 窗口摘要)。
