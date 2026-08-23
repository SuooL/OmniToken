# ADR-0027 Store 读写连接分离:让面板查询不再排在 ingest 写后面

状态:已采纳(2026-08-23)

## 背景:`/api/v1/overview` 在并发 ingest 下间歇性 hang

线上(阿里云 198 hub)现象:`omni.suool.net/#overview` 经常一直空白加载不出来。
实测直连 hub 打 `GET /api/v1/overview?days=30`,同一 URL 有时秒回、有时 hang >30s;
而 `GET /api/v1/heatmap?days=365` 无论何时都 0.2s 秒回。

根因是连接模型,不是某条慢查询:

- `store.Open` 一直用 `db.SetMaxOpenConns(1)`(`store.go`),**整个 server 的读和写共用
  一条 SQLite 连接**。注释理由是「modernc/sqlite serializes writes;单连接避免锁竞争」。
- `handleOverview` 一次要跑 **~18 个顺序查询**(今天/本周/本月/全量四个 `Summary`、
  `Daily`、五个维度 `Breakdown`、`ListDevices`、`ChannelBreakdown`、四个周期 `ModelUsage`、
  `WorkTime` 等)。每个都要抢那唯一的连接。
- 198 上所有设备(mesh + 公网 ingest)持续推事件,写事务反复占着这条唯一连接。
  `database/sql` 把要连接的请求排队,于是 overview 的 18 次读**逐个**卡在写后面、层层累积。
  `heatmap` 只需要抢**一次**连接,所以总能挤过去——这正好解释了观测到的不对称,
  且与窗口大小无关(heatmap 365 天也是一个查询)。
- 次因:`Summary(all_time)` 与 `ModelUsage(all_time)` 聚合**整表**、`WorkTime` 把区间内
  每一行 materialize 进 Go,都把唯一连接占得更久,放大了主因。这条 ADR 先不动它们。

一个旁证:线上 `omnitoken.db-wal` 长到 47MB。单连接下,一个慢的 overview 读会占着连接、
持有读事务,**挡住 WAL checkpoint** 回收帧,WAL 越涨读越慢,形成正反馈。

## 决定:写仍走单连接,读走独立的只读连接池

WAL 允许「多读者 + 单写者」并发。既有单连接把这个能力**锁死**了:`database/sql` 永远
不会发出第二条连接,读在物理上无法与写并发。

改法:

- **写连接 `db`**:保持 `MaxOpenConns(1)` 不变。所有 `Exec`/`Begin`(ingest、配额、设备
  合并、schema/迁移)继续走它。写仍然串行,`SQLITE_BUSY` 风险和以前一样是零。
- **读连接池 `rdb`**:新增,同库、WAL、`busy_timeout(5000)`,外加 `query_only(true)`
  作为安全带(这个池上任何写会立即报错而不是悄悄制造双写竞争),`MaxOpenConns(4)`。
  所有独立 `Query`/`QueryRow` 走它;事务内的 `tx.Query` 不变(读自己未提交的写)。

为什么这个方向而不是「直接把 `MaxOpenConns` 调大」:调大会让 `database/sql` 开出多条
**可写**连接,两个写事务撞上就靠 `busy_timeout` 兜底、写延迟可能飙到 5s 甚至真的
`SQLITE_BUSY`——把「读饿死」换成「写偶发失败」。读写分离没有这个风险:写仍单连接,
读池只读。

失败模式也更安全:如果漏改了某个读点(仍走 `db`),那只是它慢一点,**不会**引入
`SQLITE_BUSY`;而反过来(读池承接了写)会被 `query_only` 当场挡下。

WAL checkpoint 反而会**改善**:读搬离唯一写连接后,写连接不再被长读占住,autocheckpoint
能正常回收 WAL。

## 影响

- 读一致性:读池看到的是「最后一次已提交」的快照,面板允许瞬时滞后,可接受。库里所有
  测试用**文件**库(无 `:memory:`),所以两个连接指向同一文件、已提交写对读池可见。
- 未解决的次因(留作后续):`Summary(all_time)`/`ModelUsage(all_time)` 的整表聚合与
  `WorkTime` 的全量 materialize 仍会随库增长变慢。它们不再阻塞其它读/写,但单独看仍值得
  加上界或补索引——另开 ADR/PR。

背景:overview 查询清单与索引分析见本次排查;部署拓扑见 ADR-0026。
