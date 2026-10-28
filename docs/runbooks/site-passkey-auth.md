# 独立站点登录与 Passkey

落实 ADR-0036。单用户站点,不提供公开注册。

## 配置与首次登录

在 Hub 配置添加:

```json
{
  "site_auth_origin": "https://omni.suool.net",
  "site_auth_db": "/var/lib/omnitoken/site-auth.db"
}
```

Origin 必须是 HTTPS origin,不带路径、尾部斜杠或 query;RP ID 是其主机名。
认证库绑定 origin,换域名需重新注册。认证库与用量数据库必须分开。
启用后即使 Hub 只监听 loopback,网页与读取接口也要求会话或 read bearer。
未配置时维持已有行为。站点会话不授予 admin 或 ingest 权限。

在服务器以数据库所有者运行:

```sh
sudo -u omnitoken /usr/local/bin/omnitoken auth-code \
  -config /etc/omnitoken/config.json -ttl 30m
```

命令只输出登录码;默认有效 30 分钟,最多一小时,只能使用一次。
重新生成会立即使未消费的旧码失效。不要把码放入 URL、日志、提交或工单。

1. 打开 `https://omni.suool.net/login`,输入登录码。
2. 点击「添加此 Mac 的 Passkey」,在系统提示中使用 Touch ID 或设备密码。
3. 点击「进入面板」;下次可直接用「使用 Passkey 登录」。

Apple Passkey 可通过 iCloud Keychain 同步,不承诺硬绑定某一台 Mac。
注册必须在认证后十分钟内完成。会话有效期 30 天,退出撤销服务器会话,
SSE 在下次发送数据时终止。恢复访问通过 CLI 生成新登录码,不依赖邮件或 Authelia。

## 代理切换与回滚

先部署应用,验证 `/login`、未登录读接口 401 和原有 bearer 可用。
仅在 `omni.suool.net` 同时删除 `forward_auth` 和 `header_up Authorization`,
否则代理注入 read token 会绕过站点会话认证:

```caddy
omni.suool.net {
    reverse_proxy 127.0.0.1:8787 {
        header_up Host {host}
    }
}
```

不修改其他受 Authelia 保护的站点、ingest 域名或 agent 配置。
部署前保留二进制与配置备份;用量库使用 SQLite backup API 一致性备份。
回滚恢复原二进制、Hub 配置和 Caddy 站点配置,保留认证库。

## 验证

```sh
make check
go test -race ./internal/siteauth ./internal/server
NODE_PATH=/path/to/playwright/node_modules node scripts/site-auth-browser.cjs
```

浏览器验收需要 Chrome 和 Playwright,使用独立临时数据与虚拟平台认证器,
验证一次性码、注册、签名拒绝、退出、免用户名登录、API 权限和窄屏布局。
它不读生产凭据,不使用用户浏览器 profile,不能替代实际 Mac 的系统验证。

## 现网核查记录(2026-10-27)

实际 Hub 是 **aliyun198 / 8.163.138.198**,不是旧迁移 runbook 中的 146。
SSH 别名 `aliyun198`,管理端口 2222。服务 `omnitoken-server.service`:

- 二进制:`/usr/local/bin/omnitoken`
- 配置:`/etc/omnitoken/config.json`
- 用量库:`/var/lib/omnitoken/omnitoken.db`
- 监听:`0.0.0.0:8787`;日界:`America/New_York`;Hub 本机采集关闭。
- Caddy:`/etc/caddy/conf.d/omnitoken.caddy`
- Authelia:Docker 容器,`127.0.0.1:9091`;原先提供 omni forward_auth。
- 198 自身由独立 `omnitoken-agent.service` 向 loopback 上报。

现场读取的配置:Mac、mypc 使用 mesh 地址 `http://192.168.10.124:8787`,
a100 使用 `https://ingest.suool.net`,198 自采 agent 用 `http://127.0.0.1:8787`。
Mac 旧 server/tunnel job 未加载。七个注册节点中 suool-mac、mypc、hzsmini、a100、
aliyun198 在线,pathzl、h200 离线。此为时点观测,不是永久状态。
hzsmini 配置未能通过 SSH 复核,不把旧文档中的传输方式当作已确认现状。
