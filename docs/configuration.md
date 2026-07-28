# 配置项一览

两个配置文件,均为 JSON,全部字段可省略(有默认值)。优先级:命令行参数 >
环境变量 > 配置文件 > 默认值。

## 配置文件从哪来

不需要手写。首次运行时程序会自己生成:

- `omnitoken serve` —— 配置不存在则写入一份**填好全部默认值**的 `config.json`,
  打印路径后照常启动。编辑后重启生效。
- `omnitoken agent` —— 缺少 `server` 且配置不存在时,生成骨架并提示填哪一项。
  `server` 故意留空:填假地址会让下次运行变成连接失败,而不是这条清楚的提示。

两条规则始终成立:

- **已存在的配置文件永不被覆盖** —— 里面可能有你填的 token。
- **生成失败不影响运行** —— 目录只读之类的情况下,`serve` 会打印原因并继续用内置默认值。

`-config` 指向哪里,就在哪里生成(含父目录),默认路径与自定义路径同一条规则。

> 生成的文件里 `db` / `state` / `mirror` 是**解析后的绝对路径**,指向 `~/.omnitoken/`。
> 想在一台机器上跑第二个实例,记得把这三项改掉 —— 否则两个实例会写同一个 SQLite 库。

生成的文件权限为 `0600`,因为它承载 ingest token。

## 服务端 `~/.omnitoken/config.json`

| 字段 | 默认 | 说明 |
|---|---|---|
| `listen` | `:8787` | HTTP 监听地址 |
| `db` | `~/.omnitoken/omnitoken.db` | SQLite 路径 |
| `state` | `~/.omnitoken/server-state.json` | 采集 offset/repo 缓存 |
| `mirror` | `~/.omnitoken/mirror` | SSH 拉取镜像根目录 |
| `token` | 空 | ingest bearer token;空 = 不鉴权(启动警告) |
| `device_name` | hostname | 本机作为被统计设备的名字 |
| `pricing_overrides` | — | `{模型: {input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok}}`(单位 USD/百万 token) |
| `worktime_idle_minutes` | 5 | 工时空闲停表阈值(ADR-0006) |
| `quota_poll_minutes` | 5 | 轮询 Anthropic OAuth 用量端点取权威 5h/周配额的间隔(ADR-0007)。**仅订阅计费生效**:F9 探测判为 API key 计费、或本机无订阅凭据时完全跳过(API 按量付费无窗口概念) |
| `collect.interval_seconds` | 15 | 本地扫描周期;SSH 拉取自动降频至 ≥60s |
| `collect.local` | true | 是否采集本机日志 |
| `collect.local_dirs` | 自动探测 | Claude Code 日志目录 |
| `collect.codex_dirs` | 自动探测 | Codex 日志目录(`$CODEX_HOME` 生效) |
| `collect.ssh_hosts` | — | `[{host, name}]`,host 可用 ~/.ssh/config 别名 |

## Statusline `~/.omnitoken/statusline.json`

供 Claude Code 状态栏调用(`omnitoken statusline`),显示**本会话 + 跨设备今日 +
权威配额**。每次渲染都会调用,因此:网络超时 200ms、缓存 10s 内直接复用、
服务端不可达时用上次缓存并加 `⟳` 标记,**永不阻塞、永不报错退出**。

| 字段 | 默认 | 说明 |
|---|---|---|
| `server` | `http://127.0.0.1:8787` | OmniToken 服务端地址 |
| `token` | 空 | 服务端要求鉴权时填 |
| `segments` | `["session","today","quota"]` | 段顺序,可裁剪 |
| `separator` | ` · ` | 段分隔符 |
| `cache_path` | `~/.omnitoken/statusline-cache.json` | 兜底缓存 |
| `no_color` | false | 关闭 ANSI 颜色(`NO_COLOR` 环境变量同效) |

接入 Claude Code(`~/.claude/settings.json`):

```json
{
  "statusLine": {
    "type": "command",
    "command": "omnitoken statusline",
    "refreshInterval": 10
  }
}
```

配额百分比按用量分级着色(≥90% 红、≥75% 黄、其余暗色),数字始终与颜色同时出现。
只显示**权威**配额(5h / 周),没有权威数据时整段省略——不拿推断值冒充。

## Agent `~/.omnitoken/agent.json`

| 字段 | 环境变量 | 默认 | 说明 |
|---|---|---|---|
| `server` | `OMNITOKEN_SERVER` | 必填 | 服务端或中继的 base URL |
| `token` | `OMNITOKEN_TOKEN` | 空 | ingest token |
| `name` | `OMNITOKEN_NAME` | hostname | 设备名 |
| `relay_listen` | `OMNITOKEN_RELAY` | 空 | 开中继端口(如 `:8788`) |
| `interval_seconds` | — | 15 | 扫描周期 |
| `claude_dirs` / `codex_dirs` | — | 自动探测 | 日志目录 |
| `state` | — | `~/.omnitoken/agent-state.json` | offset 状态 |
| `proxy_listen` | `OMNITOKEN_PROXY` | 空(关闭) | 本地 API 代理监听地址,如 `127.0.0.1:8899` |
| `proxy_upstreams` | — | anthropic/openai 内置 | `{前缀: 上游 base}`,与内置合并 |

### 本地代理用法(F14)

agent 配置 `"proxy_listen": "127.0.0.1:8899"` 后,脚本把 base_url 指向
`http://127.0.0.1:8899/anthropic`(或 `/openai`、自定义前缀),请求被透明转发
(方法/头/体原样,Authorization 一并转发,仅剥 Accept-Encoding 由 Go 透明解压),
同时产出 `source=proxy` 事件:token 四分量、**精确 TTFT 与耗时**、
`account_label` = API key 的 SHA1 指纹前 12 位(绝不存明文),可区分多账号。
上报失败进内存缓冲(1000 条)自动补发。
