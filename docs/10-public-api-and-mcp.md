# 公共 API 与 MCP（Current）

## 公网 MCP

Fast Spider MCP 通过 `/mcp` 提供 Streamable HTTP，使用标准 OAuth Authorization Code + PKCE。当前固定 22 个工具：

```text
machine_list
machine_get
capability_list
audit_log
operation_log
file_read
code_search
file_edit
shell_run
job_watch
job_cancel
git_control
build_control
browser_control
screenshot_take
thinking_team
ai_control
codex_cloud_collaboration
codex_cloud_completion
artifact_get
result_get
working_context
```

Current 不提供目录列表工具；`audit_log` 只读查询 Hub 本地 `audit_entries`，始终按当前 MCP Owner 隔离，不依赖 Node 在线，也不开放给 Direct Access Key；`operation_log` 必须带 `machineId`，只读查询当前 Owner 所有且在线 Node 的近期有界操作事件，使用 `level/category/limit/before` 过滤和游标分页，并省略本地路径、消息、IP 与 Extra 字段；`thinking_team` 是调用侧只读角色协作工具，`working_context` 继续提供项目 Plan/Task + Markdown Task Workspace。

`thinking_team` 不需要 `machineId`，只返回 9 个部门、17 个角色、角色指令、协作流程和 `working_context` 资料室协议；`providerInvocation=false`，不会创建本机 AI Session。

`ai_control` 现在是 Provider-neutral 多 AI Harness 控制面。`provider.readiness` 以安全只读预检区分 route/provider/harness/session backend/create readiness；`routing.status` 独立只读 CC Switch SSOT；`providers.list` 当前发现 `codex` 与 `claude_code` 并返回各自 `supportedActions`。`providerId` 决定 Harness，CC Switch Route 另行说明 `direct|cc_switch`、真实 Provider/model mapping 与 EffectiveCapabilities；客户端模型 alias 不等于 upstream model。

Codex 保留 Provider/Model、Skills/Hooks/Permission Profiles/Plugins/MCP discovery、Thread/Goal/Settings/Review、原生多类型 Turn、`outputSchema`、steer/respond 和 app-server auto-resume。Claude Code 第一版提供 models/capabilities 与 session list/get/create/send/watch/cancel/result/rename/archive/unarchive，使用原生 UUID + `stream-json` + `--resume`，Prompt 经 stdin。FS 不映射 Codex 的 `fs/*`/`command/exec/*`/`mcpServer/tool/call`，也不提供 CC Switch Provider/Token/Takeover 写入或 Claude permission bypass 第二执行链。

Windows Node UI 首次启动由本机配置选择 Codex 会话模式：共享模式（推荐）不附加 Codex Desktop owner/control bridge，FS 接管模式才启用它。该本机选择优先于环境变量；无 Node UI 配置的 headless 进程仍可用 `FAST_SPIDER_CODEX_DESKTOP_BRIDGE=0` 关闭默认 bridge。FS 原有的 app-server 创建和执行路径不变。公开 MCP 的 `ai_control` 可通过 `providers.list`、`provider.readiness` 读取 `desktopBridge` 状态。

## ChatGPT 调用与工具发现

Hub 的 MCP `initialize` 会返回不超过 2 KiB 的常驻能力地图，并把 Server Title 固定为 `FastSpider_FS`。能力地图只说明十类能力、第一步和固定安全链路，不复制完整 Schema、参数示例或错误表。当 ChatGPT 已选择该 App 或用户显式 `@FastSpider_FS` 时，调用侧应先尝试工具而不是仅依据界面文本判断“插件未加载”：连接测试使用只读 `capability_list(view=overview)` + `machine_list`；需要本机操作但尚无 `machineId` 时先调用 `machine_list`。

所有 machine-bound MCP 工具支持可选 `diagnostics`。省略或传 `false` 时使用稳定的紧凑结果：`structuredContent` 不重复携带 transport `requestId/traceId/callRequestId/callTraceId`、通用 `timing`、搜索 elapsed 和 readiness 检查时间；这些诊断事实移到结果 `_meta.fastSpider/diagnostics`，不会进入模型对话正文。传 `diagnostics=true` 才把同一批字段同时保留在结构化结果中，供单次排障。`machineId/providerId/sessionId/jobId/turnId`、cursor、hash、state/error/exitCode、URL/expiry 等业务续作与校验事实始终保留；原生 Artifact 内容不受影响。Hub-local 工具也只返回一次 `structuredContent`，不再把同一 JSON 自动复制到文本 `content`。

公网 MCP 的 `machine_list` 使用稳定的管理备注/显示名/ID 顺序分页，默认 `limit=20`、最大 50；`hasMore=true` 时把返回的 `nextCursor` 原样传给下一页。它默认省略每台机器重复的完整 capability descriptors，只返回发现和选机需要的 machine/online/runtime/version 等事实；确需在同一响应展开时传 `includeCapabilities=true`，否则使用 `machine_get` 或 `capability_list` 读取详细能力。底层只查询当前页，默认页也不读取数据库 capability 列表。

诊断投影只属于 MCP Adapter；Direct API 和 Hub↔Node capability 契约继续返回完整诊断字段。Direct 无参数 `machine_list` 也保持旧的完整返回；Direct 调用方可显式使用同一组 `limit/cursor/includeCapabilities` 取得有界页。MCP 为 `Stateless`，因此不使用“前 N 次详细、后续自动省略”或按客户端类型猜测的进程内计数：重连、重试、并发、多实例和重启都不会改变响应形状。调用侧缓存若存在，必须把 `diagnostics` 纳入 key，并继续按 Owner/Machine/Session 隔离。

`capability_list` 是唯一按需指南入口，没有新增第 19 个 guide/help 工具：

- 省略 `machineId` 和 `view`：兼容返回 Hub Capability Catalog，并附带精简 overview。
- 提供 `machineId`、省略 `view`：保持旧行为，只返回该 Machine 的能力目录。
- `view=catalog`：显式返回 Hub 或指定 Machine 的能力目录。
- `view=overview`：返回能力分类、22 个 MCP 工具的一句话 `toolSummaries`、底层 `capabilitySummaries`、黄金规则和推荐下一步；摘要足够选择入口，不复制完整 capability actions 或 Schema。
- `view=catalog` 或省略 view 的兼容入口保留原始 `capabilities` 完整 actions；`view=overview` 的 `capabilities` 为空数组，以避免把同一目录重复塞进上下文。
- `view=capability`：必须提供底层 `capabilityId`（例如 `shell.exec`），返回该 capability 的 actions、语义和对应 MCP 工具。
- `view=tool|workflow|error`：必须提供 `name`，一次只返回一个工具、流程或真实稳定错误码的有界指南；未知 view/name 明确拒绝。

工具指南固定包含 `whenToUse/requiredInputs/safeSequence/returns/recommendedNext/commonErrors/boundedExamples`；`view=capability` 额外返回底层 capability 的 actions、summary 和 `mcpTools` 映射。overview 不超过 8 KiB，单项指南不超过 12 KiB；示例有界且不含凭据、Prompt、Cookie、环境变量或本机事实。注册工具名、指南目录、overview 摘要和本文工具列表由自动测试对账。

Codex/Claude Code 的会话能力不是独立顶层工具；统一位于 `ai_control`。查询 Codex 会话列表使用 `action=session.list`，后续读取使用 `session.get/session.watch/session.result`。`ai_control` 的 `session.create` 已支持 Codex 的 visible ChatGPT cloud CHAT 会话：传 `providerId=codex`、`backend=chatgpt_cloud`、`visibility=visible`、首条 `prompt`、绝对 `workingDirectory` 和 `idempotencyKey`，要求本机 Codex app-server 已登录 ChatGPT。创建返回模式只有两个：`quick_chat` 跳过 prepare，拿到真实 `sessionId` 即返回；`complete` 等待首个回答。Cloud 创建会在副作用前保存 Provider message ID，在首次观察到 conversation ID 时立即保存；传输断开后仍在原 deadline 内收口，同一原 key 可安全重放或精确对账，不按 Prompt 猜测。省略 mode/model/thinking 时使用 Node 本地配置中的默认值，旧配置保持 complete + Auto + Auto；请求明确传入的值优先。模型配置与返回模式正交：预设配置从实时 `modelPresets` 选择组合，高级配置从 Node 本机 `chatgpt-advanced-models.json` 形成的 `advancedModels` 选择模型；`thinkingOptions` 每次从实时 ChatGPT 模型目录提取，两者均可搭配 quick_chat/complete。`session.result` 支持 `resultMode=manifest|result-id`，只返回 Result 状态、ID、大小和 SHA-256，不回传 Cloud CHAT 正文。FS 创建后的 cloud conversation 会保存 backend sidecar，后续按 `sessionId` 自动路由，不需要调用方反复携带 `backend=chatgpt_cloud`。因此 ChatGPT App 工具页只显示 `Ai control` 属于正常设计。

长任务可用同一个 `ai_control` 为普通可见 ChatGPT Cloud CHAT 注册 Node 兜底观察：传 `sessionId + callbackTargetSessionId + callbackMissionId + callbackTaskId + callbackGeneration`。注册先持久化并立即返回，目标 Codex 查询、Provider 状态读取和 realtime watcher 建立全部退出同步分派链路。在 `codex_cloud_collaboration` 中，现有 Cloud CHAT 是主回调方：先把完整结果写到任务预登记的本地 `resultPath`，再调用强类型 `codex_cloud_completion(action=notify, collaborationId, taskId, actorSessionId=$self, outcome)`；通知不携带正文、路径、hash、Result ID、revision、generation 或 event ID。Hub 从已登记任务派生这些事实并写入 SQLite 持久队列。Dispatcher 通过同一工具 `claim` 一次领取最多 64 条，5 分钟租约超时自动释放；验收固定本地结果或兼容 Result Pool manifest 后，以 `ack` 一次确认整批。通知、claim、ack 在 Hub/Node/进程或整机正常重启后均可恢复。Node 的 `session.callback` 队列、`tick/status.poll` 只作为断线、漏通知或 Cloud CHAT 无法回调时的恢复兜底，并使用与 Hub 相同的 canonical notification ID；约每 30 秒只维护本地队列/租约，pending 约 5 分钟后才首次 nudge，重复 nudge 与 Provider 补漏查询间隔约 10 分钟。重复 notify、claim 或 ack 必须按幂等已处理。

`codex_cloud_collaboration`（“Codex 云端协作”）是显式启用的 Hub 原生持久协作面，与普通 Chat 直接使用 FS 文件、Shell、Git、浏览器或 `ai_control` 完全分开。新建协作时必须验证两个不同的本地 Codex 主控/调度会话，并确认本机 Codex app-server 可用且已登录 ChatGPT；每次分派子 CHAT 前再次预检，失败时不会回退到 Claude 或其它 AI。它保存目标、任务、父子 Cloud CHAT、事件、决策、租约、预算、调度时间和换代摘要，并允许已登记 Cloud CHAT 在父级授权交集内创建子 Cloud CHAT。服务端强制调度租约、深度、并发、创建预算、截止时间、允许动作和写入范围；`task.dispatch` 固定创建 `backend=chatgpt_cloud + visibility=visible + externalIdType=chatgpt_conversation` 的账号级普通 ChatGPT conversation。每个任务都有固定本地 `resultPath`；已有业务交付路径时复用该路径，否则使用工作目录内 FS 管理的隐藏结果文件。该受管结果文件是只读任务唯一允许的控制面写入，不会扩大其对其它项目文件的权限。现有 Cloud CHAT 完成后只调用 `codex_cloud_completion notify`，Hub 持久入队后即返回；它不受 collaboration 全局 revision、Dispatcher lease 或最长任务时长影响。Dispatcher 批量 claim 后负责 `file_read(statOnly=true)` 或兼容 Result Pool 验收，再 ack；ack 先持久完成状态，归档作为独立异步恢复动作，不会拖慢或撤销确认。对疑似卡住的 CHAT，调度先 `status.poll`；若仍在运行则 `chat.continue` 发送固定“请继续”，在观察到新进展前不重复发送；仍卡住时由主控决定是否人工接手或换代，服务不自动新建替代 CHAT。回调、状态检查或继续过程暴露的问题应通过 `working_context markdown.append` 以 CAS 写入 `docs/progress/04-open-issues.md`；若读取返回 `NOT_FOUND`，先调用 `plan.init` 并设置 `initializeMarkdown=true`。不得保存密钥、原始 payload 或完整聊天记录。完成默认归档；删除必须先有绑定精确任务的批准决定。

ChatGPT 对已发布 MCP App 的工具/输入定义可能使用经批准的快照；当 FS 修改工具名、Schema 或工具描述后，需要在 ChatGPT App/Action 管理中执行 Refresh/重新批准才能取得新的定义。纯服务可用性仍以真实 MCP initialize/tools/list 和只读调用结果为准。仓库没有独立 ChatGPT App manifest 或第二套 Plugin metadata，第一层事实源继续是 MCP initialize、工具描述和 `capability_list`。

## 资源模型

所有需要操作本机的工具首先使用 `machineId`。文件系统和进程位置直接使用本机绝对路径：

- file_read/file_edit: `path`
- code_search: `path`
- shell_run/build_control: `cwd`
- git_control: `repositoryPath`；`fetch/pull/push` 的 `remote` 可省略，Node 按“当前分支 upstream remote → origin → 唯一 remote”顺序解析，多个非 origin remote 时 fail-closed 要求显式指定
- ai_control session.create: `providerId + workingDirectory`; 可选 `visibility=visible|internal`、`backend=codex_local|claude_local|chatgpt_cloud`、`visibilityTarget=codex_local|claude_local|chatgpt_cloud|none`、`ephemeral`，以及 ChatGPT Cloud 的 `mode=complete|quick_chat`、`model`、`thinking`; `session.result` 另支持 `resultMode=manifest|result-id`; 高级配置不会新增第三种 mode
- codex_cloud_completion notify: `collaborationId + taskId + actorSessionId=$self + outcome`；Hub 绑定任务、CHAT、generation、固定结果路径和 canonical notification ID
- codex_cloud_completion claim/ack: Dispatcher `actorSessionId`；claim 可选 `limit<=64/claimId`，ack 使用同一 `claimId` 并为每条通知提供验收元数据
- ai_control session.callback.register: ChatGPT Cloud source `sessionId` + 本机 Codex `callbackTargetSessionId` + `callbackMissionId/callbackTaskId/callbackGeneration`，仅为低频 Node 兜底
- ai_control session.callback.claim/ack: `callbackTargetSessionId`; claim 可选 `callbackClaimLimit<=64` 或原 `callbackClaimId` 幂等续领，ack 使用返回的 `callbackClaimId`
- ai_control routing.status: 可选 `appType=claude|codex|claude-desktop`，只读 CC Switch 路由事实
- working_context: `projectPath`

`session.create` 的 `visible` 默认目标是本地 provider backend；`internal` 默认目标为 `none`，Codex 默认 ephemeral。`providerId=codex` + `backend=chatgpt_cloud` 用 Codex app-server 的 ChatGPT 登录态 + 自解 Sentinel 走官方 `/backend-api/f/conversation` 创建云端会话（`externalIdType=chatgpt_conversation`，会话出现在账号的 ChatGPT 聊天列表）；必须 `visibility=visible`，依赖本机 Codex app-server 已登录。`mode=quick_chat` 返回 `phase=running`、`createMode=quick_chat`、`completionPending=true`，随后用 `session.get/watch/result` 读取进度或结果。普通 Codex `session.list` 只合并 FS sidecar 中已管理的 cloud conversation，不拉取账号全部 ChatGPT 历史；显式 `backend=chatgpt_cloud` 的 Provider 读取失败时返回 `incomplete=true`、`authoritative=false` 的 sidecar 已知项，不能把缺失项解释为未创建。

`working_context` 保留 `get/set/clear` 默认 plan 兼容入口，并在同一工具中提供 `plan.init/plan.get/plan.list/plan.sync/task.update/markdown.list/markdown.read/markdown.append/progress.watch`。Plan 状态在 Node data-dir 中按 `projectPath + planId` 隔离；Markdown workspace 只操作项目内受绑定普通 `.md` 与受管区块，不保存聊天原文或凭据。

`code_search` 2.1 同一工具支持 content/files、glob/context，并返回 Managed ripgrep/native fallback 的稳定原因、扫描统计与分段耗时。`file_read` 2.0 同一工具支持 byte/line/head/tail/around/stat selectors；`file_edit` 2.1 的 mutation 仅返回固定元数据，preview 才返回 bounded diff。preview 不写盘并可安全重试；其余文件写 action 使用 CAS/原子替换且不自动重放。`shell_run/build_control` 可选 `runtime.kind=host|wsl`。

调用方必须遵守[调用方约束规格](caller-constraints.zh-CN.md)：一次浏览器验收只拥有一个 Browser Session，并在成功、失败或取消路径调用 `close`，由 Node 立即删除对应 Session 目录；调用方为测试或编译显式创建的临时目录、测试二进制和中间文件，必须在 Job 进入终态后按已记录的精确路径清理，不得扩大到未知目录、用户产物或项目根目录。

Job 后续操作只用 machineId + jobId。Artifact 获取只用 artifactId；Node 上传本机文件时使用 machineId + absolute path。`artifact_get.uploadFile/uploadJobLog/get` 优先回显有界原生 MCP 内容：PNG/JPEG 使用 `ImageContent`，小型 UTF-8 文本使用 `EmbeddedResource.text`，其余不超过 8 MiB 的内容使用 `EmbeddedResource.blob`；空内容只返回结构化元数据，不生成 malformed resource。`artifact_get.publishFile` 使用同样的绝对路径，把文件直接上传 Hub Temporary Presentation Relay 并切换到 `attachment` 资源类型；Browser screenshot 与 `screenshot_take` 也统一走 attachment。MCP 与 Direct 对这些临时附件只返回 `url/fileName/contentType/sizeBytes/expiresAt`，不返回原生图片/blob 或 `ResourceLink`，最长 48 小时后由 Hub 自动删除。旧 Node 未发送 resource kind 时 Hub 继续按 20 分钟 presentation 兼容处理。

## OAuth

OAuth resource 是公开 MCP URL。授权页复用 Owner Web Session，用户点击允许/取消。授权页 CSP 只允许当前经过后端验证的 callback origin，避免开放任意表单跳转。

Hub 在 loopback 后由 Nginx 反代时，MCP SDK 自带 localhost Host 防护关闭；Fast Spider 自己的 `AllowedHosts` + 反向代理仍负责 Host 边界，避免把合法公网 Host 误判为 DNS rebinding。

## Node HTTP/WSS

Node 首次登记使用 Connection Token 调用机器登记接口；运行时只使用设备凭据/WSS。Connection Token 不具备 MCP 权限，也不作为长期设备凭据。

## 错误原则

- 相对路径在要求本机路径的接口上返回 `ABSOLUTE_PATH_REQUIRED`。
- OS 拒绝访问时返回权限/系统错误，不伪装成目录授权失败。
- MCP 不再返回旧目录对象的不存在、禁用或越界错误。
