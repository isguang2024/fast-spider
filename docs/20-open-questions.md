# 20 开放问题（Current）

## 1. 当前已关闭的边界

以下决定属于当前事实，不作为开放设计项重新讨论；0.3.0 是完成 Machine-only 边界迁移的历史版本：

- Machine 是唯一远程资源边界。
- Node 以当前 OS 用户权限操作整台电脑；Fast Spider 不维护目录授权、路径白名单、目录注册表或目录 ID。
- `file_read`、`file_edit`、`code_search` 使用绝对 `path`；`shell_run`、`build_control` 使用绝对 `cwd`；`git_control` 使用绝对 `repositoryPath`；`ai_control` 在 create/send/fork/settings 等涉及 cwd 的动作中使用绝对 `workingDirectory`。
- Browser 允许 Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS；不维护 Fast Spider Origin 白名单。
- MCP 固定 18 个工具，不提供目录列表工具；`audit_log` 只读当前 Owner 的 Hub 本地 mutation audit，不依赖 Node 在线且不开放给 Direct Access Key；`thinking_team` 只提供调用侧角色/部门/流程/资料室协议且不调用本机 AI Provider；`working_context` 在同一工具内提供 Plan/Task + Markdown Task Workspace，不演化成长期 AI Memory。
- `agent.control` 已是多 Harness 控制面：当前 `codex` + `claude_code`；CC Switch 是只读 Routing SSOT。Harness、Routing Runtime、upstream Provider/model 与 EffectiveCapabilities 分层，不按模型品牌/客户端 alias 猜真实上游。
- CC Switch raw provider settings/meta/credential 不离开 Node；Fast Spider 不修改 Provider/Token/Takeover/Failover。真实 upstream 只有可验证 request/session correlation 时才声明。
- Codex 0.141.0 app-server 继续映射受控 discovery/Thread/Turn/Goal/Review/steer/respond 子集，不暴露 fs/command/process/MCP tool-call 第二执行链；同版本仍未公开 Automation API，因此不映射 Automations。
- Claude Code 当前以原生 CLI 2.1.207 `stream-json` + UUID/`--resume` 接入；Prompt 走 stdin，第一版只映射 text/session lifecycle，不开放 permission bypass 或伪造 Codex 输入类型。
- Local Bridge 使用当前用户 AF_UNIX/UDS；Codex app-server 与 Claude Code CLI 都由 Node Agent Manager 管理。

## 2. 进入后续阶段前决定

### Q1. 支持的平台与工具链

继续按 Windows 11/10、Ubuntu LTS amd64 为主基线；Linux arm64、其他发行版和 macOS 在实际发布需求出现时加入构建和 E2E 矩阵。Go、Playwright、Codex CLI、Claude Code CLI、CC Switch schema compatibility 和 MCP SDK 必须由 release gate/真实 E2E 持续验证。

### Q2. 文件编码与换行

默认只把有效 UTF-8（可带 BOM）作为文本编辑，保留原 BOM 与主要换行风格；无效 UTF-8/二进制拒绝文本编辑。其他编码若成为真实需求，必须增加显式转换 Action 和 Diff。

### Q3. Git hooks 与外部副作用

继续兼容用户现有 hooks、filter、credential helper 和签名配置，但在 commit、pull、push、worktree、build 和测试结果中显示风险摘要。不得偷偷修改全局 Git 配置，也不得把用户配置转换成隐式授权对象。

### Q4. Browser 高风险扩展

当前只维护隔离 Profile、固定动作和 Node 网络可达目标。真实浏览器 Profile、任意脚本、Trace/HAR、视频、下载自动执行或更多网络控制，只有出现真实需求时再单独设计 Capability、审计和清理策略。

### Q5. Artifact 与保留策略

继续使用内容寻址、大小/哈希校验、短期保留和定期清理。只有出现跨 Hub、多租户或容量瓶颈，才评估 PostgreSQL、对象存储或外部扫描器；不提前引入第二套存储路径。

### Q6. 更新与签名

个人版继续使用 Hub Ed25519 manifest、SHA-256/大小校验和人工可观察回退。Authenticode、独立 Release Key、安装器和常驻 updater 只有实际分发需求出现时再单独制定 ADR。

## 3. 以后再决定

- 多用户、团队/租户、RBAC、邀请、审批和按 Machine 的共享策略。
- Hub 多实例、PostgreSQL、事件总线、S3 兼容 Artifact 存储。
- HTTP/2、gRPC、QUIC 或其他传输替代 WSS。
- PTY/ConPTY 交互终端、真实浏览器 Profile、Trace/HAR/视频。
- TPM/硬件设备密钥、macOS notarization 和 Screen Recording 权限。
- 移动端、P2P、实时远程桌面、音频、通用输入和任意 TCP 转发，仍属于非目标。

## 4. 变化规则

真实需求变化时，先修改需求、威胁模型和相关 ADR，再修改 Contract、测试和实现。不得通过恢复已删除的目录对象、路径白名单或兼容工具来解决新问题；若需要更细的多人隔离，重新设计 Machine/租户边界并一次性替换当前模型。
