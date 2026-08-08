# 10 公共 API 与 MCP

## 1. Adapter 原则

MCP、REST、WebSocket、Web Console 和 CLI 都是 Fast Spider 的外部 Adapter。它们只能：

1. 认证并绑定调用者身份。
2. 校验外部 Schema 与大小限制。
3. 把请求转换为统一 Application Command / Capability Request。
4. 把 Job、Event、Result 和结构化错误映射回外部协议。

它们不得：

- 直接访问 Hub 数据库或 Node 连接。
- 单独实现文件、Shell、Git、浏览器或 AI 逻辑。
- 绕过 Hub Policy、Approval、审计或 Job 模型。
- 把 MCP request 直接当作 Hub↔Node Wire Protocol 消息。
- 接受绝对路径作为授权目标。

## 2. MCP 工具设计选项

### A. 多个语义明确的固定工具

优点：模型容易选择；输入 Schema 清晰；权限审查直观。缺点：能力增长会扩大工具列表；过细会造成版本和选择噪音。

### B. `capability_list + capability_execute`

优点：工具数最少、扩展快。缺点：Schema 过于动态；模型选择和审批界面不清晰；容易退化成“万能执行”接口，扩大安全风险。

### C. 少量固定常用工具 + 动态能力发现

优点：常用路径稳定，特殊能力可扩展；Schema 大小可控；权限仍按明确 capability/action 审核。缺点：Adapter 需维护固定工具和内部能力版本映射。

## 3. 最终推荐：混合模式 C

MVP 公开固定工具：

```text
machine_list
machine_get
workspace_list
workspace_open
file_read
file_edit
code_search
shell_run
job_watch
git_control
artifact_get
capability_list
```

后续阶段按功能启用：

```text
browser_control
screenshot_take
ai_control
```

保留受限 `capability_execute` 仅用于已发现、已授权但尚未提升为固定工具的低频能力。它不是任意命令入口：`capability`、`action`、版本和参数仍必须匹配 Node Descriptor 与固定策略；R3/R4 能力默认不通过通用入口执行。

不为每台机器、每个 Workspace、每个 Provider 或每个模型动态生成重复工具。

## 4. 公共目标选择

所有远程执行工具必须通过以下方式之一确定目标：

1. 参数显式提供 `machineId` 和 `workspaceId`。
2. 使用 `workspace_open` 返回的短期 `workspaceContextId`。

`workspaceContextId`：

- 由 Hub 签名并绑定 userId、clientId、machineId、workspaceId、revision 和 expiresAt。
- 只简化目标选择，不增加权限。
- Workspace 权限变化后失效。
- 不包含或透露 Node 绝对路径。

显示名只能用于列表和人类确认，不能作为执行目标的唯一键。

## 5. MCP 工具契约

以下为语义设计；具体 JSON Schema 由 `contracts/` 生成，不能在 Adapter 手写第二份不一致 Schema。

### 5.1 `machine_list`

用途：列出当前 Client 有权查看的机器。

输入：

```json
{
  "status": "online|offline|busy|any",
  "cursor": null,
  "limit": 50
}
```

输出：machineId、显示名、OS/arch、版本、状态、lastSeenAt、能力摘要。不得返回设备凭据、真实网络拓扑或本机路径。

### 5.2 `machine_get`

输入：machineId。输出机器详细逻辑信息、连接状态、版本、容量和可见 Capability Descriptor 摘要。

### 5.3 `workspace_list`

输入：machineId、可选状态和分页。输出 workspaceId、显示名、Git 摘要、read/write 能力、revision 和状态；不返回绝对路径。

### 5.4 `workspace_open`

输入：machineId、workspaceId、requestedActions、ttlSeconds。输出短期 workspaceContextId、有效期和实际授予的 Action。

“open”是建立安全上下文，不是授权新目录，也不改变 Node 本地 Registry。

### 5.5 `file_read`

输入：

```json
{
  "workspaceContextId": "wctx_opaque",
  "path": "internal/app.go",
  "offset": 0,
  "length": 65536
}
```

输出：UTF-8 内容、文件 revision/hash、是否截断、下一 offset。二进制或超限文件返回 Artifact/结构化错误，不做隐式乱码转换。

### 5.6 `file_edit`

支持固定模式：`exact_replace`、`range_replace`、`patch`、`write`。必须带 expected revision/hash；返回每个文件的新 revision、Diff 摘要和可选 Artifact。

MCP 工具不暴露“任意文件路径写入”；所有 path 都是 Workspace 相对路径。

### 5.7 `code_search`

输入：workspace context、mode（glob/grep）、pattern、globs、contextLines、maxResults。输出结构化 match；超限明确标记 truncated。

### 5.8 `shell_run`

输入：workspace context、cwd、`argv` 或已授权 shell profile、env 覆盖、timeout、background。输出 jobId 和初始状态。长输出必须由 `job_watch` 获取，不能让一次 MCP tool call 无限保持或返回无限文本。

### 5.9 `job_watch`

输入：jobId、cursor、waitMs、maxEvents。输出 Job snapshot、事件、nextCursor、terminal。watch 超时不取消 Job。

### 5.10 `git_control`

输入：workspace context、action 和 action-specific params。Action 白名单来自契约，如 status、diff、log、show、commit、fetch、pull、push、worktree。MCP 层不接受任意 Git flags 字符串。

### 5.11 `artifact_get`

输入：artifactId、可选 range/metadataOnly。输出元数据和受限下载引用；每次重新检查调用者对原 Job/Workspace 的权限。

### 5.12 `capability_list`

输入：可选 machineId/workspaceId。输出当前可见 Capability Descriptor、版本、Action、风险、平台和限额。它用于发现，不自动授权。

### 5.13 `browser_control`

使用固定 `action` 枚举，指向受管 browserSessionId/contextId/pageId；不接受任意 Playwright/CDP 原始消息。真实浏览器 Profile 的操作必须显式标记并审批。

### 5.14 `screenshot_take`

输入：targetType（desktop/display/window/page）、targetId、格式、质量和尺寸。输出 Job/Artifact；桌面/窗口截图单独授权。

### 5.15 `ai_control`

Provider-neutral：`providers.list`、`models.list`、`projects.list`、`session.create/get/send/watch/cancel/result/handoff`。工具返回真实 owner、phase 和 executionMode；打开桌面 UI 不等同于已启动 Turn。

## 6. 工具返回统一结构

小结果：

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "requestId": "req_opaque",
    "traceId": "trace_opaque",
    "nextCursor": null,
    "truncated": false
  }
}
```

失败：

```json
{
  "ok": false,
  "error": {
    "code": "WORKSPACE_REVOKED",
    "message": "workspace authorization is no longer active",
    "retryable": false,
    "details": {}
  },
  "meta": {
    "requestId": "req_opaque",
    "traceId": "trace_opaque"
  }
}
```

不依赖自然语言判断成功/失败。面向模型的 message 可简洁说明，但稳定事实放结构化字段。

## 7. MCP 版本与 SDK

- 使用官方 Go MCP SDK，而不是自建 JSON-RPC/MCP 全栈。
- 编码开始时固定一个明确 SDK 和 MCP 规范版本，运行 conformance tests。
- 外部 MCP 版本升级通过 Adapter 消化，不能迫使 Hub↔Node FSWP 同步变更。
- 在兼容窗口内支持必要的旧规范；不永久维护无期限兼容分支。
- SDK 的 OAuth、body size、transport 和错误行为仍由 Fast Spider 包装层设置安全上限，不能盲目信任默认值。

## 8. Schema 大小与模型选择

控制原则：

- 常用固定工具保持短、明确、稳定。
- action-specific 参数使用条件 Schema，但避免一个工具包含几十个完全不相关 Action。
- Capability Descriptor 不在每次工具调用重复发送。
- 列表输出分页；大 Diff、日志、截图和报告使用 Artifact。
- 描述中明确只使用 opaque ID 和相对路径。
- 机器/Workspace 当前选择可以由安全上下文减少参数，但必须可见且可过期。

预计固定核心工具 12 个，后续完整工具面约 15 个；不会随 Node 数量线性增长。

## 9. MCP 权限与审批映射

| 工具 | Hub scope 示例 | Node Action |
|---|---|---|
| machine_list/get | `machines:read` | node status/capabilities |
| workspace_list/open | `workspaces:read` | workspace get/list |
| file_read/code_search | `workspace:read` | file.read/search |
| file_edit | `workspace:write` | file.write/edit/patch |
| shell_run | `jobs:execute` | shell.run.* |
| job_watch | `jobs:read` | job events/result |
| git_control | `git:read`/`git:write`/`git:network` | action-specific |
| artifact_get | `artifacts:read` | artifact metadata/download |
| browser_control | `browser:control` | browser action |
| screenshot_take | `capture:read` | screenshot action |
| ai_control | `agent:control` | agent action |

Scope 只是 Hub 第一层；Node 本地 grant 和 Approval 仍需通过。

## 10. REST API

REST 供 Web Console、CLI/SDK 和自动化使用，资源风格与 Job 模型一致：

```text
GET    /api/v1/machines
GET    /api/v1/machines/{machineId}
GET    /api/v1/machines/{machineId}/workspaces
POST   /api/v1/workspace-contexts
POST   /api/v1/jobs
GET    /api/v1/jobs/{jobId}
GET    /api/v1/jobs/{jobId}/events
POST   /api/v1/jobs/{jobId}:cancel
GET    /api/v1/artifacts/{artifactId}
POST   /api/v1/enrollment-tokens
POST   /api/v1/machines/{machineId}:revoke
GET    /api/v1/audit
```

- 写请求支持 `Idempotency-Key` Header。
- 分页使用 opaque cursor。
- 时间统一 RFC 3339 UTC。
- API 版本 `/v1` 与 FSWP/MCP 版本独立。
- 不公开 Node 绝对路径或 Provider Token。

## 11. Event API

MVP 支持两种读取方式：

1. 长轮询 `GET /jobs/{id}/events?cursor=...&wait=...`，最容易部署和恢复。
2. 认证 WebSocket/SSE 聚合订阅，供 Web Console 优化体验。

无论传输方式，都使用同一 cursor 和 canonical Event。连接断开后客户端从 cursor 恢复；订阅连接本身不是 Job 所有权。

## 12. OAuth 与公开入口

- MCP/API Access Token 的 audience、scope 和 Client 必须匹配。
- OAuth Metadata、Protected Resource Metadata 和授权流程依据固定的官方 MCP 规范版本实现。
- 设备 WSS 使用独立设备认证路径，不接受用户 Access Token 代替。
- Web Console 使用独立 Session/CSRF 模型，不把浏览器 Cookie 直接当 MCP Token。
- 管理、MCP、Artifact、Node WSS 分别限流和记录。

## 13. 错误映射

内部错误映射到稳定公共码；HTTP status/MCP error 只表达协议层类别：

- 400：Schema/参数错误。
- 401：未认证或 Token 无效。
- 403：权限/Approval/Node 本地拒绝。
- 404：资源不可见或不存在，避免 ID 枚举。
- 409：revision/idempotency/state 冲突。
- 413：输入/Artifact 过大。
- 429：限流/Node 背压。
- 503：Node 离线或 Hub 降级。
- 504：请求/等待超时；不代表 Job 一定取消。

完整业务错误仍在结构化 `error.code` 中。

## 14. 安全要求

- 所有输入先做 JSON 深度、长度、数组和字符串限制。
- MCP tool arguments 不直接传给 shell、Git、ripgrep、CDP 或 Provider。
- 结果做敏感字段和绝对路径脱敏。
- Artifact 下载使用权限复核、Content-Disposition、nosniff 和范围限制。
- `workspace_open`、`capability_list` 或工具列表不能泄露无权资源。
- 调试接口和原始 FSWP 不对公网 Client 暴露。

## 15. 兼容与弃用

- 工具名和主语义在一个 API major 内稳定。
- 新增可选字段保持向后兼容。
- 改变 Action 语义时新增版本或 Action，不能静默替换。
- 弃用字段先发 warning 和截止版本，文档、契约、测试同时更新。
- 不为历史错误设计建立长期双实现；迁移窗口结束后删除旧路径。
