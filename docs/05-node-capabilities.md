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
| `file.read` 2.0 | `read` | byte/line/head/tail/around/stat 的有界 UTF-8 文本读取 |
| `file.write` 2.1 | `edit`, `create`, `replace`, `editMany`, `preview` | mutation 元数据-only；CAS、批量规划、原子替换与 bounded preview |
| `code.search` 2.1 | `search` | content/files、VCS ignore、稳定 rg fallback code 与扫描/timing 事实 |
| `shell.exec` 1.1 | `run` | 固定 argv 的 host/WSL 后台 Job |
| `job.control` 1.1 | `watch`, `cancel` | Job 事件、trace/timing 与进程树取消 |
| `git.repository` | `status`, `diff`, `stagedDiff`, `log`, `show`, `branches`, `currentBranch`, `worktrees`, `add`, `commit`, `fetch`, `pull`, `push`, `createWorktree`, `deleteWorktree` | 系统 Git 固定动作 |
| `build.exec` 1.1 | `run` | 固定 argv 的 host/WSL 构建/测试 Job |
| `artifact.store` | `uploadFile`, `uploadJobLog`, `publishFile` | Hub Artifact/临时 Presentation 数据面 |
| `working.context` 1.1 | `get`, `set`, `clear`, `plan.init`, `plan.get`, `plan.list`, `plan.sync`, `task.update`, `markdown.list`, `markdown.read`, `markdown.append`, `progress.watch` | Plan/Task + Markdown Task Workspace；旧入口映射默认 plan |
| `browser.automation` 1.3 | `readiness`, `launch`, `close`, `page.open`, `page.navigate`, `page.close`, `pages.list`, `click`, `type`, `press`, `wait`, `batch`, `snapshot`, `screenshot`, `events` | 隔离 Chromium，headed/headless 均由 Sidecar 管理 |
| `screenshot.capture` | `listDisplays`, `desktop`, `display`, `listWindows`, `window` | 一次性桌面/显示器/窗口截图 |
| `agent.control` 1.3 | 分层 readiness、AI Harness/Route discovery、会话可见性双模式、主动 callback 与受控 Session/Turn 生命周期 | 当前 Harness 为本机 Codex + Claude Code |

当前**没有** `mkdir`、`move`、`copy`、`delete`、`purge`、`readChunks`、任意 shell 字符串执行或远程 `node.update` capability。`file.write/create` 只能创建显式目标文件且要求父目录已存在，不是通用文件管理接口。

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
- 旧 byte `offset/limit` 保持兼容；也可选择 `lineStart+lineCount`、`headLines`、`tailLines`、`aroundLine+contextLines`，各类选择器严格互斥。
- `statOnly` 返回 stat 与原文件 SHA，不读取/返回正文；`includeLineNumbers` 对返回文本渲染行号。
- 正文单次上限 128 KiB；行数、context、长行与大文件扫描均 bounded，tail/around 不把整文件无限载入内存。
- 只返回 UTF-8 文本；二进制、NUL 或非法 UTF-8 拒绝。
- `fileSha256` 始终针对原文件 bytes；`chunkSha256` 针对实际返回 chunk，启用行号时包含渲染结果。
- 大文件通过 offset/limit 多次读取，不存在另一个 `readChunks` action。

### `file.write` 2.1

`file_edit` 仍是唯一远程文件写工具，不增加 MCP 工具。现有文件的 `edit/replace/editMany` 必须携带基于原始 bytes 的 `expectedFileSha256`；`create` 必须显式 `expectedAbsent=true`，不允许覆盖；`preview` 通过 `previewOf=create|replace|editMany` 复用同一 planner，但绝不写盘。

提交语义：

1. 读取并校验当前文件及 expected SHA。
2. `replace` 要求 oldText 在原版本唯一命中；`editMany` 的全部 oldText 都在同一原版本上唯一定位，range 不得重叠，任一失败则零写入。
3. 一次生成最终 bytes；no-op 返回 `changed=false`，不更新 mtime。
4. 在同目录写临时文件、fsync，并再次校验目标 SHA。
5. 原子替换正式文件；Windows 使用 MoveFileEx replace-existing + write-through，其他平台使用原子 rename 并尽可能同步父目录。BOM、主换行风格和平台可支持的权限位保持。

mutation 返回 `success/changed/path/operation/editsApplied/oldSha256/newSha256/bytesChanged/lineDelta/timing/warnings`，不返回正文、oldText/newText 或 diff；因此响应大小不随目标文件线性增长。`preview` 才返回最多 16 KiB 的变更 hunk 与 `diffTruncated`，且绝不写盘。冲突使用 `REVISION_CONFLICT` 和 `details.path/expectedSha256/actualSha256`，不回传文件正文。

因此断线不会把正式目标留成“半文件”。如果请求已经执行但 WSS 响应丢失，结果属于 uncertain：调用方应重新读取文件/状态确认，而不是无脑重放写操作。

## 4. 代码搜索

`code.search/search` 输入为绝对 `path`、`query`、`mode=content|files`、`regex`、`ignoreCase`、bounded include/exclude glob、context/beforeContext/afterContext 与 `limit`。结果公开 `engine`、`scannedFiles/matchedFiles/bytesScanned/matchCount/skippedFiles/skipReasons/incomplete`、`primaryElapsedMs/fallbackElapsedMs/elapsedMs` 和 `truncated`；`matchCount` 在两个引擎中统一表示匹配行数，同一行多个 occurrence 只计一行。Node 对实际 JSON 结果实施 640 KiB 预算，超出时保留聚合统计并截断明细，避免冲破控制面 1 MiB 上限。

Node 只从 `<data-dir>/components/search-ripgrep/<version>/rg(.exe)` 解析已验证 Managed Component，绝不信任 PATH，也不在每次搜索时联网安装。rg 固定安全参数并清空 `RIPGREP_CONFIG_PATH`；fallback reason 固定为 `RG_NOT_FOUND/RG_START_FAILED/RG_EXIT_ERROR/RG_TIMEOUT/RG_OUTPUT_LIMIT/RG_OUTPUT_INVALID`。默认遵守 VCS ignore 并排除通用生成目录；显式 include 可精确覆盖 ignore/生成目录策略，exclude 随后生效。带静态目录前缀的 include 会把该前缀作为 rg search target 下推，避免为了匹配窄源码范围仍从大型仓库根遍历；`**/*.ext` 这类无静态前缀的广域 include 仍按调用方明确范围执行。

native fallback 支持同一 content/files、glob、context 与 limit 语义；当前上限包括单文件 2 MiB、最多扫描 5000 个文件、默认 100 条、最大 200 条结果。搜索为只读动作，连接中断后可安全重新发起。

## 5. Shell、Build 与 Job

`shell.exec/run` 和 `build.exec/run` 都使用显式 argv、Windows 绝对 `cwd` 与可选 `runtime={kind:"host"|"wsl",distribution?}`。host 直接执行 argv；WSL 由 Node 调用目标发行版 `wslpath` 映射 cwd，再以 `wsl.exe --cd <mapped> --exec <linux argv...>` 执行，不要求调用方拼接 `/mnt/<drive>`，也不接受嵌套 `wsl.exe` argv。

Windows 不单独暴露 `powershell` 或 `cmd` capability；通过 `shell_run`/`shell.exec` 把解释器作为显式 argv[0] 调用。例如查询时间和时区：

```json
{
  "argv": ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Date; tzutil /g"],
  "cwd": "C:\\"
}
```

同一入口也支持 `pwsh.exe` 或 `cmd.exe`。调用 `shell_run` 成功只代表 Job 已启动，必须继续用 `job_watch` 读到 terminal state。

Node/Direct 启动成功后返回 `jobId/requestId/traceId/runtime/timing`；公网 MCP 默认只在模型可见结果保留 `jobId/runtime` 等业务字段，把 request/trace/timing 放入 `_meta`，显式 `diagnostics=true` 时恢复完整结构化结果。Job timing 只报告实测的 `nodeReceivedAt/processStartedAt/finishedAt/queueMs/runMs`；WSS 会话断开不会自动杀死已启动 Job，调用方通过 `watch/cancel` 读取或终止进程树。Windows WSL keepalive 按发行版去重且总数上限 8，Node 关闭只停止自己创建的进程。

启动类动作必须使用幂等键时由具体 MCP/Capability schema要求；断线后是否重试要按 Job/状态查询结果决定，不能仅根据客户端是否收到响应判断。

## 6. Git

`git.repository` 只暴露固定 action，不接受任意 Git flags 字符串。目标使用绝对 `repositoryPath`，linked worktree 和子目录仍由系统 Git 自己解析仓库事实。

只读 actions：`status`、`diff`、`stagedDiff`、`log`、`show`、`branches`、`currentBranch`、`worktrees`。

写/网络 actions：`add`、`commit`、`fetch`、`pull`、`push`、`createWorktree`、`deleteWorktree`。这些动作进入 mutation audit；网络动作要求当前 OS 用户已有 Git 凭据并使用 idempotency key。Fast Spider 不保存或回传 Git 凭据。

## 7. Artifact 与 Presentation

`artifact.store/uploadFile` 与 `uploadJobLog` 把显式文件/Job 日志上传到 Hub Artifact 存储。MCP 的 `artifact_get` 在上传完成或 `get` 时会优先返回有界原生内容：PNG/JPEG 使用 `ImageContent`，小型文本使用 `EmbeddedResource.text`，其余不超过 8 MiB 的文件使用 `EmbeddedResource.blob`；这样 GPT/Agent 无需再从外部 URL 二次下载，适合无人值守。超过原生回显上限的 Artifact 仍保留元数据与受鉴权下载路径。

`publishFile` 是显式临时附件中转入口：Node 将本机普通文件直接上传到 Hub Temporary Presentation Relay，MCP/Direct 只返回 `url/fileName/contentType/sizeBytes/expiresAt` 元数据，不返回 `ImageContent`、`EmbeddedResource` 或 `ResourceLink`，因此不会把附件再次展开到聊天界面。Browser 页面截图与 OS 截图也统一使用同一 URL-only attachment 输出；新 Node 上传的这类临时资源最长保留 48 小时，由 Hub 每分钟维护任务按 `expiresAt` 自动删除。Relay 不写业务数据库或备份；旧 Node 未携带 resource kind 时仍按 20 分钟 presentation 兼容语义处理。

## 8. Working Context

`working.context` 是 Plan/Task + Markdown Task Workspace，不是长期 AI Memory。旧 `get/set/clear` 只是默认 plan 的兼容包装；所有入口共享同一 Plan 状态和写入路径，结构化状态位于 Node data-dir，并按远程 Machine 路由、`projectPath + planId` 隔离。

Plan actions 为 `plan.init/plan.get/plan.list/plan.sync/task.update`，Markdown actions 为 `markdown.list/markdown.read/markdown.append`，`progress.watch` 提供有界 revision change wait。Markdown 仅允许 projectPath 内普通 `.md`，防 symlink/junction 逃逸；受管区块同步不覆盖 Manual 内容，写入使用 CAS、temp+fsync+atomic replace。上限为 64 文件、512 KiB/文件、4 MiB 总量、500 tasks、32 evidences/task；不保存 Token/Cookie/API Key/完整 Prompt/聊天原文/raw error。`get`/plan 读取还返回实时 Git branch/HEAD/dirty，Git 与文件内容始终是最终事实源。`plan.sync` 写入 Markdown 时，受管 `00-current-state.md` 明确记录 `dirtyBeforeSync`（同步前 Git 快照），而调用结果中的 `currentGit` 会在写入完成后重新读取，表示同步后的实时 Git 事实，避免受 Git 跟踪的 Markdown 对自身 dirty 状态形成自指。

## 9. Browser 与 Screenshot

`browser.automation` 1.2 使用 Node 管理的隔离 Chromium，不附着用户正常浏览器 Profile，也不暴露原始 CDP/Playwright。`readiness` 在 launch 前返回 runtime/sidecar/Chromium 可用性、稳定 reasonCode、缓存命中和耗时；正常动作返回 startup/operation/queue/total timing。`snapshot` 返回 `ariaSnapshot + agentSnapshot + refs`；`click/type/press/wait` 优先直接使用短期 `ref`，新 snapshot、页面导航或元素脱离 DOM 后旧 ref 以 `BROWSER_REF_STALE` 快速失败。`batch` 在 Node 内一次执行 1-32 个固定交互动作并可 `snapshotAfter`，减少 Hub/MCP 往返。浏览器可访问 Node 当前网络可达的公网、localhost 与私网 HTTP(S) 目标，不做逐请求 DNS/pinned-IP 审查；仍拒绝危险 scheme、任意 JavaScript 注入和超出固定 action 的控制。

`screenshot.capture` 只做一次性桌面/显示器/窗口捕获。窗口通过短期 opaque `windowId` 指定，不把 OS 原生句柄暴露给远程客户端。

## 10. Agent Control

当前 `agent.control` action 集合：

```text
routing.status
providers.list
provider.readiness
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
session.callback.register
session.callback.unregister
session.callback.list
session.callback.claim
session.callback.ack
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

Agent Manager 已按 manager/provider/session/routing 边界拆分；静态 Provider Registry 只注册 `codex` 与 `claude_code`，不提供动态反射插件系统。`providers.list` 为每个 Harness 返回自己的 `supportedActions`。`routing.status` 是全局只读 action：读取 CC Switch SQLite SSOT，区分 `direct|cc_switch`、current Provider、model mapping、Takeover/health 与 EffectiveCapabilities；raw provider settings/meta/credential 永不离开 Node。

`provider.readiness` 以 passive/safe 两种模式分别报告 `routeAvailable/providerAvailable/harnessAvailable/sessionBackendAvailable/readyForSessionCreate`，并为每层返回稳定 reasonCode 与耗时；safe 不创建 Session、不发送 Prompt。本地 Codex backend 使用只读 thread/list 检查 session backend；`chatgpt_cloud` 创建不依赖本地 thread/list，因此跳过该探测，只检查路由、Codex app-server 与 ChatGPT 登录，避免分派前无关的十几秒超时。ChatGPT 登录 token 仍由 Codex app-server 的 `getAuthStatus(includeToken=true)` 读取同一份 Codex/ChatGPT 登录签名，但鉴权专用适配器强制使用 Node 管理的 stdio app-server，不继承实验性的外部共享 socket，也不连接 Desktop Bridge；readiness 会把 RPC 超时、明确未登录与其它 RPC 失败分别报告为 `CHATGPT_CLOUD_AUTH_RPC_TIMEOUT`、`CHATGPT_CLOUD_NOT_AUTHENTICATED` 与 `CHATGPT_CLOUD_AUTH_RPC_FAILED`，避免把超时误判成登录缺失。一次成功 readiness 取得的 token 会在内存中短时复用，紧随其后的 dispatch 不再重复调用鉴权 RPC；失败结果不缓存。对 `codex + chatgpt_cloud`，同一 app-server 代次内最近 30 秒的成功 safe 结果还可被紧随其后的创建预检复用；进程代次变化、隔离或超时后不会复用旧结果，失败时顶层 reasonCode 直接保留真实失败层。`session.create` 是 ChatGPT Cloud 的单一创建入口，`mode=complete|quick_chat` 选择等待首个回答或创建后快速返回；公网 MCP 必须携带 12-128 字符幂等键。Node 将包含 mode/model/thinking 的 spec hash 与小型结果持久化到 data-dir，重启后仍能重放同结果，key/spec 冲突或中间态不确定时拒绝重复创建。`models.list` 返回 `defaultModel`、两个 `creationModes`、实时 `modelPresets/thinkingOptions` 与 Node 本机配置的 `advancedModels`；Preset 与 Advanced 都可搭配 quick_chat/complete。Advanced 列表保存在 Node data-dir 的 `chatgpt-advanced-models.json`，不写死在源码或同步 Hub；Auto 不发送独立 thinking，其余实时档位作为 `thinking_effort` 发送。ChatGPT Cloud 默认 `mode=complete`，等待完整 SSE；`mode=quick_chat` 跳过 `/f/conversation/prepare`，默认使用 `model=auto`，收到真实 conversation ID 后立即返回 `phase=running/completionPending=true` 并在后台排空流。完整模式一旦 SSE 已返回 conversation ID，即使后续流结束异常也保存该 ID 并把执行状态标为 unknown，而不是把“已创建”重新解释成“未创建”。Provider 已明确拒绝且确认没有副作用的 create 会立即释放 reservation；真正不确定且没有已知 Session 的记录，须先用显式 `backend=chatgpt_cloud` 的 `session.list` 对账，再以 `session.delete + idempotencyKey + decision=confirm_not_created` 显式释放。

ChatGPT Cloud CHAT 可通过 `session.callback.register` 把 `conversation.turn.complete` 主动送到一个本机 Codex 协调/调度会话。对 `codex_cloud_collaboration`，现有 Cloud CHAT 应优先在同一对话中通过 FastSpider_FS 调用 `event.ingest` → `event.ack`；Node 的 callback inbox 是断线、漏通知或 Cloud CHAT 无法回调时的恢复兜底，不是主完成路径。注册记录与 pending inbox 统一持久化在 Node data-dir 的 `agent/session-callbacks.json`，损坏时 fail-closed。每个 source Cloud CHAT 只有一个 callback owner；generation 升级会清除旧代 pending。Node 优先使用 Provider payload 中真实存在的 event/turn/message ID，并持久保留最近 256 个稳定身份用于跨重连、跨重启去重；更早的极旧重放允许按至少一次语义再次送达。Provider 未给稳定 ID 时，仅用 payload 派生键做 15 秒短窗口重复抑制，不把它永久记录为事件身份，因此相同 payload 在后续合法 Turn 仍可再次送达。每个事件同时包含持久递增的本地 `event_sequence`，本地 sequence 也作为 cursor。同一协调会话的多个 Cloud CHAT 完成结果保存在同一队列；`session.callback.list` 返回有界列表和固定 queue text，`session.callback.claim` 一次最多领取 64 条，领取租约为 5 分钟，处理并验证 Result/本地交付物后用 `session.callback.ack` 批量确认。领取租约超时会自动释放；重复 claim/ack 按持久状态对账。

实时 callback 到达时只入队并唤醒协调会话；Node 每约 30 秒检查一次本地队列，用于释放过期领取和发送轻量通知，不读取 Cloud CHAT 状态。pending 最早存在约 5 分钟且目标会话空闲时才发第一次 nudge，此后同一目标最多约每 10 分钟再通知一次，nudge 不携带任务结果正文。对 Provider 的 Cloud CHAT 状态查询另以约 10 分钟低频执行，只用于恢复漏掉的完成回调。`codex_cloud_collaboration` 对疑似卡住的 CHAT 先 `status.poll`；确认仍在运行时由 `chat.continue` 发送固定“请继续”，在观察到新进展前不重复发送；出现新进展会清零恢复计数，仍无进展时交给调度/主控决定是否人工接手或换代，不自动创建替代 CHAT。回调、状态检查或继续操作暴露的问题应通过 `working_context markdown.append` 以文件 revision CAS 追加到 `docs/progress/04-open-issues.md`；若读取返回 `NOT_FOUND`，先调用 `plan.init` 且设置 `initializeMarkdown=true` 初始化工作区，再读取并追加。不得写入密钥、原始 Provider payload、完整聊天记录或长日志。

Session 可见性契约由 `visibility`、`backend`、`visibilityTarget` 独立组成。`visible` 默认映射到 provider 的本地 backend，并返回 `externalId/externalIdType`；`internal` 默认不发布目标，Codex 默认请求 ephemeral Thread，并从 Fast Spider 的普通 `session.list` 过滤。持久 internal Thread 仍可能被其他 Codex 客户端列出，API 返回 `visibilityGuarantee=not_guaranteed` 而不是宣称绝对不可见。`backend=chatgpt_cloud`（`providerId=codex`）用 Codex app-server 的 ChatGPT 登录态 + 自解 Sentinel 走官方 `/backend-api/f/conversation` 创建云端会话，`externalIdType=chatgpt_conversation`，会话出现在账号的 ChatGPT 聊天列表；必须 `visibility=visible`，且依赖 app-server 已登录。FS 创建的 Cloud 会话会在 visibility sidecar 中保存 backend 与 workingDirectory，后续 `session.get/send/watch/result/delete/...` 只传 `sessionId` 即可自动路由回 `chatgpt_cloud`。普通 Codex `session.list` 会合并当前 FS 管理的 Cloud 会话，但不会遍历账号全部 ChatGPT 历史；显式 `backend=chatgpt_cloud` 才查询完整云端列表。

CC Switch 使用 `PRAGMA table_info` 对唯一支持 schema 计算 fingerprint 并 fail-closed；不支持时返回 `available=false/reason=unsupported_schema`，不猜旧 schema。进程内 bounded TTL 为 route 约 1.5 秒、CLI version/auth 45 秒、models 20 秒；Codex/Claude/CC Switch 独立 discovery 并行且只读，不触发模型生成。

### Codex

Codex Adapter 直接运行本机 `codex app-server --stdio`。Provider 凭据与 ChatGPT/Codex 本地认证只留在 Node 本机。Codex 的 Harness model catalog 与 CC Switch upstream route 分开返回，不把两层模型名称强行合并。

`session.create` / `session.send` 使用 Codex 原生 Turn `UserInput`：text、skill、image、localImage、mention；`session.steer` 使用同一输入集合，但只追加到调用方明确给出的 active `turnId`，不隐式修改 model/cwd/outputSchema。Skill 输入使用 `name + absolute path`，不是把 Skill 内容拼接进 prompt。图片可统一指定 `imageDetail=auto|low|high|original`。`outputSchema` 作为有界 JSON Schema 直接传给 `turn/start`，用于约束该 Turn 最终 assistant message；`session.result` 仍保留真实 raw `finalAgentMessage`，不伪装为已完成 typed decode。

发现面还包括 `provider.capabilities`（当前 Provider 的 webSearch/imageGeneration/namespaceTools）、`hooks.list`（含 enabled/source/trustStatus）、`permissions.list`、`plugins.installed` 和有界摘要形式的 `mcp.status.list`。MCP 状态只返回 server/auth/tool names/resource 摘要，不把每个 Tool 的完整 JSON Schema 通过 WSS 重复搬运。

线程管理边界：

- `session.unarchive/delete/fork/compact` 映射 Codex 原生 thread API；`session.delete` 先持久化 delete intent，再删除 Provider Session，最后回收 create 幂等记录，因此删除成功但本地落盘失败时可安全续做。
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

Hub 对连接中断返回结构化 `CONNECTION_LOST`，对 deadline 返回 `DEADLINE_EXCEEDED`。只读查询类能力可以声明 retryable；会产生副作用的文件编辑、Job 启动/取消、Git 写/网络、Build、Browser 操作、Agent Turn/Thread/Goal/Settings/Review 变更不能宣称可无脑重试。`session.create` 是例外：入口强制稳定 `idempotencyKey`，超时后只能使用原 key 和原参数重放以对账已有结果，不能生成新 key 重试。

当前 Agent 中可安全重试的读取包括 Provider/Model/Project/Skill/Hook/Permission/Plugin/MCP 状态 discovery、Session list/get/watch/result、Callback list 和 Goal get。带稳定幂等键的 `session.create` 可以按原请求重放；Thread/Goal/Settings/Review/Turn 变更以及 `session.steer/respond`、callback register/unregister 进入 mutation audit，其他变更在连接中断后不能自动重放。callback 写操作响应丢失时先用 `session.callback.list` 对账 owner 与 generation。

WSS 不做写操作自动重放：完整请求未到 Node 前断线则不会执行；请求已经执行但响应丢失时，调用方先重新读取状态，再决定下一步。
