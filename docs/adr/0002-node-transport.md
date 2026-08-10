# ADR 0002：Node 传输（0.3.0 当前有效部分）

## 状态

已接受。早期协议中的目录授权字段已由 0.3.0 的 Machine-only 请求取代。

## 决策

Node 默认不开放局域网或公网监听端口，只主动通过 HTTPS/WSS 443 连接 Hub。Hub↔Node 的版本化 JSON Capability Request/Response（包括文件读取内容与精确编辑文本）复用同一条 WSS，协议独立于 MCP。HTTPS 只用于 Machine 登记、设备 Token 和 Artifact/Presentation 等大文件数据面。

## 连接要求

- TLS、设备凭据、Hub 信任、challenge 签名、generation 和心跳在进入消息循环前完成。
- 一个 Machine 默认只允许一个 active connection，新 generation 替换旧连接。
- 心跳是连接粒度，不为单个文件、路径或路由建立探活。
- 控制消息受大小、inflight 和 deadline 限制；大文件不进入 WSS 控制帧，通过 HTTP 数据面上传。
- WSS 不自动重放断线请求：未完整接收的请求不执行；已执行但响应丢失的副作用请求结果为 uncertain，调用方必须先查询状态。
- Job 启动通过 `idempotencyKey` 去重；`file.write/edit` 通过 expected SHA CAS、临时文件、fsync 和原子替换保证并发与文件完整性。
- 当前连接读写失败、主动关闭或被新 generation 替换时，Hub 立即结束该连接全部 in-flight 调用；Node 取消该 session 的同步能力上下文，但不取消已经启动的 Job。

## 目标字段

Capability Request 只以 `machineId` 选择远程 Node；文件、Shell、Build、Git 和 AI 的绝对路径放入能力参数，Node 按当前 OS 用户权限和平台规则处理。传输层不把路径转换成相对位置，也不维护目录授权字段。

## 取舍

WSS 通过常见反向代理，兼容性和双向控制流最好；HTTP 适合可分块、校验和限制大小的大文件数据面。当前保持单体 Hub 和单连接控制面，不引入队列、Redis、新服务或新的协议消息类型。HTTP/2、gRPC、QUIC 只有真实性能或部署需求出现时再单独评估。
