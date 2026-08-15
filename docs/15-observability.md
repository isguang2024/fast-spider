# 可观测性（0.4.18）

Fast Spider 的日志和指标以低噪音、低基数、可排障为目标。

## 日志

结构化日志允许记录：requestId、machineId、capability、action、jobId、artifactId、AI harness providerId、routingMode、脱敏 route provider ID、状态、耗时和错误码。不要记录密码、Connection Token、Device Key 私钥、OAuth/API 明文 Token、Cookie、Prompt/文件正文、CC Switch raw settings/meta 或完整 endpoint。

0.3.x 起不再记录目录授权 ID。绝对路径只在确有排障价值时进入受控日志字段；0.4.0 的 CC Switch/Claude RouteSnapshot 也只保留脱敏、有界事实，默认不把用户路径、Prompt 或上游秘密写进长期普通日志。

## 请求级轻量 Timing

每次 capability 由 Hub 生成 `requestId + traceId` 并透传 Node/Job。响应仅带紧凑的实测 timing：通用层为 `nodeExecutionMs/hubPreDispatchMs/nodeRoundTripMs/hubTotalMs`；Job 为 `queueMs/runMs` 与时间戳；搜索为 primary/fallback/total；Browser 为 startup/operation/queue/total；Agent readiness 为各层 elapsed。无法准确测量的字段不伪造。

这些 timing 默认不落长期日志、不创建 span/collector/外部时序库。管理端“诊断”页只在人工刷新时显示 Agent/Browser readiness 和 WSL 可用性，不发 Prompt、不创建 Session、不高频轮询。

## MCP 调用诊断

Hub 使用 MCP SDK 正式 Receiving Middleware 观察 `initialize`、`tools/list` 与 `tools/call`；0.4.17 同时在 Bearer Token 验证成功后仅更新该 Owner 的“最近已认证 MCP HTTP 请求时间”。每个 Owner 只维护一个进程内快照和最近 64 条有界 ring，不写数据库或磁盘日志；Hub 重启后自然清空。HTTP 到达时间不伪造成 MCP method event，ring 仍只保存 initialize/tools/list/tools/call。记录字段仅包括事件时间、方法、`tools/call` 工具名、`success|failure`、稳定错误分类、归一化客户端类型、Server/Guide 版本和 Server 启动时间。

严格不读取或保存 Authorization Header、Token、Cookie、完整请求体、工具 arguments、Prompt、本机路径、文件正文、IP、原始 User-Agent 或原始错误堆栈。客户端类型只从 MCP `clientInfo.name` 或请求头做有限归一化，原值不进入快照。已登录 Web 后台通过只读 `/app/api/mcp-diagnostics` 加载当前 Owner 快照；未登录返回 401。页面进入时读取一次，并只提供手动刷新，不做轮询。

诊断含义：

- 最近 MCP 请求时间不变化：如果用户此刻明确在 ChatGPT 中重试 FastSpider_FS，但该时间仍不变化，说明该会话没有把请求发送到 Hub；优先用唯一标记 `fsprobe` 过滤发现 `machine_list`，而不是重启 Node 或重新授权。
- 有最近 MCP 请求、无 initialize：请求已经通过 OAuth 到达 Hub，但尚未形成有效 MCP initialize；检查客户端协议/请求形状。
- 有 initialize、无 tools/list：初始化后未进入工具发现。
- 有 tools/list、无 tools/call：模型尚未选择工具。
- tools/call 失败：调用已到 Hub，按稳定错误分类检查 Hub、Node、参数或运行时。
- tools/call 成功：最近一次工具调用已完成；异步 shell/build 仍必须继续用 `job_watch` 验证终态。

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

0.4.18 的清理失败必须保留可重试事实：Artifact 文件删除进入持久队列并记录尝试次数，Presentation 初始化/清理失败保持不可用状态，Release/staging 失败保留现场。运维诊断只输出阶段、计数和稳定错误分类，不输出文件正文、Token 或密钥。
