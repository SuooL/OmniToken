# ADR-0004 事件身份与幂等:event_id 构造与去重协议

状态:已采纳(2026-07-26)

## 背景

同一事件可能被多次观测:会话恢复/分支时 Claude Code 把旧条目复制进新文件;
断网重传;双通道(本地扫描 + agent 推送)并存;状态文件丢失后全量重扫。

## 决策

- 全局主键 `event_id`,存储层 `INSERT OR IGNORE` 幂等;
- **claude-code**:`cc:<message.id>:<requestId>`(ccusage 同款、社区验证:
  两 ID 联合唯一标识一次计费响应的所有副本);缺 message.id 退化用行 uuid;
- **codex**:该格式无消息 ID。`cx:sha1(rolloutID|timestamp|文件内序号|usage)`。
  前提(对照 ccusage 确认):Codex 恢复线程开新 rollout 文件,不复制旧
  token_count 行,故无跨文件副本;哈希防的是同文件重读。
  另:token_count 事件在累计值未前进时重复出现(限流刷新等),解析层直接跳过;
- **offset 协议**:先 sink 成功、后提交 offset;部分行不计 consumed;
  文件缩短视为被替换,归零重读。Codex 行不自包含(模型/cwd 在前文),
  该格式整文件重解析,事件 ID 确定性保证重解析零副作用。

## 后果

- 上游一切组件可放心重复劳动,正确性集中在存储层一处;
- 净效果为"恰好一次"计数(至少一次投递 × 幂等落库);
- 代价:codex 的 event_id 依赖解析器行为稳定(序号),更改解析逻辑时
  须保证对已入库文件产出相同 ID(有单测锁定)。
