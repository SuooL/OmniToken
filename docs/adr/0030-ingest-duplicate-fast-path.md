# ADR-0030 重复 ingest 快路径:预载现有状态,跳过证明为 no-op 的探针

状态:已采纳(2026-08-25)

## 背景

Codex 的 rollout 是 `FullReparse`(scan.go):文件每增长一次,整份从头重解析、
逐字重新交付。一个活跃的长会话因此稳定地把**几百条已入库、且字段已回填齐全的
重复事件**一遍遍推给 Hub。

实测:一个 1040 事件的重复批,`/api/v2/ingest` 在中心 Hub(共享 CPU 的阿里云机)
上要 **13.1 秒**处理,ack 返回 `accepted=0, duplicates=1040`。这些重复批以 5 秒一个
的速度灌进 agent 的 durable outbox,而 FIFO 上传一次只发一批、每个 13 秒排空,于是
积压无界增长(实测 90 分钟从 ~90 涨到 255),队头阻塞把配额等小批全堵在后面。后果:
最新用量到 Hub 的延迟越来越大,面板「数字延迟很久才更新」;写连接被一串 13 秒的
重复 ingest 串占、共享 CPU 被吃光,读侧 overview/live 端点被拖到 20–72 秒。

13 秒花在哪:`insertEventsFromTx` 对每条事件先 `INSERT OR IGNORE`(命中主键冲突后
放弃),再跑一串带守卫的 UPDATE 探针 —— dedup_key 归属查询、`keyFill`、`reclassify`、
`reattribute`、`numFill`、`textFill`、`promote`、`durFill`。每条探针都有 SQL 守卫,
对一条已回填齐全的行**必然影响 0 行**,但**执行**这些必然 no-op 的语句本身(纯 Go 的
modernc SQLite,逐条 Exec 往返)就是全部开销:~8 次 Exec × 1040 ≈ 8000 次往返。

## 决定

在逐事件级联**之前**,用**一条按主键的批量查询**预载这批 `event_id` 里已存在那些行
的可变列状态(`loadPriorEventState`)。然后:

- 已知存在的行,**跳过 `INSERT OR IGNORE`**(它只会命中冲突后放弃);
- 每条 UPDATE 探针,只在预载状态显示它**可能真的改动**时才 Exec,门控条件与该探针
  SQL 守卫**逐字同义**(例:`numFill` 只在 `已存 gen_ms=0 且 传入 gen_ms>0` 时跑;
  `reclassify` 只在 `ProviderRank(传入) > ProviderRank(已存)` 时跑);
- dedup_key 的归属查询(ADR-0020)只在预载证明该行**已自持这把键**(`已存 dedup_key ==
  传入 dedup_key`,而 dedup_key 唯一 ⇒ 该行即所有者)时跳过 —— 那种情况 `keyOwner`
  只可能报「普通重复」,原分支本就 fall through。

预载不到的 `event_id`(真正的新行,或批内重复中刚插入的那条)一律 `known == nil`,
所有门控退回「照跑」,即原行为。

### 为什么这不是「新增一处覆盖」,不改 CLAUDE.md 的白名单

本改动**不新增、不放宽、不改变**任何覆盖语义。它只是**不执行**那些预载状态已经
证明影响 0 行的语句。SQL 守卫原样保留,是真正的安全网;Go 侧门控是与守卫同义的
提前短路。`accepted`/`duplicates`/`mutated`/`filled`/`deduped`/`reclassified` 全部
计数与「全部照跑」逐位相同 —— 计数列更是从头到尾没被碰过。因此它落在既有白名单
**之内**(ADR-0009/0013/0015/0018/0020 那几处覆盖照旧),而不是新增一类。

## 验证

- `TestReingestIdenticalCodexBatchIsNoOp`:全字段已填的 codex 批重放一遍,断言
  `inserted=0`、行数不变、每一可变列逐位不变。
- `TestFastPathStillBackfillsGenMS`:live 入库 `gen_ms=0`,turn 闭合后重发 `gen_ms>0`,
  断言快路径**仍然回填**(gen_ms/ttft_ms 落地),且 token 计数不动 —— 守住「别把该做的
  回填也跳过了」这个反向风险。
- 既有 store 全套(去重/回填/重判/归属/channel/dedup_key)不改一行、全绿 —— 语义
  透明的直接证据。
- `BenchmarkReingestAllDuplicates`(1040 全重复):**59.9ms → 3.03ms(~20×)**。按同比例
  外推,Hub 侧单批 13s → ~0.65s。

## 影响

重复批的 ingest 从 O(行数 × 探针数) 降到 O(1 次查询 + 真正需要改的少数行)。这直接
排空 outbox 积压(消除写连接被串占),并让读侧端点(overview/live)不再被写侧饿死。
不改任何计数、不改任何覆盖语义、不改 ack 契约。
