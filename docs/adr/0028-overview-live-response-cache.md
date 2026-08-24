# ADR-0028 overview/live 短 TTL 响应缓存 + single-flight

状态:已采纳(2026-08-23)

## 背景:并发轮询把两个重接口打爆

ADR-0027 的读写分离修好了「读排在写后面」的间歇 hang,面板不再空白。但用户反馈**部分页面
打开后仍要等几秒**。实测(直连 hub):

- 只有两个最重的接口慢:`overview`(一次 ~18 个查询)和 `live`(重建整个实时载荷)。其余
  `telemetry`/`heatmap`/`speed`/`reports`/`models` 都 <0.35s。
- **单个 `overview` 2.8s,4 个并发 = 每个 26s(9×)**。本地纯查询并发很快(K=4=0.68s),
  WAL 大小无关——所以慢的不是 SQL,是**完整 handler 在并发下反复重算**:面板按周期轮询
  `overview` + `live`,叠加菜单栏 bar、多标签页、SSE,一起在 198 的共享 CPU 上各算各的。

也就是每次打开页面都触发一次几秒的重算,而它们互相抢 CPU。

## 决定:重接口结果按短 TTL 缓存,并发未命中合并为一次计算

新增 `respCache`(`internal/server/respcache.go`):按 key 缓存**已序列化的 JSON 字节**,TTL 内
直接返回;未命中/过期时用 **single-flight** —— 只有一个 goroutine 跑 compute,其余等待并共享
结果。套在两个重接口上:

| 接口 | key | TTL |
|---|---|---|
| `overview` | `overview:<days>` | 10s |
| `live` | `live` | 3s(实时性更强,给更紧的窗口) |

- **失败不缓存**:compute 报错就删除条目,下一次请求重试,避免把瞬时 500 粘住 TTL。
- **计算与 HTTP 解耦**:`handleOverview` 拆出 `computeOverview(days)`;`handleLive` 缓存
  `livePayload`。所有单测仍直接驱动 `computeOverview`/`livePayload` 这两个 seam,**缓存只影响
  HTTP 路径**,不影响既有断言。
- 缓存时钟走 `s.currentTime`,与测试的时钟注入一致。

为什么可接受:面板是监控视图,几秒陈旧无所谓;换来的是并发/重复轮询从「每次重算」变成
「TTL 内一次计算、大家共享」,直接消掉 9× 的并发放大,也大幅降低 198 CPU 占用。

## 影响

- 数据最多陈旧 TTL(overview 10s / live 3s)。`generated_at` 同样滞后至多一个 TTL,面板本就
  以 telemetry 的 receivedAt 展示新鲜度,可接受。
- key 数量有界(几个 days 档 + live),map 不会无限增长,无需淘汰。
- SSE(`/api/v1/stream`)不走此缓存,实时推送不受影响。
- 未触及次因(ADR-0027 提到的 all_time 全表聚合/WorkTime):TTL 内只算一次,已足够;库继续
  长大再考虑加上界/索引。

背景:ADR-0027(读写分离)、部署拓扑 ADR-0026。
