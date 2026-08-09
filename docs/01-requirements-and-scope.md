# 01 需求与范围

## 1. 需求分级

本文件使用以下优先级：

- **MUST**：该阶段不可缺少，缺失即不能验收。
- **SHOULD**：默认应实现，只有明确原因才能延后。
- **MAY**：预留或后续能力，不阻塞当前阶段。

## 2. 角色

| 角色 | 说明 |
|---|---|
| Owner | 自托管实例拥有者，MVP 唯一最高权限主体 |
| Operator | 未来可管理机器、Workspace 和任务的用户 |
| MCP Client | GPT、Claude、Codex 或其他 MCP Host |
| Local Client | 通过当前用户 AF_UNIX/UDS Local Bridge 访问 Node 的本机工具 |
| Hub | 身份、权限、路由、状态、审计和 Artifact 控制面 |
| Node | 本机执行面和最终权限裁决者 |
| Agent Provider | Codex 或其他本地 AI 服务，不持有 Hub 权限 |

## 3. 功能需求

### 3.1 Hub

- MUST 提供 HTTPS 公网入口、OAuth 2.1 授权和 MCP Server。
- MUST 管理用户、客户端、机器、Workspace 逻辑目录、权限、Job、事件、审计和 Artifact 元数据；当前个人 MVP 不建立通用审批系统。
- MUST 维护 Node 长连接注册表并按 machineId 路由。
- MUST 在请求离开 Hub 前完成第一层授权和参数约束。
- MUST 不直接读取 Node 文件或执行 Node 命令。
- MUST 提供 Web Control Center，静态资源尽量嵌入 Hub。
- MUST 提供健康检查、就绪检查、备份、升级与恢复说明。
- SHOULD 提供 REST/WebSocket API，复用同一应用服务。
- MAY 在未来支持多实例、PostgreSQL 和对象存储。

### 3.2 Node 生命周期

- MUST 使用后台生成的连接令牌完成机器登记；连接令牌属于 Owner、可在有效期内重复登记多台 Node，不绑定具体设备。令牌只允许登记接口，Node 成功后不得持久化该令牌。
- MUST 生成每台设备独立身份与私钥。
- MUST 只主动连接 Hub，不默认监听公网或局域网地址。
- MUST 上报 OS、架构、版本、能力和忙闲状态。
- MUST 支持心跳、断线重连、证书轮换、吊销和紧急断开。
- MUST 提供简单本地控制界面，显示连接状态并管理本机配置、已授权目录和危险能力开关；Windows 默认双击客户端即可打开该界面，不要求网页登录。
- 本地控制界面 MUST 只绑定 loopback，不向公网/局域网暴露，也不能形成第二套远程 Capability API。
- SHOULD 支持可验证备份/恢复、明确版本检查和手工可观察升级；自动更新只有真实需要时再设计。

### 3.3 Workspace

- MUST 由本机用户选择并授权真实目录。
- MUST 对外只暴露 opaque workspaceId 和显示名。
- MUST 保存规范化根路径、Git 根、文件系统特征和允许能力。
- MUST 防御 `..`、绝对路径注入、符号链接、junction、大小写和重解析点逃逸。
- MUST 支持启用、禁用、删除授权和少量实际需要的本机权限变化。
- Workspace 禁用/删除必须立即阻止新请求；不为普通个人使用维护通用 Capability Lease 状态机。
- SHOULD 支持子目录规则、只读 Workspace 和排除模式。

### 3.4 文件和代码

第一批实现对齐 DevSpace 已验证的能力面：

- MUST 列表、元数据、文本读取和分段读取。
- MUST UTF-8/BOM 处理、二进制检测和最大读取限制。
- MUST glob 与 ripgrep 风格搜索。
- MUST 原子写入、小范围精确编辑和 patch。
- MUST 并发修改检测、修改前后摘要和 Diff。
- MUST 创建目录、移动、复制、重命名。
- MUST 删除到可恢复区域；永久删除必须独立授权。
- MUST 所有路径都重新落在 Workspace 根内，不能信任客户端拼接结果。
- SHOULD 支持文件变更监听和事件合并。

### 3.5 Shell、构建和进程

- MUST 在指定 Workspace/cwd 执行非交互命令。
- MUST 流式返回 stdout/stderr，统一为 UTF-8 事件。
- MUST 支持 deadline、超时、取消和完整进程树终止。
- MUST 提供 Job 状态、日志游标和断点续读。
- MUST 默认以普通用户权限运行。
- MUST 对环境变量使用白名单/显式传入，并对日志脱敏。
- MUST 限制输出、并发、运行时间、Artifact 和磁盘占用。
- SHOULD 支持受控后台任务。
- MAY 后续支持 PTY/ConPTY；MVP 不依赖交互终端。
- 构建、lint、test、typecheck 初期通过受控 Shell Profile 实现，不建立语言矩阵式构建系统。

### 3.6 Git

- MUST 支持 status、diff、staged diff、log、show、branch、当前分支和 worktree 列表。
- SHOULD 支持受管 worktree 创建/删除、add、commit、fetch、pull、push。
- 读操作可按 Workspace 策略默认允许。
- Git 权限按实际副作用分为 `git-write`、`git-network`、`git-hooks` 三类即可，不再为 commit/pull/push/deleteWorktree 分别堆一层权限。
- 默认调用系统 Git，以兼容现有凭据、配置、LFS 和 hooks；高风险 hooks 行为必须可见并记录。

### 3.7 Job、事件和 Artifact

- MUST 将长操作建模为 Job，而非保持单个同步 HTTP 请求。
- MUST 支持事件序号、游标、重连续读、背压和保留窗口。
- MUST 提供 accepted、progress、stdout、stderr、diff、artifact、warning、result、error、heartbeat 等实际运行事件；`approval_required` 仅在未来多人/审批模式真正引入时再加入主链路。
- MUST 支持日志、Diff、报告、截图、压缩包的分块上传、哈希校验和大小限制。
- MUST 自动清理过期 Job 事件、日志和 Artifact，不能无限增长。
- SHOULD 支持断点续传。

### 3.8 浏览器与截图

- Phase 5 MUST 支持隔离 Profile 的受管浏览器、页面导航、点击、输入、等待、结构化页面摘要、截图、控制台日志和网络错误。
- MUST 对访问用户真实登录态进行单独显式授权。
- MUST 支持桌面、显示器、窗口和页面的一次性截图。
- MUST 在无桌面会话、锁屏或权限不足时返回结构化错误。
- MUST 不实现连续视频、音频或通用远控。

### 3.9 Local Bridge 与 AI

- 当前用户 AF_UNIX/UDS Local Bridge MUST 默认随 Node 启用，并可由本机开关关闭。
- Windows/Linux 共用同一 AF_UNIX/UDS Local Bridge 实现；Local Bridge 自身不监听 TCP/loopback HTTP。Node 的独立本地控制 UI 可绑定 loopback，但不承载 MCP/Capability 调用。
- 当前 OS 用户 + Node data-dir 权限作为本机信任边界；不建立 Local Client 注册、Token、Grant 或 Approval。
- Local Bridge MUST 直接复用既有 Workspace、Capability 和危险本机权限检查。
- MUST 提供 provider-neutral 的 provider/model/project/session create/list/get/send/watch/cancel/result/rename/archive 能力。
- Provider Token MUST 保留在 Node 本机，不能经 Hub 传播。
- 首版不开放通用 AI→AI 递归或 desktop-owned/handoff 第二执行链。

## 4. 非功能需求

### 4.1 安全

- 当前个人自托管 MVP 以 Owner 身份、Machine 和 Workspace 为主要授权边界；写入、Shell、Git 网络/副作用、Build 等真正危险操作才保留额外本机权限。
- Hub 与 Node 双重校验，Node 为最终裁决者；不为每个 Capability/Action 再建立一套通用 Grant/Lease 引擎。
- TLS 必需；设备密钥可轮换和吊销；可选 mTLS。
- 高风险能力必须可在 Node 本机关闭/收紧；只有未来多人或不可信 Client 场景再评估逐次 Approval。
- 更新包必须签名并支持回滚。
- 审计记录必须防止普通用户静默修改。

### 4.2 可靠性

- 控制消息采用至少一次传输语义，执行端通过 idempotencyKey 去重。
- 已完成的写操作和命令在断线后不能自动重跑。
- Job 必须具有明确终态；失联任务进入 lost，而不是永久 running。
- 取消失败必须报告，并由回收器检测孤儿进程。

### 4.3 性能与容量

MVP 目标以单人自托管为准：

- 一个 Hub 支持至少 50 个已注册 Node、10 个同时在线 Node。
- 默认每个 Node 同时最多 2 个写/执行 Job，读取和 watch 使用独立轻量限额。
- 4 KiB–64 KiB 事件分块；大文件使用 Artifact 通道，不塞入控制消息。
- Hub 和 Node 空闲时保持低 CPU；心跳不得形成探活风暴。
- 所有队列、日志、事件、Artifact 和临时文件都有硬上限和清理任务。

具体数值在压测后调整，但协议必须支持限额协商。

### 4.4 跨平台

- Hub：Linux 为生产主平台，开发期可运行于 Windows。
- Node：Windows 10/11、主流 Linux 发行版；macOS 后续。
- 路径、大小写、换行、代码页、进程树和权限差异必须在 Node 平台层处理。

### 4.5 可维护性

- Hub 和 Node MVP 同用 Go，减少双语言成本。
- 内部模块依赖单向，Adapter 不包含业务规则。
- 协议契约只有一个来源，生成类型不能人工复制两份。
- 所有文本、源码、协议和默认日志使用 UTF-8。
- 不引入 Redis、NATS、Kafka、Kubernetes 或微服务。

## 5. MVP 边界

### MVP 包含

- Owner 单用户模式。
- 单实例 Hub。
- SQLite WAL。
- WSS 长连接。
- Windows/Linux Node。
- 机器、Workspace、文件、搜索、编辑、Shell Job、只读 Git、事件、Artifact、MCP Adapter 和基础 Web Console。

### MVP 不包含

- 多实例 Hub。
- 大规模多租户与复杂组织 RBAC。
- 实时远程桌面、远程音频、通用输入控制。
- 任意 TCP 转发、P2P 打洞。
- Kubernetes、复杂消息队列和独立对象存储。
- 自动提权、隐蔽运行、未经授权的真实浏览器登录态。

## 6. 验收基线

最终设计和代码必须覆盖需求源定义的 15 个关键场景。各阶段的具体验收和回滚条件见 [19-roadmap.md](19-roadmap.md)，安全拒绝路径见 [09-security-threat-model.md](09-security-threat-model.md)，协议状态与幂等规则见 [06-wire-protocol.md](06-wire-protocol.md) 和 [07-job-and-event-model.md](07-job-and-event-model.md)。
