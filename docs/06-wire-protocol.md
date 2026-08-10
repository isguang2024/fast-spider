# Wire Protocol（0.3.x）

Node 与 Hub 使用版本化 WSS 控制协议。设备握手完成后，Capability Request/Response、心跳和连接关闭都复用该 Machine 当前 generation 的同一条 WSS。HTTP 只用于 Machine 登记、设备 Token，以及 Artifact/Presentation 等大文件数据面。

## Capability Request

```json
{
  "messageType": "capability.request",
  "requestId": "req_...",
  "capability": "file.read",
  "action": "read",
  "params": {
    "path": "V:/repos/GitHub/project/README.md",
    "limit": 4096
  },
  "deadline": "2026-08-09T12:00:00Z",
  "timestamp": "2026-08-09T11:59:30Z"
}
```

请求不携带 `machineId` 或目录授权 ID；Machine 由已认证连接会话确定。需要定位本机资源的能力把绝对路径/cwd 放在 `params` 中，Node 以当前 OS 用户权限执行。`file.read` 的 UTF-8 内容直接放在 WSS Response 的 `result` 中；`file.write/edit` 的 `oldText`、`newText` 和 `expectedFileSha256` 直接放在 WSS Request 的 `params` 中。

## 基本消息

协议包含设备握手、心跳、Capability Request/Response、连接关闭与结构化错误。WSS 控制帧受大小和 deadline 限制；超过控制面限制的 Artifact、Presentation、日志或其他大文件内容使用受鉴权的 HTTP 数据面传输，不新增另一套 Capability 控制协议。

## 幂等与超时

每个请求携带 deadline，Job 还有自己的 timeout。WSS 不提供断线自动重放：

- 请求帧在 Node 完整接收前断开，不执行该请求。
- Node 已执行请求但 Response 丢失时，调用结果为 uncertain；Hub 返回 `CONNECTION_LOST`，不会自动重发。
- 只读/查询动作可标记 `retryable=true`；`file.write/edit`、Shell/Build、Git 写入、浏览器动作、Agent send 等副作用动作标记为 `retryable=false`，调用方必须先重新读取或查询状态。
- Job 启动依赖 `idempotencyKey`；Node 保存有限的幂等结果并拒绝同 key 不同参数。已启动 Job 不绑定 WSS session。
- `file.write/edit` 依赖 expected SHA CAS，并通过临时文件、fsync 和原子替换避免半文件。

## 错误

相对路径用于要求本机路径的能力时返回 `ABSOLUTE_PATH_REQUIRED`。连接在响应前丢失返回 `CONNECTION_LOST`（HTTP 适配层为 503）；调用 deadline 到期返回 `DEADLINE_EXCEEDED`（HTTP 适配层为 504）。不存在旧的目录授权不存在/禁用/越界错误。
