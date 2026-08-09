# Wire Protocol（0.3.x）

Node 与 Hub 使用版本化 WSS 控制协议。0.3.0 已从 Capability Request 删除目录授权字段。

## Capability Request

```json
{
  "messageType": "capability.request",
  "requestId": "req_...",
  "machineId": "mach_...",
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

请求不携带目录授权 ID。需要定位本机资源的能力把绝对路径/cwd 放在 `params` 中，Node 以当前 OS 用户权限执行。

## 基本消息

协议仍包含设备握手、心跳、Capability Request/Response、Job/Event、Artifact 与错误结构。machineId 由连接会话确定，不允许通过 capability params 临时切换到另一台机器。

## 幂等与超时

有副作用的异步启动动作使用 idempotencyKey；Node 保存有限的幂等结果并拒绝同 key 不同参数。每个请求携带 deadline，Job 还有自己的 timeout。

## 错误

相对路径用于要求本机路径的能力时返回 `ABSOLUTE_PATH_REQUIRED`。不存在旧的目录授权不存在/禁用/越界错误。
