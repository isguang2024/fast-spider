# 05 Node 能力与执行边界（Current）

## 1. 当前原则

Fast Spider 的远程执行边界是 Machine，不是目录对象。Node 以启动它的当前 OS 用户身份运行，所有文件、进程、Git、浏览器和本机 AI 能力最终都受该 OS 用户权限约束。

Hub 只负责身份、路由、deadline、审计、Job/Artifact 元数据和连接状态；Hub 不直接读取 Node 文件，也不在 Hub 上执行开发命令。Node 收到 `capability.request` 后按 `capabilityId + action` 进入固定 dispatcher，不接受任意 capability 名、任意 CLI flag 或任意脚本协议。

控制请求包含 `requestId`、`capability`、`action`、`params` 和 deadline。`idempotencyKey` 只出现在需要它的 Job/网络写操作中，不是所有 capability 的通用必填字段。Hub↔Node 小型控制参数和结果直接走同一条 WSS；Artifact/Presentation 等大文件数据面使用独立 HTTP 链路。

## 2. 当前真实 Capability Catalog

以下表格以 `internal/protocol/v1/protocol.go` 与 `internal/node/capabilities.go` 为事实源。

| capabilityId | 当前 actions | 说明 |
|---|---|---|
| `machine.status` | `report` | Node 状态报告 |
| `file.read` | `read` | 有界 UTF-8 文本读取 |
| `file.write` | `edit` | 基于 exact match + expected SHA 的原子编辑 |
| `code.search` | `search` | 有界文本/正则搜索 |
| `shell.exec` | `run` | 固定 argv 的后台 Job |
| `job.control` | `watch`, `cancel` | Job 事件读取与进程树取消 |
| `git.repository` | `status`, `diff`, `stagedDiff`, `log`, `show`, `branches`, `currentBranch`, `worktrees`, `add`, `commit`, `fetch`, `pull`, `push`, `createWorktree`, `deleteWorktree` | 系统 Git 固定动作 |
| `build.exec` | `run` | 固定 argv 的构建/测试 Job |
| `artifact.store` | `uploadFile`, `uploadJobLog`, `publishFile` | Hub Artifact/临时 Presentation 数据面 |
| `working.context` | `get`, `set`, `clear` | 项目级轻量任务状态 |
| `browser.automation` | `launch`, `close`, `page.open`, `page.navigate`, `page.close`, `pages.list`, `click`, `type`, `press`, `wait`, `snapshot`, `screenshot`, `events` | 隔离 Chromium |
| `screenshot.capture` | `listDisplays`, `desktop`, `display`, `listWindows`, `window` | 一次性桌面/显示器/窗口截图 |
| `agent.control` | AI Harness、CC Switch Route/Model/EffectiveCapabilities discovery 与受控 Session/Turn 生命周期 | 当前 Harness 为本机 Codex + Claude Code |

当前**没有**通用 `file.write` 覆盖写文件、`mkdir`、`move`、`copy`、`delete`、`purge`、`readChunks`、任意 shell 字符串执行或远程 `node.update` capability。需要这些能力时必须单独设计、实现、测试后再进入 catalog，文档不得提前声明为已实现。

## 3. 文件读取与编辑

### `file.read/read`

输入：

```json
{
  "path": "C:\\dev\\project\\README.md",
  "offset": 0,
  "limit": 131072
}
```

规则：

- `path` 必须满足当前 OS 的绝对路径规则；Windows 允许盘符绝对路径，裸盘符 `V:` 只在明确支持它的 cwd 场景被归一为 `V:\\`，不能把 `V:folder` 当绝对路径。
- 单次读取上限 128 KiB。
- 只返回 UTF-8 文本；二进制、NUL 或非法 UTF-8 拒绝。
- 返回 `bytesRead`、`size`、`truncated`、chunk SHA-256；可编辑大小范围内同时返回完整文件 SHA-256。
- 大文件通过 offset/limit 多次读取，不存在另一个 `readChunks` action。

### `file.write/edit`

`file_edit` 是当前唯一远程文件写入口。它要求 exact `oldText`/`newText`，可携带 `expectedFileSha256` 做 CAS。

提交语义：

1. 读取并校验当前文件及 expected SHA。
2. 要求 oldText 唯一命中。
3. 在同目录写临时文件并 `Sync`。
4. 校验目标内容 SHA。
5. 原子替换正式文件；Windows 使用 replace-existing + write-through 语义。

因此断线不会把正式目标留成“半文件”。如果请求已经执行但 WSS 响应丢失，结果属于 uncertain：调用方应重新读取文件/状态确认，而不是无脑重放写操作。

## 4. 代码搜索

`code.search/search` 输入为绝对 `path`、`query`、`regex`、`ignoreCase`、`limit`。当前实现不是开放式 ripgrep 参数代理：Node 自己遍历受限文件集合并执行有界搜索。

当前上限包括：单文件 2 MiB、最多扫描 5000 个文件、默认 100 条结果、最大 200 条结果；超限通过 `truncated` 表示。搜索为只读动作，连接中断后可安全重新发起。

## 5. Shell、Build 与 Job

`shell.exec/run` 和 `build.exec/run` 都使用显式 argv 与绝对 `cwd`。Fast Spider 不把任意 shell command string 作为协议面；需要 `cmd.exe`、PowerShell、bash 等时，它们本身必须作为 argv 中的显式 executable 出现。

启动成功后返回 `jobId`。Job 由 Node `JobManager` 独立管理：WSS 会话断开不会自动杀死已启动 Job；调用方通过 `job.control/watch` 读取有界 stdout/stderr/status，通过 `job.control/cancel` 终止进程树。

启动类动作必须使用幂等键时由具体 MCP/Capability schema要求；断线后是否重试要按 Job/状态查询结果决定，不能仅根据客户端是否收到响应判断。

## 6. Git

`git.repository` 只暴露固定 action，不接受任意 Git flags 字符串。目标使用绝对 `repositoryPath`，linked worktree 和子目录仍由系统 Git 自己解析仓库事实。

只读 actions：`status`、`diff`、`stagedDiff`、`log`、`show`、`branches`、`currentBranch`、`worktrees`。

写/网络 actions：`add`、`commit`、`fetch`、`pull`、`push`、`createWorktree`、`deleteWorktree`。这些动作进入 mutation audit；网络动作要求当前 OS 用户已有 Git 凭据并使用 idempotency key。Fast Spider 不保存或回传 Git 凭据。

## 7. Artifact 与 Presentation

`artifact.store/uploadFile` 与 `uploadJobLog` 把显式文件/Job 日志上传到 Hub Artifact 存储；`publishFile` 通过 Hub Temporary Presentation Relay 暂时发布文件，适合截图和 AI 原生资源展示。

截图不依赖第三方 OSS：Node 生成图片后上传到 Hub 临时 relay；Hub 使用系统临时目录保存短期内容，不写业务数据库，并可通过 MCP 返回原生 `ImageContent` 与短期 `ResourceLink`。过期资源由维护循环清理。

## 8. Working Context

`working.context/get|set|clear` 是项目当前任务快照，不是长期 AI Memory。每个项目只保存一份有界 JSON，存于 Node data-dir，不写项目目录、不污染 Git。

保存字段包括 goal、baseline branch/commit、completed、constraints、pending、keyFiles、facts。`get` 会额外实时读取 Git branch/HEAD/dirty；Git 与文件内容始终是最终事实源。

## 9. Browser 与 Screenshot

`browser.automation` 使用 Node 管理的隔离 Chromium，不附着用户正常浏览器 Profile，也不暴露原始 CDP/Playwright。可访问 Node 当前网络可达的公网、localhost 与私网 HTTP(S) 目标；仍拒绝危险 scheme、任意 JavaScript 注入和超出固定 action 的控制。

`screenshot.capture` 只做一次性桌面/显示器/窗口捕获。窗口通过短期 opaque `windowId` 指定，不把 OS 原生句柄暴露给远程客户端。

## 10. Agent Control

当前 `agent.control` action 集合：

```text
routing.status
providers.list
models.list
provider.capabilities
projects.list
skills.list
hooks.list
permissions.list
plugins.list
plugins.installed
plugins.get
plugin.skill.read
mcp.status.list
session.list
session.get
session.create
session.send
session.steer
session.respond
session.watch
session.cancel
session.result
session.rename
session.archive
session.unarchive
session.delete
session.fork
session.compact
session.rollback
session.goal.get
session.goal.set
session.goal.clear
session.settings.update
session.review
```

`providers.list` 当前返回 `codex` 与 `claude_code` 两个 Harness，并为每个 Harness 返回自己的 `supportedActions`。`routing.status` 是全局只读 action：读取 CC Switch SQLite SSOT，区分 `direct|cc_switch`、current Provider、model mapping、Takeover/health 与 EffectiveCapabilities；raw provider settings/meta/credential 永不离开 Node。

### Codex

Codex Adapter 直接运行本机 `codex app-server --stdio`。Provider 凭据与 ChatGPT/Codex 本地认证只留在 Node 本机。Codex 的 Harness model catalog 与 CC Switch upstream route 分开返回，不把两层模型名称强行合并。

`session.create` / `session.send` 使用 Codex 原生 Turn `UserInput`：text、skill、image、localImage、mention；`session.steer` 使用同一输入集合，但只追加到调用方明确给出的 active `turnId`，不隐式修改 model/cwd/outputSchema。Skill 输入使用 `name + absolute path`，不是把 Skill 内容拼接进 prompt。图片可统一指定 `imageDetail=auto|low|high|original`。`outputSchema` 作为有界 JSON Schema 直接传给 `turn/start`，用于约束该 Turn 最终 assistant message；`session.result` 仍保留真实 raw `finalAgentMessage`，不伪装为已完成 typed decode。

发现面还包括 `provider.capabilities`（当前 Provider 的 webSearch/imageGeneration/namespaceTools）、`hooks.list`（含 enabled/source/trustStatus）、`permissions.list`、`plugins.installed` 和有界摘要形式的 `mcp.status.list`。MCP 状态只返回 server/auth/tool names/resource 摘要，不把每个 Tool 的完整 JSON Schema 通过 WSS 重复搬运。

线程管理边界：

- `session.unarchive/delete/fork/compact` 映射 Codex 原生 thread API。
- `session.rollback(numTurns)` 只从 Codex thread 历史末尾移除指定数量的 turns，**不会回滚工作树文件变更**。
- Goal 使用 Codex 原生 `thread/goal/*`，状态仅允许 `active|paused|blocked|usageLimited|budgetLimited|complete`。
- `session.settings.update` 只暴露稳定、受限字段：workingDirectory/cwd、model、effort、named permissions profile、personality、serviceTier、reasoning summary；不开放 arbitrary config map。
- `session.review` 只暴露 `uncommittedChanges`、`baseBranch`、`commit`、`custom` 四种原生 target，以及 `inline|detached` delivery。
- Plugin 是能力包。FS 可以列出 Marketplace、已安装 Plugin、读取 Plugin 与 Plugin Skill，但不向 `turn/start` 发明 `pluginId`；真正明确挂到 Turn 的是原生 Skill input，其他 Plugin App/MCP 能力由 Codex 安装后的运行环境决定。
- `session.respond` 只处理受控的 Codex Server Request：request_user_input、单次 command/file approval 和 MCP elicitation；不开放 session-wide approval widening，也不开放任意 permission object。`session.watch/get` 同时返回当前 `pendingRequests` 快照，断线或事件截断后仍可恢复 requestId。Permission 升级继续通过已命名 Permission Profile 管理。
- Codex app-server 进程重启后，Adapter 在下一次 Turn/Review 前自动 `thread/resume(threadId)`；这是内部恢复逻辑，不增加需要调用方手工维护的 resume action。
- Codex 自带 `fs/*`、`command/exec/*`、`process/*`、`thread/shellCommand`、`mcpServer/tool/call` 等第二执行面不通过 `ai_control` 暴露，文件/Shell/Git/Build 继续走 Fast Spider 自己的 Capability、Job 与 Audit 链。

### Claude Code

Claude Adapter 使用本机 `claude -p --output-format stream-json --verbose`。首 Turn 传原生 UUID `--session-id`，后续使用 `--resume`；Prompt 通过 stdin，不进入 argv。当前支持 text Prompt、可选 model、`effort=low|medium|high|xhigh|max` 与最多 16 KiB 的 JSON Schema。第一版不把 Codex Skill/Image/Mention 结构伪装成 Claude 输入。

Claude Session 的控制索引保存在 Node data-dir，只保存 session/status/model/bounded result/usage/RouteSnapshot，不复制完整 Prompt/对话。`session.rename/archive/unarchive` 只改变 Fast Spider 索引展示状态，不声称修改 Claude 原生历史。

每个 Turn 前后读取脱敏 CC Switch RouteSnapshot；只有 `routingMode=cc_switch` 且 CC Switch `proxy_request_logs.session_id` 精确等于 Claude native sessionId 时才设置 `actualUpstream`。因此并发请求不会靠“最后一条日志”错误归因模型。

`claude --version` 只代表 Runtime 可用；`claude auth status` 只代表认证配置存在。Provider 明确区分 `runtimeAvailable`、`authConfiguration`、route/health 与 `executionHealth=unknown_until_turn`。当前测试机真实 Claude Turn 返回 revoked OAuth 401，Adapter 将其标准化为 `turn.failed` 而非 Runtime unavailable。

Codex 产品层存在 Automations 概念，但本机 Codex 0.141.0 的公开 CLI/app-server schema 当前没有 Automation API。Fast Spider 因此不映射 Automations、不读取 Codex 内部数据库、不模拟桌面 UI。未来只有在公开协议出现后才考虑直接映射。

## 11. 断线、重试与审计

Hub 对连接中断返回结构化 `CONNECTION_LOST`，对 deadline 返回 `DEADLINE_EXCEEDED`。只读查询类能力可以声明 retryable；会产生副作用的文件编辑、Job 启动/取消、Git 写/网络、Build、Browser 操作、Agent Turn/Thread/Goal/Settings/Review 变更不能宣称可无脑重试。

当前 Agent 中可安全重试的读取包括 Provider/Model/Project/Skill/Hook/Permission/Plugin/MCP 状态 discovery、Session list/get/watch/result 和 Goal get。Thread/Goal/Settings/Review/Turn 变更以及 `session.steer/respond` 进入 mutation audit；steer/respond 在连接中断后不能自动重放。

WSS 不做写操作自动重放：完整请求未到 Node 前断线则不会执行；请求已经执行但响应丢失时，调用方先重新读取状态，再决定下一步。
