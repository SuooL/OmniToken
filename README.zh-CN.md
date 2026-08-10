<div align="center">

# OmniToken

**跨设备的 Claude Code / Codex token 用量监控，完全自托管。**

单个 Go 二进制，一个 SQLite 文件，数据不出你的机器。

[![CI](https://github.com/SuooL/OmniToken/actions/workflows/ci.yml/badge.svg)](https://github.com/SuooL/OmniToken/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuooL/OmniToken)](https://github.com/SuooL/OmniToken/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8)](go.mod)

[English](README.md) · [设计文档](docs/README.md) · [架构](docs/architecture.md) · [ADR](docs/adr/README.md)

</div>

---

笔记本上跑 Claude Code，开发机上跑 Codex，GPU 节点上挂着一个 agent 循环。
每台机器各写各的日志，互相不知道对方存在。于是真正要紧的问题一个都答不上来：

- 这周**一共**花了多少 —— 四台机器加起来？
- 现在这一刻，5 小时窗口还剩多少？够不够开一次大重构？
- 钱烧在哪个 repo 上？哪个模型？哪台机器？

`ccusage` 能回答**单台机器**的、**事后**的版本。OmniToken 回答的是**整个机群、实时**的版本 ——
并且把每一条事件留在你自己的 SQLite 里。

## 为什么是 OmniToken

### 🔒 数据是你的

没有 SaaS，没有遥测，不用注册。**永不读取对话内容** —— 解析器只反序列化用量数字与元数据字段，
正文在反序列化层就被丢弃。启用可选代理时，API key 只留 SHA-256 指纹前缀，绝不存明文。
设备凭据以 SHA-256 哈希存储，比对走常量时间。

### 🌐 跨设备，而且是事件级

不是摘要 —— 是**每一次请求**，来自每一台机器，汇进同一个库，靠全局 `event_id` 去重。
所以半年后你依然可以从「这周花了 X」一路下钻到造成它的那一条具体请求。
设备只出站上报，不需要公网 IP，不需要开入站端口。

### 📊 配额是权威值，不是猜的

5 小时与周窗口取自厂商自己的 `rate_limits` 载荷 —— 不是自己重实现一遍窗口逻辑然后慢慢漂移。
推断值也有（燃烧速率外推、剩余额度估计），但**永远显式标注为推断**，两者绝不混进同一个数字。

### 📦 单二进制，没有运行时

纯 Go，无 CGO，**直接依赖只有两个**。网页面板用 `go:embed` 嵌进二进制 ——
没有 Node，没有打包器，没有构建步骤，不需要容器。往 headless 服务器上扔一个 13 MB 的文件就能跑。

## 截图

<div align="center">

![总览](docs/images/overview.png)

<em><b>总览</b> —— 今日 / 滚动 5h 用量与环比，以及按来源分离的速度泳道（共享时间轴、独立零基线）。</em>

<br>

![实时](docs/images/live.png)

<em><b>实时</b> —— 总吞吐、按来源的速度曲线与覆盖率，每个并发会话一条泳道，最后一行是全设备并集。SSE 秒级推送。</em>

<br>

![配额](docs/images/quota.png)

<em><b>配额</b> —— 一个窗口一张卡：权威百分比与重置时刻，加上燃烧速率外推（超额转红）和剩余额度 ——
后者标注为<b>推断</b>，因此绝不会与旁边那个实测数字混为一谈。按量计费通道不画进度条：它们没有窗口可供成为其中的一部分。</em>

<br>

![速度](docs/images/speed.png)

<em><b>速度</b> —— 逐模型的 tok/s 与 TTFT，每条测量通道分别标注：代理实测、日志推算、Codex 保守下界。</em>

</div>

> 截图使用**虚构的演示数据**，用于展示界面布局，不代表任何实测基准。

## 快速开始

**一键安装**（Linux/macOS —— 下载二进制、对照 `SHA256SUMS` 校验、enroll 设备、
装好 launchd/systemd/cron 常驻服务）：

```sh
curl -fsSL https://github.com/SuooL/OmniToken/releases/latest/download/install.sh \
  | OMNITOKEN_ADMIN_TOKEN='<ADMIN>' sh -s -- \
      --server https://hub.example.net --name "$(hostname)"
```

**或者自己起 hub。** 在一台常开的机器上：

```sh
# 从 Releases 下载，或自己构建：
make build            # 或 go build -o omnitoken ./cmd/omnitoken

./omnitoken serve     # 自动采集本机，首次运行导入历史
```

打开 <http://127.0.0.1:8787>。单机场景到此为止，零配置。
首次启动会写一份**填满全部默认值**的 `~/.omnitoken/config.json`，想改什么一目了然。

**接入更多机器。** 在每台机器上 enroll 后常驻：

```sh
OMNITOKEN_ADMIN_TOKEN=... ./omnitoken agent enroll -server <HUB_URL> -name <NAME>
./omnitoken agent
```

Windows 有对应的 `install.ps1` 一键脚本，见[部署指南](docs/deployment.md)。

## 面板

九个页面，全部由二进制自己提供：

| 页面 | 内容 |
|---|---|
| **总览** | 今日 / 滚动 5h 用量、Claude 与 Codex 分列、机群覆盖、按来源的速度泳道、贡献者、设备用量、计费通道构成、今日模型构成、365 天热力图 |
| **实时** | 当前总生成速度、并发生成泳道、设备在线状态、**读进程表得到的「打开中的会话」**、权威配额卡与燃烧速率外推 —— SSE 推送 |
| **速度** | 60 分钟吞吐曲线、按模型的 tok/s 与 TTFT（中位 / P90）、每条通道的覆盖率、代理实测的精确值单独列出 |
| **报表** | 日 / 周 / 月 / 会话聚合，任意区间，CSV 与 JSON 导出 |
| **明细** | 事件级下钻：按设备、来源、模型、项目筛选，逐请求成本，分页 |
| **设备** | 设备对比、待发送积压、连接状态、每日堆叠用量 |
| **模型** | 模型 × 来源拆分、成本/token 散点、每日构成 |
| **缓存** | 命中率、1h/5m TTL 结构、按模型的等效节省 |
| **设置** | 定价覆盖（热重载，历史成本立即重算）、设备重命名、带预览与二次确认的设备身份合并 |

图表用 vendored 的 ECharts；热力图是手绘 SVG。

## 集成

**Claude Code 状态栏** —— 一条命令装好钩子，而且是**包住**你已有的状态栏，不是顶掉它：

```sh
omnitoken statusline -setup      # -setup-undo 还原
```

```
Opus 4.8 15.0K $1.25 · 今日 5.4M $10.00(2 台) · 5h 97% 1h08m · 周 36% 70h00m
```

会话数字来自 Claude Code 自身，**今日是跨设备合计**，配额是权威值。
渲染预算约 300ms，网络超时 200ms —— hub 不可达就退回 10 秒内的缓存并标 `⟳`，永不拖慢输入。

**macOS 菜单栏应用** —— Tauri 瘦客户端，托盘表盘图标、配额、实时速度，75% / 90% 两档预警。
它只连接一个已在运行的 hub，自身不采集。用 `make desktop` 构建
（见[局限](#局限)：目前尚未随 Release 分发）。

<div align="center">
<img src="docs/images/menubar.png" width="330" alt="macOS 菜单栏弹窗">
</div>

<div align="center"><em>与上面的面板截图不同，这张是真实机群。Claude 配额那格显示「暂无」，
是因为当时没有任何 Claude Code 在刷新状态栏 —— 这条捕获通道按设计就是机会性的，
面板如实说明，而不是拿一个陈旧的百分比顶上。</em></div>

**本地 API 代理**（可选，默认关闭）—— 把脚本的 `base_url` 指向
`http://127.0.0.1:8899/anthropic`，请求被**逐字转发**，同时记录用量、**精确 TTFT** 与耗时。
这是唯一能精确测出首 token 延迟的通道 —— 日志里根本没有这个字段。

## 工作原理

```
Claude Code / Codex 日志         进程表           可选的 API 代理
            │                     │                     │
            ▼                     ▼                     ▼
     internal/parser/*  ──►  internal/collect  ──►  Sink（注入）
                                                    ├── serve：直接入库
                                                    └── agent：持久 outbox → HTTPS
                                                                    │
                                                                    ▼
                                            Hub：幂等 ingest → SQLite
                                                                    │
                                            查询聚合 API + 内嵌网页 + SSE
```

两个角色，一个二进制。`serve` 是唯一权威 —— 一个 hub，一个库。`agent` 扫日志并只出站上报。
两者复用同一采集层，区别只在注入哪个 sink。中继是无状态反向代理，
所以 `agent → 中继 → hub` 的链式转发不会制造第二个事实源。

四种接入方式可任意混用：**加密组网**（Tailscale/WireGuard，默认推荐）、**SSH 隧道**、
**受信 LAN 直连**、**公网 HTTPS ingress**（必须走反向代理）。
另有 **SSH 拉取** —— 远端装不了 agent 时，由 hub 把日志镜像回来解析，远端零安装。

## 正确性就是产品本身

一个会重复计数的用量监控，比没有监控更糟 —— 因为你会信一个错的数。
而且和渲染 bug 不同，计数 bug **会污染数据库**，回滚代码救不回历史。
所以这些不变量是写下来的、有测试的、被强制的：

- **`event_id` 幂等去重。** 同一条日志无论从本地扫描、agent 推送、SSH 拉取、重扫还是断网重传
  哪条通道到达，都只计一次。上游组件可以放心重复劳动，收敛由存储层一处保证。
- **第二把键，认「这次生成」。** Codex 分叉（subagent、人工 fork）会把父线程整段复制进新 rollout
  并换掉时间戳 —— 所以副本的 `event_id` 必然不同。由 `turn_id` + 累计用量导出的 `dedup_key`
  专门拦这个。实测 610 个 rollout：33,445 条事件里 845 条（2.53%）是副本，
  重复计入的 output token 占 3.12%。（[ADR-0020](docs/adr/0020-codex-resume-duplicate-events.md)）
- **offset 仅在上报成功后推进。** 日志文件本身就是重传缓冲，提前推进会在断网时丢数据。
- **权威优先，推断标注。** 配额取自厂商载荷；外推与额度估计一律带显式徽标。
- **覆盖已入库的行是白名单制，且从不碰计数列。**「一次请求算给谁」和「它被计了几次」
  是两回事。每一处例外都有 ADR。
- **时区是配置项，不是宿主机的心情。** 同一份数据按 `America/New_York` 与 `Asia/Shanghai`
  聚合，「今日」差了两倍多。时区非法直接拒绝启动，而不是静默降级。
  （[ADR-0021](docs/adr/0021-aggregation-timezone.md)）
- **鉴权由监听地址推导，不由开关决定。** 只听 loopback 就零配置免鉴权；
  能被网络访问却没配凭据，**进程直接拒绝启动** —— 这条规则要防的是「忘记」。
  （[ADR-0016](docs/adr/0016-read-endpoint-auth.md)）

其中好几条是被真实数据推翻了旧决策之后重写的：速度指标在 ADR-0009 之前错了三个方向；
Codex 计费判据的第一版精确率只有 20.8%，被整个丢掉。[ADR](docs/adr/README.md)
把这些更正留在原地，而不是抹掉。

## 与同类项目的差异

以下基于对两个项目源码的实际阅读（[调研笔记](docs/references.md)）：

| | ccusage | token-monitor | OmniToken |
|---|---|---|---|
| 形态 | Node CLI | Electron 桌面应用 | 单 Go 二进制 |
| 工具覆盖 | **15+ 种** | 多种 | 仅 Claude Code + Codex |
| 多设备 | ❌ | ✅ 摘要级同步 | ✅ **事件级，单一库** |
| 实时 | ❌ 事后单次统计 | ✅ | ✅ SSE |
| headless 服务器 | ✅ | ❌ 桌面绑定 | ✅ |
| 生成速度 / TTFT | ❌ | ❌ | ✅ 并集口径 + 代理精确 |
| API 直调采集 | ❌ | ❌ | ✅ 本地代理 |
| 订阅配额 | ❌ | ❌ | ✅ 权威值 |
| 历史保留 | 本地日志 | 370 天摘要 | 全量事件 |

**ccusage 支持的工具比我们多得多**，它的逐模型成本也是我们的对账基准。
如果你只在一台机器上工作，它很可能是更合适的选择。OmniToken 存在的意义在多机场景。

## 工程

以当前代码树实测：

| | |
|---|---|
| Go 源码 | 14,565 行，12 个包 |
| Go 测试 | 16,356 行，448 个测试函数 —— **测试代码多于产品代码** |
| 直接依赖 | 2 个（`modernc.org/sqlite`、`golang.org/x/sys`） |
| CGO | 无（`import "C"` 零命中） |
| ADR | 26 篇决策记录 |
| Release 二进制 | 13 MB（darwin/linux 的 arm64+amd64、windows amd64） |

`make check`（gofmt、`go vet`、测试、覆盖率门禁、构建）与 CI 跑的是**同一条命令** ——
刻意不把它展开写进 workflow，否则两份副本必然漂移。
覆盖率门禁只卡生成 `event_id` 的三个包（`parser/codex` 90%、`parser/claudecode` 80%、
`proxy` 88%），因为只有那里的回归会永久污染数据。
HTTP handler 与面板代码不卡覆盖率 —— 给它们造 mock 代价大而收获小。

## 配置

单机场景全部可省略。最常用的几项：

```json
{
  "listen": "127.0.0.1:8787",
  "db": "~/.omnitoken/omnitoken.db",
  "timezone": "Asia/Shanghai",
  "collect": {
    "interval_seconds": 15,
    "ssh_hosts": [{ "host": "dev-server-1", "name": "server1" }]
  }
}
```

多机部署需要三个互不相同的高熵凭据 —— `token`（v1 ingest）、`read_token`（面板与 SSE）、
`admin_token`（enroll、撤销、设置写入）—— 外加每台设备在 enroll 时签发的独立 token。
完整参考：[configuration.md](docs/configuration.md) · [deployment.md](docs/deployment.md) ·
[API.md](docs/API.md)。

> **绝不要把 8787 / 8788 直接暴露到公网。** 公网接入必须经过具备 TLS 终止、路由限制、
> 鉴权、限流与超时的反向代理。部署指南里有完整的 Nginx 示例。

## 局限

直说，反正你迟早会发现：

- **只解析 Claude Code 与 Codex。** 长尾工具（`.gemini`、`.cursor`、`.aider`、`.cline` 等）
  调研过，实测取不到可解析的用量字段 —— 所以这是**明确不做**，不是待办。
- **桌面菜单栏应用只有 macOS，且尚未随 Release 分发**，需要自备 Rust 工具链构建。
  Windows 托盘端未排期。（但 Windows 的**进程表采集**已完成。）
- **Claude 配额捕获是机会性的，不是轮询。** 它搭在状态栏载荷上，只在 Claude Code 运行时更新。
  这是有意的取舍：换来的是不再读取你的凭据、不再碰 keychain。
- **工时分析（repo 投入时长、并行度）目前只有 API，面板尚未渲染。**
- **网页端用量**（claude.ai 等）不在范围内 —— 数据拿不到。
- 无多租户、无团队权限、不替代计费系统。设计上就是单用户。
- **Codex 的生成速度是保守下界** —— 含工具执行时间，面板上如实标注。
- 无 Windows arm64 原生构建，`install.ps1` 会回退到 amd64 模拟运行。

## 参与开发

```sh
make check      # 提交 PR 前必跑：fmt + vet + 测试 + 覆盖率门禁 + 构建
make release    # 交叉编译五平台到 dist/
```

PR 目标是 `dev`，`main` 只用于发布。分支模型见 [CONTRIBUTING.md](CONTRIBUTING.md)；
架构约定与不可违反的正确性铁律见 [CLAUDE.md](CLAUDE.md)。重大决策先写 ADR 再写代码。

设计文档在 [docs/](docs/README.md) —— 需求、架构、ADR、接口契约、配置、路线图。

## 许可

[MIT](LICENSE) © 2026 SuooL
