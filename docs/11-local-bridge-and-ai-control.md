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

### 1.1 FastSpider_Local 与协作控制

`fast-spider-node mcp-local` 是现有 STDIO MCP 适配入口，提供 `local_machine`、`local_capability` 和 `collaboration_control`。适配器不实现第二套状态服务；`collaboration_control` 经 Local Bridge 调用正在运行的 Node。普通 ledger action 由 Node 内部 Go/SQLite 完成；`dispatch` 在同一次调用中原子 claim 冻结 READY，使用 Node 已有的 AgentController 创建或复用 ChatGPT Cloud CHAT，注册本机 callback，并把准确 binding 写回任务数据库。`dispatch_recover` 通过任务数据库身份找回原 token，只使用原冻结包、CHAT 与幂等键恢复中断阶段，调用方不传机械 token。调用期间不启动 Python 子进程或新守护进程。

该能力只在 `HandleLocalCapability` 路径开放，不属于 Node 上报 Hub 的能力目录。Provider 凭据仍只在本机 Node/Provider 中使用；Cloud create/send 不经过 Hub。Node 将正式结果放入按 transport 隔离的本机 callback 队列，原主控通过 `callback_claim` 领取并用 `callback_ack` 确认；ACK 会退役对应 route 和 watcher。公网 `FastSpider_FS` 的能力目录、远端 dispatch 和 Hub callback 契约保持兼容。

### 1.2 轻量持久协作 v3：改造范围与运行契约

0.4.83 的 `validation_receipt.executionRef` 同时接受真实子代理的 `codex-agent:<parentThreadId>#<canonicalPath>` 和既有 `codex-thread:<id>`。canonical ref 必须与当前 claim 的 launchRef 完全一致；首尾空白、不同父任务或路径均拒绝。`next_actions` 继续通过已有 nativeBindingLookup 定向恢复子代理。验收默认由交付协调 `spawn_agent` 创建子代，不能为了获取可登记的 threadId 创建独立 tracked 任务；旧独立验收绑定继续收口，不强制迁移或重跑。

0.4.81 保留原主控的唯一业务决策权，并为长期 Luna 协调提供按需 Cloud 技术分析。主控通过 `apply` 设置 `mission.analysis_policy={enabled,authorityRef,model,thinking,machineId,workingDirectory,instructions}`；交付协调用 `analysis_prepare(expectedRevision,sourceItemIds,reason,question,brief)` 提交 1..8 个已有结果或后继准备项。reason 为 `successor_planning/conflicting_evidence/repeated_rework/cross_owner_design`。Node 从策略冻结只读、text callback、回原主控的 Cloud decision READY，执行协调原样 dispatch；model/thinking 真实透传到 create/send。相同来源版本、原因、问题、简报和策略幂等复用，已变化来源或撤销策略阻止旧请求新派发；同 mission 一次只运行一个分析，其它独立工作继续。Cloud 给技术结论与完整建议参数，最终采纳仍由主控执行。

`resolve/decision_batch` 的 accept 可加 `followup=prepare`，在同一事务建立独立 local planned decision 准备项；`prepare_followup` 路由给交付协调，不因源报告归档丢失。交付准备完整后继方案，必要时按策略调用分析，主控批准实际实现 READY 时同一次 apply 收口准备项。没有后继义务时省略该字段，旧 resolution 幂等重放保持兼容。

成功投影本机 collaboration inbox 的 managed callback 持久标记后，由 role-wakeup 推进；不再重复发送主控原始 claim/ACK nudge，也不混入新的 legacy claim。未 ACK 事件与注册继续保留，sink 失败可恢复，已有 claim 重放和最终文件校验不被放宽。业务决定提交后仍按结果单独结清 ACK。

本次改造沿用 Node 进程、Local Bridge 和总任务自己的 SQLite，不增加 Python、服务或 Markdown 状态镜像。新 `init` 建立 v3 所需表；旧库仍可使用 v2 动作，读取 v3 任务树/收件箱不会隐式迁移旧库。能力版本为 `collaboration.control/3.0`，仅本地 MCP 可见。源码升级、安装新版 Node、迁移活动任务是不同操作，不因测试通过自动迁移。

实施分为四部分：

1. **任务树**：`tree_update` 保存 mission 目标/不做事项/模式、workstream 任务线以及 task 的标题和归属。用户目标变更要求 `authorityRef`，执行计划可调整但不能静默改授权。`tree` 默认返回分页热工作集与完成汇总，不返回旧 prompt/日志；完成项用 `archive` 折叠，不删除身份。
2. **结果闭环**：Agent 先持久保存 callback，再通过进程内 sink 写入任务 SQLite inbox，然后唤醒原主控。claim/ACK 也确保投影先成功。`inbox` 列出未处理结果；`resolve` 原子保存业务决定和结果处理标记，再做幂等 transport ACK。ACK 失败不会回退业务决定，重放同一 resolve 只补 ACK。结果正文仍在既有结果池/交付文件，inbox 保存引用。
3. **稳定任务与执行轮次**：`items.id` 是稳定业务 ID；`retry` 仅由主控对已确认结束并处理过结果的执行发起，旧 claim/key/taskRef 写入 `execution_attempts`，当前 item 原子变成新 READY，后继依赖不需改 ID。新 READY 本身就是持久派发意图，沿用既有 dispatch 锁、CAS、唯一键和不确定恢复，不再造第二套 dispatcher。旧轮次迟到 callback 只归入历史，不能结束新轮次。人工取消必须带明确 `userDecisionRef` 才可重试。
4. **有界检查**：`record_check` 对一致性审计的 `completed` 自动更新完整检查时间；执行/派发/验收/阻塞检查使用 `unchanged` 或 `unavailable`，自动退避 15/30/60 分钟并尊重更长 Retry-After。连续三次无新事实后停止该检查，产生一次主控裁决义务，不判失败、不重建 CHAT。暂停/关闭不做定时业务续跑。历史事件保留最近 100 条，检查指纹覆盖并剪除失效项，终态身份用于去重而不逐轮回读。

正常流程为 **主控批准 → 执行协调 dispatch → Cloud 执行 → inbox → 交付协调准备决策包 → 主控批量决定**。Node 在状态事务中写入 role-wakeup outbox，同一目标的待投递通知合并唤醒；主控不再重复手动发送同一变化。暂停 mission 时保留通知并停止角色唤醒，恢复后继续；已替换角色的旧通知不会发给旧任务。定时兜底只补漏。

0.4.79 支持在原 mission 上启用双协调：原 `coordinator` 负责派发和执行观察，`delivery_coordinator` 负责结果证据、`validation_claim/validation_receipt` 和验收跟踪。运行时更新、两任务实际模型与思考配置核对后，原主控在已暂停派发的 mission 中调用一次 `apply`，设置 `delivery_coordinator`、`coordination_ref`，可同次恢复 active/dispatch。不更换原主控、数据库或在途 CHAT；切换前已领取验收仍由原领取者回填。交付协调不能派发或作业务决定。

`decision_batch` 接收 `expectedRevision` 和最多 20 条 `decisions`，各项沿用 resolve 的结果与决定字段；所有业务决定在同一事务提交，任何一项失败全批回滚。提交后逐项 ACK；重放相同批次只结清传输，不重做业务。独立 READY 通过同一 next_actions revision 的 `refillInputs` 有界补齐，某项等待不会阻塞其它可执行项。

执行/派发/验收检查按真实执行轮绑定稳定身份，普通文案、优先级及 `next_check_at` 更新不会清除检查次数、退避或通知去重；旧版仍有效的记录在更新前保留原预算并转换身份，CAS 仍独立校验。主控和协调的 `brief.scope` 都显示准确的精简写域，不包含任务正文；冲突错误指出占用任务与重叠路径，未创建目录也按现存祖先归一化，避免 Windows 短路径别名漏检。`local_file` bootstrap 明确要求执行者先保存并确认准确报告可读，read_only 只允许额外写入 Node 指定的报告文件；Node 不代写报告。`resolve` 已完成传输 ACK 时无需再调用 `callback_ack`。

新动作使用原有 `dbPath/missionId/actorSessionId` 身份：

- `upgrade`：仅主控在 mission paused 且 dispatch disabled 时使用，传 `expectedRevision/backupPath/evidenceRef`。先创建校验独立 SQLite 备份，再增补 v3 表/列并绑定既有 callback；保留任务、状态和 CHAT。每次尝试使用新的绝对 `.sqlite3` 备份路径。表提交后 callback 绑定失败仍保持暂停，修复明确原因后重试，不重建任务。旧 MCP 适配器可能缓存 v2 能力目录；后台 Node 升级后以新适配器能力与实际动作结果核对，不因此取消既有会话。
- `tree`：`after/limit` 分页任务；`tree_update`：`expectedRevision` + `mission/workstreams/items`，目标改变另带 `authorityRef`。
- `inbox`：`after/limit` 默认只返回待处理元数据；传 `resultId` 定向取该结果元数据。
- `resolve`：`expectedRevision/resultId/decision/evidenceRef`；decision 为 `accept/verify/integrate/rework/block`。accept 明确传 `validation=passed|not_required` 和 `integration=done|not_required`；verify 传 `validationOwner`；block 传现有 blocker 契约。后续验证和集成仍按已有业务状态更新，不把 Cloud 完成当作交付。
- `retry`：`expectedRevision/itemId/evidenceRef/item`；item 仅包含新执行者、packet、local_scope 等新执行参数。Cloud 必须新幂等键，`targetSessionId` 可复用同域原 CHAT。不确定派发只用 `dispatch_recover`，不能用 retry。
- 本地 item 的 execution_ref：独立任务或原生记录提供真实子代理 agentThreadId 时用 `codex-thread:<真实ID>`；只有 canonical path 时用 `codex-agent:<parentThreadId>#<canonicalPath>`，不能把该路径当 threadId。Node 0.4.76 的本地/验收检查返回 nativeExecutionCheck 或 nativeBindingLookup，由调用者使用 Codex 原生工具定向读准确任务，或从父任务 subAgentActivity 匹配真实子任务 ID 后回填；FS 不查询本机 Codex。终态按 terminalHandoff 通知主控，由主控采纳，不把 completed 当 PASS。local_scope 是 `{machineId,workingDirectory,accessMode:"read_only"}` 对象，写任务再加 `accessMode:"write",writeScope:["<准确写域>"]`；本地数组与 Cloud 单字符串写域不是同一契约。
- v3 一致性审计只用 record_check(completed) 原子收口，不先 observe；若 observe(full=true) 已成功，返回 auditRecorded=true/recordCheckRequired=false，旧审计动作已失效，刷新本地 next_actions 后继续其它动作即可。空/重复 callback 只结束该传输通知，不丢弃本轮尚未消费的本地验收和业务待办。
- `record_check`：`expectedRevision/expectedObservationRevision/actionId/outcome/evidenceRef`，必要时传更长 `retryAt`；它不是业务 resolve，也不调用 provider。`next_actions` 的检查动作返回 `recordCheck.params`，调用方只补 outcome/evidenceRef，不传 itemId/evidence。Cloud 检查另返回精确 `executionCheck` 与仅终态使用的 `terminalRecovery`；只返回请求，不自动查询。`agent.control/session.get(metadataOnly=true)` 在 Node 0.4.75 起返回白名单执行状态/观察时间，不返回正文；unknown 不标权威。账本 active 不能作为 unchanged 证据，Cloud 检查按 30/60/120 分钟退避，普通检查保留 15/30/60 分钟。
- `archive`：完成 task 的 `itemId/expectedRevision/archived`；`cleanup` 仍只在明确授权且 mission 关闭后删除准确数据库，不影响其它任务。

快速验证对应受影响 Agent/Node/LocalMCP/协议包，以及重复回调、ACK 后重启、resolve 后 ACK 中断、旧 attempt 回调、幂等创建、人工取消、暂停、检查收口与任务树分页。Local Bridge + fake Agent 测试属于本机协议集成证据，不等同真实 Cloud 或生产升级。

## 2. 多 AI Harness 与 CC Switch Routing

当前内置两个 AI Harness：

```text
providerId=codex        -> Node 唯一长期运行的 Codex app-server --stdio
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
session.callback.arm
session.callback.enqueue
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

这些 action 进入同一 Agent Manager；其中 `session.callback.register/arm/enqueue` 只允许 Hub 的 collaboration 内部链路调用，公网 MCP `ai_control` 会拒绝直接修改 callback 路由或投递。`providers.list` 返回每个 Harness 的 `supportedActions`，调用方不能假设所有 action 在 Codex 与 Claude Code 上都存在。Codex 保留完整结构化扩展面；Claude Code 第一版只开放已通过真实 CLI 验证的 lifecycle/discovery 子集。FS 不把任一 Provider 的全部内部命令一比一暴露出来。

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

创建前调用 `models.list`（`backend=chatgpt_cloud`）会返回 `defaultModel`、两个 `creationModes`、`configurationModes`、`modelPresets`、实时完整 `models`、Node 本机 `advancedModels` 与实时 `thinkingOptions`。创建返回模式始终只有 `quick_chat|complete` 两个；参数配置另分为 `preset|advanced`，且任一配置都可搭配任一创建返回模式。预设配置直接选择官方返回的有效组合。高级模型由 Node data-dir 下的 `chatgpt-advanced-models.json` 管理，不写死在项目源码，也不同步 Hub；本地 Node UI 可新增、编辑或删除。官方预设思考档位每次从 ChatGPT 模型目录的 presets/slider 数据提取；Advanced 还可为每个模型保存自定义 thinking 值，按模型需要选择；Auto 是本地“不发送 thinking_effort”的选择。高级组合可能被 ChatGPT 服务端解析到其它 `resolved_model_slug`，调用方不得把请求别名宣称为确定的底层模型。

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
| `session.callback.enqueue` | CHAT 已调用 `completion.notify` 后，Hub 主动把已持久化通知推入 Node 本地队列；目标 Codex 空闲立即唤醒，忙时在当前 Turn 结束后重试 |
| `session.callback.unregister` | 按 source session + generation 撤销回调 owner，同时清除该来源尚未投递的事件 |
| `session.callback.list` | 只读列出注册、pending 队列、固定 queue text、领取状态和恢复策略；可按 source 或 target 过滤 |
| `session.callback.claim` | 按目标协调会话一次领取最多 64 条 pending；同一 claim 可幂等重读，租约 5 分钟 |
| `session.callback.ack` | 按目标协调会话和 claim ID 批量确认已处理事件；确认后才从队列移除 |
| `session.steer` | 活动兼容 TPP 轮：`POST /f/steer_turn`（`asyncTaskId` 映射为 `async_task_id`；普通已完成聊天无可 steer 的活动轮时明确报错） |

实时同步基于每账号一条 `/backend-api/celsius/ws/user` pubsub 长连接（动态订阅/退订 `conversations` + 当前 `conversation-{uuid}`，
`conversation-turn-complete` 触发 refetch）——已实测：另一客户端写入后 `session.watch` 收到事件。

### `session.callback.*`

`session.callback.register/arm/enqueue/unregister/list/claim/ack` 是 `codex_cloud_collaboration` 的内部可靠性协议，不是 AI 需要手工拼装的公开工作流。Hub 用它登记 callback owner、发送前基线和 generation；CHAT 的 `completion.notify` 到达后，Hub 先持久化，再通过 `enqueue` 主动交给 Node。Node 用持久队列处理目标会话忙和重启恢复，重复通知与领取保持幂等，旧 generation 不会覆盖新任务。正式 completion ack 成功后，Node 原子退休对应活动 route；Hub 继续保存结果和审计历史。活动 route 达到 64 条时只阻止新的并发登记，已 ACK route 不占容量，重启也不会复活。

公开调用统一使用 `codex_cloud_collaboration action=dispatch`：传入 `machineId`、现有本地 Codex 的 `callbackSessionId`、绝对 `workingDirectory`、任务 `prompt` 和稳定 `idempotencyKey`。可选 `targetSessionId` 只续发指定可见 CHAT；省略时创建一个可见 `quick_chat`。Hub 派生的内部发送键绑定 collaboration、task 和 generation；明确的 `session.send` 拒绝会撤销本次 callback route 并返回错误，不会伪装为 `active`，只有连接丢失或 deadline 等无法判断 Turn 是否启动的结果才进入 `deliveryInDoubt`。Fast Spider 不区分“单主控”“主控加协调者”或“单 AI”模式，三者都只是把任务发给 CHAT，再把结果回调给 `callbackSessionId`。

CHAT 完成前使用 `task_result_submit(taskRef,status,text?)` 提交结果。默认短文本最多 2000 个 Unicode 字符和 8192 个 UTF-8 字节；长任务在 dispatch 时选择 `callbackType=local_file`，CHAT 先将完整结果写入分配的 Node 本地 `resultPath`，再省略正文提交状态。FS 校验文件元数据和 SHA-256，Hub 不接收文件正文，Codex 按本地路径读取。正常路径为 `task_result_submit → Hub 持久化 → Node 主动唤醒 callbackSessionId → Codex claim/ack`。旧 `completion.notify` 保留兼容。Provider realtime、启动补漏和低频状态读取只是兜底。dispatch 返回 `callerShouldYield=true` 和 `nextAction=end_turn` 后结束当前 Turn 等待回调，不用轮询模拟协作。

目标、进度、阻塞和下一步属于 AI 的上下文管理。确需跨会话复用时，用 `working_context get/set` 维护一段简短文本；简单任务不必建立资料室。不得写入凭据、原始 Provider payload、完整聊天记录、源码全文或长日志。

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

Node 只启动并长期持有一个 `codex app-server --stdio`。读取和列表使用 `thread/read`、`thread/list`；创建使用 `thread/start`，有首条输入时紧接 `turn/start`；续发先按当前 generation 的 loaded 状态执行 `thread/resume`，再执行 `turn/start`。app-server 重启后 loaded generation 会失效，下一次操作自动重新 resume，不存在第二条 Desktop IPC 或共享 socket 执行链。

远程创建本地 Codex 任务继续使用公开 `ai_control session.create`，参数为 `providerId=codex`、`backend=codex_local`、绝对 `workingDirectory` 和必需的 `idempotencyKey`。Hub 只做权限、审计、超时与转发；Node 与本机调用使用同一实现。普通 `session.send` 遇到归档任务明确报错，由调用方先执行 `session.unarchive`；durable callback 为确保通知可达，可自动执行 `thread/unarchive → thread/resume → turn/start`。创建和发送只有取得真实 `sessionId`/`turnId` 才报告成功。

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

Node loopback UI 继续使用 Edge App Window，不引入 Electron/Wails。一级导航为概览/连接、项目上下文、AI 与路由、组件、诊断：

- 项目上下文只读写本地 `working.context get/set/clear` 的一段普通文本。
- AI 与路由、诊断只返回显式 allowlist DTO；页面加载不自动执行真实模型健康测试。
- 组件中心只允许 `browser` 与 `search-ripgrep`，安装/更新必须手动点击并复用 component manager；状态响应不公开组件根目录、安装绝对路径或 Hub 凭据。
- 搜索与文件自检只在 NodeUI data-dir 下建立隔离临时目录，通过同一 Node local capability 调用 code.search、file.read 2.0 与 file.write preview，结束后清理；不读写用户项目、不下载组件、不执行 AI。
