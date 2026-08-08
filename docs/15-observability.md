# 15 可观测性

## 1. 目标

Fast Spider 的可观测性用于回答：

- Hub、Node 和关键依赖是否健康？
- 一次请求在哪一层被拒绝、排队、执行或丢失？
- 是否发生重复执行、事件缺口、取消不完整或权限异常？
- 连接、事件、日志、Artifact 和清理是否造成资源持续增长？
- 哪些数据可以安全记录，哪些必须脱敏或不记录？

MVP 保持简单：结构化日志、内置指标端点、Job/Event/Audit 数据和可选 OpenTelemetry Export。运行 Fast Spider 不强制部署独立 Collector、Prometheus、Loki 或复杂日志平台。

## 2. 四类信号

| 信号 | 用途 | 权威性 |
|---|---|---|
| Health | 当前进程/依赖能否服务 | 瞬时状态 |
| Metrics | 趋势、容量、SLO、告警 | 聚合事实 |
| Logs | 诊断上下文 | 非业务权威 |
| Traces | 跨 Adapter/Hub/Node/Capability 延迟链 | 采样诊断 |
| Audit | 谁在何时对何资源做了什么 | 安全/合规权威 |
| Job/Event | 执行状态和用户可见输出 | 业务权威 |

日志不能替代 Job 状态或审计；指标不能包含用户内容。

## 3. 关联字段

在不泄露秘密的前提下，相关记录使用：

```text
requestId
traceId
jobId
sessionId/runId（适用时）
machineId（日志可使用短 hash）
workspaceId（日志可使用短 hash）
clientId/userId（受保护日志）
capability/action
connectionId/generation
```

普通公开错误只返回 requestId/traceId。内部日志不打印完整 Token、私钥、绝对路径、命令秘密、环境变量或 Provider 原始凭据。

## 4. 结构化日志

建议 JSON 字段：

```json
{
  "timestamp": "2026-08-08T10:00:00Z",
  "level": "INFO",
  "component": "hub.jobs",
  "event": "job_state_changed",
  "requestId": "req_...",
  "traceId": "trace_...",
  "jobId": "job_...",
  "capability": "shell.process",
  "action": "run",
  "from": "accepted",
  "to": "running",
  "durationMs": 12
}
```

### 日志级别

- `ERROR`：需要处理的失败，如数据库不可写、签名失败、状态冲突。
- `WARN`：降级或异常但服务仍可继续，如事件缺口、取消不完整、磁盘高水位。
- `INFO`：有限的生命周期摘要，如启动、连接、Job 终态、策略变更。
- `DEBUG`：诊断细节，默认关闭并有自动过期。
- 不使用高频 `TRACE` 作为生产常态。

### 去噪

- Heartbeat 成功不逐条 INFO；仅指标统计，状态变化才记录。
- stdout/stderr 不复制到 Hub 普通日志。
- 同类连接失败、拒绝和清理错误聚合/限频。
- 重试只在首次、状态变化和最终失败时记录摘要。
- 清理任务每轮输出一条汇总，而非每条删除一行。

## 5. 敏感数据分类

### 永不记录

- Access/Refresh/enrollment/local tokens。
- 设备私钥、Provider API Key、密码和 Cookie。
- 完整环境变量。
- Authorization/Cookie Header。
- 信用卡、安全码、密码字段值。

### 默认不记录

- 文件正文、完整 prompt、完整命令输出。
- Node 绝对路径、用户主目录和 Git credential URL。
- 浏览器 DOM/页面正文、截图内容。

### 可记录摘要

- 内容 hash、长度、类型。
- 相对路径（仅受保护 Node 日志；Hub 可记录路径 hash/受限相对路径摘要）。
- 命令 executable 和参数风险摘要；敏感参数脱敏。
- Prompt hash、字符数、Provider/model。

脱敏函数集中实现并测试，不能各模块自行用不一致正则处理。

## 6. Hub 指标

### 进程与 HTTP

```text
fastspider_build_info
fastspider_process_uptime_seconds
fastspider_http_requests_total{route_group,method,status_class}
fastspider_http_request_duration_seconds{route_group,method}
fastspider_http_inflight{route_group}
fastspider_rate_limit_rejections_total{route_group,reason}
```

`route_group` 使用固定低基数值，如 mcp/api/artifact/node_ws/console，不使用完整 URL、machineId 或 workspaceId。

### Node 连接

```text
fastspider_nodes_registered{status}
fastspider_node_connections_current
fastspider_node_connection_events_total{event,reason}
fastspider_node_heartbeat_age_seconds_bucket
fastspider_node_reconnects_total{reason}
fastspider_node_send_queue_depth_bucket
fastspider_node_backpressure_total{reason}
```

不为每台 Node 创建永久 label。单机详情从管理 API/数据库查询。

### Job/Event

```text
fastspider_jobs_current{state,capability_group}
fastspider_jobs_created_total{capability_group,action_group,origin}
fastspider_jobs_terminal_total{state,capability_group,error_class}
fastspider_job_queue_wait_seconds{capability_group}
fastspider_job_run_duration_seconds{capability_group,state}
fastspider_job_cancel_duration_seconds{result}
fastspider_job_lost_total{capability_group,reason}
fastspider_events_ingested_total{type}
fastspider_event_sequence_gaps_total{source}
fastspider_event_batch_size
fastspider_event_persist_duration_seconds
```

`error_class` 为 AUTH/POLICY/NODE/WORKSPACE/PATH/PROCESS/GIT/INTERNAL 等固定类别，不用完整 error code 造成高基数。

### 数据库与存储

```text
fastspider_db_query_duration_seconds{operation_group}
fastspider_db_busy_total{operation_group}
fastspider_db_wal_bytes
fastspider_db_last_checkpoint_timestamp_seconds
fastspider_storage_bytes{kind}
fastspider_storage_files{kind}
fastspider_artifact_uploads_current{state}
fastspider_artifact_bytes_total{direction,result}
fastspider_cleanup_last_success_timestamp_seconds{task}
fastspider_cleanup_items_total{task,result}
fastspider_cleanup_bytes_reclaimed_total{task}
fastspider_backup_last_success_timestamp_seconds
fastspider_backup_last_verified_timestamp_seconds
```

### 权限与安全

```text
fastspider_auth_failures_total{mechanism,reason_class}
fastspider_policy_decisions_total{layer,effect,risk}
fastspider_approvals_current{state,risk}
fastspider_device_credential_events_total{event,result}
fastspider_security_events_total{type,severity}
```

安全指标不携带主体 ID；详细调查走 Audit。

## 7. Node 指标

Node 默认不开放网络 metrics 端口。指标通过：

- 本机管理 CLI/UI。
- 有界 heartbeat 摘要。
- 可选只绑定 Local Bridge 的管理接口。

主要指标：

```text
connection state/reconnect count/heartbeat RTT
jobs by resource group and state
job queue wait/run/cancel duration
process tree cancel failures
workspace path guard denials/races
local event buffer bytes and Hub ack lag
recovery-bin/browser-profile/temp bytes
artifact upload backlog
provider/browser runtime availability
update state and last health confirmation
```

Hub 只汇总必要低频指标，不把 Node 变成高频遥测 Agent。

## 8. Trace

OpenTelemetry 用作统一 API 和可选导出，不要求 MVP 部署 Collector。

### Span 层级

```text
external.request
  hub.adapter
  hub.policy
  hub.job.create
  hub.route.dispatch
  node.request.receive
  node.policy
  node.workspace.resolve
  node.capability.execute
  node.artifact.upload (optional)
  hub.event.persist
```

### Span 属性

允许：capability、action、Job state、risk、result class、协议版本、payload size、queue wait。

禁止：Token、文件内容、完整 path、命令输出、prompt、浏览器正文。

### 采样

- 错误、lost、cancel incomplete、安全拒绝：优先保留。
- 正常高频 read/watch：低采样。
- 单 Owner MVP 可配置较高采样，但仍有总量上限。
- Trace Export 失败不能阻塞业务或积累无限内存。

Hub↔Node 传递 traceId 和受控 trace context；不信任 Client 任意注入采样/敏感 baggage。

## 9. Audit

Audit 是独立结构化记录，详见数据模型。要求：

- 管理变更、高风险能力、审批、设备/Workspace/Client 生命周期必须审计。
- Hub decision 与 Node decision 分别记录。
- 审计写入失败时，R3/R4 操作默认 fail closed；R0/R1 可按明确策略降级并发告警。
- Audit 查询分页、权限受限、导出有水印/审计。
- 普通日志清理不能删除 Audit。

## 10. Health 与 Readiness

### `/livez`

只检查进程能响应，快速、无外部递归检查。数据库慢不应让 liveness 导致无限重启。

### `/readyz`

检查：

- 配置已验证。
- Migration 完成。
- SQLite 可执行短读写（或处于明确只读降级）。
- Artifact/upload 目录可访问且未超过硬水位。
- 监听和关键密钥可用。

不检查：

- 每个 Node 在线。
- 每个 Workspace 可访问。
- 每个 Provider/浏览器安装。
- 远程互联网任意依赖。

### Node 状态

Hub UI 将 Node 状态分为 online、busy、suspect、offline、revoked、version_incompatible；不要把没有运行 Job 的 offline 简化为 Hub unhealthy。

## 11. SLO 建议

Phase 8 正式压测前为设计目标：

- Hub 已认证控制 API 月可用性目标 99.5%（单机自托管，不承诺多实例级别）。
- 在线 Node 的只读请求 p95 dispatch 延迟 < 500 ms（不含能力执行）。
- Event 从 Node 到可 watch 的 p95 延迟 < 1 s。
- 设备吊销到拒绝新请求 < 10 s；主动关闭连接应更快。
- 清理任务连续 24 小时失败必须告警。
- 已完成写 Job重复执行率必须为 0；任何事件均为安全事故。

SLO 分母排除 Node 本身离线、用户 Approval 等待和客户端取消，但这些必须清楚显示。

## 12. 告警

### Critical

- 更新签名/信任根异常。
- 重复写执行证据或幂等冲突处理错误。
- Workspace 路径逃逸/竞态命中。
- 数据库不可写且无法进入安全降级。
- Artifact/DB 磁盘 ≥95%。
- 设备身份/sequence payload 冲突。

### High

- 大量 Node 同时断开且 Hub/代理异常。
- cancel incomplete/孤儿进程。
- backup/restore verification 长期失败。
- Audit 写入失败。
- Local Bridge 非法 Origin/认证尝试激增。

### Warning

- 磁盘 ≥70/85%。
- 清理滞后、WAL 异常增长。
- Node/Provider/Browser 版本不兼容。
- Event ack lag 或发送队列持续高。

告警必须有去重、抑制和恢复通知；不能每个失败请求一条告警。

## 13. Dashboard

### Hub Overview

- 版本、uptime、ready、DB/磁盘。
- Node online/suspect/offline。
- Job 状态、队列、终态和错误类别。
- Event lag、Artifact 上传/容量。
- 清理、备份、更新状态。

### Execution

- capability group 的运行量、时延、失败、取消、lost。
- Shell/浏览器/Agent 资源组占用。
- 输出截断、Artifact 转存和取消完整性。

### Security

- auth/policy deny、Approval、凭据轮换/吊销。
- 路径防护、重放、旧 generation、Local Bridge 拒绝。

MVP Web Console 可直接查询内部聚合，不必引入独立 Dashboard 服务。

## 14. 保留与轮换

建议默认：

| 数据 | 保留 |
|---|---|
| Hub/Node INFO 日志 | 7–14 天，按大小轮换 |
| DEBUG | 最长 1 小时或配置的短窗口 |
| Metrics 内存时间序列 | 进程内短期；外部系统由用户决定 |
| Trace | 采样并由外部 Exporter决定；本地不长期堆积 |
| Job Event | 14 天 |
| stdout/stderr | 7 天或转 Artifact |
| Audit | 365 天（可配置） |
| 安全事件 | 与 Audit 等级一致 |

所有保留策略有硬容量限制和分批清理。

## 15. 调试包

管理员可显式生成受限诊断包，包含：

- 版本、平台、配置 Schema 摘要（不含秘密）。
- 最近健康和关键指标快照。
- 指定 request/trace/job 的脱敏日志。
- Migration、清理、备份和更新状态。

默认不含文件内容、Artifact、完整命令输出、绝对路径、Provider Session 或 Token。生成和下载诊断包本身要审计并自动过期。

## 16. 验收

- 从一个 MCP requestId 能定位到 Hub decision、Node decision、Job、Event 和 Result。
- Heartbeat/正常 watch 不产生高频 INFO 日志或高写入。
- machineId/workspaceId/jobId 不成为指标 label 高基数爆炸。
- Token、Cookie、Provider Key、完整 env 和密码字段不会进入日志/Trace。
- 磁盘、WAL、Event lag、清理和备份均可观察。
- lost、cancel incomplete、sequence conflict 和路径防护命中有明确告警。
- 关闭外部 OTel/指标系统不影响 Fast Spider 核心运行。
