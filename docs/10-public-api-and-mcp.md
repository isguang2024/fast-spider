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
