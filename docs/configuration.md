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
| `statusline_cache_path` | `~/.omnitoken/statusline-cache.json` | 定位 `omnitoken statusline` 的产物;配额从它旁边的 `rate-limits.json` 读取(ADR-0011)。不再轮询任何端点 |
| `collect.interval_seconds` | 15 | 本地扫描周期;SSH 拉取自动降频至 ≥60s |
| `collect.local` | true | 是否采集本机日志 |
| `collect.local_dirs` | 自动探测 | Claude Code 日志目录 |
| `collect.codex_dirs` | 自动探测 | Codex 日志目录(`$CODEX_HOME` 生效) |
| `collect.ssh_hosts` | — | `[{host, name}]`,host 可用 ~/.ssh/config 别名 |
| `proxy_listen` | 空 | 在服务端内起本地 API 代理(F14),如 `127.0.0.1:8899`;空 = 不启用 |
| `proxy_upstreams` | — | `{前缀: 上游 base}`,合并覆盖内置的 `anthropic` / `openai` |

### 重扫 `-rescan`

```sh
omnitoken serve -rescan          # 本次启动前清空读取位点,重扫全部本地日志
omnitoken agent -rescan -once    # agent 侧同理,配合 -once 就是一次性回填
```

**什么时候需要**:解析器新增了一个派生字段,而历史事件里是空的。至今只有一次 ——
ADR-0009 的 `gen_ms`(生成区间)。加字段前入库的事件不会自己长出这个值,重扫才会补。

**为什么是安全的**:入库按 `event_id` 幂等(ADR-0004),重复观测直接忽略,
只有派生列会被补上,且每行只补一次。token 数、成本、事件条数完全不变。

**为什么是启动开关而不是独立命令**:进程跑起来后位点在内存里,从外面改状态文件
会被下一次提交覆盖。因此重置发生在采集器启动之前。

**代价**:一次全量重扫要读完所有日志(本机 5 万条事件量级约数十秒),期间 CPU 占用
高于平时。另外它只能补回**日志还在**的那部分 —— Claude Code 30 天后清理日志,
更早的历史事件仍然留在库里(N6),但它们的派生字段永远补不上了。

## Statusline `~/.omnitoken/statusline.json`

供 Claude Code 状态栏调用(`omnitoken statusline`),显示**本会话 + 跨设备今日 +
权威配额**。每次渲染都会调用,因此:网络超时 200ms、缓存 10s 内直接复用、
服务端不可达时用上次缓存并加 `⟳` 标记,**永不阻塞、永不报错退出**。

> **这是 Claude 配额的唯一来源**(ADR-0011)。渲染的同时,它会把 Claude Code
> 递来的 `rate_limits` 落到 `~/.omnitoken/rate-limits.json`,再由本机采集器读走。
> OmniToken 不再调 OAuth 端点,也不会去读其它状态栏工具的产物。

### 一键安装

```sh
omnitoken statusline -setup        # 安装;已有状态栏会被保留
omnitoken statusline -setup-undo   # 还原
```

`-setup` 会读 `~/.claude/settings.json`(尊重 `CLAUDE_CONFIG_DIR`):槽位为空就直接
注册 `omnitoken statusline`;**已经有别的命令则生成一个 wrapper 把两者都调用**,
而不是把它顶掉。改写前先备份,只动 `statusLine.command`,其余键与 `padding` /
`refreshInterval` 原样保留;settings.json 若不是合法 JSON 则拒绝改写并报错退出。
重复执行不会嵌套。

`-setup-undo` 按安装时记下的原值还原,并删掉 wrapper。

### 手动配 wrapper

不想用 `-setup` 的话,原理是这样:

```sh
#!/bin/sh
# ~/.claude/omnitoken-statusline.sh —— chmod +x 后填进 settings.json
# stdin 只能读一次,所以先读进来再分发给两边。
input=$(cat)
printf '%s' "$input" | omnitoken statusline -capture-only
printf '%s' "$input" | ccstatusline      # 换成你自己的那个
```

```json
{ "statusLine": { "type": "command", "command": "~/.claude/omnitoken-statusline.sh" } }
```

`-capture-only` 不输出任何内容、不请求服务端、不渲染,只把配额落盘,
所以额外开销就是一次进程启动。

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

**服务端或 agent 都能起**,配 `"proxy_listen": "127.0.0.1:8899"` 即可,二选一:
单机场景配在服务端(它本来就在扫这台机器的日志,再跑一个 agent 只为代理会把
日志白扫一遍);被监控机器上没有服务端时配在 agent。

把工具的 base_url 指过去:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8899/anthropic   # Claude Code
export OPENAI_BASE_URL=http://127.0.0.1:8899/openai         # Codex 等
```

请求被透明转发(方法/头/体原样,Authorization 一并转发,仅剥 Accept-Encoding
由 Go 透明解压),同时产出 `source=proxy` 事件:token 四分量、**精确 TTFT 与耗时**、
`account_label` = 凭据的 SHA-256 指纹前 12 位(绝不存明文),可区分多账号。
上报失败进内存缓冲(1000 条)自动补发。

计费通道按**凭据形态**判定,不按「走了代理就是 API key」:
`x-api-key` 或 `Bearer sk-ant-api…` / `sk-…` 记为 API 计费(真实美元);
`Bearer sk-ant-oat…`(Claude Code 订阅)与 `Bearer eyJ…`(ChatGPT 计划的 Codex)
记为订阅(等效成本);认不出的形态保持未定,按等效成本算 —— 猜成 API key 会在
账单上凭空多出真实支出,猜成订阅只是少算一个本就标着「等效」的数。

#### 与 Clash / 分流代理共存

TUN 模式抓的是 IP 层流量,而 127.0.0.0/8 走 lo0 不进 TUN,所以「工具 → 本地代理」
这一跳分流规则看不见也不需要看见。代理自己发往上游的那一跳走正常网络栈,照样进
TUN、照样匹配你的**域名规则**。

**唯一会失效的是按进程匹配的规则**(`PROCESS-NAME` / `PROCESS-PATH`):发起连接的
进程从 `claude` / `codex` 变成了 `omnitoken`。有这类规则的话,补一条指向同一节点的
`PROCESS-NAME,omnitoken,<节点>` 即可。
