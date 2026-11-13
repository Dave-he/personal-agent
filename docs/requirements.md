# 需求实现对照表

对照 `MVP 需求清单` 的编号，说明每条需求在本仓库中的落地位置。标注为"骨架"的表示流程完整、外部依赖需自行接入。

## 接入与账号（A）

| 编号 | 状态 | 落地位置 |
|---|---|---|
| A-01 账号与设备 | 骨架 | `api.userID`（请求头识别，上线需替换） |
| A-02 通知通道 | 骨架 | `scheduler.Notifier` 钩子 |
| A-03 授权中心 | 部分 | `POST /api/system/pause`、审批拒绝；按平台撤销需接入凭据库 |
| A-04 告知与同意 | 文档 | `docs/security.md`、`SECURITY.md` |
| A-05 信任等级展示 | 有 | `domain.User.TrustLevel`、`policy.AutoAllowedTier` |

## 目标与任务（G）

| 编号 | 状态 | 落地位置 |
|---|---|---|
| G-01 自然语言建目标 | 有 | `planner.Extractor.Parse` |
| G-02 目标卡片确认 | 有 | `POST /api/goals` 返回结构体，前端展示后确认 |
| G-03 三类任务 | 有 | `Task.Schedule`（once / interval，持续监控用短间隔） |
| G-04 任务列表 | 有 | `GET /api/tasks` |
| G-05 任务详情 | 有 | `GET /api/tasks/{id}`，含 run 与 step 轨迹 |
| G-06 任务控制 | 有 | `POST /api/tasks/{id}/runs`、`/terminate` |
| G-07 任务编辑 | 骨架 | 需新增 PATCH 接口，指纹需重算 |
| G-08 目标去重 | 有 | `planner.Fingerprint`、`Store.GoalByFingerprint` |
| G-09 完成摘要 | 部分 | `run.Reason` + `notifier` |

## 约束与记忆（C）

| 编号 | 状态 | 落地位置 |
|---|---|---|
| C-01 基础约束 | 有 | `POST /api/constraints` |
| C-02 推断约束建议态 | 有 | 推断约束 `Active=false`，不参与拦截 |
| C-03 执行前拦截 | 有 | `verifier` 边界规则族 |
| C-04 冲突提醒 | 骨架 | 需在创建目标时比对已有约束 |
| C-05 约束溯源 | 有 | `Constraint.Source`、`Confidence` |

## 监控场景（S1）与整理推荐（S2）

| 编号 | 状态 | 说明 |
|---|---|---|
| S1-01 价格监控 | 有 | 模拟执行器可直接跑；真实站点需实现 `Executor` |
| S1-02 库存 | 骨架 | 证据字段已支持 `stock` |
| S1-03 订阅巡检 | 骨架 | `subscription_scan` 规划路径已存在 |
| S1-04 到期提醒 | 骨架 | `expiry_remind` 规划路径已存在 |
| S1-05 账单异动 | 部分 | 异常规则族可复用，需接入账单源 |
| S1-06 优惠汇总 | - | 需真实数据源 |
| S1-07 售后与物流 | - | 二期 |
| S2-01/02/03 | 骨架 | 规划器可扩展场景类型，动作保持 `read` |
| S2-04 截止日看板 | - | 二期 |

## 审批（P）

| 编号 | 状态 | 落地位置 |
|---|---|---|
| P-01 审批卡 | 有 | `domain.Approval`（内容、金额、对象、拒绝后果） |
| P-02 一次性令牌 | 有 | 签名 + nonce + 15 分钟 TTL |
| P-03 审批记录 | 有 | 审计链 + `Store.ApprovalsByUser` |
| P-04 审批节流 | - | 需按用户统计频次，二期 |

## 安全与审计（K）

| 编号 | 状态 | 落地位置 |
|---|---|---|
| K-01 凭据托管 | 设计 | 凭据不出网关；本仓库不存储明文凭据 |
| K-02 代执行与签名 | 有 | `gateway.Sentinel` + `executor.Verify` |
| K-03 权限三档路由 | 有 | `policy.RiskOf` + `Sentinel.Authorize` |
| K-04 审计与回放 | 有 | `store.AppendAudit` 哈希链、`GET /api/audit` |
| K-05 紧急关停 | 有 | `POST /api/system/pause` |
| K-06 日志脱敏 | 有 | 审计只记录对象与原因码，不记录凭据 |
| K-07 导出与删除 | - | 需新增数据生命周期接口，二期 |

## 校验（V）

| 编号 | 状态 | 落地位置 |
|---|---|---|
| V-01 一致性 | 有 | 证据缺 source / 缺字段的处理 |
| V-02 边界 | 有 | budget、channel_allow、channel_deny |
| V-03 异常 | 有 | 价格剧变判定为 uncertain |
| V-04 三态判定 | 有 | `domain.Verdict`，无"默认通过"分支 |
| V-05 规则回归集 | 有 | `internal/verifier/verifier_test.go` |

## 观测与商业

| 编号 | 状态 | 落地位置 |
|---|---|---|
| O-01/02 指标与告警 | 部分 | `/metrics`、`Incidents`；告警推送需接外部通道 |
| O-03 三级熔断 | 部分 | 任务级与全局级已实现，场景级需按场景统计 |
| O-04 灰度开关 | 有 | 全局 pause + 任务级终止 |
| O-05 人工接管 | - | 二期 |
| O-06 成本归因 | 有 | `Task.CostCNY`、`Run.CostCNY` |
| B-01 节省量 | - | 需真实价格历史 |
| B-02/03 商业化 | - | 明确不做，避免污染开源版本 |

## 明确不做

自动下单、自动支付、代发消息、签约、多 Agent 协作。这些动作在 `policy` 表中标记为 `forbidden`，**没有代码路径可以执行**。
