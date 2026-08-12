# 可观测性（0.4.11）

Fast Spider 的日志和指标以低噪音、低基数、可排障为目标。

## 日志

结构化日志允许记录：requestId、machineId、capability、action、jobId、artifactId、AI harness providerId、routingMode、脱敏 route provider ID、状态、耗时和错误码。不要记录密码、Connection Token、Device Key 私钥、OAuth/API 明文 Token、Cookie、Prompt/文件正文、CC Switch raw settings/meta 或完整 endpoint。

0.3.x 起不再记录目录授权 ID。绝对路径只在确有排障价值时进入受控日志字段；0.4.0 的 CC Switch/Claude RouteSnapshot 也只保留脱敏、有界事实，默认不把用户路径、Prompt 或上游秘密写进长期普通日志。

## 请求级轻量 Timing

每次 capability 由 Hub 生成 `requestId + traceId` 并透传 Node/Job。响应仅带紧凑的实测 timing：通用层为 `nodeExecutionMs/hubPreDispatchMs/nodeRoundTripMs/hubTotalMs`；Job 为 `queueMs/runMs` 与时间戳；搜索为 primary/fallback/total；Browser 为 startup/operation/queue/total；Agent readiness 为各层 elapsed。无法准确测量的字段不伪造。

这些 timing 默认不落长期日志、不创建 span/collector/外部时序库。管理端“诊断”页只在人工刷新时显示 Agent/Browser readiness 和 WSL 可用性，不发 Prompt、不创建 Session、不高频轮询。

## 指标

指标 label 只使用固定低基数字段，如 route_group、capability、result。不要把完整 URL、machineId、jobId、artifactId 或绝对路径作为 label。

## 健康

- `/livez`：进程存活。
- `/readyz`：数据库等关键依赖可用。
- Node 在线状态来自真实 WSS generation/心跳，不通过高频独立探活制造请求风暴。

## 审计

需要审计：Owner 登录/密码变更、OAuth 授权/撤销、Connection Token 创建/撤销/删除、Machine 备注/撤销/删除、远程高风险能力结果和 Artifact 生命周期。

个人版不记录不存在的逐目录授权生命周期。

## 清理

维护任务分批清理过期 Web/OAuth/连接令牌、旧审计、过期 Artifact/上传和临时状态，避免长期运维无限堆日志/文件。
