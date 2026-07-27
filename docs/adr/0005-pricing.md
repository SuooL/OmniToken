# ADR-0005 成本计算:LiteLLM 定价、查询时计算、真实/等效成本分流

状态:已采纳(2026-07-26)

## 背景

用户长期以 ccusage 的成本数字为参照;模型价格随时间修订;
订阅用量(包月)与 API 用量(按量)的"成本"语义不同。

## 决策

- 定价数据源与 ccusage 一致:LiteLLM `model_prices_and_context_window.json`,
  构建时裁剪为仅 5 个 cost 字段内嵌(~270KB),支持配置文件覆盖单模型价格;
- **成本一律查询时计算,不落库**:事实(token 数)与估值(价格)分离,
  价格修订后历史成本自动重算;
- 模型名归一匹配:小写精确 → 剥 Bedrock 区域前缀(us./eu. 等)与 `-v1:N` 后缀 →
  Vertex `@` 形态 → `anthropic/`、`openai/` 前缀候选;
- Codex 无定价模型(codex-auto-review、空模型)按日期区间 fallback 表
  映射到当期真实模型(表数据借鉴 ccusage,随其更新);
- cache_read 价缺失时按 input 价计(ccusage 同口径);cache 写入价区分
  1h/5m TTL(日志有分量,LiteLLM 有 above_1hr 字段);
- **分流展示**:provider ∈ {anthropic-api, bedrock, vertex, relay} 计"真实成本",
  订阅(anthropic-oauth / codex 订阅)计"等效成本",总览分开呈现,不合并成一个数。

## 后果

- 与 ccusage 同期数字可对账(同源同口径),作为解析正确性的持续校验手段;
- 无定价模型在面板显式标注"无定价"而非静默计 0(无静默截断原则)。
