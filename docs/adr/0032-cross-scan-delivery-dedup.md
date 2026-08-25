# ADR-0032 FullReparse 跨扫描交付去重:持久化每块内容 key,跳过未变块

状态:已采纳(2026-08-25)

## 背景

Codex(与 dsh)是 `FullReparse`:文件每增长一次,整份从字节 0 重解析、重新交付。
交付层用 `logicalDeliveryKey("events", ordinal, chunk)`(内容哈希)+ `DeliveryDone`
去重,但那个台账存在 `InFlight[file].Delivered` 里,**`Commit` 一到就被清空** ——
它的用途是「单次扫描中断后续传」,**不跨扫描**。

于是一个**活跃增长中的会话**,每次 5 秒扫描都把**全部**已交付事件重新 `sink` 一遍:
enqueue 进 outbox、上传、Hub 再 ingest。一个 17 分钟、涨到 1040 事件的会话,约 200 次
扫描 × 平均 ~270KB ≈ 50MB 上传,而其唯一数据只有 ~538KB —— ~100× 带宽/磁盘浪费。
(ADR-0030 已把 Hub 侧对这些重复的 ingest 从 13s+ 降到亚秒,所以这不再是延迟主因;
剩下的就是这条上传/磁盘浪费,尤其在公网 ingest 链路上。)

原设计**刻意接受**这种重发(FullReparse 注释:event_id 确定性 + 入库去重使其不双计)。
本 ADR 在不动那条正确性保证的前提下,消掉其带宽代价。

## 决定

给 `State` 加一张 `Chunks map[string]map[int]string`(file → ordinal → 上次交付的内容 key),
**它随 `Commit` 一起持久化、不随 `InFlight` 清空**。只有**非续传的 FullReparse 扫描**
(`spec.FullReparse && !resuming`)会读它、重建它:

- 交付循环里,每个 ordinal 都把当前 key 记进本次的 `nextChunks`(无论是否交付),这样
  提交的记录反映整份文件、并自然淘汰不再出现的高位 ordinal;
- 若 `Chunks[file][ordinal] == 当前 key`,说明**同样的内容已在某次已提交 offset 的扫描里
  交付过**,跳过 `sink`;否则交付,并记录新 key;
- `Commit(file, offset, turnStart, nextChunks)` 把 offset 与这张记录**同一次 save** 落盘,
  二者原子推进。非 FullReparse / 续传扫描传 `nil`,不动这张记录、保持原行为。

### 为什么安全(不违反「offset 仅在上报成功后推进」)

`Chunks` 里的每个 key 都是**在某次 `Commit` 里、即 sink 成功之后**写入的,所以它命名的
内容必然已经交付、且当时的 offset 已覆盖它。因此「命中即跳过」永远安全 —— 最坏是一条
陈旧 key **匹配不上**而**重发**(入库去重吸收),绝不会跳过未交付的内容。sink 失败时
`Commit` 不执行,`Chunks` 与 offset 都不推进,下次重试。

### 为什么是内容 key,不是字节偏移

`closeTurn`(ADR-0009)在 turn 闭合时把 gen_ms 回写到**已交付的**事件上:同样的字节这次
解析出**不同的事件**。key 基于事件内容(而非字节),所以那一块的 key 会变 → 重发 →
gen_ms 得以送达 store。`TestFullReparseRedeliversChunkWhoseEventsChanged` 守这条。

## 范围

- **只处理事件交付**。配额快照(`quotas`)因 `observed_at`(及 Codex 的 `resets_at` 抖动)
  每次内容都不同,跨扫描 key 必然不匹配,本机制对它无效也不介入 —— 配额去重是另一个
  问题(容量估计 ADR-0025 依赖其历史),不在此 PR。
- 文件被截断/替换时 `DiscardScan` 一并清掉 `Chunks[file]`,下次重读重建。

## 验证

- `TestFullReparseSkipsUnchangedChunksAcrossScans`:2500 事件分两块,追加 100 后第二次扫描
  只重发增长的 chunk 1、跳过未变的 chunk 0;无增长时零交付。
- `TestFullReparseRedeliversChunkWhoseEventsChanged`:字节不变但 chunk 0 的事件被打上
  gen_ms,断言该块**仍然重发**。
- 既有 collect 全套(续传、截断、offset、start window、quota)不改语义、全绿。

## 影响

活跃 FullReparse 会话的重复上传从 O(会话长度 × 文件大小)降到「每块只在其内容真正变化时
交付一次」。不改 offset 推进契约、不改入库去重、不双计;Hub 侧行为不变(收到的仍是幂等
的事件,只是数量少得多)。
