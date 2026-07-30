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
| `token` | 空 | legacy v1 ingest bearer;旧配置中也作为 read/admin fallback |
| `read_token` | 未显式填写时回落 `token` | 非 loopback 查询、面板与 SSE 的只读 credential |
| `admin_token` | 未显式填写时回落 `token` | enrollment 与 settings mutation credential;新部署应与 read/v1 分离 |
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

| `listen` | scoped credentials | 读接口 | 启动 |
|---|---|---|---|
| loopback(`127.0.0.1` / `localhost` / `[::1]`) | read 可空;enrollment 仍要求非空 admin | 免鉴权 | 正常 |
| 非 loopback(`0.0.0.0`、`:8787`、组网 IP…) | `token`、`read_token`、`admin_token` 均非空 | 要 read bearer | 正常 |
| 非 loopback | 任一缺失 | —— | **拒绝启动,退出码 1** |

> `":8787"` 的主机名是空的,那表示**所有网络接口**,不是本机。所以它属于第二、三行。

拒绝启动而不是打警告,是因为旧默认值(`:8787` + 免鉴权读)已经证明警告拦不住什么:
那份配置会把全部用量历史对同网段公开,而唯一的提示说的是写接口。错误信息里直接给出
两条修法。

**破坏性变更:配置文件里显式写着 `"listen": ":8787"` 的部署,升级后会启动失败。**
默认值改了救不了它 —— 显式值优先。首次运行自动生成的 `config.json` 里就带着旧的
`":8787"`,所以命中的是「装完没动过配置」这类部署。二选一:

```jsonc
// 1) 保持对外可达,分离三个用途
{ "listen": ":8787",
  "token": "<LEGACY_V1_INGEST>",
  "read_token": "<READ_ONLY>",
  "admin_token": "<ADMIN>" }

// 2) 改回只听本机,让别的机器经隧道/中继上报(下一节)
{ "listen": "127.0.0.1:8787" }
```

配了 scoped token 之后:

- **网页面板**:设置 → 访问令牌,分别填 `read_token` 与 `admin_token`,保存在该浏览器
  的 localStorage。没填时面板顶部会挂一条
  横幅提示,不会九个页面都报 401 而不说原因。
- **桌面端**:设置里在服务端地址下面填 `read_token`。它明文存在应用配置目录里
  (与地址并列),理由见 ADR-0016 第 8 条。
- **v2 agent**:enrollment 后使用自己的 `device_token`;不共享 read/admin。
- **statusline / curl**:读取接口使用 `read_token`。

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

服务端对外可达,所以必须有三类 scoped credential:

```jsonc
// 服务端 ~/.omnitoken/config.json
{ "listen": "100.64.0.10:8787",
  "token": "<LEGACY_V1_INGEST>",
  "read_token": "<READ_ONLY>",
  "admin_token": "<ADMIN>" }
```

```jsonc
OMNITOKEN_ADMIN_TOKEN='<ADMIN>' omnitoken agent enroll \
  -server http://100.64.0.10:8787 -name macmini -allow-insecure-http
```

`server` 填组网虚拟 IP(Tailscale / EasyTier)完全同理 —— agent 不感知拓扑,
对它来说都只是一个 URL。

**B. 只能经某台同伴到达 —— 链式中继**(ADR-0003 第 3 条)

机器 d 连不到服务端但能连到 c。c 上的 agent 开一个只转发 ingest/heartbeat 的认证
中继:

```jsonc
// 机器 c 的 agent.json:自己上报,同时替 d 转发
{ "server": "https://hub.example",
  "relay_listen": "100.64.0.20:8788",
  "relay_token": "<C_LISTENER_SECRET>" }
```

```jsonc
// 机器 d 的 agent.json:指向 c
{ "server": "http://100.64.0.20:8788",
  "allow_insecure_http": true,
  "relay_upstream_token": "<C_LISTENER_SECRET>",
  "protocol_version": 2,
  "device_id": "<ENROLLED_UUID>",
  "device_token": "<DEVICE_SECRET>" }
```

relay 自身无权替代设备身份:`X-OmniToken-Relay-Token` 逐跳更换,最终
`Authorization: Bearer <device_token>` 原样到 Hub。不同跳可用
`relay_token/relay_upstream_token` 配不同 secret。

**C. 想让服务端继续只听本机 —— SSH 隧道**

这是让 Hub 不对网络监听的最直接方式:传输加密与端口访问由 SSH 承担;v2 设备身份
仍由 enrollment/device token 提供。

```jsonc
// 服务端保持 loopback;为 enrollment 显式配置 admin credential
{ "listen": "127.0.0.1:8787", "admin_token": "<ADMIN>" }
```

在被统计机器上把服务端的端口拉到本地,再让 agent 连本地:

```sh
# 被统计机器 → 服务端:把服务端的 8787 映射成本机的 47871
ssh -N -L 47871:127.0.0.1:8787 user@server       # 常驻交给 launchd(见下)/ autossh / systemd
```

```jsonc
OMNITOKEN_ADMIN_TOKEN='<ADMIN>' omnitoken agent enroll \
  -server http://127.0.0.1:47871 -name macmini
```

反过来,只有服务端能主动连出去时用反向隧道:在**服务端**上跑
`ssh -N -R 47871:127.0.0.1:8787 user@被统计机器`,被统计机器那侧的 `agent.json` 写法不变。

**隧道的本地端口和服务端的 `listen` 端口是两回事。** 冒号左边的 47871 是隧道落地端口,
右边的 `127.0.0.1:8787` 是服务端**自己**监听的地址 —— A/B/D 里的 8787 是产品默认值,
不要跟着改。

为什么不用 8787 当落地端口:它是本机随手就会被占的端口(比如这台机器上也跑着一个
`omnitoken serve`),撞上之后的现象很难认。挑 **40000–49151** 之间的一个数,
这段实践中没有常见占用,更要紧的是它**低于 macOS 的临时端口起点**
(`net.inet.ip.portrange.first`,本机是 49152)。落地端口若落在临时端口区间里,
可能在隧道 bind 之前先被某条出站连接抢走,于是 `ExitOnForwardFailure`(见下一节)
时灵时不灵地把隧道踢掉,看起来像网络抽风,其实是端口撞车。

```sh
sysctl net.inet.ip.portrange.first               # 确认这台机器的临时端口起点
lsof -nP -iTCP:47871 -sTCP:LISTEN                # 无输出 = 空闲
```

第二条要在**隧道 bind 的那台机器**上跑 —— `-L` 是被统计机器,`-R` 是**被统计机器**
(不是服务端:反向隧道的监听端口开在 ssh 连过去的那一头,这一点最容易搞反)。

**D. 远端不允许装任何东西 —— SSH 拉取**(ADR-0003 第 2 条)

服务端 rsync 远端日志目录到本地镜像再解析,远端零部署,只需要一条你本来就有的
SSH 通道:

```jsonc
// 服务端 config.json:host 可以是 ~/.ssh/config 里的别名
{ "listen": "127.0.0.1:8787",
  "collect": { "ssh_hosts": [{ "host": "macmini", "name": "macmini" }] } }
```

实时性受拉取周期限制(自动降频至 ≥60s),换来的是远端完全不用动。

#### 常驻:让隧道和 agent 活过重启与断线(macOS launchd)

上面四条讲「怎么接通」,这一段讲「怎么一直通」。手敲的 `ssh -N` 随终端一起死,
前台跑的 `omnitoken agent` 也一样,重启后更是什么都不剩 —— 表现出来是面板上某台设备
时有时无,像是 OmniToken 坏了。macOS 上交给 launchd,最多两个 job:一个管隧道
(只有 C 需要),一个管 agent(A/B/C 都需要;D 是远端零部署,两个都不需要)。

**先决定隧道装在哪台机器上。** 规则只有一条:**ssh 客户端跑在能主动连出去的那一台,
launchd job 就装在那一台。**

- 被统计机器能 SSH 到服务端 → 用 `-L`,隧道 job 和 agent job **都在被统计机器上**,
  服务端一个字节都不用改。两个方向都通时选它 —— 常驻状态集中在一台机器,排查只看一处。
- 只有服务端能 SSH 到被统计机器(对方在 NAT 后、无公网,但你能从服务端登进去)→ 用
  `-R`,隧道 job 装在**服务端**,agent job 仍在被统计机器上。代价是常驻状态分在两台机器,
  而且转发失败发生在远端:被统计机器上只看得到 connection refused,原因得回服务端看。

下面以 `-R`(装在服务端)为例,`-L` 只是把 `-R 47871:127.0.0.1:8787 user@被统计机器`
换成 `-L 47871:127.0.0.1:8787 user@服务端`,plist 其余部分一模一样。落地端口沿用 C 里
选好的 47871,右边的 8787 始终是服务端自己的 `listen`,不要一起改。

服务端 `~/Library/LaunchAgents/com.omnitoken.tunnel.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.omnitoken.tunnel</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/ssh</string>
    <string>-N</string>
    <string>-T</string>
    <string>-o</string><string>BatchMode=yes</string>
    <string>-o</string><string>ExitOnForwardFailure=yes</string>
    <string>-o</string><string>ServerAliveInterval=30</string>
    <string>-o</string><string>ServerAliveCountMax=3</string>
    <string>-i</string><string>/Users/me/.ssh/id_ed25519_omnitoken</string>
    <string>-R</string><string>47871:127.0.0.1:8787</string>
    <string>user@被统计机器</string>
  </array>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>/Users/me/.omnitoken/tunnel.log</string>
  <key>StandardErrorPath</key><string>/Users/me/.omnitoken/tunnel.log</string>
</dict>
</plist>
```

plist 里**不展开 `~`**,所有路径写绝对路径。`ssh` 也要写 `/usr/bin/ssh`:launchd 不读
shell profile,job 拿到的是一份最小环境。

每个参数为什么在那里:

| 参数 | 理由 |
|---|---|
| `-N` | 只做端口转发,不在对端执行任何命令 |
| `-T` | 不要 tty。与 `-N` 同时写其实是冗余的(不执行命令本来就不分配 tty),留着无害 |
| `BatchMode=yes` | 无人值守下密码/口令提示是最坏的失败:ssh 会挂在那里等一个永远不会来的输入,而 launchd 认为它「还活着」。改成直接失败退出,交给 `KeepAlive` 重来。前提是这把密钥免口令 |
| `ServerAliveInterval=30` + `ServerAliveCountMax=3` | 半死的 TCP 链路(NAT 表项超时、Wi-Fi 切网、睡眠唤醒)不会给任何一端发 FIN,ssh 会永远挂着,agent 看到的是一个黑洞 —— 连得上、发得出去、永远没有响应。这两项让 ssh 在约 90 秒内自己判死并退出,launchd 才有机会重启它。90 秒不是巧合:菜单栏客户端的静默看门狗为同一种失败存在(ADR-0014) |
| `ExitOnForwardFailure=yes` | **最容易漏的一条。** 断链后远端 sshd 可能还占着 47871 没释放,新连接的转发请求会被拒绝;没有这一项,ssh 会照常保持登录、只是**一个转发都没有** —— 隧道「在」,数据一个字节过不去,`ps` 里看着完全正常。有了它 ssh 直接退出,launchd 隔 `ThrottleInterval` 再试,等远端端口释放后自愈 |
| `-i` | 显式指定密钥最省事。gui 域的 job 是有 `HOME` 的,`~/.ssh/config` 通常读得到;但**能不能用上 ssh-agent / 钥匙串里的带口令密钥,取决于 job 拿不拿得到 `SSH_AUTH_SOCK`,这一点我没有逐版本验证过** —— 别赌,给这条隧道单独生成一把免口令密钥,在对端 `authorized_keys` 里用 `restrict,permitlisten="127.0.0.1:47871"` 限死它只能开这一个转发(具体写法见 `man authorized_keys`) |

`KeepAlive=true` 本身就意味着「一直跑」,加载时也会拉起,所以它和 `RunAtLoad` 并存时
后者是冗余的;写着无害,而且一旦把 `KeepAlive` 改成条件字典,`RunAtLoad` 就成了唯一的
开机开关。`ThrottleInterval` 默认就是 10 秒,写出来只是让「重启不会空转成 busy loop」
这件事显式。

加载与卸载:

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.omnitoken.tunnel.plist
launchctl print     gui/$(id -u)/com.omnitoken.tunnel | grep -E 'state|pid|last exit'
launchctl kickstart -k gui/$(id -u)/com.omnitoken.tunnel   # 重启进程
launchctl bootout   gui/$(id -u)/com.omnitoken.tunnel      # 卸载
```

**改了 plist 要 `bootout` 再 `bootstrap`** —— launchd 记的是加载时读到的那份定义,
`kickstart -k` 只重启进程,不重读文件。老写法 `launchctl load -w` / `unload -w` 现在
仍然能用,但已是遗留接口,别和 bootstrap/bootout 混着用。

`~/Library/LaunchAgents` 下的 job **在用户登录后才启动**。一台重启后停在登录界面、
没人登录的机器上,它不会跑;要真正的开机即起,得放 `/Library/LaunchDaemons` 由 root 跑
(那样密钥也得是 root 的),或者给那台机器打开自动登录。

**agent 自己也要一个 job**(装在被统计机器上,`Label` 用 `com.omnitoken.agent`)。外壳
与上面那份完全一样,只列出不同的键:

```xml
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/omnitoken</string>
    <string>agent</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin</string>
  </dict>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>/Users/me/.omnitoken/agent.log</string>
  <key>StandardErrorPath</key><string>/Users/me/.omnitoken/agent.log</string>
```

- `PATH` 这一条不是仪式:agent 会调 `git` 判定 repo 归属,调不到时是**静默退化**成
  「事件没有 repo」,不报错。launchd 的默认 PATH 里没有 Homebrew 目录,所以显式补上。
- 日志走标准错误(`agent: reported N events` / `agent: report failed (will retry)`),
  两个路径指同一个文件即可。launchd 不做轮转,这个文件会一直长,自己定期清。
- 隧道抖动**不会丢数据**:上报失败时 offset 不推进(日志本身就是重传缓冲),隧道恢复后
  下一轮自动补齐,只是晚到。所以隧道 job 和 agent job 谁先起来都无所谓。

**验证它真的在工作**,五条命令:

```sh
# 1. 两个 job 都在跑,且不是在起—崩—起的循环里(看 state / pid / last exit status)
launchctl print gui/$(id -u)/com.omnitoken.tunnel | grep -E 'state|pid|last exit'
launchctl print gui/$(id -u)/com.omnitoken.agent  | grep -E 'state|pid|last exit'

# 2. 隧道落地端口在「被统计机器」上真的听着(-R 默认只绑对端 loopback)
lsof -nP -iTCP:47871 -sTCP:LISTEN

# 3. 打得通服务端。health 免鉴权,专门用来区分「地址错了」和「令牌错了」
curl -fsS http://127.0.0.1:47871/api/v1/health

# 4. 事件进来了,而且挂在正确的设备名下
curl -fsS 'http://127.0.0.1:47871/api/v1/breakdown?by=device&days=1'

# 5. agent 自己怎么说
tail -f ~/.omnitoken/agent.log
```

第 2、3、5 条在被统计机器上跑。第 4 条设备名对不上就查 `agent.json` 的 `name` ——
设备归属自报优先(ADR-0015)。

**完整卸掉**(装什么都得说清楚怎么删):

```sh
launchctl bootout gui/$(id -u)/com.omnitoken.tunnel   # 在装了隧道 job 的那台
launchctl bootout gui/$(id -u)/com.omnitoken.agent    # 在被统计机器上
rm ~/Library/LaunchAgents/com.omnitoken.tunnel.plist ~/Library/LaunchAgents/com.omnitoken.agent.plist
rm ~/.omnitoken/agent.json ~/.omnitoken/agent-state.json ~/.omnitoken/agent.log ~/.omnitoken/tunnel.log
rm /usr/local/bin/omnitoken                           # 或你实际放的位置
```

`bootout` 之后进程立即结束,`KeepAlive` 不再兜底。另外把对端 `authorized_keys` 里那把
专用公钥删掉 —— 卸载没删的密钥是最容易留下的一条后门。`agent-state.json` 是读取位点:
删了将来重装会从头扫一遍(幂等,不会重复计数,只是慢),留着则接着上次。这些都只在
被统计机器上,**已入库的历史事件在服务端,删这些不影响它们**。

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
| `allow_insecure_http` | — | false | 允许连接非 loopback 的明文 `http://`;只应在已确认的加密 overlay/受控 LAN 上显式开启 |
| `protocol_version` | — | 1 | `1` 为 legacy push,`2` 为 device-scoped + durable outbox |
| `token` | `OMNITOKEN_TOKEN` | 空 | v1 ingest token |
| `device_id` | — | enrollment 生成 | v2 稳定 UUID;改显示名不会改变它 |
| `device_token` | `OMNITOKEN_DEVICE_TOKEN` | enrollment 生成 | v2 设备独立 credential;文件权限 `0600` |
| `outbox` | — | 与 state 同目录的 `outbox.db` | v2 本地 durable SQLite outbox |
| `outbox_max_bytes` | — | 64 MiB | outbox 逻辑 payload 上限;满时回压且不丢旧批次 |
| `name` | `OMNITOKEN_NAME` | hostname | 设备名 |
| `relay_listen` | `OMNITOKEN_RELAY` | 空 | 开中继端口(如 `:8788`) |
| `relay_token` | `OMNITOKEN_RELAY_TOKEN` | 空 | 保护本机 relay listener 的独立 credential;启用 relay 时必填 |
| `relay_upstream_token` | `OMNITOKEN_RELAY_UPSTREAM_TOKEN` | 回落 `relay_token` | 连接上游 relay 时使用,支持每跳不同 credential |
| `interval_seconds` | — | 15 | 扫描周期 |
| `claude_dirs` / `codex_dirs` | — | 自动探测 | 日志目录 |
| `state` | — | `~/.omnitoken/agent-state.json` | offset 状态 |
| `since` | — | 空(不限) | 采集起点 `YYYY-MM-DD`,早于该日零点的事件不上报。日期写错**直接启动失败**,不会静默当成「不限」 |
| `proxy_listen` | `OMNITOKEN_PROXY` | 空(关闭) | 本地 API 代理监听地址,如 `127.0.0.1:8899` |
| `proxy_upstreams` | — | anthropic/openai 内置 | `{前缀: 上游 base}`,与内置合并 |

v2 设备先在目标机器执行 enrollment。admin secret 只通过环境或 secret store 注入,
不会出现在命令行参数、成功输出或 Hub 响应中:

```sh
OMNITOKEN_ADMIN_TOKEN='<ADMIN_SECRET>' \
  omnitoken agent enroll \
  -server https://ingest.example.invalid \
  -name research-workstation
```

命令仅在 Hub 接受注册后才以 `0600` 原子写入 `agent.json`。重复执行会复用已有
`device_id/device_token` 并可更新显示名;不要复制这份文件到另一台设备。若使用
loopback SSH 隧道,`-server http://127.0.0.1:47871` 不需要
`allow_insecure_http`;非 loopback 的 `http://` 默认拒绝。

### 什么时候必须设 `since`

**这台机器的日志不完全是它自己干的活时。** 家目录同步(iCloud / Syncthing / 恢复备份)
会让同一批会话文件出现在多台机器上,而 agent 推送是**自报**,在服务端优先级高于
SSH 拉取的旁观推断(ADR-0015)。所以给一台家目录是同步副本的机器新装 agent,
它会理直气壮地把另一台机器的历史认领过去。

实测数据:第二台 Mac 的日志事件里 **92% 已经存在于第一台名下** —— 543 个 Codex
rollout 文件有 539 个连 UUID 都一模一样。

判断方法:在两台机器上比一下**会话文件名**。Codex 的 rollout 文件名里带 UUID,
Claude 的会话文件名就是 session id —— 两边出现同名文件,只可能是同一批日志。

```sh
# Codex:注意目录是 sessions/YYYY/MM/DD/,要 find 到文件,不能 ls 顶层
ssh 另一台 'find ~/.codex/sessions -name "*.jsonl" -exec basename {} \; | sort' > /tmp/b.txt
find ~/.codex/sessions -name "*.jsonl" -exec basename {} \; | sort | comm -12 - /tmp/b.txt | wc -l

# Claude:会话文件散在 projects/<项目>/ 下
ssh 另一台 'find ~/.claude/projects -name "*.jsonl" -exec basename {} \; | sort' > /tmp/b.txt
find ~/.claude/projects -name "*.jsonl" -exec basename {} \; | sort | comm -12 - /tmp/b.txt | wc -l
```

任一条输出非 0 就说明有同步。**必须 `find` 到文件再比**:`ls ~/.codex/sessions`
只会列出 `2026` 这一个年份目录,两边一比得到 1,看着像「几乎没重叠」,而真实答案
可能是 539 —— 这个假阴性恰好出现在最需要这条检查的场合。

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
