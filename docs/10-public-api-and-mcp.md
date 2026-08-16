# 公共 API 与 MCP（Current）

## 公网 MCP

Fast Spider MCP 通过 `/mcp` 提供 Streamable HTTP，使用标准 OAuth Authorization Code + PKCE。当前固定 17 个工具：

```text
machine_list
machine_get
capability_list
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
artifact_get
working_context
```

Current 不提供目录列表工具；`thinking_team` 是调用侧只读角色协作工具，`working_context` 继续提供项目 Plan/Task + Markdown Task Workspace。

`thinking_team` 不需要 `machineId`，只返回 9 个部门、17 个角色、角色指令、协作流程和 `working_context` 资料室协议；`providerInvocation=false`，不会创建本机 AI Session。

`ai_control` 现在是 Provider-neutral 多 AI Harness 控制面。`provider.readiness` 以安全只读预检区分 route/provider/harness/session backend/create readiness；`routing.status` 独立只读 CC Switch SSOT；`providers.list` 当前发现 `codex` 与 `claude_code` 并返回各自 `supportedActions`。`providerId` 决定 Harness，CC Switch Route 另行说明 `direct|cc_switch`、真实 Provider/model mapping 与 EffectiveCapabilities；客户端模型 alias 不等于 upstream model。

Codex 保留 Provider/Model、Skills/Hooks/Permission Profiles/Plugins/MCP discovery、Thread/Goal/Settings/Review、原生多类型 Turn、`outputSchema`、steer/respond 和 app-server auto-resume。Claude Code 第一版提供 models/capabilities 与 session list/get/create/send/watch/cancel/result/rename/archive/unarchive，使用原生 UUID + `stream-json` + `--resume`，Prompt 经 stdin。FS 不映射 Codex 的 `fs/*`/`command/exec/*`/`mcpServer/tool/call`，也不提供 CC Switch Provider/Token/Takeover 写入或 Claude permission bypass 第二执行链。

## ChatGPT 调用与工具发现

Hub 的 MCP `initialize` 会返回不超过 2 KiB 的常驻能力地图，并把 Server Title 固定为 `FastSpider_FS`。能力地图只说明九类能力、第一步和固定安全链路，不复制完整 Schema、参数示例或错误表。当 ChatGPT 已选择该 App 或用户显式 `@FastSpider_FS` 时，调用侧应先尝试工具而不是仅依据界面文本判断“插件未加载”：连接测试使用只读 `capability_list(view=overview)` + `machine_list`；需要本机操作但尚无 `machineId` 时先调用 `machine_list`。

`capability_list` 是唯一按需指南入口，没有新增第 18 个 guide/help 工具：

- 省略 `machineId` 和 `view`：兼容返回 Hub Capability Catalog，并附带精简 overview。
- 提供 `machineId`、省略 `view`：保持旧行为，只返回该 Machine 的能力目录。
- `view=catalog`：显式返回 Hub 或指定 Machine 的能力目录。
- `view=overview`：返回能力分类、17 个 MCP 工具的一句话 `toolSummaries`、黄金规则和推荐下一步；摘要足够选择入口，但不复制完整 Schema。
- `capabilitySummaries`：在 overview/catalog 的结果中，按 `capabilityId/version` 返回底层 Node capability 的一句话语义；原始 `capabilities` 仍保留完整 actions。
- `view=capability`：必须提供底层 `capabilityId`（例如 `shell.exec`），返回该 capability 的 actions、语义和对应 MCP 工具。
- `view=tool|workflow|error`：必须提供 `name`，一次只返回一个工具、流程或真实稳定错误码的有界指南；未知 view/name 明确拒绝。

工具指南固定包含 `whenToUse/requiredInputs/safeSequence/returns/recommendedNext/commonErrors/boundedExamples`；`view=capability` 额外返回底层 capability 的 actions、summary 和 `mcpTools` 映射。overview 不超过 8 KiB，单项指南不超过 12 KiB；示例有界且不含凭据、Prompt、Cookie、环境变量或本机事实。注册工具名、指南目录、overview 摘要和本文工具列表由自动测试对账。

Codex/Claude Code 的会话能力不是独立顶层工具；统一位于 `ai_control`。查询 Codex 会话列表使用 `action=session.list`，后续读取使用 `session.get/session.watch/session.result`。因此 ChatGPT App 工具页只显示 `Ai control` 属于正常设计。

ChatGPT 对已发布 MCP App 的工具/输入定义可能使用经批准的快照；当 FS 修改工具名、Schema 或工具描述后，需要在 ChatGPT App/Action 管理中执行 Refresh/重新批准才能取得新的定义。纯服务可用性仍以真实 MCP initialize/tools/list 和只读调用结果为准。仓库没有独立 ChatGPT App manifest 或第二套 Plugin metadata，第一层事实源继续是 MCP initialize、工具描述和 `capability_list`。

## 资源模型

所有需要操作本机的工具首先使用 `machineId`。文件系统和进程位置直接使用本机绝对路径：

- file_read/file_edit: `path`
- code_search: `path`
- shell_run/build_control: `cwd`
- git_control: `repositoryPath`
- ai_control session.create: `providerId + workingDirectory`; `providerId` 选择 Harness，不是上游 API Provider
- ai_control routing.status: 可选 `appType=claude|codex|claude-desktop`，只读 CC Switch 路由事实
- working_context: `projectPath`

`working_context` 保留 `get/set/clear` 默认 plan 兼容入口，并在同一工具中提供 `plan.init/plan.get/plan.list/plan.sync/task.update/markdown.list/markdown.read/markdown.append/progress.watch`。Plan 状态在 Node data-dir 中按 `projectPath + planId` 隔离；Markdown workspace 只操作项目内受绑定普通 `.md` 与受管区块，不保存聊天原文或凭据。

`code_search` 2.1 同一工具支持 content/files、glob/context，并返回 Managed ripgrep/native fallback 的稳定原因、扫描统计与分段耗时。`file_read` 2.0 同一工具支持 byte/line/head/tail/around/stat selectors；`file_edit` 2.1 的 mutation 仅返回固定元数据，preview 才返回 bounded diff。preview 不写盘并可安全重试；其余文件写 action 使用 CAS/原子替换且不自动重放。`shell_run/build_control` 可选 `runtime.kind=host|wsl`。

Job 后续操作只用 machineId + jobId。Artifact 获取只用 artifactId；Node 上传本机文件时使用 machineId + absolute path。`artifact_get.uploadFile/uploadJobLog/get` 优先回显有界原生 MCP 内容：PNG/JPEG 使用 `ImageContent`，小型 UTF-8 文本使用 `EmbeddedResource.text`，其余不超过 8 MiB 的内容使用 `EmbeddedResource.blob`；空内容只返回结构化元数据，不生成 malformed resource。`artifact_get.publishFile` 使用同样的绝对路径，但文件由 Node 直接上传 Hub Temporary Presentation Relay；Relay 不创建 Artifact/数据库记录，仅在调用方显式要求临时分享时生成 20 分钟短期 `ResourceLink`。browser/screenshot 的原生图片回显不附带 `publicUrl` 或 `ResourceLink`。

## OAuth

OAuth resource 是公开 MCP URL。授权页复用 Owner Web Session，用户点击允许/取消。授权页 CSP 只允许当前经过后端验证的 callback origin，避免开放任意表单跳转。

Hub 在 loopback 后由 Nginx 反代时，MCP SDK 自带 localhost Host 防护关闭；Fast Spider 自己的 `AllowedHosts` + 反向代理仍负责 Host 边界，避免把合法公网 Host 误判为 DNS rebinding。

## Node HTTP/WSS

Node 首次登记使用 Connection Token 调用机器登记接口；运行时只使用设备凭据/WSS。Connection Token 不具备 MCP 权限，也不作为长期设备凭据。

## 错误原则

- 相对路径在要求本机路径的接口上返回 `ABSOLUTE_PATH_REQUIRED`。
- OS 拒绝访问时返回权限/系统错误，不伪装成目录授权失败。
- MCP 不再返回旧目录对象的不存在、禁用或越界错误。
