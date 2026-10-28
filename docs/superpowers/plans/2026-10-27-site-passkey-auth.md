# 站点认证实施计划

1. 核查现网部署、代理和节点通信,以实时证据纠正文档中的旧拓扑。
2. 加入认证需求及 ADR;先写认证存储/HTTP 安全边界回归测试并确认失败。
3. 使用维护中的 go-webauthn 库实现挑战验证,独立认证数据库、登录码 CLI 和会话。
4. 实现站点登录、Passkey 注册/登录/退出界面,保持 agent 和桌面端 bearer 兼容。
5. 执行 focused tests、完整 make check 和真实浏览器验收,审查 diff 后提交 PR 至 dev。
6. 备份并部署到 aliyun198,仅切换 omni.suool.net 的认证;核对上报和现网页面,
   最后生成用户的一次性登录码。用户在 Mac 上完成系统 Passkey 验证。

完成记录:PR #126 经远端 CI 合入,应用已部署到 198。完整本地 gate、race 检查、
隔离浏览器平台 Passkey 注册/签名拒绝/登录以及公网会话权限验收均通过。
生产库未添加测试 Passkey,实际 Mac 系统验证由用户完成。备份和部署证据见 runbook。
