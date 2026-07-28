# OmniToken

跨设备 LLM Token 用量监控。采集各机器上 Claude Code / Codex 的使用日志(以及可选的
API 直调代理),汇总到自托管服务端,用内嵌网页面板做统计分析:用量、成本、设备、
模型、项目工时、缓存效率、生成速度、**订阅配额**。

单二进制、纯 Go(无 CGO),服务端内嵌前端,采集端无 GUI 依赖 —— headless 服务器友好。

设计文档见 [docs/](docs/README.md)(需求 · 架构 · ADR · 参考调研 · 接口 · 配置 · 路线图)。

## 它解决什么

| | ccusage | token-monitor | OmniToken |
|---|---|---|---|
| 本地日志解析 | ✅ | ✅ | ✅ |
| 多设备汇集 | ❌ | ✅(仅摘要) | ✅ **事件级** |
| 实时监控 | ❌ | ✅ | ✅ SSE |
| headless 服务器 | ✅ | ❌ 桌面应用 | ✅ |
| 项目/工时分析 | ❌ | 部分 | ✅ repo 归一 + 双指标工时 |
| 生成速度 / TTFT | ❌ | ❌ | ✅ 代理实测 |
| API 直调采集 | ❌ | ❌ | ✅ 本地代理 |
| 权威配额(5h/周) | ❌ | ❌ | ✅ 官方端点 |
| 数据自持有 | 本地 | 摘要 370 天 | ✅ 中心 SQLite 全量 |

## 快速开始

从 [Releases](https://github.com/SuooL/OmniToken/releases) 下载对应平台的二进制
(附 `SHA256SUMS` 可校验),或自己构建:

```sh
make build            # 或 go build -o omnitoken ./cmd/omnitoken
```

```sh
# 在常开的机器上启动服务端(自动采集本机 + 首次全量导入历史)
./omnitoken serve
# 浏览器打开 http://<该机器>:8787
```

服务端配置 `~/.omnitoken/config.json`(全部可省略):

```json
{
  "listen": ":8787",
  "token": "换成你的随机串",
  "device_name": "gui-mac",
  "collect": {
    "interval_seconds": 15,
    "ssh_hosts": [
      { "host": "dev-server-1", "name": "server1" },
      { "host": "user@192.0.2.10", "name": "server2" }
    ]
  }
}
```

全部配置项见 [docs/configuration.md](docs/configuration.md);接口契约见 [docs/API.md](docs/API.md)。

## 面板

| 页面 | 内容 |
|---|---|
| 总览 | 今日/周/月/累计 token 与成本(真实 vs 订阅等效分列)、30 天趋势、按模型/设备/项目分布 |
| 实时 | **权威配额**(Claude 5h/周、Codex 周)、燃烧速率、设备在线状态、活跃会话,SSE 秒级推送 |
| 报表 | 日/周/月/会话聚合,任意区间,CSV / JSON 导出 |
| 明细 | 事件级下钻:多维筛选、分页、逐请求成本 |
| 缓存 | 命中率、1h/5m TTL 结构、节省金额(按模型) |
| 速度 | tokens/sec 与 TTFT,近似(日志)与精确(代理)双通道分离标注 |

## 四种设备接入方式(可混用)

| 方式 | 适用 | 远端安装 |
|---|---|---|
| SSH 拉取 | 你能 ssh 到的机器,零改动 | 无 |
| Agent 直连 | 与服务端同内网 / 服务端有公网 | omnitoken 二进制 |
| Agent 走组网 | Tailscale / EasyTier 等虚拟网 | omnitoken 二进制 |
| Agent 经中继 | 只能访问某台同伴机器的设备(可链式) | omnitoken 二进制 |

不需要公网 IP,agent 只出站。配置文件 `~/.omnitoken/agent.json`,配好后每台机器
只需 `./omnitoken agent` 一条命令:

```jsonc
// 机器 a、b:直连或组网 —— 区别只是 server 填哪个地址
{ "server": "http://192.0.2.1:8787", "token": "…", "name": "dev-a" }

// 机器 c:自己上报之余,给不能组网的邻居当中继
{ "server": "http://192.0.2.1:8787", "token": "…", "name": "dev-c", "relay_listen": ":8788" }

// 机器 d:够不到服务端,但能访问 c —— 指向 c 的中继端口(可继续链式)
{ "server": "http://c.internal:8788", "token": "…", "name": "dev-d" }
```

常驻模板见 [deploy/](deploy/)(systemd 与 launchd)。

## 采集 API 直调用量(可选)

agent 配 `"proxy_listen": "127.0.0.1:8899"`,脚本把 base_url 指向
`http://127.0.0.1:8899/anthropic`(或 `/openai`、自定义前缀),请求被透明转发,
同时记录 token 用量、**精确 TTFT 与耗时**、API key 指纹(仅哈希,绝不存明文)。

## Claude Code 状态栏

```json
// ~/.claude/settings.json
{ "statusLine": { "type": "command", "command": "omnitoken statusline", "refreshInterval": 10 } }
```

输出形如 `Opus 4.8 15.0K $1.25 · 今日 89.7M $89.50 · 5h 2% 4h31m · 周 37% 67h51m`
—— 本会话来自 Claude Code 自身,**今日为跨设备合计**,配额为官方权威值。
渲染约 10ms,服务端不可达时用缓存并标 `⟳`,永不拖慢界面。

## 正确性基石

- **event_id 幂等去重**:同一日志无论从几条通道到达(本地扫描、agent 推送、SSH 拉取、
  重扫、断网重传)都只计一次 —— 上游可以放心重复劳动
- **offset 仅在上报成功后推进**:日志文件本身就是重传缓冲,断网不丢数据
- **权威优先、推断标注**:配额取自官方端点(订阅计费才轮询;API 计费无窗口概念),
  推断值永远显式标注,两者不混淆
- **只采元数据**:永不读取对话内容;数据只存在你自己的机器上

## 开发

```sh
make check      # 提交前必跑:vet + 测试 + 覆盖率门禁 + 构建
make release    # 交叉编译五平台到 dist/
```

想参与贡献看 [CONTRIBUTING.md](CONTRIBUTING.md)(分支模型、PR 流程、验收标准)。
项目的架构约定与正确性铁律在 [CLAUDE.md](CLAUDE.md) —— Claude Code 会自动读它。

路线图与当前状态见 [docs/roadmap.md](docs/roadmap.md)。
