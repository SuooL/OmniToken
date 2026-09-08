# ADR-0033 Codex 订阅归类改用 config.toml 的 `requires_openai_auth`,不再只认 provider 名

状态:已采纳(2026-09-07,与用户确认)

## 背景

[ADR-0018](0018-billing-channel-classification.md) 定的规则是:一个 Codex 会话算订阅,
必须两个信号同时成立 —— ① `model_provider` **精确等于** Codex 内置的 `openai`(区分大小写),
② `rate_limits` 里有只有真账户才会给的 `plan_type` 或 `credits`。

②没问题,今天仍然成立。问题出在①:**名字从来就不是证据**。

2026-09-07 在本 fleet 上实测到的后果:

| 观测 | 值 |
|---|---|
| `~/.codex/config.toml` 的 `model_provider` | `cc-switch-official` |
| 官方 5 小时窗口(来自 `rate_limits`) | 已用 **73%** |
| 面板 5 小时卡片统计到的 codex 订阅 tokens | **0** |
| 同期 codex 一天的实际用量 | 19.7M tokens / 136 次 |

cc-switch 这类配置切换器会把 provider 块**改名**再写回 config.toml。名字一变,①就不成立,
一个月的订阅流量被整体归到「第三方中转」。更糟的是它污染了 ADR-0025 的容量校准:
`quota_capacity` 里 codex 的样本变成 `peak=100% / tokens=0.53M`,估出 0.5M 的窗口容量,
面板于是显示「已用 73%,还剩 19M」这种自相矛盾的话。

## 决策

**①的判据从「名字」换成「这个名字解析到的 provider 块拿的是谁的凭据」。**

Codex 的 config.toml 里写着:

```toml
[model_providers.cc-switch-official]
name = "OpenAI"
requires_openai_auth = true
base_url = "http://127.0.0.1:15721/v1"
```

`requires_openai_auth = true` 的含义是 **Codex 会把 ChatGPT 账户的凭据带上**。
这正是「花的是订阅额度」的定义,而且对上面那个 `127.0.0.1` 的本地代理同样成立 ——
不管最终谁应答,被消耗的是那个账户的窗口。反过来,一个拿不到这份凭据的中转商
设不了这个键,它是用别的方式付费的,这恰好就是 ADR-0018 想区分的东西。

具体:

1. 新增机器级探测 `collect.ProbeCodexAuth(sessionDirs)`,读 `$CODEX_HOME`
   (未设则 `~/.codex`)以及每个已配置 session 目录的上一级里的 `config.toml`,
   收集所有 `requires_openai_auth = true` 的 provider id。
2. `codex.ParseWith(trusted)` 接受这个集合。判定变成
   「(id == 内置 `openai` **或** id ∈ trusted)**且** planEvidence」。
   **内置 id 仍然单独成立** —— 未改动的安装上没有别的东西叫这个名字。
3. **只对本机日志生效**(`collect.LocalSpecs`),`SSHSpecs` 不接 —— provider 块在写日志
   的那台机器上,拿本机的配置替远端回答就是「猜测披着证据的外衣」。这条边界和
   ADR-0018 §3 给 Claude 探测划的是同一条。
4. `codex.Parse`(trusted 为 nil)行为不变,继续只认内置 id。

## 为什么不用别的做法

- **看 `base_url` 是不是官方域名**:错的方向。官方域名 + API key 是按量计费,不是订阅;
  而本机这个订阅走的恰恰是 `127.0.0.1`。
- **只要 `rate_limits` 存在就算订阅**:ADR-0018 提过并且被实测否掉了 —— 610 个 rollout 里,
  523 个有 token_count 的会话**全部**带 rate_limits,中转商也合成这个信封,准确率 20.8%。
- **在设置页做一张人工白名单**:判据确实在系统之外,但每换一次 cc-switch 配置就要补一次,
  而机器上明明有可核实的事实。(仍可作为将来 provider 指向局域网另一台机器时的兜底。)

## 影响

- **不动两把去重键**。`event_id` 与 `dedup_key` 都不含 provider,已有单测锁定
  (`TestCodexEventIDIndependentOfProvider`、`TestDedupKeyIndependentOfProvider`),
  本次新增 `TestCodexKeysIndependentOfTrustedProvider` 把「加了探测也不许动」这条钉住。
- **provider 列的覆盖仍在 ADR-0018 §5 的证据阶梯内**:写入的是 `openai-chatgpt`
  (rank `rankBilling` = 3),不会覆盖 `relay`(rank `rankNotFirstParty` = 4)。所以
  哪怕探测错了,也压不掉「这条明确不是第一方」的结论。

### 只修未来,不改历史 —— 这是有意的

自报的 provider 名(`cc-switch-official`)在阶梯上是 rank 4,`openai-chatgpt` 是 rank 3,
而重判**只许升不许降**。所以已经入库的那些行**不会**因为这次改动被重新贴标签:

- **新事件立刻正确**。Codex rollout 是每次增长整文件重解析的,新的 token_count 行以
  新 event_id 入库,带的就是 `openai-chatgpt`。5 小时窗口卡片因此在**一个窗口之内**
  自己恢复正常,周窗口在 7 天之内。
- **历史行保持原样**,代价是这段时间的 codex 订阅用量在历史视图里仍记在「第三方中转」下。

没有顺手把它改掉,是因为两条候选都会造成新的错误:降低「自报名字」的 rank 会让所有
中转名字变得可被覆盖;像 ADR-0018 §6 那样把它们清成 `unknown` 再重扫,会把**真的**
中转流量(本机的 `custom`、`sub2api`)在日志已过期的地方永久留在 unknown 列。
真要重贴历史标签,值得单独一条 ADR 和一次显式的人工发起操作,而不是搭这次的车。

另外:已有的 codex `quota_capacity` 样本是在错误归类下算出来的(分子接近 0,估出
0.5M 的窗口容量),它们同样需要作废。那属于容量校准的事,在容量那条改动里一起处理。

## 局限

config.toml 用的是手写的最小扫描,只认 `[model_providers.X]` 表头与
`requires_openai_auth = true` 这一个键;点号键、内联表这些形态不认。认不出来的
provider 会回落到「只认内置 id」的旧规则 —— 是少一个正确答案,不是多一个错误答案。
