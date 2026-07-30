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

生成的文件权限为 `0600`,因为它承载读写共用的 token。

## 服务端 `~/.omnitoken/config.json`

| 字段 | 默认 | 说明 |
|---|---|---|
| `listen` | `127.0.0.1:8787` | HTTP 监听地址。**默认只听本机**;填非 loopback 地址时必须配 `token`,见下 |
| `db` | `~/.omnitoken/omnitoken.db` | SQLite 路径 |
| `state` | `~/.omnitoken/server-state.json` | 采集 offset/repo 缓存 |
| `mirror` | `~/.omnitoken/mirror` | SSH 拉取镜像根目录 |
| `token` | 空 | 共享 bearer token,读写共用。写接口:配了就要,空 = 不鉴权(启动警告)。读接口:`listen` 非 loopback 时必须有,见下 |
| `device_name` | hostname | 本机作为被统计设备的名字 |
| `pricing_overrides` | — | `{模型: {input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok}}`(单位 USD/百万 token) |
| `worktime_idle_minutes` | 5 | 工时空闲停表阈值(ADR-0006) |
| `statusline_cache_path` | `~/.omnitoken/statusline-cache.json` | 定位 `omnitoken statusline` 的产物;配额从它旁边的 `rate-limits.json` 读取(ADR-0011)。不再轮询任何端点 |
| `collect.interval_seconds` | 15 | 本地扫描周期;SSH 拉取自动降频至 ≥60s |
| `collect.local` | true | 是否采集本机日志 |
| `collect.local_dirs` | 自动探测 | Claude Code 日志目录 |
| `collect.codex_dirs` | 自动探测 | Codex 日志目录(`$CODEX_HOME` 生效) |
| `collect.ssh_hosts` | — | `[{host, name, since}]`,host 可用 ~/.ssh/config 别名;`since` 为 `YYYY-MM-DD`,该日零点之前的事件不从这台机器入库(留空 = 不限,ADR-0015)。日期写错会直接拒绝启动 |
| `proxy_listen` | 空 | 在服务端内起本地 API 代理(F14),如 `127.0.0.1:8899`;空 = 不启用 |
| `proxy_upstreams` | — | `{前缀: 上游 base}`,合并覆盖内置的 `anthropic` / `openai` |

### 读接口鉴权:由 `listen` 推导(ADR-0016)

要不要令牌不是一个开关,而是从监听地址算出来的:

| `listen` | `token` | 读接口 | 启动 |
|---|---|---|---|
| loopback(`127.0.0.1` / `localhost` / `[::1]`) | 空或有 | 免鉴权 | 正常 |
| 非 loopback(`0.0.0.0`、`:8787`、`192.168.x.x`、组网 IP…) | 有 | 要 `Authorization: Bearer <token>` | 正常 |
| 非 loopback | 空 | —— | **拒绝启动,退出码 1** |

> `":8787"` 的主机名是空的,那表示**所有网络接口**,不是本机。所以它属于第二、三行。

拒绝启动而不是打警告,是因为旧默认值(`:8787` + 免鉴权读)已经证明警告拦不住什么:
那份配置会把全部用量历史对同网段公开,而唯一的提示说的是写接口。错误信息里直接给出
两条修法。

**破坏性变更:配置文件里显式写着 `"listen": ":8787"` 的部署,升级后会启动失败。**
默认值改了救不了它 —— 显式值优先。首次运行自动生成的 `config.json` 里就带着旧的
`":8787"`,所以命中的是「装完没动过配置」这类部署。二选一:

```jsonc
// 1) 保持对外可达,配一个令牌(面板、桌面端、agent、statusline 填同一个)
{ "listen": ":8787", "token": "生成一个随机串,如 openssl rand -hex 24" }

// 2) 改回只听本机,让别的机器经隧道/中继上报(下一节)
{ "listen": "127.0.0.1:8787" }
```

配了 token 之后:

- **网页面板**:设置 → 访问令牌,填 `config.json` 里的同一个 `token`。就是原来那个
  「写入令牌」框,读写共用一个,存在本浏览器的 localStorage。没填时面板顶部会挂一条
  横幅提示,不会九个页面都报 401 而不说原因。
- **桌面端**:设置里在服务端地址下面填同一个 token。它明文存在应用配置目录里
  (与地址并列),理由见 ADR-0016 第 8 条。
- **agent / statusline**:各自配置里的 `token` 字段,同一个串。
- **curl**:`curl -H "Authorization: Bearer $TOK" http://host:8787/api/v1/overview`。

`GET /api/v1/health` 与面板外壳 `GET /` 始终免鉴权:前者不含任何用量数据,是用来
区分「地址错了」与「令牌错了」的探针(返回 `auth_required`);后者是因为浏览器首次
导航没法带请求头,而静态文件里没有私有内容 —— 它之后的每一次请求都要令牌。

**`?access_token=` 只有 `/api/v1/stream` 认。** 浏览器的 `EventSource` 不能设请求头,
SSE 只剩这条路。URL 里的令牌会进访问日志和 `Referer`,所以这个口子被限死在这一个
端点上,拿它去请求 `/api/v1/live` 会照样 401。自己写脚本消费 SSE 时能设头就设头。

### 多设备怎么配

不需要为多设备发明新机制,ADR-0003 的四条(直连 / 组网 / 中继 / SSH 拉取)已经够用,
可任意混用,服务端不区分来路。按「第二台机器能不能直接连上服务端」选:

**A. 内网/组网可直达 —— agent 出站直连**(ADR-0003 第 1 条)

服务端对外可达,所以必须有 token:

```jsonc
// 服务端 ~/.omnitoken/config.json
{ "listen": ":8787", "token": "<随机串>" }
```

```jsonc
// 被统计机器 ~/.omnitoken/agent.json
{ "server": "http://192.168.1.10:8787", "token": "<同一个随机串>", "name": "macmini" }
```

`server` 填组网虚拟 IP(Tailscale / EasyTier)完全同理 —— agent 不感知拓扑,
对它来说都只是一个 URL。

**B. 只能经某台同伴到达 —— 链式中继**(ADR-0003 第 3 条)

机器 d 连不到服务端但能连到 c。c 上的 agent 开一个中继端口,原样转发 ingest:

```jsonc
// 机器 c 的 agent.json:自己上报,同时替 d 转发
{ "server": "http://192.168.1.10:8787", "token": "<随机串>", "relay_listen": ":8788" }
```

```jsonc
// 机器 d 的 agent.json:指向 c
{ "server": "http://c.local:8788", "token": "<同一个随机串>", "name": "d" }
```

中继无状态、不落盘(日志本身就是重传缓冲),转发失败向下游透传错误。token 是原样
带过去的,所以三处必须一致。

**C. 想让服务端继续只听本机 —— SSH 隧道**

这是不想管令牌时最省事、也是鉴权最强的一种:加密与鉴权都由 SSH 承担,服务端一个
字节都不对外。

```jsonc
// 服务端:不动,保持默认
{ "listen": "127.0.0.1:8787" }
```

在被统计机器上把服务端的端口拉到本地,再让 agent 连本地:

```sh
# 被统计机器 → 服务端:把服务端的 8787 映射成本机的 8787
ssh -N -L 8787:127.0.0.1:8787 user@server        # 常驻可交给 autossh / systemd
```

```jsonc
// 被统计机器 ~/.omnitoken/agent.json:token 留空即可,读写都由 SSH 保护
{ "server": "http://127.0.0.1:8787", "name": "macmini" }
```

反过来,只有服务端能主动连出去时用反向隧道:在**服务端**上跑
`ssh -N -R 8787:127.0.0.1:8787 user@被统计机器`,被统计机器那侧的 `agent.json` 写法不变。

**D. 远端不允许装任何东西 —— SSH 拉取**(ADR-0003 第 2 条)

服务端 rsync 远端日志目录到本地镜像再解析,远端零部署,只需要一条你本来就有的
SSH 通道:

```jsonc
// 服务端 config.json:host 可以是 ~/.ssh/config 里的别名
{ "listen": "127.0.0.1:8787",
  "collect": { "ssh_hosts": [{ "host": "macmini", "name": "macmini" }] } }
```

实时性受拉取周期限制(自动降频至 ≥60s),换来的是远端完全不用动。

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
| `token` | 空 | 服务端 `listen` 非 loopback 时必填 —— 它读 `/api/v1/overview` 与 `/api/v1/quota`,那也是读接口(ADR-0016) |
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
| `since` | — | 空(不限) | 采集起点 `YYYY-MM-DD`,早于该日零点的事件不上报。日期写错**直接启动失败**,不会静默当成「不限」 |
| `proxy_listen` | `OMNITOKEN_PROXY` | 空(关闭) | 本地 API 代理监听地址,如 `127.0.0.1:8899` |
| `proxy_upstreams` | — | anthropic/openai 内置 | `{前缀: 上游 base}`,与内置合并 |

### 什么时候必须设 `since`

**这台机器的日志不完全是它自己干的活时。** 家目录同步(iCloud / Syncthing / 恢复备份)
会让同一批会话文件出现在多台机器上,而 agent 推送是**自报**,在服务端优先级高于
SSH 拉取的旁观推断(ADR-0015)。所以给一台家目录是同步副本的机器新装 agent,
它会理直气壮地把另一台机器的历史认领过去。

实测数据:第二台 Mac 的日志事件里 **92% 已经存在于第一台名下** —— 543 个 Codex
rollout 文件有 539 个连 UUID 都一模一样。

判断方法:在两台机器上比一下文件名。

```sh
ssh 另一台 'ls ~/.codex/sessions | sort' > /tmp/b.txt
ls ~/.codex/sessions | sort | comm -12 - /tmp/b.txt | wc -l   # 非 0 就说明有同步
```

有重叠就把 `since` 设成你真正希望它开始计入的那天。**只影响归属与上报范围,不影响
计数** —— 同一次请求无论被几台机器看到都只计一次,这条不变。

### 本地代理用法(F14)

**服务端或 agent 都能起**,配 `"proxy_listen": "127.0.0.1:8899"` 即可,二选一:
单机场景配在服务端(它本来就在扫这台机器的日志,再跑一个 agent 只为代理会把
日志白扫一遍);被监控机器上没有服务端时配在 agent。

把工具的 base_url 指过去:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8899/anthropic   # Claude Code
```

**Claude Code 指过来是安全的**:代理会为第一方 Anthropic 请求算出与日志完全相同的
event_id(`cc:<message.id>:<request-id>`),两次观测在入库时合成一行 —— 日志出
repo 归属,代理出 TTFT,token 只计一次(ADR-0013)。

**Codex / OpenAI 侧目前不要指过来**:那边没有可共享的标识,日志与代理会各记一条,
token 会计两遍。只有自己写的、不落日志的脚本适合走 `/openai` 前缀。

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
