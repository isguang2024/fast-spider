# 公共 API 与 MCP（Current）

## 公网 MCP

Fast Spider MCP 通过 `/mcp` 提供 Streamable HTTP，使用标准 OAuth Authorization Code + PKCE。当前固定 21 个工具：

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
artifact_get
result_get
working_context
```

Current 不提供目录列表工具；`audit_log` 只读查询 Hub 本地 `audit_entries`，始终按当前 MCP Owner 隔离，不依赖 Node 在线，也不开放给 Direct Access Key；`operation_log` 必须带 `machineId`，只读查询当前 Owner 所有且在线 Node 的近期有界操作事件，使用 `level/category/limit/before` 过滤和游标分页，并省略本地路径、消息、IP 与 Extra 字段；`thinking_team` 只返回调用侧角色指导，`working_context` 只保存每个项目一段普通文本。

入口按交付方式选择，而不是按任务看起来“简单”还是“复杂”：

| 用户意图 | 公开入口 | 调用方行为 |
|---|---|---|
| 直接操作本地文件、命令、Git、浏览器 | 对应的 `file_*` / `shell_run` / `git_control` / `browser_control` | 在当前会话内完成 |
| 直接查看、创建或续发 AI 会话，并由当前调用方交互式读取 | `ai_control` | 只有明确需要同步观察时才使用 `session.get/watch/result` |
| 创建或续发 Cloud CHAT，结果稍后回到 Codex | `codex_cloud_collaboration action=dispatch` | 提供 `callbackSessionId`；FS 创建或复用一个 CHAT、发送任务并登记回调，成功后立即结束本轮 |
| CHAT 通知完成 | `codex_cloud_collaboration action=completion.notify` | Hub 先持久化，再主动推入 Node 队列；Node 在目标空闲时立即唤醒 `callbackSessionId`，忙时等当前 Turn 结束后重试 |
| 展示或临时分享已有文件 | `artifact_get` | 只有明确需要展示或临时 URL 时才上传，不作为协作回调通道 |

`thinking_team` 不需要 `machineId`，只返回 9 个部门、17 个角色、角色指令、协作流程和“普通文本项目上下文”建议；`providerInvocation=false`，不会创建本机 AI Session。

`ai_control` 现在是 Provider-neutral 多 AI Harness 的直接会话与生命周期控制面，不是“创建 CHAT 后等待回调”的入口。`provider.readiness` 以安全只读预检区分 route/provider/harness/session backend/create readiness；`routing.status` 独立只读 CC Switch SSOT；`providers.list` 当前发现 `codex` 与 `claude_code` 并返回各自 `supportedActions`。`providerId` 决定 Harness，CC Switch Route 另行说明 `direct|cc_switch`、真实 Provider/model mapping 与 EffectiveCapabilities；客户端模型 alias 不等于 upstream model。

Codex 保留 Provider/Model、Skills/Hooks/Permission Profiles/Plugins/MCP discovery、Thread/Goal/Settings/Review、原生多类型 Turn、`outputSchema`、steer/respond 和 app-server auto-resume。Claude Code 第一版提供 models/capabilities 与 session list/get/create/send/watch/cancel/result/rename/archive/unarchive，使用原生 UUID + `stream-json` + `--resume`，Prompt 经 stdin。FS 不映射 Codex 的 `fs/*`/`command/exec/*`/`mcpServer/tool/call`，也不提供 CC Switch Provider/Token/Takeover 写入或 Claude permission bypass 第二执行链。

Windows Node UI 首次启动由本机配置选择 Codex 会话模式：共享模式（推荐）不附加 Codex Desktop owner/control bridge，FS 接管模式才启用它。该本机选择优先于环境变量；无 Node UI 配置的 headless 进程仍可用 `FAST_SPIDER_CODEX_DESKTOP_BRIDGE=0` 关闭默认 bridge。FS 原有的 app-server 创建和执行路径不变。公开 MCP 的 `ai_control` 可通过 `providers.list`、`provider.readiness` 读取 `desktopBridge` 状态。

## ChatGPT 调用与工具发现

Hub 的 MCP `initialize` 会返回不超过 2 KiB 的常驻能力地图，并把 Server Title 固定为 `FastSpider_FS`。能力地图只说明十类能力、第一步和固定安全链路，不复制完整 Schema、参数示例或错误表。当 ChatGPT 已选择该 App 或用户显式 `@FastSpider_FS` 时，调用侧应先尝试工具而不是仅依据界面文本判断“插件未加载”：连接测试使用只读 `capability_list(view=overview)` + `machine_list`；需要本机操作但尚无 `machineId` 时先调用 `machine_list`。

所有 machine-bound MCP 工具支持可选 `diagnostics`。省略或传 `false` 时使用稳定的紧凑结果：`structuredContent` 不重复携带 transport `requestId/traceId/callRequestId/callTraceId`、通用 `timing`、搜索 elapsed 和 readiness 检查时间；这些诊断事实移到结果 `_meta.fastSpider/diagnostics`，不会进入模型对话正文。传 `diagnostics=true` 才把同一批字段同时保留在结构化结果中，供单次排障。`machineId/providerId/sessionId/jobId/turnId`、cursor、hash、state/error/exitCode、URL/expiry 等业务续作与校验事实始终保留；原生 Artifact 内容不受影响。Hub-local 工具也只返回一次 `structuredContent`，不再把同一 JSON 自动复制到文本 `content`。

公网 MCP 的 `machine_list` 使用稳定的管理备注/显示名/ID 顺序分页，默认 `limit=20`、最大 50；`hasMore=true` 时把返回的 `nextCursor` 原样传给下一页。它默认省略每台机器重复的完整 capability descriptors，只返回发现和选机需要的 machine/online/runtime/version 等事实；确需在同一响应展开时传 `includeCapabilities=true`，否则使用 `machine_get` 或 `capability_list` 读取详细能力。底层只查询当前页，默认页也不读取数据库 capability 列表。

诊断投影只属于 MCP Adapter；Direct API 和 Hub↔Node capability 契约继续返回完整诊断字段。Direct 无参数 `machine_list` 也保持旧的完整返回；Direct 调用方可显式使用同一组 `limit/cursor/includeCapabilities` 取得有界页。MCP 为 `Stateless`，因此不使用“前 N 次详细、后续自动省略”或按客户端类型猜测的进程内计数：重连、重试、并发、多实例和重启都不会改变响应形状。调用侧缓存若存在，必须把 `diagnostics` 纳入 key，并继续按 Owner/Machine/Session 隔离。

`capability_list` 是唯一按需指南入口，没有新增独立的 guide/help 工具：

- 省略 `machineId` 和 `view`：兼容返回 Hub Capability Catalog，并附带精简 overview。
- 提供 `machineId`、省略 `view`：保持旧行为，只返回该 Machine 的能力目录。
- `view=catalog`：显式返回 Hub 或指定 Machine 的能力目录。
- `view=overview`：返回能力分类、21 个 MCP 工具的一句话 `toolSummaries`、底层 `capabilitySummaries`、黄金规则和推荐下一步；摘要足够选择入口，不复制完整 capability actions 或 Schema。
- `view=catalog` 或省略 view 的兼容入口保留原始 `capabilities` 完整 actions；`view=overview` 的 `capabilities` 为空数组，以避免把同一目录重复塞进上下文。
- `view=capability`：必须提供底层 `capabilityId`（例如 `shell.exec`），返回该 capability 的 actions、语义和对应 MCP 工具。
- `view=tool|workflow|error`：必须提供 `name`，一次只返回一个工具、流程或真实稳定错误码的有界指南；未知 view/name 明确拒绝。

工具指南固定包含 `whenToUse/requiredInputs/safeSequence/returns/recommendedNext/commonErrors/boundedExamples`；`view=capability` 额外返回底层 capability 的 actions、summary 和 `mcpTools` 映射。overview 不超过 8 KiB，单项指南不超过 12 KiB；示例有界且不含凭据、Prompt、Cookie、环境变量或本机事实。注册工具名、指南目录、overview 摘要和本文工具列表由自动测试对账。

Codex/Claude Code 的会话能力不是独立顶层工具；统一位于 `ai_control`。查询 Codex 会话列表使用 `action=session.list`，后续读取使用 `session.get/session.watch/session.result`。`ai_control` 的 `session.create` 已支持 Codex 的 visible ChatGPT cloud CHAT 会话：传 `providerId=codex`、`backend=chatgpt_cloud`、`visibility=visible`、首条 `prompt`、绝对 `workingDirectory` 和 `idempotencyKey`，要求本机 Codex app-server 已登录 ChatGPT。创建返回模式只有两个：`quick_chat` 跳过 prepare，拿到真实 `sessionId` 即返回；`complete` 等待首个回答。Cloud 创建会在副作用前保存 Provider message ID，在首次观察到 conversation ID 时立即保存；传输断开后仍在原 deadline 内收口，同一原 key 可安全重放或精确对账，不按 Prompt 猜测。省略 mode/model/thinking 时使用 Node 本地配置中的默认值，旧配置保持 complete + Auto + Auto；请求明确传入的值优先。模型配置与返回模式正交：预设配置从实时 `modelPresets` 选择组合，高级配置从 Node 本机 `chatgpt-advanced-models.json` 形成的 `advancedModels` 选择模型；`thinkingOptions` 每次从实时 ChatGPT 模型目录提取，两者均可搭配 quick_chat/complete。`session.result` 支持 `resultMode=manifest|result-id`，只返回 Result 状态、ID、大小和 SHA-256，不回传 Cloud CHAT 正文。FS 创建后的 cloud conversation 会保存 backend sidecar，后续按 `sessionId` 自动路由，不需要调用方反复携带 `backend=chatgpt_cloud`。因此 ChatGPT App 工具页只显示 `Ai control` 属于正常设计。

会话 ID 是可复用地址，不绑定最初创建它的 Codex、CHAT 或 collaboration。用户明确给出 Codex/CHAT ID 时，调用方先验证并只使用该 ID；本地 Codex 可用 `session.get(metadataOnly=true)` 快速核对，空闲后再 `session.send`，显式目标忙时返回/等待 `AGENT_SESSION_BUSY`，不得偷偷新建替代会话。用户没有给 ID 且任务与当前上下文无关联时，应创建全新会话，禁止先 `session.list`、搜索或猜测旧会话；只有用户明确要求查找、列举或挑选历史会话时才调用列表。当前 Codex 或 CHAT 自身能完成任务时直接完成，Cloud CHAT 只是可选协作者，不是代码任务的强制前置步骤。ChatGPT Cloud 的既有会话续发同样支持 `mode=quick_chat|complete`：quick 在 Provider 接受后返回，并可带稳定 `idempotencyKey` 以固定 message ID 对账和恢复；complete 等待本轮响应。

需要稍后回调的任务统一使用 `codex_cloud_collaboration action=dispatch`。公开调用只需给出 `machineId`、现有本地 Codex 的 `callbackSessionId`、`workingDirectory`、任务 `prompt` 和稳定 `idempotencyKey`；可选 `targetSessionId` 表示只续发那个可见 CHAT，省略则新建一个可见 `quick_chat`。FS 不再要求调用方先创建 controller/dispatcher、抢 lease、增加 goal/task 再 dispatch；一个主控、主控加协调者或单 AI 都使用同一条协议，角色关系留在调用方上下文里。

CHAT 收到的是一份直接任务说明，可按范围使用 Fast Spider 的文件、Shell、Git、浏览器和测试能力。完成前使用同一个 `codex_cloud_collaboration` 的 `completion.notify` 回传短文本、固定本地文件槽或纯状态。Hub 先把完成通知写入持久队列，再调用 Node 的内部 callback 入队动作；Node 本地落盘后，目标 Codex 空闲就立即收到唤醒，目标正忙则保留到当前 Turn 结束后再投递。dispatch 返回 `callerShouldYield=true` 与 `nextAction=end_turn` 后，调用方结束当前 Turn 等待回调，不用 `session.get/watch/result` 轮询。

正常链路是 `CHAT completion.notify → Hub 持久化 → Node 主动唤醒 Codex → Codex claim/ack`。Provider realtime、启动时对账、低频状态读取和外部定时查询只在通知缺失或链路中断时补漏，不能当作正常完成通道；尤其不能假设以后新建的 CHAT/任务一定已经被某个定时器纳入查询。Hub/Node 内部的 callback 注册、generation、CAS、幂等、claim/ack、重启恢复和归档/释放只是同一 dispatch 链路的可靠性实现，不是第二套面向 AI 的协作流程。问题、目标和进度若需要跨 AI 复用，只在 `working_context` 中维护一段简短文本；简单任务无需建立资料室。

ChatGPT 对已发布 MCP App 的工具/输入定义可能使用经批准的快照；当 FS 修改工具名、Schema 或工具描述后，需要在 ChatGPT App/Action 管理中执行 Refresh/重新批准才能取得新的定义。纯服务可用性仍以真实 MCP initialize/tools/list 和只读调用结果为准。仓库没有独立 ChatGPT App manifest 或第二套 Plugin metadata，第一层事实源继续是 MCP initialize、工具描述和 `capability_list`。

## 资源模型

所有需要操作本机的工具首先使用 `machineId`。文件系统和进程位置直接使用本机绝对路径：

- file_read/file_edit: `path`
- code_search: `path`
- shell_run/build_control: `cwd`
- git_control: `repositoryPath`；`fetch/pull/push` 的 `remote` 可省略，Node 按“当前分支 upstream remote → origin → 唯一 remote”顺序解析，多个非 origin remote 时 fail-closed 要求显式指定
- ai_control session.create: `providerId + workingDirectory`; 可选 `visibility=visible|internal`、`backend=codex_local|claude_local|chatgpt_cloud`、`visibilityTarget=codex_local|claude_local|chatgpt_cloud|none`、`ephemeral`，以及 ChatGPT Cloud 的 `mode=complete|quick_chat`、`model`、`thinking`; `session.result` 另支持 `resultMode=manifest|result-id`; 高级配置不会新增第三种 mode
- ai_control session.get: 已知本地 Codex ID 可传 `metadataOnly=true` 只验证元数据；CHAT 给定 Codex ID 时先 get，空闲后用同一 ID `session.send`
- codex_cloud_collaboration dispatch: `machineId + callbackSessionId + workingDirectory + prompt + idempotencyKey`；可选 `targetSessionId` 只复用该 CHAT，省略则新建一个可见 `quick_chat`；默认 `accessMode=write`、`writeScope=workingDirectory`、`callbackType=text`
- codex_cloud_collaboration completion.notify: `collaborationId + taskId + actorSessionId=$self + outcome + callbackType`；仅 `text` 可带 `text`，最多 2000 字符/8192 字节；`local_file` 路径由 Hub 从任务派生，不接收上传内容或调用方路径
- ai_control session.callback.register/arm/enqueue: 仅供 Hub 的 `codex_cloud_collaboration` 内部登记、激活和主动投递；公网 AI 直接调用会返回 `CALLBACK_ROUTE_MANAGED_ONLY`
- ai_control session.callback.claim/ack: `callbackTargetSessionId`; claim 可选 `callbackClaimLimit<=64` 或原 `callbackClaimId` 幂等续领，ack 使用返回的 `callbackClaimId`
- ai_control routing.status: 可选 `appType=claude|codex|claude-desktop`，只读 CC Switch 路由事实
- working_context: `machineId + action=get|set|clear + projectPath`；`set` 另带普通文本 `text`，可选 `expectedRevision`

`session.create` 的 `visible` 默认目标是本地 provider backend；`internal` 默认目标为 `none`，Codex 默认 ephemeral。`providerId=codex` + `backend=chatgpt_cloud` 用 Codex app-server 的 ChatGPT 登录态 + 自解 Sentinel 走官方 `/backend-api/f/conversation` 创建云端会话（`externalIdType=chatgpt_conversation`，会话出现在账号的 ChatGPT 聊天列表）；必须 `visibility=visible`，依赖本机 Codex app-server 已登录。裸 `ai_control` 的 `mode=quick_chat` 返回 `phase=running`、`createMode=quick_chat`、`completionPending=true`；只有明确的交互式调用方才随后用 `session.get/watch/result` 读取进度或结果，不能用这条轮询链模拟协作回调。需要稍后回到 Codex 的任务改用 `codex_cloud_collaboration` 并在 dispatch 后结束当前 Turn。普通 Codex `session.list` 只合并 FS sidecar 中已管理的 cloud conversation，不拉取账号全部 ChatGPT 历史；显式 `backend=chatgpt_cloud` 的 Provider 读取失败时返回 `incomplete=true`、`authoritative=false` 的 sidecar 已知项，不能把缺失项解释为未创建。

`working_context` 只提供 `get/set/clear`。每个规范化 `projectPath` 对应 Node data-dir 中一段最多 64 KiB 的普通文本与 revision；AI 可自行写目标、进度、阻塞、决定和下一步，多 AI 时由主控合并。它不会在项目内创建 Markdown Workspace，也不建模 Plan/Task 树。

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
