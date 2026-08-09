# ADR 0002：Node 传输（0.3.0 当前有效部分）

## 状态

已接受。早期协议中的目录授权字段已由 0.3.0 的 Machine-only 请求取代。

## 决策

Node 默认不开放局域网或公网监听端口，只主动通过 HTTPS/WSS 443 连接 Hub。协议使用版本化 JSON 控制消息与二进制 Artifact 分块，独立于 MCP。

## 连接要求

- TLS、设备凭据、Hub 信任、challenge 签名、generation 和心跳在进入消息循环前完成。
- 一个 Machine 默认只允许一个 active connection，新 generation 替换旧连接。
- 心跳是连接粒度，不为单个文件、路径或路由建立探活。
- 控制消息、事件、取消、重连对账和 Artifact 上传都受大小、inflight、发送队列和 deadline 限制。
- 不宣称恰好一次网络传输或执行；通过 requestId、idempotencyKey、sequence 和状态对账实现业务幂等。

## 目标字段

Capability Request 只以 `machineId` 选择远程 Node；文件、Shell、Build、Git 和 AI 的绝对路径放入能力参数，Node 按当前 OS 用户权限和平台规则处理。传输层不把路径转换成相对位置，也不维护目录授权字段。

## 取舍

WSS 通过常见反向代理，兼容性和双向事件流最好。HTTP/2、gRPC、QUIC 只有真实性能或部署需求出现时再单独评估。
