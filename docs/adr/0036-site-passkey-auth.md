# ADR-0036 站点独立登录与 Passkey

状态:实施中(2026-10-27)

## 背景与决策

现网 Hub 位于 aliyun198,`omni.suool.net` 经 Caddy 的 Authelia forward_auth
认证后注入共享 read token。站点改为自行认证,不再依赖 `auth.suool.net`。

- 显式配置 `site_auth_origin` 启用,RP ID 为该 HTTPS origin 的主机名。
- 保持单用户,不提供公开注册、密码或邮件依赖。服务器 CLI 生成有时效、一次性的
  高熵登录码;数据库只存摘要,成功消费与创建会话在同一事务内。
- 登录后通过 WebAuthn 注册平台 Passkey,要求用户验证和 discoverable credential。
  后续免用户名登录。Mac 的 Touch ID/系统验证由用户本人完成;标准 Apple Passkey
  可由 iCloud Keychain 同步,不声称硬绑定到某一台 Mac。
- 会话使用独立的 host-only `__Host-` cookie,Secure/HttpOnly/SameSite=Strict,
  服务器存摘要,退出撤销。挑战绑定浏览器、用途和短时有效期,完成时一次性消费。
- 所有认证写接口校验精确 Origin,限制请求体并对登录失败限速。登录码不放 URL,
  不写访问日志。认证状态使用独立 SQLite,不触碰用量事件和设备凭据。
- 站点会话授权读取和 Passkey 管理,不授权设备 enrollment、合并、撤销或定价写入;
  原有 read/admin/per-device bearer 范围不扩大。Passkey 注册要求近期认证。
- Caddy 仅在应用认证与验收通过后去掉此站点的 forward_auth 和 read token 注入,
  保留 ingest、mesh、桌面端链路以及其他站点的 Authelia。

## 验收与回滚

测试覆盖登录码到期/单次消费、会话撤销、Origin/挑战/用途校验、无会话拒读,
以及原有 bearer 和 v2 ingest 不回归。真实浏览器验收登录、注册、退出、Passkey
登录与未授权拒读;实际 Mac Touch ID 需要用户完成。部署前备份二进制、配置和
Caddy 站点配置,数据库使用 SQLite 一致性备份。失败时恢复旧配置和二进制。
