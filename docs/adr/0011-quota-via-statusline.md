# ADR-0011 配额改由 statusLine 捕获(取代 ADR-0007 的 OAuth 通道)

状态:已采纳(2026-07-29,与用户确认)。取代 [ADR-0007](0007-quota-observation.md) 的
Claude 采集方式;Codex 部分不变。

## 背景

ADR-0007 让 OmniToken 直接调 Anthropic 的 OAuth 用量端点:

- 读 `~/.claude/.credentials.json`,macOS 回落 keychain 服务 `Claude Code-credentials`;
- `GET https://api.anthropic.com/api/oauth/usage`,带 `Authorization: Bearer <token>`;
- 处理 429 退避、占位桶过滤、扁平桶与 `limits[]` 的优先级。

调研 abtop(`~/git/references/abtop`)时发现另一条路:它不调任何端点,而是装一个
Claude Code 的 statusLine hook,从 Claude Code 递给状态栏的 stdin 里直接取 `rate_limits`。

**这条契约已被证实**,不是推测:ccstatusline 的 `src/types/StatusJSON.ts:74` 明确声明
了 stdin 载荷的 schema,含 `rate_limits.{five_hour, seven_day, seven_day_sonnet,
seven_day_opus}`,每个带 `used_percentage` 与 `resets_at`,并有测试固定
(`StatusJSON.test.ts:86-89`)。

## 决策

**Claude 配额只从 statusLine 载荷捕获,移除 OAuth 轮询。**

1. `omnitoken statusline` 在渲染的同时,把 `rate_limits` 落到
   `~/.omnitoken/rate-limits.json`(temp + rename 原子写)。
2. 本机采集器读该文件,转成既有的 `model.QuotaSnapshot`,走原来的入库通道。
3. 删除 `internal/collect/claudequota.go` 与配置项 `quota_poll_minutes`。

**不采用 abtop 的安装方式。** 它的 `--setup` 会把自己的脚本写进 `settings.json` 的
`statusLine`,而那个脚本不往 stdout 输出任何内容 —— 用户的状态栏因此变成空白,且
全项目没有卸载路径。`omnitoken statusline` 本身就是一个真正的状态栏渲染器,顺手
捕获即可,用户看到的状态栏不受影响。

**也不寄生在别的插件上。** 不去读 ccstatusline 之类第三方工具的产物:那会让 OmniToken
的核心功能依赖用户恰好装了某个别的东西。

**不强迫用户换掉自己的状态栏。** `statusLine` 槽位只能配一条命令,但 OmniToken 需要的
只是**看见**那份 stdin,并不需要占住渲染权。`omnitoken statusline -capture-only` 只捕获、
不输出、不请求服务端,用一个三行的 wrapper 把同一份 stdin 分发给它和用户原有的工具即可
(见 `docs/configuration.md`)。首版只提供了「渲染 + 捕获」一种模式,等于逼用户二选一,
这是设计缺陷,已补。

### 为什么落文件而不是直接上报

statusLine 是 Claude Code 每次刷新拉起的短命进程,F18 定的预算是 10ms 内出结果。
一次 HTTP 往返放不进这个预算,而且服务端不可达时数据会直接丢。文件把捕获与上报解耦,
两边各自失败互不影响。

## 代价(必须写下来)

**这条通道是机会式的,不是轮询。** 只在 Claude Code 运行且刷新状态栏时才观测得到。
OAuth 轮询在无人使用时照样更新,statusLine 不会。具体影响:

- 用户不在会话里时,配额停留在最后一次观测值;
- 只有把 `omnitoken statusline` 配成状态栏命令才有数据(见 `docs/configuration.md`);
- 因此采集器对超过 12 小时的捕获直接丢弃 —— 那时窗口极可能已经翻篇,
  继续上报等于把面板钉在一个不再成立的读数上。

**`limits[]` 数组拿不到。** OAuth 响应里新账号用 `limits[]` 表达分模型周配额;
statusLine 载荷没有这个数组。但它直接给了 `seven_day_sonnet` / `seven_day_opus`
两个具名桶,ADR-0007 修过的「分模型周配额被主键塌缩丢弃」这个问题依然被覆盖到。
其它模型的周配额则观测不到。

**换来的**:不再读凭据、不再访问 keychain、不再有 HTTP 客户端与退避逻辑、
不再有令牌泄漏面。`internal/collect` 少 263 行。

## 保留的部分

- **Codex 配额不变**:仍从 rollout 的 `rate_limits` 解析(ADR-0007)。
- **`authprobe.go` 不动**:它属于 F9(订阅 vs API key 判定),只检查凭据文件
  是否存在,从不调端点,与本决策无关。
- 存储、主键、面板口径全部不变 —— 换的只是数据来源。

## 实现修订(2026-08-09):采集器只装在了一半的部署形态上

上面写的「由本机采集器读走」在实现里只兑现了一半:`StatusQuotaReader` 仅在
`internal/server/collector.go` 被构造,`internal/agent` 从不构造它。

于是在 `docs/deployment.md` 推荐的主力形态 —— 每台机器跑 agent、Hub 在别处 ——
Claude 配额**从未离开过捕获它的那台机器**:`rate-limits.json` 一直是新鲜的,
只是没有任何组件读它。面板显示「官方端点未报配额」,这句话本身没说错,
错的是 Hub 确实什么都没收到。

**Codex 掩盖了这个洞**:它的配额来自扫描本就会解析的 rollout 日志,走的是事件
上报通路,不依赖这个 reader。两个来源里一个照常工作,于是缺口看起来像是 Claude
侧的临时性中断,而不是缺了一个采集器。

修复是把 reader 也接进 agent(`collectStatusQuota`,与 `reportProcs` 并列放在常驻
循环里),归属沿用 `scanDevice()` —— 与扫描给事件的归属同一个函数,免得同一台机器
因为上报面不同而在库里裂成两个身份(ADR-0019 §7)。

留下的教训与 ADR-0016 是同一条:**能力写在文档里不等于它被接线了**。所以这次的
回归测试里有一条断言的不是行为而是接线本身 —— 配好的 agent 从 `New` 出来必须带着
一个指向真实捕获文件的 reader。
