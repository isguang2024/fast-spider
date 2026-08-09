# 可观测性（0.3.x）

Fast Spider 的日志和指标以低噪音、低基数、可排障为目标。

## 日志

结构化日志允许记录：requestId、machineId、capability、action、jobId、artifactId、状态、耗时和错误码。不要记录密码、Connection Token、Device Key 私钥、OAuth 明文 Token、Cookie 或文件正文。

0.3.x 不再记录目录授权 ID。绝对路径只在确有排障价值时进入受控日志字段，默认避免把用户文件路径写进长期普通日志。

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
