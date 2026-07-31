# ADR-0018 计费通道分类：订阅 / 官方 API / 第三方中转

状态：已采纳（2026-07-31，与用户确认）。修订 [ADR-0005](0005-pricing.md) 的成本分流
依据；与 [ADR-0013](0013-proxy-log-same-request.md)、[ADR-0015](0015-device-attribution.md)
同类：新增一处对已入库行的单向覆盖，**不改动 [ADR-0004](0004-event-identity.md) 的铁律**。

## 背景

### 为什么要分类

订阅配额有窗口（Claude 的 5h / 周，Codex 的周），按量计费没有。今天面板把三种钱
混成一个数：订阅额度的进度条旁边摆着一个包含中转用量的 token 总数，两个数字互相
矛盾，而用户无从判断哪个错了。成本也一样 —— ADR-0005 早就把「真实成本」与
「等效成本」分了流，但它分流的依据是 `provider` 列，而这一列已被实测证伪。

### 证伪：`provider` 靠模型名猜，猜错了

`internal/model/provider.go` 的 `FingerprintProvider` 只看 model ID 字符串形态。
本机实测：

- 被判为 `bedrock` 的 **3,172 条全是误判**。判据只有一条「model ID 含
  `anthropic.claude`」，而这台机器上没有任何 Bedrock 配置 —— Claude Code 走 Bedrock
  必须 `CLAUDE_CODE_USE_BEDROCK=1`，`settings.json` 与环境变量里都没有。那些事件
  是某个中转商采用了 Bedrock 风格的命名。
- 反向同样不成立：中转商**也会用裸名**，`claude-opus-4-6` 有 443/995 条无
  `requestId`。

模型名不是通道的证据，只是厂商的命名习惯，而命名习惯谁都能模仿。按模型名分类在
原理上就不成立，不是阈值调一调能救的。

### 可用信号：逐事件 `requestId` 有无

第一方 Anthropic 端点会回一个 request id，Claude Code 把它记进日志行；打其他端点
的记录里这一位是空的。本机 120 个会话实测：

| 模型 | 带 requestId | 无 requestId |
|---|---:|---:|
| `claude-opus-4-8` | 23,031 | 0 |
| `claude-opus-5` | 15,579 | 108 |
| `anthropic.claude-opus-4-8` | 0 | 4,218 |
| `anthropic.claude-opus-4-6` | 0 | 402 |

判据取**逐事件**，不取会话级。会话级统计是 78 个全带、20 个全无、22 个混合；
混合的 22 个里只有 1 个是真的中途换了端点（304/1645），其余每个只有 1–4 条
`<synthetic>` 噪声 —— `<synthetic>` 是 Claude Code 本地生成的占位记录，压根没发出
过网络请求，必须排除，否则几乎每个会话都会掺进几条假的「第三方」。

### 另外两块已有的证据

- `internal/collect/authprobe.go` 做四级探测（运行中 claude 进程的环境 → 采集进程
  自身环境 → `settings.json` 的 env 块 → OAuth 凭证/keychain），本机得出
  `anthropic-oauth`。这是订阅的**正面证据**，不是形态推断。
- Codex 侧的 `provider` 取自 rollout 的 `session_meta.model_provider`
  （`openai` / `custom` / `sub2api` / `enjoy` / `aihub`），是用户自己在配置里写下的
  声明值。声明不等于探测，但比猜模型名可靠得多，本决策不动它的采集方式。

## 决策

### 1. 三类的定义

| 类 | 含义 | 订阅配额窗口 | 成本口径（ADR-0005） |
|---|---|---|---|
| `subscription` 订阅 | 包月额度：Claude Pro/Max、ChatGPT Plus/Pro | **受约束** | 等效成本 |
| `api` 官方 API | 第一方按量计费，含 Bedrock / Vertex 这类第一方托管 | 不受约束 | 真实成本 |
| `relay` 第三方中转 | 非第一方端点 | 不受约束 | 真实成本（按公开价推算，仅供参考） |
| `unknown` 未知 | 证据不足 | 不进任何一栏 | 不并入任何一类 |

Bedrock / Vertex 归在「官方 API」而不是单列：它们与直连 API key 一样是按 token 计费、
没有订阅窗口，对用户要回答的那个问题（这些 token 吃不吃我的订阅额度）而言是同一类。

第四类 `unknown` 不是凑数，见 §5。

### 2. 三分类是查询时派生，落库的是证据

与 ADR-0005 把成本放在查询时计算同构：**事实与归类分离**。落库的仍然只有
`provider` 一列（记端点与凭证形态），三分类由一张纯映射表在查询时得出：

| `provider` | 通道 |
|---|---|
| `anthropic-oauth` | `subscription` |
| `anthropic-api` / `bedrock` / `vertex` | `api` |
| `relay` | `relay` |
| `anthropic`（确认第一方，但付费方式不明）/ `unknown` / 空 | `unknown` |
| Codex：`openai` + 订阅证据 | `subscription` |
| Codex：非 `openai` 的 `model_provider` | `relay` |

好处是映射改了不用重扫全库；代价是 `provider` 的取值集合从内部细节变成了对外契约
的一部分，改值域要同时改映射和面板。不新增列，也就不新增一处可覆盖的状态。

### 3. 判定优先级：机器级证据只回答机器级问题

Claude Code 侧分两步，**事件级先分端点，机器级再分付费方式**：

1. 事件带 `requestId`（排除 `<synthetic>`）→ 第一方端点已确定，交给第 2 步；
   不带 → `relay`，机器级探测不得推翻。
2. 在第一方那一侧，由 authprobe 决定：确认 OAuth 凭证 → `anthropic-oauth`；
   确认 API key → `anthropic-api`；探不出 → 停在 `anthropic`，即通道 `unknown`。

**为什么 authprobe 优先于 `requestId`**：在两者都有话说的地方，正面证据压过形态推断
（CLAUDE.md 铁律「权威优先，推断标注」）。但它们其实说的不是同一件事 ——
`requestId` 的分辨力**止步于「第一方 / 非第一方」**，它区分不了 OAuth 与 API key，
因为这两者打的是同一个端点、回同一种 request id；订阅与按量的区分只能靠 authprobe。

反过来，authprobe 也绝不能无差别地覆盖事件级信号：它的作用域是「这台机器怎么付钱」，
不是「这一条走了哪个端点」。**一台机器同时用订阅和中转是常态**（本机就是：23,031 条
第一方 + 4,620 条非第一方），让机器级的一句 `anthropic-oauth` 把中转流量也刷成订阅，
正是这次要修的错误的另一个版本。所以第 1 步的 `relay` 判定是终局的。

authprobe 现有的「端点覆盖时保持沉默」纪律继续有效：`ANTHROPIC_BASE_URL` 等变量置位
时它返回空，事件落回第 1 步，与 `requestId` 缺失互为佐证。

Codex 侧同一形状，只是证据换了一套：

- 非 `openai` 的 `model_provider`（`custom` / `sub2api` / `enjoy` / `aihub` …）→ `relay`；
- `openai` 需要再分订阅与官方 API。首选**会话级正面证据**：rollout 里出现过
  `rate_limits`（按量计费不返回订阅窗口）；兜底是机器级探测 `~/.codex/auth.json`
  （ChatGPT 登录 token vs 纯 `OPENAI_API_KEY`）。两者都拿不到 → `unknown`。
  **`rate_limits` 这条判据在实现前必须先用真实数据验证**（本机 `openai` 会话是否
  100% 带 `rate_limits`），验不过就只留 auth.json 兜底，不许凭合理性直接上。
- `model_provider` 是用户起的名字，`custom` 可能指向任何东西，包括用户自建的官方 API
  代理。所以第三方那一栏的准确含义与 Claude 侧完全一致：**「不是第一方端点」，
  不多不少**。

### 4. 判据的局限：证明的是「非第一方」，不是「中转」

这一条必须写明，不能假装判据是完备的。

`requestId` 缺失只证明这次响应不是第一方 Anthropic 端点返回的。**真 Bedrock / Vertex
同样不带 Anthropic 的 request id**，走的是 AWS / GCP 的响应头 —— 它们会被这条规则
判成 `relay`。今天这不出错，因为本机确实没有 Bedrock / Vertex；将来有用户真的启用，
它就是错的。

要把 Bedrock / Vertex 从 `relay` 里择出来，需要额外信号：`CLAUDE_CODE_USE_BEDROCK` /
`CLAUDE_CODE_USE_VERTEX`（authprobe 已经在读，只是读到就沉默）、对应的
base URL / project id 变量，以及模型名形态作为辅证。本轮不实现，记为已知缺口，
但要求一条安全阀：**探到这两个覆盖变量时，该机新采事件落 `unknown` 而不是 `relay`**
—— 判不出比判错便宜。

还有一条依赖：这套判据挂在上游日志格式上。Claude Code 若某天不再写 `requestId`，
全库会在一夜之间变成「第三方」。缓解只有两点：重判幂等可重复，格式恢复后重扫能纠正；
以及面板上通道占比的突变必须看得见，别静默地漂移过去。

### 5. 历史重判：这是第三处覆盖

CLAUDE.md 规定只有两处允许覆盖已入库的行 —— `source` 的 `proxy → 真实工具`
（ADR-0013）、`device` 的 `observed → self`（ADR-0015），新增第三处前先写 ADR。
这就是那份 ADR。边界逐条钉死：

1. **只写 `provider` 一列。** 计数列（四个 token 列、cache 分量、条数）、`ts`、
   `source`、`device`、`event_id` 一律不碰。回归测试盯死：重判前后逐行的各计数列
   相等、总量相等。一次请求算给哪个通道，和它被计几次是两回事。
2. **`event_id` 生成依据不变。** `requestId` 早就参与 Claude Code 的 event_id 拼接
   （`message.id` + `requestId`），本决策不动那段代码，也不把 `provider` 放进 ID。
3. **可重复执行且幂等。** 同一批日志重判 N 次结果相同；重判是纯函数（证据 → provider），
   不依赖执行次数或顺序。
4. **正面证据不被后来的推断覆盖。** 已经写着 `anthropic-oauth` / `anthropic-api` 的
   行是当时探测的结论，沿用，不用今天的 authprobe 结果去回填过去 —— 用户中途换过
   付费方式的话，那样回填就是拿现在的配置改写历史。形状与 ADR-0015 第 3 条
   「`self` 不覆盖 `self`」一致。
5. **可审计。** 重判在日志里打印每一类的变更条数与总量前后对比，便于核对
   「归属变了、总数没变」。

### 6. 历史行没有 `requestId` 怎么办

库里没有这一位：`requestId` 只被用来拼 `event_id`，从未落到 `events` 表。策略分两半：

- **日志还在**（Claude Code 默认 30 天清理，Codex rollout 一般更久）→ `-rescan` 重扫，
  从日志重新读出 `requestId` 并重判。走的是同一条幂等入库路径，`event_id` 不变。
- **日志已被清理** → 该行落 `unknown`，面板单列「未知通道」并显示它占总量的比例。
  **不猜。**

理由是 N6 与 ADR-0009 的同一条原则：N6 保的是「数据入库后独立于源日志生命周期」——
token 数这个**事实**已经保全了，缺的只是分类**证据**；ADR-0009 定的是「没有数据不能
画成零」，同理，没有证据也不能画成一个已被证伪的猜测。用模型名回填只是把这次要修的
bug 重新写一遍，还盖上一层「已重判」的假可信度。

面板要求：三栏之外必须有 `unknown` 栏与覆盖率数字；`unknown` 不得被静默并入任一类，
也不得按比例摊分到三类。

### 7. 与配额的关系

这是提出这个需求的根本动机。

配额值本身取自权威端点（ADR-0007 的 Codex `rate_limits`、ADR-0011 的 Claude
statusLine），本决策不改它们。受影响的是**与配额并排展示的用量数字**，以及任何我们
自己按窗口聚合出来的消耗：**它们只能计 `subscription` 那一类**。把中转用量算进 5h
窗口的分子，会让「已用 80%」和「窗口内 X tokens」互相矛盾。

反向也要说白：`api` 与 `relay` 不受任何订阅窗口约束，**给它们画配额进度条本身就是
概念错误**。这两类该看的是成本与速率，不是剩余额度。

## 后果

- **判据是「第一方 / 非第一方」，不是「中转 / 非中转」。** 真 Bedrock / Vertex 会落进
  `relay`，直到补上 §4 的覆盖变量探测。这是已知缺口，写在这里而不是等着被用户发现。
- **`unknown` 会长期存在且不小**：日志被清理的那段历史、探不出付费方式的第一方事件、
  端点被改道的机器，都在这一栏。界面要为它留位置，不能设计成三等分饼图。
- **第一次重判会有一大批行改归属**（至少 3,172 条 `bedrock` 误判 + 4,620 条非第一方
  事件），日志里能看见每类的条数；总量不变是硬要求。
- **`provider` 的值域成了对外契约**：查询时映射的代价是每次查询多一次纯映射，
  但改分类口径不用重扫全库。
- Codex 的 `openai` 细分依赖一条**尚未验证**的判据（`rate_limits` 出现即订阅），
  验证不通过就退回 auth.json 兜底，`unknown` 相应变多。
- 本决策不改 `event_id`、不改任何计数列、不动 `source` 与 `device` 的既有覆盖规则，
  也不新增第四处覆盖。要再加一处，照旧先写 ADR。
