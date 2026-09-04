# 11 Local Bridge 与 AI 控制（Current）

## 1. Local Bridge 的定位

Fast Spider Node 提供只面向当前 OS 用户的 Local Bridge，使本机 AI/CLI 可以复用 Node 已有的 Capability Engine，而不再启动第二套 HTTP 服务或第二套权限系统。

Local Bridge 与远程 Hub 请求共用同一套：

```text
参数/schema 校验
→ Machine / OS-user 执行边界
→ Capability Dispatcher
→ Job / Event / resource limits
→ Agent / Browser / Git / File 等实际能力
```

Windows/Linux 当前使用用户 data-dir 下的 AF_UNIX/UDS；Windows 遇到 AF_UNIX 路径上限时先使用 data-dir 的 8.3 别名，仍过长则使用当前用户 home ACL 下按 data-dir 哈希命名的等价端点。默认随 Node 启用，可通过本机设置/启动参数关闭。它不监听局域网 TCP，不引入 Local Client 注册、Token、Grant、Lease 或逐请求 Approval。当前 OS 用户及 data-dir ACL（或 Windows 回退端点的用户 home ACL）就是本地信任边界。

Provider Token、Codex/ChatGPT 本地认证和其他 Provider secret 只保留在 Node/Provider 本机，不进入 Hub、MCP 响应或 Working Context。

## 2. 多 AI Harness 与 CC Switch Routing

当前内置两个 AI Harness：

```text
providerId=codex        -> Codex app-server --stdio + Windows 默认 Desktop owner/control bridge
providerId=claude_code  -> Claude Code CLI stream-json
```

CC Switch 是独立 Routing Runtime，不是 Harness。Fast Spider 使用三层事实模型：

```text
AI Harness
→ Routing Runtime (direct | cc_switch)
→ Upstream Provider / Model
```

因此 `sonnet`、`opus`、Codex model catalog 或 UI 展示名称都不能自动当作真实上游模型。`routing.status` 只读 `~/.cc-switch/cc-switch.db`，这是 Provider、Endpoint、Takeover、Health、Model Mapping 和 Proxy Request Log 的 SSOT；`~/.cc-switch/settings.json` 只用于设备当前选择对账，Claude/Codex live config 只是投影。

CC Switch SQLite 始终以 `mode=ro` 打开。Fast Spider 不返回 raw `settings_config` / `meta` / API Key / Token / Cookie / Authorization；Endpoint 只返回 hostname[:port]。`credentialPresent` 只表示是否检测到凭据，不暴露凭据正文。

Agent 实现按 Manager、静态 Provider Registry、Provider Adapter、Session Store/Event 与独立 `internal/agent/routing` 分层；Registry 只注册 Codex/Claude Code，不做动态插件加载。CC Switch 对唯一支持 schema 使用 `PRAGMA table_info` + fingerprint fail-closed，不兼容时只返回 `unsupported_schema`。只读 discovery 使用 bounded 进程内 TTL（route 约 1.5 秒、CLI version/auth 45 秒、models 20 秒），互不依赖的 Codex/Claude/CC Switch 探测并行执行，不触发 Session 或模型调用。

最终能力使用 tri-state `supported|unsupported|unknown`，原则是：

```text
EffectiveCapabilities
= Harness capability
∩ Routing/conversion capability
∩ Upstream provider/model capability
∩ Fast Spider policy
```

未知事实保持 unknown，不能按模型品牌猜测。

## 3. `agent.control` Action 集合

当前固定 action：

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

这些 action 同时通过公网 MCP `ai_control` 和 Local Bridge 进入同一 Agent Manager。`providers.list` 返回每个 Harness 的 `supportedActions`，调用方不能假设所有 action 在 Codex 与 Claude Code 上都存在。Codex 保留完整结构化扩展面；Claude Code 第一版只开放已通过真实 CLI 验证的 lifecycle/discovery 子集。FS 不把任一 Provider 的全部内部命令一比一暴露出来。

### `routing.status`

这是 Provider-neutral 的 CC Switch 路由 discovery。可选 `appType=claude|codex|claude-desktop`；省略时读取三类 Route。结果区分 `proxyEnabled`、`takeoverEnabled`、`liveTakeoverActive`，因此“本地代理进程已开”不会被误判成“当前 Harness 已接管”。同时返回 DB/current settings 的 `selectionConsistent`，用于发现 CC Switch SSOT 与设备选择投影漂移。

Model Mapping 目前识别 Claude 的 `ANTHROPIC_MODEL` / Sonnet / Opus / Haiku 角色、Claude Desktop `claudeDesktopModelRoutes`、Codex Provider 配置中的 `model/review_model/model_provider/wire_api/service_tier`。模型列表最多返回有界摘要，不返回原始 Provider 配置。

### `provider.readiness`

默认 `mode=safe`：逐层返回 route、provider executable、Codex harness、session backend 与 create readiness，只复用/启动 app-server 并调用只读 thread/list，不创建 Thread、不发送 Prompt。`mode=passive` 不启动尚未运行的 harness。结果包含顶层布尔字段、每层 state/reasonCode/elapsedMs 与总 elapsedMs，避免把“routing available”误报成“session.create 一定可用”。

## 4. Codex Session 与 Turn

### `session.create`

公网 MCP 必需：绝对 `workingDirectory` 与 12-128 字符 `idempotencyKey`。可选：`model`、`thinking`，以及首个 Turn 的输入。Node 对 Codex 与 Claude Code 均持久保存 key/spec hash/小型结果；同 key 同 spec 在进程重启后仍重放同一 Session，同 key 不同 spec（包括可见性语义、ChatGPT Cloud create mode、model 与 thinking）返回冲突，中间态不确定时不重复创建。索引采用严格全量校验，任何损坏或语义不完整记录均 fail-closed；记录不按时间自动过期。ChatGPT Cloud 在发出创建副作用前先保存 Provider 可见的首条 message ID；完整模式一旦从 SSE 观察到 conversation ID 就立即保存，不等待整段回答结束。Hub/WebSocket 断开不会取消已开始的创建，Node 仍在原执行 deadline 内完成对账和落盘。同一原 key 遇到 `in_doubt` 时会用精确 message ID 自动查找 Provider 会话，绝不按 Prompt、标题或时间猜测。`mode=quick_chat` 跳过 conversation prepare，默认 `model=auto`，拿到真实 conversation ID 后立即返回并在后台排空 SSE。显式 Cloud `session.list` 的登录态和 Provider GET 分别有界；失败时只返回 `incomplete=true`、`authoritative=false` 的 FS sidecar 已知列表，不能据此确认未创建。只有权威对账明确确认没有创建后，才可用原 `idempotencyKey` 与 `decision=confirm_not_created` 显式释放无 ID 的旧 `in_doubt` 记录。Claude 原生 history 仍由 Claude 自身保存。

`session.delete` 使用持久 delete intent：先把与 Session 关联的 create 记录标为 deleting，再删除 Provider Session，最后回收记录。若 Provider 已删除但最终落盘失败，重试同一删除会把 Provider not-found 视为已完成并续做回收，不会留下无法清理的容量占用。

如果没有任何 Turn input，只创建 Codex Thread 并返回 `phase=ready`。如果存在 text/Skill/Image/Mention 任一输入，则创建 Thread 后立即启动 Turn，返回 `sessionId + turnId + phase=running`。

Git 子目录和 linked worktree 会解析到主工作树对应的 Codex Desktop 项目展示上下文，但真正执行 cwd 保留为调用方传入的绝对 `workingDirectory`。跨主 Git 项目继续工作时必须新建 Session。

### 会话可见性双模式

`session.create` 将 `visibility`、`backend` 与 `visibilityTarget` 分开表达：`visibility` 取 `visible|internal`；本地后端取 `codex_local|claude_local`，`visibilityTarget` 取匹配的本地目标或内部专用的 `none`。省略时兼容旧调用：`visibility=visible`，backend/target 从 `providerId` 推导；Codex `internal` 且未指定 `ephemeral` 时默认请求 `thread/start.ephemeral=true`，显式 `ephemeral=false` 才创建持久内部 Thread。`visible` 会返回并保存 provider-native `externalId`、`externalIdType` 及对应的 `externalThreadId`/`externalSessionId`。

`internal` 不进入 Fast Spider 的普通 `session.list`，也不会同步 Codex Desktop 项目状态；这不是跨客户端 ACL。Codex 持久内部 Thread 仍可能被其他 Codex 客户端的本地 `thread/list` 列出，因此返回 `visibilityGuarantee=not_guaranteed` 和限制说明；ephemeral 只报告 `best_effort`，并提示可能不跨 app-server 重启存活，不虚报为绝对不可见。现有会话没有 sidecar 元数据时按 `visible/unmanaged_existing` 返回，并明确没有更强的 UI 保证。

`backend=chatgpt_cloud`（或 `visibilityTarget=chatgpt_cloud`）在 `providerId=codex` 下创建 ChatGPT 云端会话：
Fast Spider 用 Codex app-server 的 ChatGPT 登录态（`getAuthStatus`）配合自解 Sentinel（PoW + turnstile token）走官方
`/backend-api/f/conversation` 流创建会话，`externalIdType=chatgpt_conversation`，会话出现在账号的 ChatGPT 聊天列表。
`chatgpt_cloud` 必须 `visibility=visible`（云端会话天然对外可见），`ephemeral=true` 不支持；首次消息必须随
`session.create` 提供（ChatGPT 无空会话创建接口）。依赖本机 Codex app-server 已登录 ChatGPT，否则返回明确错误。创建成功后 Fast Spider 在 sidecar 中保存 `sessionId → backend=chatgpt_cloud + workingDirectory`，因此受管会话后续 `session.get/send/watch/result/rename/delete/cancel/steer` 只需 `sessionId`，不必重复声明 backend。对于用户直接提供的现有 `chatgpt.com/c/<conversation-id>`，调用方使用该 conversation ID，并显式传 `backend=chatgpt_cloud`，也可用 `appType=chatgpt` 作为等价入口。普通 Codex `session.list` 会合并 FS 自己管理过的 Cloud 会话；显式 `backend=chatgpt_cloud` 的 `session.list` 才访问 `/backend-api/conversations`。Provider 读取失败时返回带 `source=fast_spider_sidecar`、`incomplete=true`、`authoritative=false` 的本地已知项，而不是伪造空的完整云列表。

Cloud 只有一个创建入口 `session.create`，用 `mode=quick_chat|complete` 切换两种模式。Node 本地 `config.json` 可设置省略 `mode/model/thinking` 时采用的默认值；未配置的旧客户端仍为 `complete` + Auto 模型 + Auto 思考。请求明确传入的值（包括以空字符串表示 Auto）优先。`complete` 保留 prepare + 完整 SSE 等待；`quick_chat` 与 Codex Quick chat 一样不请求 `/f/conversation/prepare`，最终模型为空时发送 `model=auto`，收到首个真实 conversation ID 就返回 `phase=running`、`createMode=quick_chat`、`completionPending=true`。同一 `idempotencyKey` 不能在两种 mode 或不同 model/thinking 之间复用；幂等匹配使用应用本机默认值后的最终参数，旧版未记录 mode/thinking 的默认完整创建仍可按原 key 重放并迁移。

创建前调用 `models.list`（`backend=chatgpt_cloud`）会返回 `defaultModel`、两个 `creationModes`、`configurationModes`、`modelPresets`、实时完整 `models`、Node 本机 `advancedModels` 与实时 `thinkingOptions`。创建返回模式始终只有 `quick_chat|complete` 两个；参数配置另分为 `preset|advanced`，且任一配置都可搭配任一创建返回模式。预设配置直接选择官方返回的有效组合。高级模型由 Node data-dir 下的 `chatgpt-advanced-models.json` 管理，不写死在项目源码，也不同步 Hub；本地 Node UI 可新增、编辑或删除。思考档位每次从 ChatGPT 模型目录的 presets/slider 数据提取，当前仍为 Medium (`standard`)、High (`extended`) 与 Extra High (`max`)；Auto 是本地“不发送 thinking_effort”的选择。高级组合可能被 ChatGPT 服务端解析到其它 `resolved_model_slug`，调用方不得把请求别名宣称为确定的底层模型。

`chatgpt_cloud` 的操作映射（`providerId=codex` + `backend=chatgpt_cloud`）：

| `ai_control` | chatgpt_cloud 后端 |
|---|---|
| `models.list` | `GET /backend-api/models`；返回实时 presets/thinking，加上 Node 本机配置的 `advancedModels`，与 Codex/工作模型分开 |
| `session.create` | `mode=complete`：prepare 后等待完整 `POST /backend-api/f/conversation`；`mode=quick_chat`：跳过 prepare，拿到 conversation UUID 即返回，后台排空流 |
| `session.send` | follow-up（`conversation_id` + `parent_message_id`=最后 assistant 消息，自动解析）；`mode=quick_chat` 可带稳定 `idempotencyKey` 做重启安全投递 |
| `session.get` | `GET /backend-api/conversation/{id}`；完整 mapping 只留在 Node，默认返回活动分支最近 8 条有界文本。响应的 `nextCursor` 可作为下一次 `pageCursor`，只取得更新消息，不重复注入历史；单次 `limit` 最大 32 |
| `session.result` | 同上，按 async/terminal 事实返回 `running|completed|failed|canceled|unknown`；旧调用返回有界（64 KiB）`finalAgentMessage`，`resultMode=manifest|result-id` 用于只取 Result 元数据 |
| `session.list` | 显式 `backend=chatgpt_cloud` 时有界调用 `GET /backend-api/conversations`；Provider 失败时只返回标记为不完整、非权威的 FS sidecar 已知项。省略 backend 的普通 Codex 列表仅合并受管 Cloud 会话，不扫描整个账号历史 |
| `session.rename` | `POST /conversation/id/{id}/rename` |
| `session.delete` | `DELETE /conversation/id/{id}` |
| `session.cancel` | `POST /stop_conversation`（无活动轮时幂等返回） |
| `session.watch` | 每账号复用一条 `/celsius/ws/user` pubsub 长连接，动态订阅/退订 `conversations` + 当前 `conversation-{uuid}`；`conversation-turn-complete` 等事件 → `session.watch` 事件（提示 refetch `session.get` 取内容） |
| `session.callback.register` | Hub 内部先持久保存 mission/task/generation、发送前 completion 基线和可选本地交付路径；可保持未激活 |
| `session.callback.arm` | Hub 在新任务已投递后持久激活 callback，并建立不会被普通 watch 空闲淘汰的订阅及一次基线围栏补漏 |
| `session.callback.unregister` | 按 source session + generation 撤销回调 owner，同时清除该来源尚未投递的事件 |
| `session.callback.list` | 只读列出注册、pending 队列、固定 queue text、领取状态和恢复策略；可按 source 或 target 过滤 |
| `session.callback.claim` | 按目标协调会话一次领取最多 64 条 pending；同一 claim 可幂等重读，租约 5 分钟 |
| `session.callback.ack` | 按目标协调会话和 claim ID 批量确认已处理事件；确认后才从队列移除 |
| `session.steer` | 活动兼容 TPP 轮：`POST /f/steer_turn`（`asyncTaskId` 映射为 `async_task_id`；普通已完成聊天无可 steer 的活动轮时明确报错） |

实时同步基于每账号一条 `/backend-api/celsius/ws/user` pubsub 长连接（动态订阅/退订 `conversations` + 当前 `conversation-{uuid}`，
`conversation-turn-complete` 触发 refetch）——已实测：另一客户端写入后 `session.watch` 收到事件。

### `session.callback.*`

`session.callback.register/arm` 只支持 `providerId=codex + backend=chatgpt_cloud`，并由 Hub 的 `codex_cloud_collaboration` 内部调用；公网 `ai_control` 不允许 AI 单独创建或激活回调路由。复用 CHAT 时，register 保存发送前最新 assistant/message identity 与 `armed=false`，Hub 保存任务绑定，再用任务代次稳定的 Provider message ID 幂等发送新 Prompt，最后 arm。arm 建立持久订阅并做一次补漏；只有当前 identity 不同于基线才生成完成通知，因此旧完成回合、订阅重放和发送前 catch-up 都不能占用本任务 generation。若进程在注册、发送或激活任一步崩溃，下一次 tick/dispatch 会使用同一 message ID 对账或续发原任务，再幂等 arm；不会只激活未发送的任务，也不会重复创建新回合。一个 source CHAT 同时只能有一个 owner；generation 对应一次真实尝试，更高 generation 替换旧代并清除旧 pending。`session.callback.unregister` 必须携带当前 generation；`session.callback.list` 可对账 owner、baseline identity、armed 与 pending。

Cloud 的 `conversation.turn.complete` 按登记类型形成兜底记录并写入 Node data-dir 下的 `agent/session-callbacks.json`，再按期限唤醒协调会话。`local_file` 只保存登记的 Node 本地路径引用，绝不复制或上传文件；`text` 只保存最多 2000 个 Unicode 字符且最多 8192 个 UTF-8 字节的短文本；`status` 不保存正文。持久索引损坏或 pending/registration 序列不一致时 fail-closed，不会用空状态覆盖。复用 CHAT 的 realtime terminal 事件先读取当前会话 identity 与基线确认，防止旧 websocket 事件误报；新建 CHAT 可直接采用 Provider 稳定 event key。Node 队列对每个 mission/task/generation 只保留一个 canonical completion，generation 升级后才允许新的真实尝试再次完成。同一协调会话的多个 CHAT 可通过一次 `session.callback.claim` 批量领取；一次最多 64 条且内联文本总计不超过 64 KiB，claim ID 与 5 分钟租约持久化，超时自动释放，只有完成 Hub notify/验收/ack 后才调用 `session.callback.ack` 移除 Node 通知。队列不携带 Prompt、Provider payload、Token 或原始错误。

投递策略是 `Cloud CHAT self-callback / Node fallback`：任务先固定 `callbackType=local_file|text|status`。文件型结果由 Cloud CHAT 通过 FS 直接写到登记的 Node 本地路径，再只发路径型通知；这个固定 `resultPath` 是回调专用输出槽，即使任务为 `read_only` 或未列出 `file.write` 也只允许写这一处，不授权其它文件写入。短结果直接文本回调；无正文任务只回状态。只要调用方需要“创建或续发 CHAT 后稍后得到结果”，即使只有一个简单任务也使用 `codex_cloud_collaboration`，不能用裸 `ai_control quick_chat + session.get/watch/result` 轮询来模拟回调。`task.dispatch` 成功会返回 `awaitMode=callback`、`activePollingAllowed=false`、`callerShouldYield=true` 和 `nextAction=end_turn`；调度只返回简短派发回执并结束当前 Turn。Node 只在主动回调缺失、断线或漏通知时恢复，并保持同一种类型。本地 pending/claim 队列由新事件、目标 Turn 结束、claim/ack 变化和精确到期时间唤醒，不再按固定周期扫描。单 Codex 简单协作把同一个现有 Codex ID 同时登记为 controller、dispatcher 和回调目标，完成事件到达后立即尝试唤醒该 ID；目标仍忙时通知保留在持久队列，并在目标 Turn 结束后重试。普通多角色模式的 pending 最早存在约 5 分钟且目标调度会话空闲时才发送不含正文的 nudge；此后同一目标最多约每 10 分钟再提醒一次，只有真实投递或落盘错误才约 30 秒重试。Provider 状态恢复查询以约 30 分钟为最低频率，仅在启动后的首次恢复、官方长连接发生过中断或当前仍离线时合成漏掉的 terminal callback；长连接持续健康时不会周期性重读全部 CHAT。Node 重启后恢复持久订阅、pending、claim 与 nudge 状态；注册和注销会直接在同一条账号级长连接上更新 topic，watcher 生命周期带 generation 围栏，旧代注销不会关闭新 owner 的订阅。协调会话只处理登记路径、受限短文本或状态，不抓取完整会话网页；实际业务网页仍由对应 Cloud CHAT 读取和操作。

`codex_cloud_collaboration` 的卡住恢复由调度 AI 决策：默认 heartbeat 30 分钟、stall 60 分钟，并要求至少两次无进展检查才标记疑似卡住。每个自检回合只读取一次持久状态、执行至多一个有界动作，随后立即空闲；`tick` 在无动作时返回 `idle=true + nextCheckAt`，过早调用 `status.poll` 只返回 `not_due + nextPollAt`，不会调用 Node 或 Provider；只有状态到期且必要时才真正执行 `status.poll`；若 Provider 已完成则走结果恢复，若仍在运行则 `chat.continue` 发送固定“请继续”，在观察到新进展前不重复发送。后续观察到新 cursor 会清零 `continueAttempts` 并恢复正常调度；继续后仍无进展、Provider 失败/取消或状态不确定时，返回 `chat_recovery_decision`/`controller_decision`，允许主控决定人工接手或换代，但服务不会自动创建替代 CHAT。回调、状态检查、继续或 Cloud CHAT 本身遇到的问题/疑问，应先读取并以 file revision CAS 调用 `working_context markdown.append`，追加到 `docs/progress/04-open-issues.md`；若读取返回 `NOT_FOUND`，先调用 `plan.init` 且设置 `initializeMarkdown=true` 初始化 Markdown 工作区。禁止记录凭据、原始 Provider payload、完整聊天记录或长日志。

### `session.send`

只能向空闲 Thread 启动下一 Turn。若 Codex 已有 active Turn，返回 `AGENT_SESSION_BUSY`，Fast Spider 不把第二条 send 暗中转换成 steer。

`session.send` 可以覆盖同一项目内的 `workingDirectory`、model、reasoning effort、personality、serviceTier 与 reasoning summary；跨项目目录被拒绝。

对于普通 ChatGPT 会话，续聊使用 `providerId=codex + backend=chatgpt_cloud + session.send`；也可用 `appType=chatgpt` 选择相同后端。用户已给出准确 conversation ID 且目标只是继续时，不需要先读取完整历史；`session.send` 会在 Node 内部解析 parent/model/thinking，只把小型启动结果返回调用方。需要跨进程恢复的 quick send 应传稳定 `idempotencyKey`：Node 将它和 conversation ID 派生为固定 Provider message ID，重试先按该 ID 对账，传输结果不确定时也不会另建一轮；同 key 改变正文会明确冲突。若确实需要观察新内容，保存 `session.get` 的 `nextCursor`，后续作为 `pageCursor` 只读取增量消息。

### `session.steer`

`session.steer` 映射 Codex `turn/steer`。调用方必须提供当前 active `turnId` 作为 `expectedTurnId`，Codex 会在 Turn 已切换时拒绝请求，从而避免把纠偏指令误发给下一 Turn。steer 只接受 text/skill/image/localImage/mention 与 `imageDetail`，不改变 model、cwd、outputSchema 或 Thread settings。

对于 `backend=chatgpt_cloud`，调用方使用 `asyncTaskId` 标识活动且兼容的 TPP 轮；适配器会读取会话详情中可用的异步任务元数据，并将请求发送到 `/backend-api/f/steer_turn`。普通 `/f/conversation` 聊天完成后没有可 steer 的活动 task，调用会返回 `no active steerable turn`，不会把 Codex 的 `turnId` 当作云端 `async_task_id`。

### `session.respond`

Codex app-server 可以在 Turn 中主动发送 Server Request。Fast Spider 当前把以下请求变成有界 `session.watch` 事件并保存为 pending request：

- `item/tool/requestUserInput` → `user_input.requested`
- `item/commandExecution/requestApproval` / `item/fileChange/requestApproval` → `approval.requested`
- `mcpServer/elicitation/request` → `mcp_elicitation.requested`

事件包含 opaque `requestId`；调用方随后用 `session.respond` 回答。request_user_input 使用 question-id → answers；command/file approval 只允许单次 `accept|decline|cancel`，不暴露 `acceptForSession` 或策略修改；MCP form elicitation 的 accept 必须提供有界 `responseContent`。`item/permissions/requestApproval` 不通过远程任意 permission object 响应，而是明确拒绝，Permission 继续使用 named profile 管理。

### `session.watch` / `session.result`

`session.watch` 使用 Node 维护的有界事件 cursor，返回标准化 Turn/assistant/status/error/interactive request 事件；最长单次 long-poll 15 秒。`session.watch` 与 `session.get` 都同时返回当前 `pendingRequests` 快照，因此即使客户端断线、事件 cursor 过旧或事件环发生截断，也能重新取得仍待回答的 requestId。

仅用于存在性授权的 session action（steer/respond/watch/cancel/rename/archive/fork/compact/rollback/goal/review）以 `thread/read(includeTurns=false)` 读取元数据，避免长会话在每次控制动作前重复加载完整历史；`session.get/result/send` 等确实需要 Turn 内容的动作继续使用 `includeTurns=true`。

Codex 的 resume/unsubscribe/start-turn/archive/delete 只按同一 `sessionId` 串行；不同 Session 使用独立短生命周期锁，可以并发推进。最后一个持有者/等待者退出后锁项立即删除，不形成随历史 Session 数量增长的常驻锁表。

`session.result` 读取 Codex 持久 Thread 的最新 Turn 事实，返回真实 status 与 `finalAgentMessage`。Cloud `session.result` 不把缺失 async/terminal 事实默认为 completed，而是返回 `unknown`；旧调用的正文最多 64 KiB。传 `resultMode=manifest` 或 `resultMode=result-id` 时仅返回 Result 状态/ID/大小/hash/页数，便于手动对账，不回传正文或 artifact ID。即使 Turn 使用了 `outputSchema`，Fast Spider 仍保留 raw final message，不宣称已经变成强类型对象；调用方可在需要时自行 JSON decode。

### `session.cancel`

映射 `turn/interrupt`。收到 interrupt ack 只表示取消已请求，最终状态仍以后续 `watch/result` 为准。

## 5. Codex 原生 Turn 输入与结构化输出

Codex 0.141.0 `turn/start.input` 原生支持：

```text
text        -> text
skill       -> name + absolute path
image       -> absolute HTTP(S) URL
localImage  -> absolute local path
mention     -> name + absolute path
```

Fast Spider 的 `session.create` / `session.send` / `session.steer` 直接构造这些原生 `UserInput`，不会把 Skill 文件内容拼接进 prompt 来“假装使用 Skill”。本地 Skill/Mention/Image path 必须满足 Node 当前 OS 的绝对路径规则；远程 image URL 只接受 HTTP(S)。图片还可统一指定 Codex 原生 `detail=auto|low|high|original`。单类输入数量和总请求大小都有边界。

`session.create/send` 还允许 Codex 原生单 Turn override：`personality`、`serviceTier`、`summary`；安全相关 approval/sandbox policy 不作为任意远程参数开放，仍由 Fast Spider Adapter 固定控制。

`outputSchema` 直接映射 Codex `turn/start.outputSchema`，当前 Fast Spider 限制为有界 JSON object：序列化大小最大 64 KiB、嵌套深度最大 12。它用于约束**这一轮最终 assistant message**，不是新的 Fast Spider 数据库格式。

## 6. Codex Provider、Model、Hooks、Permission 与 MCP Discovery

`models.list` 直接以 Codex `model/list` 为权威来源，并保留 `inputModalities`、`supportsPersonality`、`serviceTiers`、`defaultServiceTier`、`isDefault`、完整 `supportedReasoningEfforts` 等模型元数据，不按模型名维护静态猜测表。

Codex `provider.capabilities` 先读取 `modelProvider/capabilities/read` 的 Harness 原生 `webSearch`、`imageGeneration`、`namespaceTools`，再与 CC Switch 当前 Route 合并成 `effectiveCapabilities`。直连时原生能力可直接作为 Harness 事实；经 CC Switch 时，如果转换/upstream 未证明能力可保留，就从 supported 降为 unknown，明确不支持则降为 unsupported。响应同时保留 `harnessCapabilities + route + effectiveCapabilities`，不把派生结果伪装成 Codex 原生返回。

`hooks.list` 映射 `hooks/list`，可按绝对 `workingDirectory` 查询 Hook metadata，包括 eventName、handlerType、enabled、source、sourcePath、currentHash 与 `trustStatus=managed|untrusted|trusted|modified`。它只读，不负责绕过 Hook 信任。

`permissions.list` 映射 `permissionProfile/list`，用于发现可传给 `session.settings.update.permissions` 的合法 named profile ID，而不是让调用方构造任意文件/网络 Permission object。

`mcp.status.list` 映射 `mcpServerStatus/list`，但 Fast Spider 会把响应归一化成 server/auth/tool-name/resource 摘要，默认使用 `toolsAndAuthOnly`，避免完整 MCP Tool Schema 放大 WSS 控制消息。FS 不映射 `mcpServer/tool/call` 或 `mcpServer/resource/read`，避免形成 ChatGPT → FS → Codex → MCP 的第二执行链。

## 7. Skills

`skills.list` 映射 Codex `skills/list`：

- 可带绝对 `workingDirectory`，Fast Spider 转换为 Codex 原生 `cwds`。
- `forceReload=true` 时绕过 Codex Skill cache 重新扫描。
- 返回 Codex 提供的 Skill metadata，如 name、path、description、enabled、scope、dependencies/interface。

如果调用方要明确让某个 Skill 参与 Turn，应把 discovery 得到的 `name + path` 放入 `session.create/send.skills[]`。这是 Codex 原生 Skill input，而不是提示词约定。

## 8. Plugins

Plugin 是 Codex 的能力包，可包含 Skills、Apps、MCP servers、Hooks 等。Fast Spider 当前只开放读取面：

- `plugins.list` → `plugin/list`，可按绝对 `workingDirectory` 发现 repo marketplace，并可使用 Codex 原生 `marketplaceKinds`：`local|vertical|workspace-directory|shared-with-me|created-by-me-remote`。
- `plugins.installed` → `plugin/installed`，区分 Marketplace Catalog 与本机真正已安装/启用的 Plugin。
- `plugins.get` → `plugin/read`，使用官方字段 `pluginName`，可选 `marketplacePath` / `remoteMarketplaceName`。
- `plugin.skill.read` → `plugin/skill/read`，使用 `remoteMarketplaceName + remotePluginId + skillName`。

本轮不开放 Plugin install/uninstall。更重要的是，Codex `turn/start` 当前**没有**一个可直接附加的 `pluginId` 字段，因此 Fast Spider 不发明这个协议。Plugin 中具体 Skill 可以通过原生 Skill input 明确附加；Plugin 安装后提供的 App/MCP 能力由 Codex 自己的运行环境负责。

## 9. Thread 管理与自动 Resume

- `session.rename` → `thread/name/set`
- `session.archive` → `thread/archive`
- `session.unarchive` → `thread/unarchive`
- `session.delete` → `thread/delete`，属于破坏性动作
- `session.fork` → `thread/fork`，可选绝对 `workingDirectory`
- `session.compact` → `thread/compact/start`
- `session.rollback` → `thread/rollback`

`session.rollback` 的参数是 `numTurns`（1–1000），表示从 Thread 末尾删除 N 个 Codex turns。**它只修改 Codex 对话历史，不回滚本地工作树文件，也不等价于 Git reset/revert。**因此 rollback 之后仍应以 Git/文件系统事实判断代码状态。

Codex app-server 默认是由 Fast Spider 管理的本机子进程。Fast Spider 记录当前进程内已加载 Thread；若 app-server 崩溃或被重启，下一次 Turn/Review 前自动调用官方 `thread/resume(threadId)` 重新加载持久 Thread，再继续操作。调用方无需维护第二个 resume 状态机，也不需要新增公开 `session.resume`。Windows Node UI 首次启动由本机配置选择是否连接当前用户的 Codex Desktop owner/follower IPC：共享模式（推荐）关闭，FS 接管模式启用；该本机设置优先于环境变量。没有 Node UI 配置的 headless 进程仍可用 `FAST_SPIDER_CODEX_DESKTOP_BRIDGE=0` 明确关闭默认 bridge。它只认领当前 adapter 已加载的本地 Thread，终态或归档后通过 `thread/unsubscribe` 自动释放，并提供 follower 控制转发；它不替代 FS 自己的 app-server，也尚不能生成 Desktop renderer 私有的完整 `conversationState` snapshot/patch，因此不承诺 Desktop 原生界面实时显示完整内容。`providers.list`、`provider.readiness`、`provider.capabilities` 和本地 Codex `session.create/send` 结果都会返回 `desktopBridge` 状态与该限制。

实验性共享 app-server owner 模式仍可通过绝对 `FAST_SPIDER_CODEX_APP_SERVER_SOCKET` 接入外部 `codex app-server --listen unix://...`，此时 Fast Spider 只管理 proxy 客户端，不直接修改 Codex 状态文件；它与 Desktop owner/control bridge 是不同层：前者替换 app-server transport，后者只是附加的 Desktop IPC 控制路由。

## 10. Goal

Fast Spider 映射：

```text
session.goal.get
session.goal.set
session.goal.clear
```

`session.goal.set` 支持 objective、`goalStatus` 和 tokenBudget。当前 Codex 原生状态只允许：

```text
active
paused
blocked
usageLimited
budgetLimited
complete
```

Goal 是 Codex 的持久目标状态，不只是 Fast Spider 标签。真实 E2E 已验证：把 Goal 设置为 `active` 可能使 Thread 进入活动执行态，因此调用方在随后启动普通 Turn 前必须重新查询 Thread/active Turn 状态；测试或只做目标元数据管理时可使用 `paused`。

## 11. 稳定 Thread Settings

`session.settings.update` 只映射 Codex 当前稳定且适合安全暴露的字段：

- `workingDirectory` → Codex `cwd`，必须是绝对且存在的目录
- `model` → 必须来自本机 `model/list`
- `effort` → `low|medium|high|xhigh`
- `permissions` → Codex **named permission profile ID string**
- `personality` → `none|friendly|pragmatic`
- `serviceTier` → 有界字符串
- `summary` → `auto|concise|detailed|none`

Fast Spider 不开放 arbitrary config map，也不允许通过这个 action 注入任意 Codex 配置键。

## 12. Review

`session.review` 映射 `review/start`。delivery 只允许：

```text
inline
detached
```

Review target 精确映射 Codex 原生四种结构：

```text
uncommittedChanges
baseBranch  + reviewBranch
commit      + reviewSha (+ optional reviewTitle)
custom      + reviewInstructions
```

不存在模糊的任意 review flags 透传。Review 会启动 Codex 工作，属于副作用 action，连接中断时不能无脑重试。

## 13. 重试、审计与执行链边界

连接中断后的自动重试策略按 action 语义区分。

可安全重新查询：Provider/Model/Project/Skill/Hook/Permission/Plugin/MCP 状态 discovery、Session list/get/watch/result、Callback list、Goal get。

不可宣称可无脑重试：create/send/steer/respond/callback register/callback unregister/cancel/rename/archive/unarchive/delete/fork/compact/rollback、Goal set/clear、settings.update、review。尤其 steer/respond 可能已送达 Codex 但 Hub 响应丢失，调用方必须先 watch/get 查询真实状态；callback register/unregister 则先用 callback list 对账 owner 与 generation。

所有 Thread/Turn/Goal/Settings/Review 状态变更及 steer/respond 进入 Hub mutation audit；Provider secret、完整 prompt、交互回答正文、原始内部事件和环境变量不写入审计详情。

Codex 本身还公开 `fs/*`、`command/exec/*`、`process/*`、`thread/shellCommand`、`mcpServer/tool/call` 等接口，但这些不会通过 `ai_control` 再暴露。文件、Shell、Git、Build、Artifact、Browser 继续只走 Fast Spider 自己的 Capability/Job/Audit 链，避免两套权限和两套副作用状态机。

## 14. Claude Code Provider（0.4.2）

当前本机验证基线为 Claude Code 2.1.207。Adapter 使用原生 CLI：

```text
claude -p
--output-format stream-json
--verbose
--permission-mode acceptEdits
--session-id <uuid>   # 首 Turn
--resume <uuid>       # 后续 Turn
--model <alias/model> # 可选
--effort low|medium|high|xhigh|max
--json-schema <schema>
```

Prompt 从 stdin 输入，不进入命令行参数；这样避免 Windows argv 长度限制，也减少进程列表泄露 Prompt。Claude `outputSchema` 先通过 Fast Spider JSON Schema 有界校验，并额外限制为 16 KiB，避免 Windows 命令行被结构化 Schema 放大。

Fast Spider 使用自己生成的合法 UUID 作为 Claude 原生 Session ID；首 Turn 用 `--session-id`，后续 Turn 用 `--resume`。同一 Session 的 active Turn 在 `cmd.Start` 前原子占位，因此两个并发 `session.send` 不会启动两个 Claude 进程。

Fast Spider 只保存：

```text
<Node data-dir>/agent/claude-code-sessions.json
```

内容是小型 Session 控制索引：sessionId、workingDirectory、requested/native model、status、latest turn/result、bounded error/usage、RouteSnapshot 与 actualUpstream。**不保存完整 Prompt 或完整 Claude 对话历史。** 原生历史仍归 Claude Code 管理。Node 重启时本地索引中残留 `running` 的 Turn 会标记为 `interrupted`，不会虚报仍在运行。

Claude 第一版公开：

```text
models.list
provider.capabilities
projects.list
session.list
session.get
session.create
session.send
session.watch
session.cancel
session.result
session.rename
session.archive
session.unarchive
```

`session.rename/archive/unarchive` 当前只管理 Fast Spider 索引中的展示/可见性，不声称修改 Claude Code 原生历史。Claude 第一版只接受 text Prompt；不把 Codex Skill/Image/Mention 结构硬塞给 Claude，也不用提示词伪造不存在的原生输入协议。

### Claude stream-json 归一化

- `system/init` → `session.status=initialized`，保存 Claude 报告的 native model。
- `system/api_retry` → warning。
- assistant text → `assistant.message`。
- tool_use → `tool.started`。
- tool_result → `tool.completed`。
- result → `turn.completed` / `turn.failed`。

### RouteSnapshot 与 actualUpstream

每个 Claude Turn 启动前和结束后读取脱敏 CC Switch RouteSnapshot。只有 `routingMode=cc_switch` 且 `proxy_request_logs.session_id` 与当前 Claude native sessionId **精确相等**时，Fast Spider 才声明 `actualUpstream`。并发 Session 导致最后一条日志属于别人时保持 unknown，不进行错误归因。

### 认证与可用性

`claude --version` 只证明 Runtime 可启动；`claude auth status --json` 只表示认证配置存在，不证明 Token/Route 健康。因此 Provider 分开报告 `runtimeAvailable`、安全的 `authConfiguration`、CC Switch Provider health 与 `executionHealth=unknown_until_turn`。`email`、`orgId` 不返回。

真实 E2E 已验证当前机器 Claude Runtime/stream 正常，但当前官方 OAuth 上游返回 401 revoked token。Fast Spider 将其正确归一化为 `turn.failed`；这不是 Runtime unavailable。用户切换到健康 CC Switch Provider 或修复认证后，同一 Adapter 无需改变 Session 协议。

## 15. Automations 边界

Codex 产品层存在 Automations/定时任务体验，但当前验证的 Codex CLI 0.141.0 与 `app-server generate-json-schema --experimental` **没有公开 Automation RPC**。

因此当前 Fast Spider：

- 不映射 Codex Automations；
- 不读取 Codex 私有 SQLite/内部任务存储；
- 不模拟 Codex Desktop UI 点击；
- 不把 Fast Spider 自己未来可能存在的 Scheduler 冒充成 Codex Automation。

只有 Codex 后续公开稳定协议时，才评估以同样的 provider adapter 方式直接映射。

## 16. 本地 Edge App Window

Node loopback UI 继续使用 Edge App Window，不引入 Electron/Wails。0.4.2 一级导航为概览/连接、任务与进度、AI 与路由、组件、诊断：

- 任务与进度复用本地 `working.context` Plan/Task/Markdown actions，不复制第二套状态机。
- AI 与路由、诊断只返回显式 allowlist DTO；页面加载不自动执行真实模型健康测试。
- 组件中心只允许 `browser` 与 `search-ripgrep`，安装/更新必须手动点击并复用 component manager；状态响应不公开组件根目录、安装绝对路径或 Hub 凭据。
- 搜索与文件自检只在 NodeUI data-dir 下建立隔离临时目录，通过同一 Node local capability 调用 code.search、file.read 2.0 与 file.write preview，结束后清理；不读写用户项目、不下载组件、不执行 AI。
