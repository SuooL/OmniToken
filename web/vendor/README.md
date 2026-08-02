# Vendored 前端库

预构建产物直接入库,不引入构建链(ADR-0010)。浏览器 `<script>` 直接加载,
`go:embed` 打进二进制,离线可用。

| 文件 | 来源 | 版本 | 许可 |
|---|---|---|---|
| `echarts.min.js` | https://registry.npmjs.org/echarts/-/echarts-6.1.0.tgz | 6.1.0 | Apache-2.0 |

`echarts.min.js` 取自 npm tarball 的 `dist/echarts.min.js`,tarball 的
sha512 已核对为 registry 声明的 `q0yaFPggC9FUdsWH4blavRWFmxdrIodbkoKNAjJudAI6CA9gNPxHtV2RcZNEepZVlk4yvBYkOkbk6HIVpIyHZA==`。

取完整包而非 `common`/`simple`:日历热力图不在精简包里。

## 升级步骤

1. `curl -sL -o /tmp/e.tgz https://registry.npmjs.org/echarts/-/echarts-<版本>.tgz`
2. 用 registry 的 `dist.integrity` 核对 sha512,不一致就停下
3. 解包后覆盖 `echarts.min.js`,同步更新本表与 LICENSE
