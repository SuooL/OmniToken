# ADR-0017 速度分解语义：共享时间轴上的可加和贡献

状态：已采纳（2026-07-30，与用户确认）。补充 ADR-0009，不改变其生成区间与并集
口径。

## 背景

ADR-0009 正确地把整机生成吞吐定义为输出 token 除以所有生成区间的墙钟并集。
它可以区分并发与错峰：

- 两个会话完全并发 10 秒，各输出 1,000 tokens：并集 10 秒，总吞吐 200 tok/s；
- 两个会话前后各运行 10 秒，各输出 1,000 tokens：并集 20 秒，总吞吐 100 tok/s。

现有 `/api/v1/live` 同时返回整机 `speed.tps` 和每会话 `sessions[].tps`，但两者
分母不同：整机使用全局并集，每个会话使用自己的区间并集。因此会话行表达的是
“该会话运行时自身多快”，不是“它对整机窗口贡献了多少”。除非各会话拥有完全相同
的活跃区间，否则把这些行相加不会得到整机总吞吐。

这在数学上没有破坏 ADR-0009，却破坏了产品的可核算性：一个标为“当前贡献”的列表
理应能够相加还原标题总数。

## 决策

### 1. 保留整机并集吞吐

```text
aggregate_tps = measured_output_tokens(all)
              / union_ms(all measured generation intervals) * 1000
```

它回答“至少一个会话正在生成时，整台机器平均每秒输出多少 token”。空闲时间仍由
burn / active ratio 表达，不混入这个分母。

### 2. 新增共享分母的贡献速度

所有用于解释整机总计的来源、设备、模型和会话行都使用同一个全局并集分母：

```text
contribution_tps(group) = measured_output_tokens(group)
                        / union_ms(all measured generation intervals) * 1000
```

因此在同一统计区间内必须满足：

```text
aggregate_tps
  = Σ source contribution_tps
  = Σ device contribution_tps
  = Σ model contribution_tps
  = Σ session contribution_tps
```

计算与核算使用未舍入值；客户端只在最后展示时舍入。

### 3. 原生速度单独命名且不可相加

```text
native_tps(group) = measured_output_tokens(group)
                  / union_ms(group generation intervals) * 1000
```

它回答“这个组自己运行时多快”。不同组的分母不同，不能进入总计、贡献排名或任何
暗示可以相加的列表。兼容期内旧 `sessions[].tps` 保留原生速度语义，同时新增
`sessions[].contribution_tps`；新 API 显式使用 `native_tps`。

### 4. 三种时间关系

假设 A、B 各输出 1,000 tokens，原生速度均为 100 tok/s：

| 关系 | 全局并集 | A 贡献 | B 贡献 | 总吞吐 |
|---|---:|---:|---:|---:|
| 完全并发 | 10s | 100 | 100 | 200 |
| 完全错峰 | 20s | 50 | 50 | 100 |
| 部分重叠、全局并集 15s | 15s | 66.67 | 66.67 | 133.33 |

并集不是把会话速度强行平均，而是在共享墙钟时间轴上保留真实重叠关系。

### 5. 覆盖率必须显式

只有带可靠生成区间的事件才能进入速度分子和分母。Codex rollout 当前有 70% 的记录
使用批量回放时间戳，不能可靠生成 `gen_ms`（ADR-0009），所以：

- Codex 的五小时和今日**用量**继续完整统计；
- Codex 的**速度**标为 unmeasured / unavailable；
- 未测来源不冒充 0，也不静默进入已测总吞吐；
- API 返回 `measured_sources` 与 `unmeasured_sources`。

## 后果

- 总吞吐仍与 ADR-0009 和既有历史速度可比；
- Web 与 menu bar 的来源/会话贡献可以严格还原总数；
- 同一行可能同时有 `contribution_tps` 与 `native_tps`，界面必须清楚标注角色；
- 需要新增顺序、并发、部分重叠和未测来源回归测试；
- 若未来取得可靠 Codex 生成区间，只需把它从 unmeasured 移入 measured，不改变公式。
