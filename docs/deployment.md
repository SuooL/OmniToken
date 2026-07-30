# 部署与安全指南

OmniToken 的多机架构只有一个数据权威：

```text
device agent ──outbound──> hub ──> authoritative SQLite
device agent ──outbound──> hub ──> dashboard / desktop
```

每台设备的 agent 可以在本地暂存尚未确认的批次，但它不是第二个 hub，也不是数据库
副本。不要在多台机器上运行指向同一业务环境的独立 hub 后再尝试合并 SQLite。

## 先选拓扑

推荐顺序如下：

1. **加密 overlay**：Tailscale、WireGuard 或同类组网。它是普通多机部署最简单的默认
   选择。
2. **SSH 隧道**：已有 SSH 或 `ProxyJump`，且希望 hub 继续只监听 loopback。
3. **受信 LAN 直连**：只适用于网络边界明确、设备均受控，并已显式接受明文 HTTP
   风险的环境。
4. **公网 HTTPS ingress**：只有前述方式不可用时才采用；必须由反向代理终止 TLS、
   限制路由并实施鉴权、限流和超时。

任何拓扑都不应把裸 `8787` 或 `8788` 暴露到公网。OmniToken 自身的 `http://` 地址
不代表具备 TLS；只有 URL 明确为 `https://` 且前方代理或 overlay 提供加密时，才能
认为链路受到加密保护。

## Hub 基线

常见且最安全的基线是让 hub 只监听本机：

```json
{
  "listen": "127.0.0.1:8787",
  "db": "/var/lib/omnitoken/omnitoken.db",
  "state": "/var/lib/omnitoken/server-state.json",
  "mirror": "/var/lib/omnitoken/mirror"
}
```

在这种模式下，远端通过 overlay 入口、SSH 隧道或本机 HTTPS 反向代理接入。配置文件、
设备 credential 和服务环境文件应由运行用户拥有，权限设为 `0600`：

```sh
chmod 600 ~/.omnitoken/config.json ~/.omnitoken/agent.json
```

如果 hub 必须监听非 loopback 地址，需分别配置 legacy v1 ingest、只读和管理凭据。
在 v1 迁移结束前，legacy ingest 路由仍然存在，因此不能只保护 v2：

```json
{
  "listen": "100.64.0.10:8787",
  "token": "<LEGACY_V1_INGEST_SECRET>",
  "read_token": "<READ_ONLY_SECRET>",
  "admin_token": "<ADMIN_SECRET>"
}
```

示例中的尖括号是占位符。请为每个用途生成不同的高熵随机值，例如：

```sh
openssl rand -hex 32
```

不要把真实 secret 写入 shell history、版本库、工单或反向代理访问日志。

## 方案 A：加密 overlay（推荐）

1. 在 hub 和各 agent 设备上建立加密 overlay。
2. 用 overlay ACL 只允许 agent 访问 hub 的 ingest/heartbeat 端口；管理和 dashboard
   访问应限制到管理员设备。
3. 将 hub 绑定到它的 overlay 地址，并配置 scoped read/admin credential。v1 仍启用
   时也保留 legacy ingest credential。
4. agent 使用 overlay 地址：

```json
{
  "server": "http://100.64.0.10:8787",
  "name": "research-workstation",
  "allow_insecure_http": true
}
```

这里的 `allow_insecure_http` 是对“应用层 URL 使用 HTTP”的显式确认；链路机密性来自
overlay。先确认路由确实只走加密接口，不能把同一配置换成普通公网 IP。

若 overlay 提供内部 HTTPS，可把 `server` 改为对应 `https://` 地址，并关闭
`allow_insecure_http`。

## 方案 B：受信 LAN 直连

LAN 明文 HTTP 会暴露 bearer credential 和用量元数据，只适合受控、隔离且没有不可信
客户端的网段。配置与 overlay 类似，但还应同时满足：

- hub 防火墙只允许明确的 agent 地址；
- agent 显式设置 `allow_insecure_http: true`；
- 每台 v2 agent 使用独立 credential；
- 不通过访客 Wi-Fi、共享办公网或跨租户 VLAN 传输；
- 网络边界变化时立即切换到 overlay 或 HTTPS。

“能 ping 通”不等于“可信”。如果不能证明 LAN 的访问边界，就不要使用此方案。

## 方案 C：SSH 与 ProxyJump

SSH 隧道允许 hub 保持 `127.0.0.1:8787`，也不需要让产品端口在 LAN 或公网监听。
命令中的主机名可以是 `~/.ssh/config` 别名。

### Agent 能 SSH 到 hub

在 agent 设备上建立本地转发：

```sh
ssh -NT \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 \
  -o ServerAliveCountMax=3 \
  -L 127.0.0.1:47871:127.0.0.1:8787 \
  -J user@bastion.example \
  user@hub.internal
```

agent 只连接本机隧道入口：

```json
{
  "server": "http://127.0.0.1:47871",
  "name": "research-workstation"
}
```

### 只有 hub 能 SSH 到 agent

在 hub 上建立反向转发；监听端口位于远端 agent 设备：

```sh
ssh -NT \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 \
  -o ServerAliveCountMax=3 \
  -R 127.0.0.1:47871:127.0.0.1:8787 \
  -J user@bastion.example \
  user@agent.internal
```

agent 仍使用 `http://127.0.0.1:47871`。`47871` 是隧道落地端口，
`127.0.0.1:8787` 是 hub 自己的监听地址，不要把二者混为一谈。

生产环境应由 systemd、launchd 或同等 supervisor 管理隧道。使用 SSH key、限制 key
用途、验证 host key，并避免在 unit/plist 中放入私钥或明文口令。若不需要跳板机，
删除整段 `-J user@bastion.example`。

## 方案 D：公网 HTTPS ingress

公网入口只应转发 enrollment、v2 ingest 和 heartbeat。除非另有严格鉴权，不要把
dashboard、settings、legacy v1 ingest 或 relay 一并公开。

下面是 provider-neutral 的 Nginx 示例骨架。证书路径、域名、允许的路由和速率需按
实际环境调整：

```nginx
limit_req_zone $binary_remote_addr zone=omnitoken_agent:10m rate=5r/s;

server {
    listen 443 ssl;
    server_name ingest.example.invalid;

    ssl_certificate     /etc/ssl/omnitoken/fullchain.pem;
    ssl_certificate_key /etc/ssl/omnitoken/private.key;

    client_max_body_size 2m;
    client_body_timeout 10s;
    proxy_connect_timeout 5s;
    proxy_read_timeout 30s;
    proxy_send_timeout 30s;

    location = /api/v2/enroll {
        limit_req zone=omnitoken_agent burst=2 nodelay;
        proxy_pass http://127.0.0.1:8787;
    }

    location = /api/v2/ingest {
        limit_req zone=omnitoken_agent burst=10 nodelay;
        proxy_pass http://127.0.0.1:8787;
    }

    location = /api/v2/heartbeat {
        limit_req zone=omnitoken_agent burst=5 nodelay;
        proxy_pass http://127.0.0.1:8787;
    }

    location / {
        return 404;
    }
}
```

额外要求：

- 使用受信任证书和现代 TLS 配置，并验证从公网到 `https://` 地址的完整链路；
- enrollment 必须使用一次性或短期授权，ingest/heartbeat 使用 device-scoped bearer；
- 不在 URL query、日志或错误页中记录 credential；
- 对请求体字节数、事件数、连接数和请求速率同时设限；
- 代理到 hub 的链路只走 loopback、WireGuard 或 SSH/FRP 隧道；
- 监控证书到期、上游健康、401/403/413/429 和 5xx；
- 明确设置连接、读取、写入与 idle timeout，避免慢请求耗尽资源。

若代理产品不能满足这些要求，应回到 overlay 或 SSH，而不是直接开放 `8787`。

## Relay 使用边界

`8788` relay 不是公网 ingress。只在受保护的 loopback、overlay 或严格受控 LAN 上
使用，并要求下游鉴权、上游鉴权、请求体限制和超时。公网映射必须落到前述 HTTPS
入口，不能把 relay 端口直接转发到互联网。

## v1 到 v2 的迁移

v1 `/api/v1/ingest` 在迁移期继续工作，因此可以逐台迁移，不需要同时停机：

1. 先升级 hub，并保留现有 legacy `token`。
2. 选择一台低风险设备，备份其 agent 配置和本地状态。
3. 运行 `omnitoken agent enroll`，让 agent 生成并保存稳定 `device_id`，领取该设备
   独有的 ingest credential。
4. 启动 v2 agent，确认 heartbeat、显示名称和能力信息正确。
5. 等待 durable outbox 清空，并确认新事件只插入一次。
6. 对其余设备逐台重复；不要复制一台机器的 identity/credential 文件到另一台机器。
7. 所有活跃 agent 都完成 v2 迁移并度过至少一个正常采集周期后，撤销 legacy shared
   ingest credential。

历史事件无需重写。旧 agent 数据可以保留 `legacy_unbound` 或既有设备标签；不要把
无法证明的历史归属自动声明为精确 UUID 归属。`event_id` 继续保证 v1/v2 重叠上报不会
重复计数。

### Enrollment、rotation 与 revocation

- **Enrollment**：在目标设备本机运行 `omnitoken agent enroll`。通过受保护通道提供
  enrollment 授权，核对 hub 地址和证书；生成的 `device_id` 与 credential 只属于该
  设备。
- **Rotation**：先签发新 credential，原子更新 agent 的受保护配置或环境文件，重启
  agent 并确认新 credential 已产生 heartbeat/ack，最后撤销旧 credential。不要先
  撤销再更新，否则会把仍在 outbox 中的数据暂时锁在设备上。
- **Revocation**：设备遗失、重装、credential 泄漏或退役时，立即从 hub 的管理入口
  按 `device_id` 撤销。预期结果是该 credential 的 ingest 和 heartbeat 都返回
  401/403，且 hub 不发生数据变更。

不要直接编辑 SQLite 中的 token hash 或 revocation 字段。管理命令的精确参数以当前
版本的 `omnitoken --help` / `omnitoken agent enroll --help` 为准；自动化时通过
权限为 `0600` 的环境文件或 secret store 传递授权，不使用命令行明文参数。

## 常驻进程

仓库提供 [systemd server unit](../deploy/omnitoken-server.service)、
[systemd agent unit](../deploy/omnitoken-agent.service) 和
[launchd agent plist](../deploy/com.omnitoken.agent.plist) 作为起点。

systemd 的基本安装与检查：

```sh
sudo install -m 0644 deploy/omnitoken-server.service /etc/systemd/system/
sudo install -m 0644 deploy/omnitoken-agent.service /etc/systemd/system/
# 启动前编辑对应 unit：把 User=%i 换成实际低权限账号，并核对 ExecStart
sudoedit /etc/systemd/system/omnitoken-server.service
sudoedit /etc/systemd/system/omnitoken-agent.service
sudo systemctl daemon-reload
# 每台机器只启用它承担的角色；hub 示例：
sudo systemctl enable --now omnitoken-server
systemctl status omnitoken-server
journalctl -u omnitoken-server -n 100 --no-pager
# agent 机器改为：
# sudo systemctl enable --now omnitoken-agent
# systemctl status omnitoken-agent
# journalctl -u omnitoken-agent -n 100 --no-pager
```

部署前应根据机器修改 unit 中的用户、路径和 hardening 选项。hub 与 agent 使用独立
低权限账号；数据目录可写，二进制和 unit 只读。不要用 root 运行仅为了绕过路径权限。

macOS 使用现代 launchd 子命令：

```sh
install -m 0644 deploy/com.omnitoken.agent.plist \
  "$HOME/Library/LaunchAgents/com.omnitoken.agent.plist"
launchctl bootstrap "gui/$(id -u)" \
  "$HOME/Library/LaunchAgents/com.omnitoken.agent.plist"
launchctl kickstart -k "gui/$(id -u)/com.omnitoken.agent"
launchctl print "gui/$(id -u)/com.omnitoken.agent"
```

隧道应使用独立 unit/plist，并依赖网络在线。supervisor 负责自动重启，agent 自身负责
带 jitter 的退避；不要用每分钟 cron 同时启动整批 agent。

## 健康检查

hub 本机探针：

```sh
curl --fail --silent --show-error http://127.0.0.1:8787/api/v1/health
```

该端点只说明进程和 HTTP listener 可达，不证明 ingest credential 有效或数据正在
流动。完整健康检查还应确认：

- agent heartbeat 的 server-received age；
- outbox queued batches/bytes、最老批次年龄和最后成功上传时间；
- 最近的 401/403、永久拒绝、超时和退避状态；
- hub 数据库可写、磁盘余量和最近备份时间；
- 公网入口证书、上游健康和 413/429/5xx 比例；
- systemd/launchd 进程状态和异常重启次数。

应以 hub 收到 heartbeat 的时间判断 online/stale/offline；agent 自报时钟只用于诊断，
不能让一台时钟错误的设备保持“在线”。

## 备份与恢复

### Hub

SQLite 数据库是唯一权威数据。运行中不要直接复制单个 `.db` 文件并忽略 `-wal`；
可使用 SQLite 在线备份，或先停止 hub 再复制完整数据库：

```sh
mkdir -p /var/backups/omnitoken
sqlite3 /var/lib/omnitoken/omnitoken.db \
  ".backup '/var/backups/omnitoken/omnitoken-$(date +%F).db'"
```

同时备份 server 配置、collector state 和必要的 SSH 配置；包含 credential 的备份应
加密并限制访问。定期在隔离目录执行恢复演练，而不只是检查备份文件存在。

恢复步骤：

1. 停止 hub，保留损坏数据库和日志用于诊断。
2. 将验证过的备份恢复到配置的 `db` 路径，并恢复正确 owner/mode。
3. 启动 hub，检查 migration 日志和 `/api/v1/health`。
4. 检查设备 registry、最近事件和 dashboard 查询。
5. 让 agent 继续重放未确认 outbox；观察重复数、拒绝和 backlog 下降。

durable outbox 只保证**未确认传输**在断网/重启后可重试，不是 hub 备份。已经收到 ack
而后因恢复旧备份丢失的事件通常已从 outbox 删除，因此恢复点之后的数据仍受备份 RPO
约束。

### Agent

备份 agent 的 stable identity、配置和 outbox 数据库时先停止 agent，或使用 SQLite
一致性备份。不要把同一份 identity 恢复到两台同时运行的机器。

若 identity 或 credential 已确认泄漏，先在 hub 撤销，再以新身份 enrollment。若只
丢失 outbox，但源日志仍存在，可在明确 `since` 边界后重新扫描；`event_id` 会去重，
但错误的历史边界可能造成错误设备归属，因此不要盲目全量 rescan。

## 故障与恢复预期

| 故障 | 预期行为 | 运维动作 |
|---|---|---|
| hub/网络暂时不可达 | 批次留在 durable outbox，上传按退避和 jitter 重试 | 恢复链路，观察 FIFO backlog 下降 |
| ack 丢失或响应超时 | 使用相同 batch identity 重试；hub receipt/event idempotency 防止重复 | 不手工删除 outbox |
| credential 被撤销 | ingest/heartbeat 拒绝且不发生 mutation，outbox 保留 | 确认撤销是否预期；需要时安全 rotation |
| 永久无效批次 | 进入可诊断 rejected/dead-letter 状态，不阻塞成无穷重试 | 检查 rejection code，修复版本或数据 |
| agent 磁盘满 | 无法持久化时不能推进源 cursor，不应静默丢弃最旧数据 | 释放空间并检查 outbox/源日志 |
| hub 数据库损坏或磁盘满 | 停止写入并暴露错误；agent 继续保留未确认批次 | 停 hub、修复存储或从备份恢复 |
| agent 重启 | stable identity 与 outbox 从磁盘恢复 | 确认 heartbeat 恢复、backlog 清空 |

只有当 ack 的 protocol、device、batch 和 sequence 全部匹配时，agent 才能删除对应
outbox 数据。任何不确定结果都应重试，而不是猜测成功。
