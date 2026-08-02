# ADR-0026 中心 Hub 上公网:HTTPS+SSE 传输、分面安全,以域名解耦实现可迁移

状态:已采纳(2026-08-01,与用户确认)

## 背景

到目前为止 Hub 蹲在本机 Mac 的 loopback(`127.0.0.1:8787`),每台被统计设备各自
穿反向 SSH 隧道 + 跳板去够它(见部署拓扑)。新增一台经跳板可达的 H200 时,这套
「每设备一条隧道」的别扭暴露无遗:设备越多、网络越异构,隧道就越多越脆。

用户决定把中心 Hub 搬到一台公网服务器(阿里云 146),并提出三条硬要求:

1. **安全第一** —— 多机 agent 与公网 Hub 的通信必须足够安全;
2. **保住全部现有功能的实时性 / 准确性 / 效率** —— 效率含 agent→Hub,也含本机→Hub;
3. **去掉 SSH 隧道那种别扭的机器间通信**,倾向 WebSocket 之类的「正经接口」。

并追加一条同等重要的约束:**这台服务器将来不用了、要换另一台时,迁移必须非常
方便。**

关键事实(来自代码,决定了下面怎么选):

- agent 上报本就是纯 HTTP 客户端:`internal/agent/agent.go:552` = `POST
  {ServerURL}/api/v2/ingest` + `Authorization: Bearer <device_token>`,前面压着一层
  持久化 outbox(FIFO + ack,offset 仅在 ack 后推进,ADR-0004)。**SSH 隧道从来不是
  这条链路的一部分**,只是 loopback Hub 逼出来的外壳。
- 面板/菜单栏的实时是 **SSE**(`internal/server/live.go:252 handleStream`,
  `GET /api/v1/stream`),不是 WebSocket。
- Hub 是纯 `http.Server`,**无内建 TLS**(`server.go:165`),跨机加密本就设计成交给
  前置反代。
- 设备凭据只以 **SHA-256 存在库内**、常量时间比对、绑定 `device_id`、可单独 revoke
  (`store/deviceregistry.go`)。
- 读鉴权由监听地址推导:`loopbackOnly()` 为真时 `readAuth` 是 **no-op**
  (`server.go:283`,ADR-0016);非 loopback 监听则 `requireAuthConsistency`
  (`server.go:307`)在缺 `Token`/`ReadToken`/`AdminToken` 任一时**拒绝启动**。

## 决策

### 1. 单一权威搬到公网服务器,Mac 降为一台设备

全网只有一个 Hub、一份权威 SQLite(部署铁律:不得多 Hub 后合并库)。阿里云 146
成为唯一 Hub;本机 Mac 从「既是 Hub 又同机直采」变成一台 agent,以设备身份 enroll,
自身归属仍按 ADR-0015 `observed→self`。

本机 loopback Hub 拓扑保留为**离线回退形态**(文档保留),但它与公网 Hub **互斥、不
并存** —— 任何时刻只有一个权威。

### 2. 传输不变:上报走 HTTPS POST,看数走 SSE;否决 WebSocket 与机器间 SSH 隧道

去 SSH 的正解不是换协议,而是 Hub 上公网后隧道自然消失。机器间通信就两种,都走
标准 HTTP:

- **agent → Hub**:agent 的 `server` 从隧道口换成 `https://ingest.<域名>`,**传输代码
  一行不改**(`validateServerURL` 对 https 本就放行,连 `allow_insecure_http` 都不用
  开)。**不换 WebSocket**:现有「批量 POST + 持久 outbox + ack 幂等」对断网/重启/重传
  免疫,长连接会把这套 durable 语义拆掉却换不来任何东西 —— 用量事件是秒级批量,不是
  需要长连推的流。
- **Hub → 面板/菜单栏**:继续用 SSE(`/api/v1/stream`)。SSE 天生为「单向下推 + 走
  HTTP 基础设施」而生,穿反代、过 TLS、自动重连全现成,`X-Accel-Buffering: no` 已设。
  **同样不需要 WebSocket。**

**全系统零 WebSocket、零机器间 SSH 隧道。** 唯一残留的 SSH 是「运维本人登录自己的
VPS 做管理操作」,那不是数据通道(见 §5)。

### 3. 可迁移性的核心:设备与 UI 只认自有域名,不认 IP / 云厂商

这是让「换服务器」变简单的那一个决定。**agent 的 `server`、菜单栏/网页的地址,一律
指向你自己域名下的稳定子域名**(`ingest.<域名>`、`omni.<域名>`),**绝不写阿里云 146
的 IP,也不写任何绑云厂商的名字**。

于是「服务器身份(IP / 厂商)」被藏在 DNS 后面。换机时:

```
迁移 = 搬数据(§4) + 改 DNS 指向新机 + 新机自动签同名证书(§5)
```

**四台设备与所有浏览器一处都不用动** —— 这正是要避免的痛(尤其那台要靠 PowerShell
EncodedCommand 才能可靠操作的 Windows 机)。DNS/域名注册应独立于阿里云,别把解耦点
又锁回同一个厂商;并把该子域名的 DNS TTL 调低(如 60s),让切换即时生效。

**可选:把解析钉到 IP,治 DNS 不稳(不是把 URL 写成 IP)。** 国内网络 DNS 可能污染
或解析不稳,为此允许绕开运行期 DNS —— 但**做法是钉解析,不是改 URL**。`server` 恒为
`https://ingest.<域名>`(SNI / 证书校验 / Host 都要它),另给一个 IP,把这个域名的解析
钉死到该 IP:

- **URL 直写 `https://<IP>` 被否决**:ACME/Let's Encrypt 不给 IP 签常规证书,连 IP 会
  证书名不匹配,只能关校验或手工 pin —— 拿安全换一个 DNS 本就几乎不影响的速度,不值。
  (DNS 只在建连时查一次、按 TTL 缓存、还有 keep-alive 复用,且完全不碰吞吐。)
- **钉解析保住三样**:稳(运行期不依赖 DNS)、安全(TLS 仍走域名,证书照常有效)、
  可迁移(域名仍是迁移主杠杆)。语义取**硬钉死**(填了就只用该 IP),不做「DNS 优先、
  失败兜底」的半解析,避免迁移期新旧机并存的玄学。
- **落地是 agent 配置项 `resolve_ip`**,不是 hosts 文件。空则走正常 DNS;填了就给
  agent 的 http.Client 换一个自定义 `DialContext`,把 server host 钉到该 IP,而 TLS 的
  `ServerName` / 证书校验仍取自 URL 的域名。不用 hosts 是因为它是机器级、跨工具的隐式
  状态,排障时容易被忘;配置项与其它 agent 设置同处一档、显式、可测。
- **迁移代价**:一旦启用 IP 钉死,换机时这一个 `resolve_ip` 字段要更新(不再是纯域名的
  零改动)。用户明确接受这个代价。仍建议 DNS 与钉死 IP 一起改,DNS 作为不设钉死的设备的
  迁移杠杆。

### 4. Hub 的全部状态 = 一个 SQLite + 一份 config;搬库即保号

Go 无 CGO 单二进制(ADR-0002)让 Hub 天生可迁移:它的**全部权威状态就是一个
`.db` 文件 + 一份 `config.json`**。

- **设备凭据在库内**(`token_hash`),`read_token`/`admin_token`/legacy `token` 在
  config。**搬这两样即保号**:四台设备的 identity 与凭据继续有效,**无需重新 enroll、
  无需下发新 token**。
- 迁移用 SQLite 在线备份,不裸拷带 `-wal` 的库:
  `sqlite3 omnitoken.db ".backup '/path/out.db'"`。新机恢复该文件 + config,起进程即可。
- `event_id` 幂等与 codex `dedup_key`(ADR-0004 / 0020)是端到端的,与 Hub 在哪台机器、
  走隧道还是公网无关 —— 迁移不会造成重复计数或漏计。

### 5. 分面安全:三子域名 + admin 只 loopback,TLS 由反代按域名签

Hub 继续不自持 TLS。VPS 上放前置反代(推荐 Caddy,自动 ACME;或 nginx+certbot),
按**攻击面切三块**,而不是把整个 8787 丢上公网:

| 子域名 | 暴露的路由 | 防护 |
|---|---|---|
| `ingest.<域名>` | **仅** `/api/v2/enroll`、`/api/v2/ingest`、`/api/v2/heartbeat` | TLS + per-device bearer + 限流限体积 |
| `omni.<域名>` | 网页面板 + 读 API + `/api/v1/stream`(SSE) | TLS + **身份网关**(SSO/Access)+ `read_token` |
| (不开子域名) | revoke、devices/merge、`PUT /api/v1/settings` 等 admin 面 | **只在 VPS 本地可达**,运维经 SSH 到自己 VPS 或本地转发操作 |

要点:

- **admin 面永不上公网。** enroll 因需 admin token 且极少发生,可放 `ingest` 并狠限流 +
  定期轮换 admin token;更稳的是装机时经一次性本地转发做 enroll。
- **公网面板再加一层身份网关**(Cloudflare Access / Tailscale Serve / Caddy
  `forward_auth`):面板读鉴权是 `read_token`,浏览器要看数就得揣着它,对公开子域名
  单靠一个共享 bearer 偏弱;前面套 SSO,`read_token` 只在网关后用。这是把「安全第一」
  拉满的关键动作,且不改代码。
- **证书随域名走,不随机器走**:新机 DNS 指过来后 ACME 自动为同名子域签发,证书不是
  迁移包袱(DNS-01 或 HTTP-01 皆可;用 HTTP-01 时先切 DNS 再签)。
- 反代给 SSE 关缓冲(nginx 对 stream location `proxy_buffering off`;Caddy 默认即可),
  否则实时会被反代攒住。

### 6. 反代前置下 ADR-0016 的「loopback 免鉴」推定失效,Hub 必须非 loopback 监听

`readAuth` 在 `loopbackOnly()` 时是 no-op(`server.go:283`)。若 Hub 照旧听
`127.0.0.1:8787` 再套公网反代,服务端会误判自己 loopback-only、关掉读/管鉴权,而
反代把它捅上公网 —— 等于 `omni.<域名>` **免 token 裸读**。这是本次拓扑下最容易踩的洞。

因此:**Hub 必须以非 loopback 地址监听**(如 VPS 私网 IP,或 `0.0.0.0` 且用云安全组/
防火墙只放行本机反代到该端口),使 `loopbackOnly()` 为假、读/管鉴权强制开启;
`requireAuthConsistency`(`server.go:307`)随之要求 `Token`/`ReadToken`/`AdminToken`
齐备,缺一起不来 —— 这条守卫正好替我们兜底,配不齐就别想上公网。

### 7. 实时性与效率不由传输决定,本机→Hub 的代价写清楚

- **实时性不变**:端到端延迟主要是 agent 端扫描粒度(默认 15s)+ 上传 worker 空转
  1s + 心跳 30s(`agent.go` 常量),公网化不动这些数。要更实时就调小 `Interval`,与
  搬 Hub 正交。SSE 下推本身秒级。
- **本机(Mac)→Hub 的变化**:Mac 自己的用量从进程内直采变成 `Mac → 146` 走公网上报
  (批量元数据 JSON,量小,效率无压力);菜单栏/网页从读 localhost 免鉴变成 HTTPS SSE
  读 `omni.<域名>` + `read_token`(持久连接、开销低,更新多一个网络 RTT)。代价是 Mac
  离线时看不到自己的实时数 —— 数据不丢(agent outbox 缓冲,恢复补传),只是延迟。

## 影响

- 主体不改传输/解析代码;本 ADR 是**部署与运维决策**。落地清单(Caddy/nginx 两子域名
  配置、搬库步骤、四台设备改 `server` URL + `read_token`、菜单栏 settings、admin 走
  loopback)另出 runbook,并据此细化 `docs/deployment.md` 方案 D 为这套真实拓扑。
- §3 的 IP 钉死是本 ADR 唯一的代码改动,已实现:agent config 加可选 `resolve_ip`
  字段 + 自定义 `DialContext`(host→IP 映射,`ServerName` 保持域名),很收敛;不填则
  行为与从前一致。有单测覆盖三点:不填时用默认 transport、非法 IP 报错、钉 IP 后仍按
  域名校验证书(`internal/agent/resolveip_test.go`)。
- H200 及以后新设备接入退化成一件事:装 agent、`server = https://ingest.<域名>`、
  enroll。反向隧道 / 跳板 / `47871` 落地口全部退休。relay 也不再需要 —— 所有设备都有
  直接出站。
- Mac 需以设备身份 enroll(新增一台 agent);原「hub 本机不标旧版设备」等本机特例
  随之失效,按普通设备处理。

## 未决

- **enroll 是否公开**:放 `ingest` 子域名意味着一个 admin-token 端点暴露公网(靠限流 +
  轮换兜底),vs 装机时走一次性本地转发(更安全但多一步)。倾向后者,runbook 里定。
- **身份网关选型**:Cloudflare Access / Tailscale / 自建 OIDC 反代,取决于用户是否愿意
  再引入一个外部依赖;不影响本 ADR 的结构决策。
- **迁移演练**:恢复演练应在隔离目录跑通「备份→新机恢复→切 DNS→设备零改动继续上报」
  全链,而不是只验证备份文件存在(沿用 deployment.md 的演练要求)。
