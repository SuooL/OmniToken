# ADR-0019 设备身份合并:用户显式发起的第三处覆盖

状态:已采纳(2026-07-31,与用户确认)。延伸 [ADR-0015](0015-device-attribution.md),
补上它没有覆盖的那一格:**两个都是 `self` 的身份其实是同一台机器**。
本 ADR 是 CLAUDE.md「新增第三处覆盖前先写 ADR」那句话所要求的那份 ADR。

## 背景

本机同时以两个身份存在于库里:

- `hostname` 返回 `JasonHudeMacBook-Pro.local`;
- `~/.omnitoken/config.json` 的 `device_name` 是 `suool-mac`。

两者是同一台机器。污染范围已数清:

| 表 | 行数 | 说明 |
|---|---:|---|
| `events` | 1 | `device_origin = 'self'` |
| `quota_snapshots` | 1,296 | codex primary 1,288 + claude five_hour/seven_day 各 4 |

时间集中在 2026-07-31 00:00:12–00:00:36,之后未再出现。所以**污染的主要是配额,
几乎不影响 token 计数** —— 但配额是「五小时还剩多少」这类当下要看的数,分成两条
序列之后每一条都是残缺的,面板上会出现两张互相矛盾的窗口卡。

根因是 hostname 兜底路径:`cmd/omnitoken/main.go:153`(agent)与
`internal/server/config.go:146`(serve),在未配 `device_name` 时各自回落到
`os.Hostname()`。

### ADR-0015 判不了这一格

ADR-0015 的三条规则里,与此相关的是第 3 条「`self` 不覆盖 `self`,先到的自报赢」。
它处理的问题是**一行事件该记给哪台机器**,赌的是时序。这里的问题不是同一行被两台
机器争,而是**两个名字本来就是一台机器**:两次自报都是真的,谁先到都不重要,
先到者胜只会把同一台机器的历史劈成两半。

也没有任何本地信号能自动判定这件事。hostname 与 `device_name` 之间没有可验证的
关系;反过来,两台真机完全可以有相同的 hostname。**判据在系统之外,只有人知道。**

### 现有的 `device_labels` 解决不了

设置页已有的设备重命名(`app_settings` 的 `device_labels`)只改显示名,不动数据:
两行仍然是两行。配额查询取「每 `(device, source, limit_id, window_minutes)` 最新
一条」(`internal/store/quota.go` 头注释),两个 `device` 值就是两条独立序列,
改显示名之后只会得到两张标题相同的卡。要让它们变成一条序列,必须真的改数据。

## 决策

### 1. 开第三处覆盖,与前两处的区别是「谁来断言」

| | 触发方式 | 判据 | 守卫 |
|---|---|---|---|
| ADR-0013 `source`:`proxy` → 真实工具 | 自动,在写入路径上 | 代码内可验证:该行 `source` 恰为 `proxy` | 条件钉死在 SQL 里 |
| ADR-0015 `device`:`observed` → `self` | 自动,在写入路径上 | 代码内可验证:本批数据的通道级别 | 条件钉死在 SQL 里 |
| **本 ADR `device`:A → B** | **用户在设置页显式发起** | **系统内无从验证,由人断言** | **无守卫可写 —— 代之以确认、审计、不可撤销的告知** |

前两处敢做成自动的,是因为判据是系统自己能核实的事实,因此可以写成一条永远成立的
守卫。这一处不行:没有任何守卫能拦住「用户把两台真实的不同设备合并了」。所以安全
边界不在代码里,而在**发起方式**上 —— 它必须是人显式要求的一次性管理操作,系统
只负责如实执行并留下痕迹。

由此定下一条实现纪律:**合并不进入采集写入路径**。它是一个独立的管理接口
(`POST /api/v1/devices/merge`,走 `adminAuth`,与设置写入同级,见 ADR-0016);
采集器、解析器、ingest 处理器中**任何自动路径都不许调用它**。前两处覆盖是「每行至多
发生一次、随数据流自然发生」,这一处是「用户一年按几次的按钮」,两者不能长在一起。

### 2. 合并语义:改归属,行数不增不减

把 source device 的行改写为 target device。整个合并在**单个事务**内完成,失败整体
回滚,不留「一半已并」的中间状态。

表清单(逐个核对 `internal/store/` 得出,不是猜的):

| 表 | 列 | 处理 |
|---|---|---|
| `events` | `device` | `UPDATE ... SET device = :target WHERE device = :source`。不可能撞主键,见 §4 |
| `quota_snapshots` | `device` | 先删同键冲突行,再 UPDATE,见 §4 |
| `live_sessions` | `device`(主键 `(device, pid)`) | **删掉 source 的行,不搬迁**,见 §4 |
| `live_reports` | `device`(主键) | 同上 |
| `app_settings` → `device_labels` | JSON 的 key | 把 source 的显示名迁到 target;两边都有时保留 target 的 |
| `devices` / `device_heartbeats` | `device_id` | **不动**,见下 |
| `ingest_receipts` | `device_id` | **不动**,见下 |

`events.device` 在 v2 通道下存的是注册表里的 `device_id`(UUID),
`internal/server/devicesview.go` 正是用 `indexByDevice[record.DeviceID]` 把两者对齐的。
所以合并的两侧可以一个是 UUID、一个是人取的名字 —— 合并操作只认 `device` 这个字符串
的字面值,不关心它长什么样。

**为什么不动注册表。** `devices` 是凭据表(`token_hash` 唯一),一台机器注册两次就是
两套凭据;把两行并成一行等于合并密钥,语义不明且有安全含义。要下掉多余身份,用已有的
`POST /api/v2/devices/{device_id}/revoke`。`device_heartbeats` 有外键指向 `devices`,
跟着它走。`ingest_receipts` 是按 `batch_id` 判重放的幂等收据,`ApplyIngestV2` 会在
`receipt.DeviceID != envelope.DeviceID` 时报 `ErrIngestReceiptConflict` —— 改它的
`device_id` 会让老 batch 的重传当场变成冲突错误,把幂等机制打坏。

**`device_origin` 不改。** 它记的是「这一行的 `device` 当初是怎么来的」,合并没有改变
那个事实。后果是:并进来的 `observed` 行,以后仍可能被 ADR-0015 规则 2 的自报改走 ——
这是**好事**,自报是比人工合并更强的证据,它只会把行改到真正跑它的那台机器名下。

### 3. 不变量:计数一个都不许变

这是本 ADR 最硬的一条,写成可执行的断言。合并前后,下面两个查询的结果必须**逐字相等**:

```sql
SELECT COUNT(*), SUM(input_tokens), SUM(output_tokens),
       SUM(cache_read_tokens), SUM(cache_creation_tokens),
       SUM(cache_1h_tokens), SUM(cache_5m_tokens)
FROM events;                        -- 全库,不带任何 device 过滤

SELECT COUNT(DISTINCT event_id) FROM events;
```

回归测试在合并前后各跑一次并断言相等;`events` 的 UPDATE 语句里出现任何计数列名
即为 bug。这与 ADR-0013 的填空守卫、ADR-0015 的覆盖名单是同一条纪律:
**一次请求算给谁,和它被计几次是两回事。**

注意断言是「相等」,不是「减少」。如果实现出来事件条数变少了,那说明写了 DELETE 或者
改错了表 —— 是 bug,不是特性,理由见 §4。

### 4. 主键冲突:事件不会撞,配额会

**`events` 不会撞,也不会去重。** `event_id` 是主键,而 `device` **不参与** event_id
的构造(ADR-0004 铁律,ADR-0015 复述过)。因此库里每个 `event_id` 只有一行,那一行只
有一个 `device` 值 —— 「两个身份下存在相同 event_id」这个情形**在库里根本不存在**:
真有同一条日志从两个身份到达,插入时就已经被 `INSERT OR IGNORE` 收敛成一行了。
所以改 `device` 不可能造成主键冲突,合并也永远不会合掉任何事件。

**`quota_snapshots` 会撞。** 主键是
`(device, source, limit_id, scope, window_minutes, observed_at)`。两个身份来自同一台
机器上并行跑的两个进程,在同一毫秒观测到同一个窗口完全可能,合并后 `device` 一致就撞了。

策略:**同键冲突时保留 target 的行,丢弃 source 的行。** 理由是配额快照是**可重新观测
的状态**,不是累加量 —— 查询取每组最新一条,从不求和;同一时刻同一窗口的两条快照描述
的是同一个事实,留哪条都对,固定留 target 只是为了让结果确定、可重跑。这也不违反
「绝不碰计数列」:被丢的是一份重复观测,不是一次请求。

实现上有个反直觉的坑,必须写在这里:**不要用 `UPDATE OR REPLACE`。** SQLite 的
`OR REPLACE` 在冲突时删掉的是**已存在的那一行**(target),保下来的是被 UPDATE 的那行
(source)—— 与本决策要的方向正好相反。正确写法是显式两步,且顺序不能反:

```sql
DELETE FROM quota_snapshots WHERE device = :source AND EXISTS (
  SELECT 1 FROM quota_snapshots t WHERE t.device = :target
    AND t.source = quota_snapshots.source AND t.limit_id = quota_snapshots.limit_id
    AND t.scope = quota_snapshots.scope
    AND t.window_minutes = quota_snapshots.window_minutes
    AND t.observed_at = quota_snapshots.observed_at);
UPDATE quota_snapshots SET device = :target WHERE device = :source;
```

**`live_sessions` / `live_reports` 直接删 source 的行,不搬迁。** 主键含 `device`,
而两个身份的 PID 空间是同一个,撞车很常见;更重要的是这两张表存的是瞬时状态,
`ApplyProcReport` 每一轮都会用整份快照覆盖该设备的行(`internal/store/procs.go` 明说
报告是快照)。搬迁最多在几十秒内有效,搬错却会让面板短暂显示一个并不存在的会话。

### 5. 可审计:合并不可逆,必须留痕

不可逆是真的。`device` 改掉之后原值无处可寻 —— 重扫源日志也救不回来,重扫会按**当前**
配置重新判定 device,不会还原旧名。所以每次合并追加一条审计记录。

存储形式:`app_settings` 的 `device_merges` 键,一个 JSON 数组
(`GetSettingsJSON` / `SetSettingsJSON` 现成),追加写:

```json
[{"from": "JasonHudeMacBook-Pro.local", "to": "suool-mac",
  "at": 1753920036000, "actor": "admin",
  "events": 1, "quota_snapshots": 1288, "quota_dropped": 8,
  "live_rows_dropped": 0}]
```

不新建表:合并是一辈子按几次的人工操作,一行 key/value 足够;`app_settings` 本就随 DB
一起走,不需要额外迁移。行数计入记录,是为了让人事后能核对「当初到底动了多少行」——
出问题时这是唯一的对照物。

`actor` 只记到「持 admin 凭据发起」这个粒度。本产品是单用户的,没有账号体系,
不要假装记了个人名。

审计记录在设置页可见(合并历史列表),**不提供删除入口**。

### 6. 二次确认与防误操作

合并两台真实的不同设备是灾难性且不可逆的,UI 的职责是让它做不到「顺手点错」:

1. 入口放在设置页设备区,**默认折叠,且不与重命名混排**。重命名可逆、合并不可逆,
   两者不能长得像。
2. 选定 source / target 后先展示**影响预览**:将改写的 `events` 条数、`quota_snapshots`
   条数(含将被丢弃的冲突行数)、两个身份各自的首末事件时间与总 token。预览由**服务端**
   算,不是前端估的 —— 用户据以决策的数字必须与真正要执行的语句同源。
3. 二次确认要求**手工输入 source 设备名全称**,不是点一下「确定」。在选择器里挑错的人,
   抄一遍名字时还有一次看清自己在合并什么的机会。
4. 确认框明说**不可撤销**,并提示先备份 DB(单文件 SQLite,`cp` 就是备份)。
5. 接口走 `adminAuth`;读接口凭据不足以发起合并。
6. 拒绝 `source == target`;拒绝任一方在库中不存在(防止拼错造出一次空合并却显示成功)。
7. 若 target **不是**本机当前写入所用的设备名,给出警告 —— 合并方向选反的话,
   下一批事件会重新落到 source 名下,等于白做。

**不做「这两个看起来是同一台机器」的自动建议。** 任何这类启发式(hostname 前缀相同、
事件时间不重叠、同一批 repo)都会产生假阳性,而假阳性在这里的代价是不可逆的数据破坏。
宁可让用户自己看见两行、自己决定。唯一的例外见 §7 第 3 点,那一条不是启发式。

### 7. 防复发:保留 hostname 兜底,但只留一条身份解析路径

这是个真实的权衡,两边都是真需求:

- **去掉兜底、强制配 `device_name`**:headless 服务器上多一个必配项,`serve` 会因为缺一个
  纯展示用的字符串而拒绝启动。这与 N1 要的部署形态(单二进制、零维护成本)相悖 ——
  为了防一个偶发 bug 让**所有**部署多一步,不划算。
- **保留兜底**:本 bug 会复发。

决策:**保留 hostname 兜底,但消除真正的病根。**

病根不是 hostname 这个值,而是**同一台机器上有两条互不相干的身份解析路径**:
`cmd/omnitoken/main.go:153` 与 `internal/server/config.go:146` 各写了一遍兜底,其中一条
另外读到了 `config.json` 的 `device_name`,另一条没有。两条路径给出不同答案,才是两个身份
的来源;只要它们共享同一个答案,兜底成 hostname 也只会得到**一个**身份,合起来照样是对的。

因此:

1. 本机身份**只解析一次**,单一优先级链:显式 flag > 环境变量 >
   `~/.omnitoken/config.json` 的 `device_name` > `os.Hostname()`。`serve` 与 `agent`
   调**同一个函数**,不再各写一遍。
2. 落到 hostname 兜底时**在启动日志里说明**(`device name not configured, using hostname
   "X"`)。让用户在看见两个名字之前就有线索。
3. 启动时若发现库里同时存在**本机的两个候选名字**(解析结果与 hostname 都作为
   `device_origin = 'self'` 出现过),在日志与设置页提示「这两个可能是同一台机器,
   可在设置页合并」。**只提示,不自动合并。** 这不违反 §6 的「不做自动建议」——
   §6 禁的是跨设备的相似度猜测,而这一条的判据是「本机自己的两个候选名字都在库里」
   这个确定事实,不涉及任何别的设备。

## 为什么不 新增 `device_origin = 'merged'`

看上去更诚实,代价却不对等:ADR-0015 的三条规则会立刻多出一整格要定义
(`self` 能不能覆盖 `merged`?`merged` 能不能覆盖 `observed`?),而目前**没有任何读者**
需要在查询时区分「这一行是合并来的」。需要知道的时候,§5 的审计记录里写着谁在什么时候
把谁并进了谁,比多一个枚举值信息量大得多。

## 为什么不 做成一次性迁移脚本

因为它不是一次性的。同步过家目录、换过 hostname、从备份恢复、先跑 `serve` 后装 agent ——
每一种都会再造出一个多余身份,而 §7 明确选择保留 hostname 兜底,等于承认它还会发生。
脚本还有两个具体的坏处:它绕过 `adminAuth`(谁能拿到 shell 谁就能改数据)、它不产生
§5 的审计记录。做成设置页里的常规能力,这两条都自然满足。

## 代价与边界

- **不可逆。** 备份是用户责任,产品只负责在确认框里把这件事说清楚。
- **`quota_snapshots` 是唯一会减少行数的表**(同键冲突时丢 source 的重复观测)。
  `events` 永不减少,这条差别写进测试。
- **合并只处理已入库的行。** 合并之后新事件仍按当前配置入库,所以 §7 的路径统一是本
  决策的一部分,不是可选的后续 —— 少了它,用户会需要反复合并同一对身份。
- **注册表不参与合并**,所以合并后 `/api/v1/devices` 可能仍有一行
  `identity_status: registered` 却没有任何事件。这是对的:凭据还在就该看得见,
  要下掉用 revoke。
- **按设备分组的历史图表会改变形状。** source 的历史整段并入 target,合并前的截图不再
  能对上。总量不变,分组变了 —— 这句要出现在确认框里,否则用户会以为数据丢了。
- **不做设备拆分。** 合并丢掉了区分依据,拆分在原理上做不到。这是「二次确认必须严格」
  的另一半理由。
- 落地该能力的 PR 需要同步把 CLAUDE.md「只有两处允许覆盖已入库的行」改成三处,
  并在 `docs/roadmap.md` 追加自己那一行。
