# ADR-0031 overview 的全时段面板:单独的长 TTL 缓存,把全表扫描移出热路径

状态:已采纳(2026-08-25)

## 背景

`/api/v1/overview` 在中心 Hub 上实测 days=1 要 20 秒、days=7 要 38 秒,常常超时。
拆开看,`computeOverview` 串行发约 18 条 store 查询,其中绝大多数按 `ts` 窗口有界、
随 `days` 线性增长;但有**两条没有任何 `ts` 边界**,每次都全表扫描:

- `Summary(UnixMilli(0), end)` —— all_time 累计(events + 各 token 列之和);
- `periodCost(UnixMilli(0), end)` → `ModelUsage(UnixMilli(0), end)` —— all_time 花费。

纯 Go 的 modernc SQLite、无覆盖索引,这两条随库增长,构成一个与 `days` 无关的
**~17 秒固定地板**(把 20s/38s 两点拟合成 `T ≈ 17s + 3s/day`)。ADR-0027 与 ADR-0028
都明确把它记为「暂不处理的次因」。本 ADR 处理它。

## 决定

把这两条 all_time 查询合到**一个单独的、TTL 远长于 overview 主缓存的缓存条目**里
(`cachedAllTime`,key `overview:all_time`,`allTimeCacheTTL = 60s` vs `overviewCacheTTL = 10s`)。

- 终身累计在相邻请求之间变化微乎其微,晚 60 秒反映一条刚到的事件对「终身总量」毫无
  感知差异;
- overview 主缓存每 10 秒过期、按需重算,但其中的 all_time 部分每 60 秒才真正扫一次,
  于是每 6 次 overview 重算里最多 1 次付那 ~17 秒,其余 5 次直接命中缓存 → 常态从
  ~20s 降到只剩窗口查询的开销;
- 两条 all_time 合在一个条目里一起算,保证那对全表扫描一次性完成、共享同一次过期。

### 为什么是缓存,不是增量聚合

更「彻底」的做法是维护一张增量的全时段聚合表,让 all_time 变 O(1)。否决它:那张表要
在每次 ingest 增、在 ADR-0020 的 `dropCopy` 删行时减,一旦与 events 表**漂移**就是
silent 的错账 —— 正撞 CLAUDE.md 的正确性铁律。缓存**始终从 events 表重算**,只是不那么
频繁,**没有任何漂移面**:唯一被交易掉的是「刚到的事件多久反映进终身总量」,这对一个
终身累计数是完全可接受的。

`end` 上界始终落在未来(`now + 1h`),所以一个偏旧的 `end` 仍覆盖全部已入库事件 ——
缓存陈旧只体现为「最近 60 秒的新事件还没算进终身总量」,不会漏任何历史。

## 验证

- `TestAllTimeOverviewCachedAcrossTTL`:注入时钟,断言 TTL 内 all_time 保持旧值、today
  同步刷新;超过 TTL 后 all_time 追平。既证缓存生效、又证陈旧值仍是正确值(只是更旧)。
- 既有 overview 测试(`TestOverviewExposesChannelBreakdown` 等)不变、全绿。

## 影响

overview 常态延迟从 ~20s(days=1)降到窗口查询量级;固定的全表扫描每 60 秒最多一次。
不改任何计数、不新增可漂移的状态。随 `days` 增长的次因(`WorkTime` 在 Go 里物化窗口内
全部行做区间并集)未在本 ADR 处理 —— 它有界于窗口、且无固定地板,留作后续把并集下推
到 SQL 的优化。
