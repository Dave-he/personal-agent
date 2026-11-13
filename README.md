# Personal Agent

一个**只读优先、关键动作人工确认、全程可审计**的个人生活助理参考实现。

它不是"全自动帮你下单"的 Agent。它盯价格、盯账单、盯到期日，把结论推给你；真正会改变世界状态的动作，一律要求人类点确认。

> 设计前提：**模型一定会出错**。所以规划、授权、执行、校验被拆成四层，模型只输出意图，不持有凭据，也不决定"是否完成"。

---

## 快速开始

```bash
# 需要 Go 1.23+
make run          # 或 go run ./cmd/agentd
# 打开 http://localhost:8080
```

创建第一个目标（在控制台里，或用 API）：

```bash
curl -X POST localhost:8080/api/goals \
  -H 'Content-Type: application/json' \
  -d '{"text":"盯着这款耳机，降到 800 以内就提醒我","interval_s":60}'
```

查看执行结果与审计链：

```bash
curl localhost:8080/api/state
curl localhost:8080/api/audit/verify
```

没有配置真实数据源时，系统使用模拟执行器（SimExecutor），整个"计划 → 授权 → 执行 → 校验 → 落库"链路仍然完整跑通。

---

## 六层架构

```
接入层        控制台 / API
   ↓
编排层        目标解析 · 约束编译 · 任务调度
   ↓
规划层        只产出 intent，不接触凭据
   ↓
Sentinel     唯一持有凭据，负责签名、审批、审计
   ↓
执行层        隔离沙箱，只认签名，不判断对错
   ↓
事实与校验    规则引擎三态判定，不确定转人工
```

三条不可绕过的规则：

1. **模型侧零凭据** —— 密码、Cookie、支付令牌永远不进入模型上下文。
2. **执行侧零决策** —— 执行器只执行带签名、带时效、单次有效的指令。
3. **校验侧零依赖模型** —— 是否完成由规则引擎基于证据判定，模型的"我做好了"只作线索。

详见 [`docs/architecture.md`](docs/architecture.md)。

---

## 目录结构

```
cmd/agentd/        进程入口与依赖装配
internal/domain/   数据模型，无外部依赖
internal/store/    内存状态 + 快照持久化 + 审计哈希链
internal/gateway/  Sentinel：权限路由、意图签名、审批
internal/executor/ 模拟执行器 / HTTP 执行器（含 SSRF 防护）
internal/verifier/ 一致性、边界、异常三类规则，三态判定
internal/planner/  自然语言目标解析 + 步骤计划
internal/scheduler/五步主循环、配额、退避、熔断
internal/api/      HTTP 接口
web/               内嵌控制台（纯静态）
```

---

## 接口一览

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/goals` | 自然语言创建目标与任务（带指纹去重） |
| GET | `/api/tasks` | 任务列表 |
| GET | `/api/tasks/{id}` | 任务详情与执行历史 |
| POST | `/api/tasks/{id}/runs` | 立即执行一次 |
| POST | `/api/tasks/{id}/terminate` | 终止任务 |
| GET | `/api/approvals` | 待确认动作 |
| POST | `/api/approvals/{id}/grant` | 同意（签发一次性令牌） |
| POST | `/api/approvals/{id}/reject` | 拒绝 |
| POST | `/api/constraints` | 添加约束（推断约束默认不生效） |
| GET | `/api/audit` | 审计链，可按 `task_id` 过滤 |
| GET | `/api/audit/verify` | 校验哈希链完整性 |
| POST | `/api/system/pause` | 全局熔断开关 |
| GET | `/metrics` | Prometheus 指标 |

身份：参考实现通过 `X-User-Id` 请求头识别用户，**上线前必须替换为真实鉴权**。

---

## 已实现的需求

对应 `MVP 需求清单` 的模块编号，完整映射见 [`docs/requirements.md`](docs/requirements.md)。

- **A 接入与账号**：账号绑定骨架、通知钩子、授权撤销（令牌即时失效）、全局关停
- **G 目标与任务**：自然语言建目标、结构化卡片、三类调度、任务列表与详情、手动执行/终止、指纹去重、完成摘要
- **C 约束与记忆**：结构化约束、推断约束默认建议态、执行前强制拦截
- **S1 监控场景**：价格监控、订阅巡检、到期提醒（真实平台适配需接入执行器）
- **S2 整理与推荐**：材料清单、预约方案、客服话术的生成与跳转（不代提交）
- **P 审批**：审批卡、一次性令牌、审批记录、内容变更失效
- **K 安全与审计**：凭据不出网关、签名执行、三档权限路由、哈希链审计、日志脱敏字段、关停
- **V 校验**：一致性、边界、异常、三态判定、规则可回归测试
- **O 观测**：指标、熔断、灰度开关、成本归因字段
- **B 商业**：节省量字段与返佣链路预留（不结算）

明确未做：自动下单、自动支付、代发消息、签约、多 Agent 协作。这些在 `policy` 表中被标为 `forbidden`，**代码层面不可达**。

---

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `ADDR` | `:8080` | 监听地址 |
| `DATA_PATH` | `data/state.json` | 状态快照路径，置空则纯内存 |
| `TICK` | `15s` | 调度扫描间隔 |
| `SIGN_KEY` | 随机生成 | HMAC 签名密钥；未设置时重启后签名失效 |

---

## 测试

```bash
make test
```

覆盖：阈值判定、越界拦截、异常判定、签名与重放、审批内容变更失效、熔断、成本上限、去重指纹、审计链校验。

---

## 基于既有 Code Agent 继续演进

本仓库不是某个 Code Agent 的 fork，而是一套独立、可运行的最小内核——它优化的目标是"个人事务"，不是"写代码"。若你想基于手上已有的 Code Agent（例如 ZCode 这类）改造，接入点只有两个：

| 想复用的能力 | 替换位置 | 约束 |
|---|---|---|
| 任务理解与规划 | `planner.Planner` | 只能产出 `domain.Intent`，不得接触凭据 |
| 工具调用 / 网页操作 | `executor.Executor` | 必须校验签名，不得自行决定"要不要做" |

其余部分（权限路由、审批、签名、审计、校验、熔断）建议保留——这正是把"能跑"变成"敢用"的部分。改造时请先跑通现有测试，再替换实现。

## 参与共建

- 先读 [`CONTRIBUTING.md`](CONTRIBUTING.md)：零外部依赖、安全红线、PR 要求
- 再看 [`ROADMAP.md`](ROADMAP.md)：真实平台适配器是最缺人的方向
- 安全与隐私设计见 [`docs/security.md`](docs/security.md)，漏洞请走私下渠道（[`SECURITY.md`](SECURITY.md)）
- Issue 里的 `good first issue` 标签适合第一次参与

## 边界与提醒

- 本项目是**参考实现**，不是可直接商用的产品：缺少真实鉴权、多租户隔离、真实平台适配与 LLM 规划器接入。
- 真实平台适配器需自行实现 `executor.Executor` 接口，并遵守目标站点的条款与 robots 规则。
- 浏览器自动化路径存在被平台风控阻断的风险，架构上的对策是**官方接口优先、自动化降级**。
- 没有交易返佣或订阅收入的场景，不建议开启自动执行：单任务成本虽低，重试与验证会放大 3–5 倍。

## 许可

MIT
