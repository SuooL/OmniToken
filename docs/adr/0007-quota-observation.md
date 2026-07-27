# ADR-0007 配额观测:权威限额数据优先,推断窗口兜底

状态:已采纳(2026-07-27)

## 背景

用户关心订阅账户的 5 小时窗口与**周用量**配额。此前 F11 用 ccusage 的
blocks 算法**推断** 5 小时窗口(按事件时间戳分块),这是在"日志无限额数据"
假设下的最优解。调研 cc-statusline 与 ccusage 后发现该假设对 Codex 不成立。

## 调研结论(本机真实日志核对,2026-07)

| 数据 | Claude Code | Codex |
|---|---|---|
| 5h 窗口用量 | ✅ **OAuth 用量 API**(见下);日志本身无 | ✅ `rate_limits` 中 `window_minutes=300` |
| 周窗口用量 | ✅ 同上(含分模型 weekly) | ✅ `window_minutes=10080`(7 天) |
| 已用百分比 | ✅ API `utilization` | ✅ `used_percent` |
| 配额重置时刻 | ✅ API `resets_at`;另在触限时日志有 `Claude AI usage limit reached\|<unix秒>` | ✅ 每次请求 `resets_at` |
| 套餐/余额 | ⚠️ API 有 `extra_usage` | ✅ `plan_type`、`credits` |

**关键来源(ccstatusline 的做法)**:`GET https://api.anthropic.com/api/oauth/usage`,
头 `Authorization: Bearer <OAuth token>` + `anthropic-beta: oauth-2025-04-20`。
token 取自 `~/.claude/.credentials.json` 的 `claudeAiOauth.accessToken`,
macOS 回落 keychain(`security find-generic-password -s "Claude Code-credentials" -w`)。
响应两种形态都要支持:扁平 `five_hour`/`seven_day`/`seven_day_opus` 桶,
以及新账号的 `limits[]` 数组(`kind`、`percent`、`resets_at`、
`scope.model.display_name` 表示分模型周配额)。扁平桶优先,limits[] 补空缺;
`utilization=0 且无 resets_at` 是占位符,须丢弃。429 按 `Retry-After` 退避。

本机样本:`window_minutes` 仅两种取值——300(7149 次)与 10080(4139 次),
即 5 小时与周窗口;plan_type=plus。

**cc-statusline 的实现方式**(用户提问点):它不解析日志,而是消费 Claude Code
传给 statusline 钩子的 stdin JSON(`.cost.total_cost_usd`、`.cost.total_duration_ms`、
`.context_window.total_input_tokens/total_output_tokens`),据此算 $/h 与 tok/min;
5 小时窗口它**自己不算**,shell 调用 `ccusage blocks --json`(5s 超时)取
`isActive` 块的 `usageLimitResetTime // endTime`。因此它是"单会话实时 + 外部工具
补窗口",既无跨设备也无周用量。

## 决策

1. 新增**配额快照**概念,与 usage event 分离(事实类型不同:event 是累加流水,
   quota 是某时刻的状态观测)。表 `quota_snapshots`,主键 (device, source,
   limit_id, window_minutes, observed_at)。
2. **Codex**:解析 `token_count` 事件里的 `rate_limits`,主/次窗口各存一条快照。
3. **Claude Code**:①**轮询 OAuth 用量 API**(默认 5 分钟,可配
   `quota_poll_minutes`)取 5h/周/分模型权威配额——这是主通道;
   ②解析 `isApiErrorMessage` 行中的 `Claude AI usage limit reached|<epoch>`,
   作为触限时刻的补充记录。
   token 只用作 Bearer 头,不落盘、不进日志、不进快照。
4. **仅订阅计费才轮询**:配额窗口是订阅制的概念,API key 计费按量付费、
   不存在 5h/周窗口。因此轮询以 F9 探测结果门控——探测判为
   `anthropic-api` 时**完全跳过**(连端点都不请求),避免机器上遗留的旧 OAuth
   凭据产生一份与实际计费方式无关的配额数字。探测无法判定时按凭据存在性处理。
4. 展示分层且**必须标注来源**:权威快照优先(显示 `已用 X% · Y 后重置`),
   无快照时回落到推断窗口(F11 blocks,标注"推断")。两者不可混淆。
5. 解析器契约扩展:`ParseFunc` 返回 `ParseResult{Events, Quotas, Consumed}`——
   快照与事件同源同批,共享 offset 提交协议(ADR-0004)。

## 后果

- Codex 用户获得真正准确的配额视图(含周用量),这是 ccusage/token-monitor 都没有的;
- Claude 侧仍以推断为主,但触限时刻能记录权威重置点(可校准推断窗口);
- 快照是状态而非流水,**不做累加**,查询取每
  (device, source, limit_id, **scope**, window_minutes) 的最新一条;
  重复观测同一时刻幂等(主键去重),与事件同样可安全重扫。
- **scope 必须进主键**(2026-07-27 审查发现并修复):多个 scope 共用同一窗口长度
  —— Claude 的 `seven_day`/`seven_day_opus`/`seven_day_sonnet` 都是 10080 分钟,
  Codex 的 primary/secondary 也可能同为周窗口。主键漏掉 scope 会让同一时刻的
  多个 scope 在 `INSERT OR IGNORE` 下静默塌缩成一条(实测 3 条只剩 1 条)。
  已加单测锁定;旧库由 `migrateQuotaSchema` 自动重建(快照是可再观测的状态,
  下次轮询即恢复)。
