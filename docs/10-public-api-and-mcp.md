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

工具数少，但会把所有能力重新压进一个万能执行入口，Schema 动态、调试体验差。当前个人项目不采用，也不为未来预留第二条执行链。

### C. 固定常用工具 + 内部动态能力发现

外部工具保持稳定；`capability_list` 只负责发现当前 Node 的真实能力，真正执行仍走语义明确的固定工具。这样模型选择简单，内部 Capability Engine 仍可扩展。

## 3. 当前固定工具面

Phase 6 公开固定工具共 16 个：

```text
machine_list
machine_get
workspace_list
file_read
file_edit
code_search
shell_run
job_watch
job_cancel
git_control
build_control
artifact_get
capability_list
browser_control
screenshot_take
ai_control
```

不为每台机器、每个 Workspace、Provider、模型或 Session 动态生成重复工具，也不提供通用 `capability_execute`。

## 4. 公共目标选择

远程执行工具直接使用 opaque `machineId + workspaceId`。当前个人模式不再增加 `workspace_open/workspaceContextId` 短期上下文；需要减少重复参数时由客户端会话在本地记住当前选择即可，但每次请求仍由 Hub/Node 校验真实资源归属和 Workspace 状态。

显示名只能用于列表和人类识别，不能作为执行目标的唯一键。

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

### 5.4 `file_read`

输入：

```json
{
  "machineId": "mach_opaque",
  "workspaceId": "ws_opaque",
  "path": "internal/app.go",
  "offset": 0,
  "limit": 65536
}
```

输出：UTF-8 内容、文件 SHA-256、字节范围和是否还有后续内容。二进制、非法 UTF-8 或超限请求返回结构化错误，不做隐式乱码转换。

### 5.5 `file_edit`

当前只提供最常用的精确替换：`path + oldText + newText + expectedFileSha256`。`oldText` 必须唯一匹配，写入使用原子替换和乐观并发校验。远程不暴露任意绝对路径写入或万能 patch 模式。

### 5.6 `code_search`

输入：machineId、workspaceId、query、可选相对 path、regex、ignoreCase、limit。输出 path/line/column/text 结构化 match，并带 scannedFiles/truncated。

### 5.7 `shell_run`

输入：machineId、workspaceId、显式 `argv[]`、可选相对 cwd、timeoutSeconds、idempotencyKey。没有隐式 shell 插值、远程 env 覆盖或 background 魔法参数；输出 jobId 和初始状态，长输出统一由 `job_watch` 获取。

### 5.8 `job_watch`

输入：machineId、workspaceId、jobId、cursor、waitSeconds。输出 Job snapshot、事件、nextCursor、terminal。Node 会再次校验 Workspace 仍启用且 Job 确实属于该 Workspace；跨 Workspace 的 jobId 按不存在处理。watch 超时不取消 Job；Node 在线事件窗口有硬上限，完整 Job 本地日志另行按 24 小时保留。

### 5.9 `job_cancel`

输入：machineId、workspaceId、jobId。Node 同样校验 Job 的 Workspace 归属；只有完整进程树真正退出后才返回 terminal canceled，重复取消终态 Job 安全。

### 5.10 `git_control`

输入：machineId、workspaceId、action 和固定 action-specific 参数。当前白名单包含 status/diff/stagedDiff/log/show/branches/currentBranch/worktrees/add/commit/fetch/pull/push/createWorktree/deleteWorktree；MCP 不接受任意 Git flags 字符串。

### 5.11 `build_control`

固定 action 为 `list` / `run`。`list` 只返回 profileId、显示名、相对 cwd 和 timeout，不返回真实 argv；`run` 只能按 Node 本机登记的 profileId 启动并要求 idempotencyKey，远端不能覆盖命令模板。

### 5.12 `artifact_get`

当前固定 action：`get`、`uploadFile`、`uploadJobLog`。`get` 返回元数据、下载路径，并仅对不超过 128 KiB 的文本/JSON/XML Artifact 内联内容；`uploadFile` 只能读取授权 Workspace 相对路径；`uploadJobLog` 只能导出对应 Workspace 的终态 Job 日志。1 MiB chunk/offset/resume/hash 等原始上传协议不直接暴露给 MCP。

### 5.13 `capability_list`

输入：可选 machineId；省略时返回 Hub 公共能力目录，指定在线 machineId 时返回该 Node 实际宣告的能力。它用于发现，不新增权限。

### 5.14 `browser_control`

Phase 5 当前固定 action：`launch`、`close`、`page.open`、`page.navigate`、`page.close`、`pages.list`、`click`、`type`、`press`、`wait`、`snapshot`、`screenshot`、`events`。

调用必须显式提供 machineId、workspaceId；除 `launch` 外使用 opaque browserSessionId，页面动作再使用 opaque pageId。Node 只在本机安装 Playwright Sidecar + 受管 Chromium 后宣告 `browser.automation`。Browser 不再额外要求独立 Workspace 权限：公网网页默认可访问，localhost/私网 Origin 由 Node 本机持久白名单控制，远程不能新增白名单。

不接受任意 JavaScript、`evaluate`、Playwright API、CDP 消息或现有浏览器 Profile。`screenshot` 结果直接返回 Artifact 元数据，不返回 Node 临时路径。

### 5.15 `screenshot_take`

当前固定 action：`listDisplays`、`desktop`、`display`、`listWindows`、`window`。调用提供 machineId、workspaceId；workspaceId 仅用于确认当前 Workspace 仍启用并归属截图 Artifact，不再要求额外 `screenshot` 权限。窗口截图先用 `listWindows` 取得短期 opaque `windowId`，不暴露 OS 句柄；结果只返回尺寸与 Artifact 元数据，不返回 Node 临时路径。

### 5.16 `ai_control`

当前固定 action：`providers.list`、`models.list`、`projects.list`、`session.list`、`session.get`、`session.create`、`session.send`、`session.watch`、`session.cancel`、`session.result`、`session.rename`、`session.archive`。

Phase 6 只实现本机 Codex 的 `bridge_owned` 执行：Node 直接启动 `codex app-server --stdio`，Provider 凭据和本机认证状态不进入 Hub。`session.create/send` 复用 Workspace 已有 `write + shell` 权限，不新增 `agent` 权限。未指定 model 时先读取当前 `model/list` 并选择当前 CLI 实际可用模型；显式传入不存在的 model 会在启动 Turn 前返回错误。`desktop_owned/handoff/Hook` 不进入当前 MVP。

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
- 客户端可以自己记住当前 Machine/Workspace 选择来减少 UI 操作，但请求仍发送 opaque ID，不再维护额外短期授权 Context。

当前固定工具就是 16 个，不随 Node、Workspace、Provider、模型或 Session 数量增长。

## 9. MCP 权限与审批映射

| 工具 | Hub scope 示例 | Node Action |
|---|---|---|
| machine_list/get | `machines:read` | node status/capabilities |
| workspace_list | `workspaces:read` | workspace list |
| file_read/code_search | `workspace:read` | file.read/search |
| file_edit | `workspace:write` | file.write/edit/patch |
| shell_run | `jobs:execute` | shell.run.* |
| job_watch | `jobs:read` | job events/result |
| git_control | `git:read`/`git:write`/`git:network` | action-specific |
| artifact_get | `artifacts:read` | artifact metadata/download |
| browser_control | `browser:control` | browser action |
| screenshot_take | `capture:read` | screenshot action |
| ai_control | `agent:control` | agent action |

Scope 是 Hub 第一层；Node 继续执行 Workspace、路径、进程和网络等实际安全边界。个人使用场景不要求每个 Capability 再叠一层独立 grant，只有真正有本机副作用的写入/Shell/Git 网络/Build 等操作保留独立本机权限。

## 10. REST API

REST 供 Web Console、CLI/SDK 和自动化使用，资源风格与 Job 模型一致：

```text
GET    /api/v1/machines
GET    /api/v1/machines/{machineId}
GET    /api/v1/machines/{machineId}/workspaces
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
- 403：Workspace/Node 本地权限拒绝。
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
- `workspace_list`、`capability_list` 或工具列表不能泄露无权资源，也不能返回 Node 绝对路径。
- 调试接口和原始 FSWP 不对公网 Client 暴露。

## 15. 兼容与弃用

- 工具名和主语义在一个 API major 内稳定。
- 新增可选字段保持向后兼容。
- 改变 Action 语义时新增版本或 Action，不能静默替换。
- 弃用字段先发 warning 和截止版本，文档、契约、测试同时更新。
- 不为历史错误设计建立长期双实现；迁移窗口结束后删除旧路径。
