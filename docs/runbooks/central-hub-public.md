# Runbook:中心 Hub 上公网(阿里云 146)与后续换机迁移

落实 ADR-0026。目标拓扑:唯一权威 Hub 跑在公网服务器(阿里云 146),各设备 agent 用
**HTTPS 直连**上报,菜单栏/网页用 **HTTPS SSE** 看数,**无 SSH 隧道、无 WebSocket**。
本文按顺序照做即可;最后一节是「以后换另一台服务器」的可复制流程(设计初衷)。

占位符:`<域名>`(你自有、注册商独立于阿里云的域名)、`ingest.<域名>`、`omni.<域名>`、
`<146-公网IP>`、`<146-私网IP>`(阿里云 ECS 内网 IP,形如 172.x)。

---

## 0. 前提与命名(一次性)

- 一个你能自己改解析的域名,加两条 A 记录指向 `<146-公网IP>`,**TTL 调到 60s**:
  - `ingest.<域名>` —— 设备上报入口
  - `omni.<域名>` —— 面板/菜单栏入口
- 阿里云安全组:公网只放行 `443`(和你管理用的 `22`)。**绝不放行 `8787` 到公网。**
- 生成三个互不相同的高熵凭据,存进 146 上权限 0600 的文件,别进 shell history:
  ```sh
  openssl rand -hex 32   # READ_TOKEN
  openssl rand -hex 32   # ADMIN_TOKEN
  openssl rand -hex 32   # LEGACY_TOKEN(仅 v1 设备迁移期需要,迁完删)
  ```

---

## 1. 在阿里云 146 上装 Hub

### 1.1 二进制与低权限账号

```sh
# 交叉编译(在开发机):make release 产出 dist/,取 linux amd64 那个
# 传到 146 后:
sudo useradd --system --home /var/lib/omnitoken --shell /usr/sbin/nologin omnitoken || true
sudo install -m 0755 omnitoken /usr/local/bin/omnitoken
sudo install -d -o omnitoken -g omnitoken -m 0750 /var/lib/omnitoken /etc/omnitoken
```

### 1.2 config.json —— 关键是 `listen` 必须非 loopback(ADR-0026 §6)

`/etc/omnitoken/config.json`,owner `omnitoken`,权限 `0600`:

```json
{
  "listen": "<146-私网IP>:8787",
  "db": "/var/lib/omnitoken/omnitoken.db",
  "state": "/var/lib/omnitoken/server-state.json",
  "mirror": "/var/lib/omnitoken/mirror",
  "read_token": "<READ_TOKEN>",
  "admin_token": "<ADMIN_TOKEN>",
  "token": "<LEGACY_TOKEN>",
  "timezone": "America/New_York"
}
```

- **`listen` 用私网 IP,不是 `127.0.0.1`。** 听 loopback 会让 `loopbackOnly()` 为真、把
  读鉴权关成 no-op,而反代又把它捅上公网 = 免 token 裸读(ADR-0026 §6)。用私网 IP 后
  `requireAuthConsistency` 会强制三凭据齐备,缺一起不来 —— 正好兜底。若这台没有独立私网
  IP,退而用 `0.0.0.0:8787` 但**必须**靠安全组 + 本机防火墙(ufw/nftables)只放行 443。
- `token`(legacy)只在把 v1 设备迁到 v2 之前需要;迁完删掉这行(见 §5.3)。
- `timezone` 沿用现网的 `America/New_York`(ADR-0021,日界)。

### 1.3 systemd 常驻

仓库有 `deploy/omnitoken-server.service` 作起点。装好、改 `User=omnitoken`、
`ExecStart=/usr/local/bin/omnitoken serve -config /etc/omnitoken/config.json`:

```sh
sudo install -m 0644 deploy/omnitoken-server.service /etc/systemd/system/
sudoedit /etc/systemd/system/omnitoken-server.service
sudo systemctl daemon-reload
sudo systemctl enable --now omnitoken-server
# 本机私网探针(不经公网):
curl --fail --silent --show-error http://<146-私网IP>:8787/api/v1/health
```

---

## 2. 反代 + TLS + 两子域名(Caddy)

用 Caddy:自动 ACME 签证书,**证书随域名走、不随机器走**(换机时新机自动重签,不是迁移
包袱,ADR-0026 §5)。装好后写 `/etc/caddy/Caddyfile`:

```caddy
# 设备上报入口:只放三条 v2 路由(迁移期临时加 v1 ingest,见 §5.3)
ingest.<域名> {
	@ingest path /api/v2/enroll /api/v2/ingest /api/v2/heartbeat
	handle @ingest {
		reverse_proxy <146-私网IP>:8787
	}
	respond 404              # 其余路径一律 404
}

# 面板/菜单栏入口:读 API + SSE,前面必须再套身份网关(§3)
omni.<域名> {
	# SSE 端点:Caddy 默认就会 flush,不缓冲,无需额外配置
	reverse_proxy <146-私网IP>:8787
}
```

要点:

- **admin 面(revoke / devices/merge / `PUT /api/v1/settings`)哪个子域名都不暴露。**
  它们只在 146 本地可达,运维经 §7 操作。
- **限流**:Caddy 限流要装 `caddy-ratelimit` 插件;或者在阿里云侧用 WAF/安全组限速。
  给 `ingest.<域名>` 的 ingest/heartbeat 设 5–10 r/s、enroll 设 burst 2。
- **换 nginx 也行**:`deploy` 无 nginx 样例,但 `docs/deployment.md` 方案 D 有骨架;
  SSE 那段要 `proxy_buffering off;` + 大 `proxy_read_timeout`,否则实时被攒住。

```sh
sudo systemctl reload caddy
# 从外部验证证书与路由:
curl -I https://omni.<域名>/api/v1/health
```

---

## 3. 给 `omni.<域名>` 套身份网关(强烈建议)

面板读鉴权是 `read_token`,浏览器要看数就得揣着它;对公开子域名,单靠一个共享 bearer
偏弱。在 Caddy 前(或 `omni` 这个 site 内)加一层:

- 最省事:**Cloudflare Access** 或 **Tailscale Serve** 包住 `omni.<域名>`,只放行你自己;
- 或 Caddy `forward_auth` 接一个 OIDC 反代。

`read_token` 只在网关后使用。`ingest.<域名>` 不套网关(设备要能直接到),它靠 per-device
bearer + 限流。

---

## 4. 把权威库从 Mac 搬到 146

现网权威库在 Mac(`~/.omnitoken/omnitoken.db`)。**设备凭据在库内(`token_hash`),
搬库即保号** —— mypc 等 v2 设备迁移后无需重新 enroll,只改 URL(§5)。

```sh
# Mac 上:停 Hub,做一致性备份(别裸拷带 -wal 的库)
launchctl bootout gui/$(id -u)/com.omnitoken.server 2>/dev/null || true
sqlite3 ~/.omnitoken/omnitoken.db ".backup '/tmp/omni-migrate.db'"

# 传到 146
scp /tmp/omni-migrate.db 146:/tmp/omni-migrate.db

# 146 上:停 Hub,恢复到配置的 db 路径,修 owner/mode,重启
sudo systemctl stop omnitoken-server
sudo install -o omnitoken -g omnitoken -m 0600 /tmp/omni-migrate.db /var/lib/omnitoken/omnitoken.db
sudo rm -f /tmp/omni-migrate.db
sudo systemctl start omnitoken-server
sudo journalctl -u omnitoken-server -n 50 --no-pager   # 看 migration 日志正常
```

> 若你想干脆重开一套(不带历史),跳过本节,让 146 空库起来,各设备重新 enroll 即可。
> 但历史会留在 Mac,不会自动合并(单一权威,不能两库合并)。

---

## 5. 各设备切到公网

对每台设备,改它的 `~/.omnitoken/agent.json` 的 `server`,指向 **`https://ingest.<域名>`**。

### 5.1 v2 设备(mypc 等):只改 URL

```jsonc
{
  "protocol_version": 2,
  "server": "https://ingest.<域名>",   // ← 从隧道口 http://127.0.0.1:47871 改成这个
  "device_id":  "…(不动)…",
  "device_token": "…(不动)…",
  "name": "mypc"
}
```

改完重启该机 agent。因为库已搬来、凭据不变,ingest 直接通。**同时可以把老的反向隧道
job 停掉删掉**(`com.omnitoken.tunnel.*`)—— 隧道退休了。

Windows(mypc)改法照部署备忘:`ssh mypc` 落 PowerShell,复杂命令走
`powershell -EncodedCommand`;换 agent 二进制/配置的停起顺序见现网备忘。

### 5.2(可选)DNS 不稳时把解析钉到 IP —— 用 `resolve_ip` 配置项,不是把 URL 写成 IP

国内 DNS 可能污染。要绕开运行期 DNS,**保留 `server` 为域名**(TLS 要它),用 agent 的
`resolve_ip` 字段把该域名的解析钉到 146 的公网 IP:

```jsonc
{
  "server": "https://ingest.<域名>",   // TLS 仍按这个域名校验证书
  "resolve_ip": "<146-公网IP>",         // 连接时钉到这个 IP,绕开 DNS
  "device_id": "…", "device_token": "…", "protocol_version": 2
}
```

- 填了就把 server host 钉到该 IP、`ServerName` 仍保持域名,证书照常有效;留空则走正常
  DNS。TLS 验证按域名做(不是按 IP),所以既治 DNS 又不弱化安全。
- **换机时改这一个字段**(`resolve_ip` 指向新机 IP);不填 `resolve_ip` 的设备靠 DNS 零
  改动跟着走。
- URL 直写 `https://<IP>` 被否决(ACME 不签 IP 证书,连 IP 会证书名不匹配、破 TLS)。
- 不用 hosts 文件:那是机器级隐式状态,排障时容易被忘;`resolve_ip` 与其它 agent 设置同
  处一档、显式可查。

### 5.3 v1 设备(hzsmini):迁到 v2(推荐),或临时留 v1 通道

hzsmini 现在是 v1(共享 token)。两选一:

- **推荐:enroll 到 v2。** 在 hzsmini 本机跑(admin token 只从环境变量取):
  ```sh
  OMNITOKEN_ADMIN_TOKEN='<ADMIN_TOKEN>' \
    omnitoken agent enroll -server https://ingest.<域名> -name hzsmini
  ```
  它会生成稳定 `device_id`/`device_token` 写进 `agent.json`(0600)、`protocol_version`
  置 2。确认 heartbeat 正常后重启 agent。
- **临时留 v1**:在 §2 的 `@ingest` path 里临时加上 `/api/v1/ingest`,hzsmini 只改 URL 就
  能继续走 legacy(靠 config 的 `token`=`<LEGACY_TOKEN>`)。**所有设备迁到 v2 后,删掉
  这条 v1 path 和 config 里的 `token`,并按 deployment.md 撤销 legacy 凭据。**

### 5.4 Mac 从「Hub」降为「一台设备」

Mac 不再是 Hub,要装 agent 把本机日志推到 146:

```sh
OMNITOKEN_ADMIN_TOKEN='<ADMIN_TOKEN>' \
  omnitoken agent enroll -server https://ingest.<域名> -name suool-mac
# 然后用 deploy/com.omnitoken.agent.plist 起 agent(launchd),别再起 server。
```

> **归属衔接(必做)**:Mac 以前作为 hub-self 采集,历史行挂在它的旧身份下;新 enroll 会
> 给它一个新 `device_id`,之后的事件挂新身份 —— **本质上是同一台设备,必须合并**,不能
> 留成两台。用管理接口的**设备合并**(ADR-0019,人工发起、只走 loopback,见 §7)把旧
> hub-self 身份并入新 agent 身份:先 `merge/preview` 核对,再 `merge` 执行。合并前先在
> 面板确认旧身份的 `device_id`。这一步不做,Mac 的历史与新数据会永久分裂在两个身份下。

---

## 6. 桌面菜单栏 + 网页

- **菜单栏(Tauri)**:设置里把 server 改成 `https://omni.<域名>`,`read_token` 填
  `<READ_TOKEN>`(菜单栏走 header 带 token)。它优先 SSE,断了降级轮询 `/api/v1/live`。
- **网页**:浏览器开 `https://omni.<域名>`(先过 §3 身份网关),面板 XHR/EventSource 用
  `<READ_TOKEN>`(EventSource 经 `?access_token=`,这是唯一接受 query token 的端点)。

---

## 7. admin 操作只走 loopback

revoke / 设备合并 / 改 settings 这些高危口子不上公网。要用时,在 146 本地打私网地址:

```sh
# 在 146 上(或从你机器 ssh -L 转发到 146 的私网口):
curl -X POST http://<146-私网IP>:8787/api/v2/devices/<device_id>/revoke \
  -H "Authorization: Bearer <ADMIN_TOKEN>"
```

不要把 admin token 放进 URL、日志或持久化 history;从 0600 文件构造 header。

---

## 8. 验收清单

- [ ] `curl -I https://omni.<域名>/api/v1/health` 通,证书有效;`8787` 从公网不可达。
- [ ] 未带 `read_token` 访问 `omni.<域名>` 的读端点返回 401(证明 §6 生效,读鉴权没被
      loopback 关掉)。
- [ ] 每台设备 heartbeat 的 server-received age 正常(以 Hub 收到时间判在线,不看设备自
      报时钟)。
- [ ] outbox backlog 清空,新事件只插一次(`event_id`/`dedup_key` 去重生效)。
- [ ] 老的反向隧道 job 全部停用删除。
- [ ] 迁移期的 v1 path 与 legacy `token` 在全员 v2 后已删除并撤销。
- [ ] Mac 的旧 hub-self 身份已用设备合并并入新 agent 身份(§5.4),面板里 Mac 只剩一个身份。

---

## 9.【设计初衷】以后换另一台服务器

因为**设备/浏览器只认域名**、**Hub 全部状态 = 一个库 + 一份 config**,换机是这套固定动作,
**四台设备与浏览器一处都不用动**(除非某台用了 §5.2 的 IP 钉死):

```sh
# 1. 新机:照 §1、§2、§3 装好 Hub + Caddy + 网关(config.json 的 listen 用新机私网 IP)
# 2. 旧机:一致性备份权威库
sqlite3 /var/lib/omnitoken/omnitoken.db ".backup '/tmp/omni.db'"
scp /tmp/omni.db 新机:/tmp/omni.db
# 3. 新机:恢复库、起服务、私网 health 通过
sudo systemctl stop omnitoken-server
sudo install -o omnitoken -g omnitoken -m 0600 /tmp/omni.db /var/lib/omnitoken/omnitoken.db
sudo systemctl start omnitoken-server
# 4. 切 DNS:ingest.<域名> / omni.<域名> 的 A 记录改指新机公网 IP(TTL 60s,分钟级生效)
#    新机 Caddy 自动为同名子域重签证书
# 5. 用了 IP 钉死的设备(§5.2):把 agent.json 的 resolve_ip 改到新机 IP
# 6. 停旧机 Hub;观察各设备 heartbeat 与 outbox 在新机恢复
```

关键:**迁移 = 搬一个库文件 + 改 DNS**。设备凭据在库里、URL 是域名,所以设备侧零改动
(除 IP 钉死的那几台)。建议定期做一次真实演练(隔离目录恢复 + 切一个测试子域名验证设备
零改动继续上报),而不是只检查备份文件存在。

---

## 10. 回退

出问题要退回「本机 Mac Hub」:把最近一次 `.backup` 恢复到 Mac 的 `~/.omnitoken/omnitoken.db`,
重启 Mac 的 server,把设备 `server` 改回隧道口(或临时 hosts 指回)。单一权威,别让两台
Hub 同时收数据。

---

## 备份(常态)

146 上定期在线备份 + 异地留存(含 config,含凭据的备份要加密):

```sh
sqlite3 /var/lib/omnitoken/omnitoken.db \
  ".backup '/var/backups/omnitoken/omnitoken-$(date +%F).db'"
```
