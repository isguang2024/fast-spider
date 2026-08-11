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

Windows/Linux 当前使用用户 data-dir 下的 AF_UNIX/UDS；默认随 Node 启用，可通过本机设置/启动参数关闭。它不监听局域网 TCP，不引入 Local Client 注册、Token、Grant、Lease 或逐请求 Approval。当前 OS 用户及 data-dir ACL 就是本地信任边界。

Provider Token、Codex/ChatGPT 本地认证和其他 Provider secret 只保留在 Node/Provider 本机，不进入 Hub、MCP 响应或 Working Context。

## 2. 多 AI Harness 与 CC Switch Routing

0.4.0 当前内置两个 AI Harness：

```text
providerId=codex        -> Codex app-server --stdio
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

这些 action 同时通过公网 MCP `ai_control` 和 Local Bridge 进入同一 Agent Manager。`providers.list` 返回每个 Harness 的 `supportedActions`，调用方不能假设所有 action 在 Codex 与 Claude Code 上都存在。Codex 保留完整结构化扩展面；Claude Code 第一版只开放已通过真实 CLI 验证的 lifecycle/discovery 子集。FS 不把任一 Provider 的全部内部命令一比一暴露出来。

### `routing.status`

这是 Provider-neutral 的 CC Switch 路由 discovery。可选 `appType=claude|codex|claude-desktop`；省略时读取三类 Route。结果区分 `proxyEnabled`、`takeoverEnabled`、`liveTakeoverActive`，因此“本地代理进程已开”不会被误判成“当前 Harness 已接管”。同时返回 DB/current settings 的 `selectionConsistent`，用于发现 CC Switch SSOT 与设备选择投影漂移。

Model Mapping 目前识别 Claude 的 `ANTHROPIC_MODEL` / Sonnet / Opus / Haiku 角色、Claude Desktop `claudeDesktopModelRoutes`、Codex Provider 配置中的 `model/review_model/model_provider/wire_api/service_tier`。模型列表最多返回有界摘要，不返回原始 Provider 配置。

## 4. Codex Session 与 Turn

### `session.create`

必需：绝对 `workingDirectory`。可选：`model`、`thinking`，以及首个 Turn 的输入。

如果没有任何 Turn input，只创建 Codex Thread 并返回 `phase=ready`。如果存在 text/Skill/Image/Mention 任一输入，则创建 Thread 后立即启动 Turn，返回 `sessionId + turnId + phase=running`。

Git 子目录和 linked worktree 会解析到主工作树对应的 Codex Desktop 项目展示上下文，但真正执行 cwd 保留为调用方传入的绝对 `workingDirectory`。跨主 Git 项目继续工作时必须新建 Session。

### `session.send`

只能向空闲 Thread 启动下一 Turn。若 Codex 已有 active Turn，返回 `AGENT_SESSION_BUSY`，Fast Spider 不把第二条 send 暗中转换成 steer。

`session.send` 可以覆盖同一项目内的 `workingDirectory`、model、reasoning effort、personality、serviceTier 与 reasoning summary；跨项目目录被拒绝。

### `session.steer`

`session.steer` 映射 Codex `turn/steer`。调用方必须提供当前 active `turnId` 作为 `expectedTurnId`，Codex 会在 Turn 已切换时拒绝请求，从而避免把纠偏指令误发给下一 Turn。steer 只接受 text/skill/image/localImage/mention 与 `imageDetail`，不改变 model、cwd、outputSchema 或 Thread settings。

### `session.respond`

Codex app-server 可以在 Turn 中主动发送 Server Request。Fast Spider 当前把以下请求变成有界 `session.watch` 事件并保存为 pending request：

- `item/tool/requestUserInput` → `user_input.requested`
- `item/commandExecution/requestApproval` / `item/fileChange/requestApproval` → `approval.requested`
- `mcpServer/elicitation/request` → `mcp_elicitation.requested`

事件包含 opaque `requestId`；调用方随后用 `session.respond` 回答。request_user_input 使用 question-id → answers；command/file approval 只允许单次 `accept|decline|cancel`，不暴露 `acceptForSession` 或策略修改；MCP form elicitation 的 accept 必须提供有界 `responseContent`。`item/permissions/requestApproval` 不通过远程任意 permission object 响应，而是明确拒绝，Permission 继续使用 named profile 管理。

### `session.watch` / `session.result`

`session.watch` 使用 Node 维护的有界事件 cursor，返回标准化 Turn/assistant/status/error/interactive request 事件；最长单次 long-poll 15 秒。`session.watch` 与 `session.get` 都同时返回当前 `pendingRequests` 快照，因此即使客户端断线、事件 cursor 过旧或事件环发生截断，也能重新取得仍待回答的 requestId。

`session.result` 读取 Codex 持久 Thread 的最新 Turn 事实，返回真实 status 与 `finalAgentMessage`。即使 Turn 使用了 `outputSchema`，Fast Spider 仍保留 raw final message，不宣称已经变成强类型对象；调用方可在需要时自行 JSON decode。

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

Codex app-server 是可重启的本机子进程。Fast Spider 记录当前进程内已加载 Thread；若 app-server 崩溃或被重启，下一次 Turn/Review 前自动调用官方 `thread/resume(threadId)` 重新加载持久 Thread，再继续操作。调用方无需维护第二个 resume 状态机，也不需要新增公开 `session.resume`。

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

可安全重新查询：Provider/Model/Project/Skill/Hook/Permission/Plugin/MCP 状态 discovery、Session list/get/watch/result、Goal get。

不可宣称可无脑重试：create/send/steer/respond/cancel/rename/archive/unarchive/delete/fork/compact/rollback、Goal set/clear、settings.update、review。尤其 steer/respond 可能已送达 Codex 但 Hub 响应丢失，调用方必须先 watch/get 查询真实状态。

所有 Thread/Turn/Goal/Settings/Review 状态变更及 steer/respond 进入 Hub mutation audit；Provider secret、完整 prompt、交互回答正文、原始内部事件和环境变量不写入审计详情。

Codex 本身还公开 `fs/*`、`command/exec/*`、`process/*`、`thread/shellCommand`、`mcpServer/tool/call` 等接口，但这些不会通过 `ai_control` 再暴露。文件、Shell、Git、Build、Artifact、Browser 继续只走 Fast Spider 自己的 Capability/Job/Audit 链，避免两套权限和两套副作用状态机。

## 14. Claude Code Provider（0.4.0）

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
