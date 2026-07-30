# 系统架构设计

> 配套阅读:重大决策的"为什么"在 [adr/](adr/) 中,一事一档。

## 1. 总体视图

```
┌────────── 设备(headless 服务器 / GUI 机)──────────┐
│  工具日志:~/.claude/projects/**.jsonl              │
│           ~/.codex/sessions/**.jsonl               │
│  [可选] omnitoken agent:扫描→上报;可开中继端口     │
└──────┬─────────────────────────────┬───────────────┘
       │ A. agent 出站 HTTP(直连/组网/经中继)         │ B. 服务端 SSH 拉取
       ▼                             ▼                 (rsync 镜像,远端零安装)
┌──────────────────── omnitoken serve(任一常开机器)────────────────────┐
│ Ingest API(幂等)→ SQLite(事件级全量)→ 查询/聚合 API → 内嵌网页面板 │
│ 内置采集器:本机日志扫描 + SSH 拉取镜像扫描(与 agent 复用同一采集层)  │
└──────────────────────────────────────────────────────────────────────┘
```

两角色一个二进制:`omnitoken serve` / `omnitoken agent`。查看端只是浏览器。

## 2. 分层与包结构

| 层 | 包 | 职责 | 依赖方向 |
|---|---|---|---|
| 领域模型 | `internal/model` | Event 统一事件、provider 指纹 | 无(最底层) |
| 解析器 | `internal/parser/claudecode`、`internal/parser/codex` | 单一日志格式 → []Event | → model |
| 采集 | `internal/collect` | 增量扫描、offset 状态、repo 归一、SSH 镜像 | → parser, model |
| 存储 | `internal/store` | SQLite 持久化与聚合查询 | → model |
| 服务 | `internal/server` | HTTP API、内置采集器调度、内嵌前端 | → collect, store |
| 推送端 | `internal/agent` | 推送模式采集、中继 | → collect |
| 入口 | `cmd/omnitoken` | 子命令、配置装配 | → server, agent |

依赖单向向下,解析器互不感知,server/agent 复用同一采集层(DRY)。

## 3. 关键设计模式与不变量

- **Strategy(解析器插件)**:`collect.ParseFunc` 统一契约
  `(io.Reader, device) → ([]Event, consumedBytes)`;`SourceSpec{Dirs, Parse, FullReparse}`
  把"目录集 × 解析策略"声明化。新工具 = 新增一个 parser 包 + 一行 spec,零侵入。
- **Sink 抽象(依赖注入)**:采集层只认 `Sink func([]Event) error`;
  serve 注入"直接入库",agent 注入"HTTP 上报"。同一扫描代码服务两种角色。
- **幂等 Ingest(全系统基石)**:`event_id` 主键 + `INSERT OR IGNORE`。
  推论:任何组件可以放心重复劳动(重扫、重传、双通道观测),
  正确性由存储层一处保证,上游全部免锁免协调。
- **Offset 提交协议(至少一次投递)**:先投递成功、后提交 offset;
  部分行(无换行符结尾)不计入 consumed。配合幂等 ingest 得到"恰好一次"净效果。
- **传输即 URL**:agent 不感知网络拓扑;中继 = 无状态反向代理,
  失败向下游透传错误,由最末端的日志文件承担缓冲职责(N3)。

## 4. 数据模型

事件表 `events`(唯一事实表,聚合皆为查询时派生):

```
event_id PK | ts | device | source | model | provider | account_label
input/output/cache_read/cache_creation tokens | cache_1h/5m tokens
duration_ms | ttft_ms | session_id | cwd | git_branch | repo | app_version | received_at
```

- `event_id` 构造:claude-code = `cc:<message.id>:<requestId>`(社区验证的去重键);
  codex = `cx:sha1(rolloutID|ts|seq|usage)`(该格式无消息 ID,见 ADR-0004)。
- token 四分量语义统一为 Anthropic 口径:`input` 不含 cache;
  OpenAI 口径(input 含 cached)在 codex 解析器内换算(ADR-0004)。
- `repo` = 归一化 git remote(`github.com/user/repo`),跨设备同 repo 自动归并;
  无 remote 用 `local:<toplevel>`,非 git 目录为空。缓存键为 `(device, cwd)`。

## 5. 待实现部分的设计(M2/M3)

- **成本(F7)**:`internal/pricing` 包,内嵌裁剪版 LiteLLM 定价
  (仅 5 个 cost 字段,~270KB);模型名归一匹配(bedrock 前缀/vertex @ 形态);
  Codex 无定价模型(如 codex-auto-review)按日期 fallback 表映射(借鉴 ccusage);
  成本一律查询时计算,不落库(价格可修订,事实与估值分离);
  订阅来源标"等效成本",API/Bedrock 标"真实成本"。
- **工作时长(F8)**:纯查询派生:按 (device, repo) 取事件时间戳序列,
  相邻间隔 ≤5min(可配)计入,>5min 停表。不建新表,不引入采集端状态。
- **Live(F10)**:SQLite 插入后发内存广播 → SSE 推送;无消息队列,单进程内闭环。
- **本地代理(F14,已实现)**:agent 内手写转发(非 ReverseProxy——需要 tee
  解析响应流),逐块转发并 Flush,SSE 按行增量解析 usage,记录首字节时刻(TTFT)
  与总耗时;产出 `source=proxy` 事件进入同一管线,`account_label` 存 key 指纹。
  解析失败不影响转发;速度在查询时由 output/duration 派生。

## 6. 安全与隐私边界

- ingest 与设置写入走共享 bearer token(常量时间比较);查询 API 要不要同一个
  token,**由监听地址推导**(ADR-0016):默认 `127.0.0.1:8787` 只听本机时免鉴权,
  可被其它机器访问时必须配 `token`,没配则启动即拒绝。内网/组网是多设备汇集的常态,
  这层鉴权因此做在本体里(十几行常量时间比较,不是轮子);暴露公网另说 —— TLS、
  限速、多用户仍应交给反代,本体不做。
- 采集内容白名单:仅 usage 数字与元数据字段,解析器结构体按需取字段,
  对话正文在反序列化层即被丢弃。
