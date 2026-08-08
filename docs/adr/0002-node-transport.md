# ADR 0002：Hub 与 Node 传输

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：Hub↔Node 长连接和应用层消息

## 背景

Node 默认不开放任何局域网或公网监听端口，只主动通过 443 连接德国 Hub。传输需要通过常见反向代理，支持双向请求、日志/Event 流、取消、心跳、断线恢复和二进制 Artifact。核心协议必须独立于 MCP。

候选包括 WebSocket、HTTP/2、gRPC 和 QUIC。MVP 是单 Hub、少量 Node 的自托管系统，优先简单、可调试和可运营。

## 决策驱动因素

- 出站 443 和常见代理兼容。
- 双向、低延迟、长连接。
- Go 生态和跨平台可靠性。
- 可观察、可抓取和可复现的协议。
- 至少一次传输、幂等、重连和背压可由应用层明确控制。
- 不引入独立代理、服务网格或消息总线。

## 考虑的方案

### 方案 A：WSS + JSON 控制 + WebSocket 二进制帧

优点：

- 443/反向代理兼容成熟。
- 浏览器之外的 Go Client/Server 也易实现。
- 控制消息可读、便于诊断和 Golden Test。
- 同一连接可双向发送请求、事件、取消和心跳。
- 二进制帧避免大数据 base64。

缺点：

- 应用层需要实现请求关联、顺序、ack、恢复、流控和多路复用。
- 慢消费者和大日志必须严格限制。

### 方案 B：HTTP/2/gRPC 双向流

优点：多路复用、流控、代码生成和成熟 RPC 语义。

缺点：代理和浏览器/Adapter 边界更复杂；调试和版本治理较重；容易把内部 RPC 与核心业务协议绑定。

### 方案 C：QUIC/HTTP/3

优点：弱网连接迁移和多路复用能力强。

缺点：UDP 可达性、代理、运维和实现复杂度不适合 MVP。

## 决策

MVP 选择 **TLS 上的 WebSocket（WSS）**：

- 一个 Node 默认保持一个认证 active connection。
- JSON 文本帧只承载有上限的控制消息。
- WebSocket 二进制帧承载 Artifact chunk 等大数据。
- FSWP 作为独立、版本化应用层协议；WebSocket 帧不等同于 MCP 请求。
- 传输语义为至少一次；通过 requestId、idempotencyKey、Job Event sequence、chunk offset/hash 去重。
- 不宣称恰好一次网络传输或执行。
- 心跳是单连接级别，不为每个 Workspace/路由创建探活。
- 默认关闭 permessage-deflate；以后只有在安全和性能测试通过后按内容类型协商。

首选库候选为 `coder/websocket`，但在 Phase 1 技术验证后锁定版本；公共协议不依赖其私有类型。

## 初始限额

- 控制消息最大 1 MiB。
- Event payload 最大 64 KiB。
- 二进制 chunk 默认 1 MiB，可协商 64 KiB–4 MiB。
- 每 Node inflight、发送队列字节数和资源组并发均为有界配置。
- 心跳默认 30 秒并有抖动；连续多个周期无有效消息进入 suspect/offline。
- stdout/stderr 超限转 Artifact，不无限占用连接内存。

具体数值经压测调整，但拒绝码、协商字段和有界原则保持稳定。

## 连接所有权

- Hub connection registry 以 machineId 为键并保存 generation。
- 新连接认证和协商完成后才能替换旧连接。
- 旧 generation 的 Event、Result 和 ack 一律拒绝。
- Node 重连先做 Job/Event 对账，再接收新请求。
- `dispatched` 未确认的有副作用请求不能盲目重发为新执行。

## 安全

- TLS 1.2+，优先 1.3；严格验证 Hub 身份。
- 设备凭据、nonce challenge、machineId 和 connection generation 绑定。
- WSS Origin/Host/代理 Header 按入口类型验证。
- JSON 深度、字段、字符串和数组有硬限制。
- 二进制帧有固定头、长度、offset、CRC32C，完整对象使用 SHA-256。
- 解压前后大小受限，防止压缩炸弹。

## 后果

### 正面

- 最简单的跨网络、跨平台双向通道。
- 便于开发、诊断和反向代理部署。
- 不阻塞未来替换底层 transport。

### 负面

- 需要自行实现可靠的应用层状态、背压和恢复。
- 单连接多路复用需要谨慎队列优先级，不能让日志阻塞取消/控制消息。

## 退出策略

FSWP 的 Request/Job/Event/Artifact 模型与 transport 解耦。未来切换 HTTP/2、gRPC 或 QUIC 时：

- machineId、workspaceId、idempotency、状态和事件语义不变。
- 新增 transport Adapter 和协商，不在 Capability 中引入 transport 判断。
- 只有真实多实例、弱网或吞吐需求证明收益后才迁移。

## 相关文档

- [线路协议](../06-wire-protocol.md)
- [Job 与 Event](../07-job-and-event-model.md)
- [部署与运维](../14-deployment-and-operations.md)
