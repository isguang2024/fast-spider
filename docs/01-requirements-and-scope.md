# 01 需求与范围

## 1. 需求分级

- **MUST**：该阶段不可缺少，缺失即不能验收。
- **SHOULD**：默认应实现，只有明确原因才能延后。
- **MAY**：预留或后续能力，不阻塞当前阶段。

## 2. 角色

| 角色 | 说明 |
|---|---|
| Owner | 自托管实例拥有者，MVP 唯一最高权限主体 |
| Operator | 未来可管理 Machine 和任务的用户 |
| MCP Client | GPT、Claude、Codex 或其他 MCP Host |
| Local Client | 通过当前用户 AF_UNIX/UDS Local Bridge 访问 Node 的本机工具 |
| Hub | 身份、路由、状态、审计和 Artifact 控制面 |
| Node | 当前 OS 用户上下文中的本机执行面和最终裁决者 |
| AI Harness | Codex、Claude Code 等本机 AI 执行环境，不持有 Hub 权限 |
| Routing Runtime | CC Switch 等模型/上游路由层；当前 Fast Spider 只读其事实，不管理凭据或切换状态 |

## 3. 功能需求

### 3.1 Hub

- MUST 提供 HTTPS 公网入口、OAuth 2.1 授权和 MCP Server。
- MUST 管理用户、客户端、Machine、Job、Event、审计和 Artifact 元数据。
- MUST 维护 Node 长连接注册表并按 `machineId` 路由；Hub↔Node 的 Capability Request/Response（包括 `file.read` 内容和 `file.write/edit` 的 `oldText`/`newText`）直接复用同一条 WSS。
- MUST 在请求离开 Hub 前完成身份、Machine 归属、Schema 和资源限制校验。
- MUST 仅将 HTTPS 用于 Machine 登记、设备 Token，以及 Artifact/Presentation 等大文件数据面；不得为普通 Capability 另建 HTTP 控制通道。
- MUST 不直接读取 Node 文件或执行 Node 命令。
- MUST 提供 Web Console、健康检查、备份、升级和恢复说明。
- SHOULD 提供复用同一应用服务的 REST/WebSocket API。

### 3.2 Node 生命周期

- MUST 使用后台生成的连接令牌完成 Machine 登记；成功后不得持久化令牌。
- MUST 为每台设备生成独立身份与私钥。
- MUST 只主动连接 Hub，不默认监听公网或局域网地址。
- MUST 上报 OS、架构、版本、能力和忙闲状态。
- MUST 支持心跳、断线重连、设备凭据轮换、吊销和紧急断开。
- MUST 提供只管理连接和本机设置的 loopback 控制界面；不得提供第二套远程 Capability API。
- MUST 以当前 OS 用户运行并直接使用其可访问的整台电脑资源；Fast Spider 不添加目录授权层。

### 3.3 文件和代码

- MUST 接受绝对 `path`，并按平台规则验证存在性、类型、大小和 UTF-8 编码。
- MUST 提供有界分段文件读取、代码搜索，以及带 expected SHA/CAS 的原子精确编辑和 Diff；当前不提供通用覆盖写文件 API。
- MUST 防御 NUL、无效路径格式、TOCTOU、符号链接和平台特殊文件导致的意外行为。
- MUST 在结果中报告真实目标的结构化摘要；不得把秘密、Token 或不必要的环境信息写入日志。
- MUST 对大文件、二进制、搜索结果、事件和 Artifact 设置硬上限。

### 3.4 Shell、构建和进程

- MUST 使用绝对 `cwd` 执行非交互命令，显式传递 `argv[]`，避免把参数拼接成隐式命令。
- MUST 流式返回 stdout/stderr，统一为 UTF-8 事件。
- MUST 支持 deadline、超时、取消和完整进程树终止。
- MUST 提供 Job 状态、日志游标、断点续读和幂等去重。
- MUST 以当前 OS 用户权限运行，不自动提权。
- MUST 限制环境变量、输出、并发、运行时间、Artifact 和磁盘占用。

### 3.5 Git

- MUST 使用绝对 `repositoryPath` 支持 status、diff、staged diff、log、show、branch、当前分支和 worktree 列表。
- SHOULD 支持受管 worktree 创建/删除、add、commit、fetch、pull、push。
- MUST 默认调用系统 Git，兼容用户现有配置、凭据、LFS 和 hooks；高风险副作用必须可见并审计。
- MUST 对 remote URL、凭据和 hooks/filter 输出脱敏。

### 3.6 Job、事件和 Artifact

- MUST 将长操作建模为 Job，而不是保持单个同步 HTTP 请求。
- MUST 支持事件序号、游标、重连续读、背压和保留窗口。
- MUST 支持日志、Diff、报告、截图和压缩包的分块上传、哈希校验和大小限制。
- MUST 自动清理过期 Job 事件、日志和 Artifact。

### 3.7 浏览器与截图

- MUST 支持隔离 Profile 的受管浏览器、页面导航、点击、输入、等待、结构化页面摘要、截图、控制台日志和网络错误。
- MUST 允许 Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS 目标，不要求 Fast Spider Origin 白名单。
- MUST 支持桌面、显示器、窗口和页面的一次性截图。
- MUST 在无桌面会话、锁屏或 OS 权限不足时返回结构化错误。
- MUST 不实现连续视频、音频或通用远控。

### 3.8 Local Bridge 与 AI

- 当前用户 AF_UNIX/UDS Local Bridge MUST 默认随 Node 启用，并可由本机开关关闭。
- Local Bridge 不监听 TCP/loopback HTTP，不建立 Local Client 注册、Token、Grant 或 Approval。
- Local Bridge MUST 直接复用 Machine、Capability、Job、资源限制和审计语义。
- MUST 提供 Provider-neutral `routing.status` 与 `providers.list`，明确区分 AI Harness、Routing Runtime、Upstream Provider/Model；当前 Harness 至少包括 `codex` 与 `claude_code`。
- CC Switch discovery MUST 只读 `~/.cc-switch/cc-switch.db`，不得返回 raw `settings_config`/`meta`、API Key、Token、Cookie、Authorization 或带 userinfo/query 的 Endpoint；不得通过 Fast Spider 修改 Provider、Takeover、Failover 或凭据。
- `providers.list` MUST 返回每个 Harness 的 `supportedActions`；调用方不得假设 Codex 与 Claude Code 支持完全相同的 action。
- `ai_control.session.create` MUST 使用绝对 `workingDirectory`；各 Harness 在提供 cwd 类参数时同样按绝对路径校验。
- Codex `session.create/send/steer` MUST 使用公开原生 text/skill/image/localImage/mention UserInput；`session.steer` MUST 带 active `turnId`。Codex `outputSchema`、rollback/Goal/Settings/Review/respond/resume MUST 继续按公开 app-server schema 和现有安全边界执行。
- Claude Code MUST 使用原生 Session UUID、`stream-json` 与 `--resume`；Prompt MUST 从 stdin 传入而不是写入 argv。第一版只公开经过真实 CLI 验证的 text/session lifecycle，不用提示词伪造 Codex Skill/Image/Mention 协议。
- Claude Code Session index MUST 只保存控制事实、终态摘要、usage 与 RouteSnapshot，不复制完整 Prompt/对话；`actualUpstream` 只有在 CC Switch request log session ID 与 Claude native Session ID 精确匹配时才能声明。
- EffectiveCapabilities MUST 按 Harness ∩ Routing/conversion ∩ Upstream ∩ Fast Spider policy 派生；不确定能力必须保持 `unknown`，不得只按模型品牌或 UI 别名推断。
- Codex `fs/*`、`command/exec/*`、`process/*`、`mcpServer/tool/call` 以及 Claude/CC Switch 的任意配置写入/权限绕过 MUST NOT 通过 `ai_control` 形成第二执行链。
- AI/Provider 凭据 MUST 保留在 Node/原生 Provider/CC Switch 本机，不能经 Hub 传播。

## 4. 非功能需求

### 4.1 安全

- 远程授权只绑定 Owner/Client 与 Machine；Node 以当前 OS 用户权限执行整台电脑上的请求。
- Hub 与 Node 双重校验，Node 为最终裁决者；不建立目录授权、路径白名单或本地 Grant 引擎。
- TLS 必需；设备密钥可轮换和吊销；可选 mTLS。
- 高风险操作必须可审计、可取消、可超时和受资源限制。
- 更新包必须签名并支持回滚；日志、错误和 Artifact 必须脱敏。

### 4.2 可靠性与容量

- WSS 控制请求不做断线自动重放：请求帧未被 Node 完整接收时不执行；请求已执行但响应丢失时，结果为 uncertain。
- 连接丢失后，只读/查询调用可由调用方重试；写操作和其他副作用调用必须先重新读取或查询状态，再决定后续动作，不能自动重跑。
- Job 启动必须携带 `idempotencyKey` 并由 JobManager 去重；连接断开不取消已经启动的 Job，调用方重连后按 Job ID 查询。
- `file.write/edit` 必须使用临时文件、fsync、原子替换和 expected SHA CAS，避免半文件并检测并发修改。
- 所有队列、日志、事件、Artifact 和临时文件都有硬上限和清理任务。

### 4.3 跨平台

- Hub：Linux 为生产主平台，开发期可运行于 Windows。
- Node：Windows 10/11、主流 Linux 发行版；macOS 后续。
- 路径、大小写、换行、代码页、进程树和 OS 权限差异由 Node 平台层处理。

## 5. MVP 边界

### MVP 包含

- Owner 单用户模式、单实例 Hub、SQLite WAL、WSS 长连接和 Windows/Linux Node。
- Machine 登记、能力发现、绝对路径文件/搜索/编辑、Shell Job、绝对仓库路径 Git、Build、Event、Artifact、浏览器、截图、Local Bridge、Working Context、Thinking Team、Codex + Claude Code Harness、只读 CC Switch Routing facts 和 17 个 MCP 工具。

### MVP 不包含

- 多实例 Hub、大规模多租户与复杂组织 RBAC。
- 实时远程桌面、远程音频、通用输入控制、任意 TCP 转发或 P2P 打洞。
- 自动提权、隐蔽运行、未经明确请求的真实浏览器登录态接管。
- 0.3.0 之前的目录 API、目录授权或兼容双路径。
- Codex Automations 不属于本项目映射范围：0.141.0 app-server 当前没有公开 Automation API。
