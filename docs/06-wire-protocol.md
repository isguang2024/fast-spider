# Wire Protocol（0.4.14）

Node 与 Hub 使用版本化 WSS 控制协议。设备握手完成后，Capability Request/Response、心跳和连接关闭都复用该 Machine 当前 generation 的同一条 WSS。HTTP 只用于 Machine 登记、设备 Token，以及 Artifact/Presentation 等大文件数据面。

## Capability Request

```json
{
  "messageType": "capability.request",
  "requestId": "req_...",
  "traceId": "tr_...",
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

Hub 为每次调用生成 `requestId + traceId` 并透传 Node、Job 与响应。请求不携带 `machineId` 或目录授权 ID；Machine 由已认证连接会话确定。需要定位本机资源的能力把绝对路径/cwd 放在 `params` 中，Node 以当前 OS 用户权限执行。`file.read` 的 UTF-8 内容直接放在 WSS Response 的 `result` 中；`file.write` 的编辑文本只存在于 Request，mutation Response 不回显正文。

## 基本消息

协议包含设备握手、心跳、Capability Request/Response、连接关闭与结构化错误。Node heartbeat `status` 真实反映 `ready|busy`；Hub heartbeat ACK 可选携带 `updatePushId + updateVersion` 作为轻量 release notice。Release notice 不携带下载地址、凭据或可执行内容，Node 仍从固定 Hub Release API 获取签名 manifest 和更新包。WSS 控制帧受大小和 deadline 限制；超过控制面限制的 Artifact、Presentation、日志或其他大文件内容使用受鉴权的 HTTP 数据面传输，不新增另一套 Capability 控制协议。

## 幂等与超时

每个请求携带 deadline，Job 还有自己的 timeout。WSS 不提供断线自动重放：

- 请求帧在 Node 完整接收前断开，不执行该请求。
- Node 已执行请求但 Response 丢失时，调用结果为 uncertain；Hub 返回 `CONNECTION_LOST`，不会自动重发。
- 只读/查询动作可标记 `retryable=true`；`file.write/edit`、Shell/Build、Git 写入、浏览器动作、Agent send/steer/respond/Thread/Goal/Settings/Review 变更等副作用动作标记为 `retryable=false`，调用方必须先重新读取或查询状态。`session.create` 因强制稳定 `idempotencyKey`，只允许以原 key 和原参数安全重放。
- Job 启动依赖 `idempotencyKey`；Node 保存有限的幂等结果并拒绝同 key 不同参数。已启动 Job 不绑定 WSS session。
- Codex `session.create` 同样使用持久幂等记录；ChatGPT Cloud 的 `mode`、`model` 与 `thinking` 都属于创建 spec，中间状态无法确认时不自动创建第二个 Thread。
- `file.write/edit` 依赖 expected SHA CAS，并通过临时文件、fsync 和原子替换避免半文件。

## 错误

相对路径用于要求本机路径的能力时返回 `ABSOLUTE_PATH_REQUIRED`。文件 CAS 冲突的 `ProtocolError.details` 只包含 path/expected/actual SHA；WSL runtime 使用 `RUNTIME_UNAVAILABLE` 与 `WSL_CWD_UNMAPPABLE`；搜索 fallback 使用稳定 `RG_*` reason code。Node 已进入 release drain 时，新 Capability 返回可重试 `NODE_UPDATING`，已经运行的任务不被取消。连接在响应前丢失返回 `CONNECTION_LOST`（HTTP 适配层为 503）；调用 deadline 到期返回 `DEADLINE_EXCEEDED`（HTTP 适配层为 504）。`session.create` 将 Provider 执行 deadline 与 Node 最终回执窗口分离；已经开始的外部创建不随 Hub/WebSocket 断开取消，但仍受原执行 deadline 限制。Cloud 创建在副作用前持久化首条 Provider message ID，并在 SSE 首次给出 conversation ID 时立即写入幂等账本；同一原 key 可直接重放已知 ID，或用 message ID 做精确 Provider 对账。`mode=quick_chat` 不调用 conversation prepare，并在观察到 conversation ID 后返回，剩余 SSE 由 Node 在后台排空。若仍无法确认，调用方只能保留原 `idempotencyKey` 和原参数继续对账，禁止换 key 重建。
