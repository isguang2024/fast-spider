# 08 身份与权限

## 1. 安全目标

Fast Spider 的授权不能退化为“拿到 Hub Token 就能控制所有 Node”，但个人自托管 MVP 也不需要把每个 Action 都做成企业级审批系统。当前实际边界是：

```text
Owner / Client 身份
→ Machine
→ Workspace
→ 少量真正危险的本机权限（write / shell / git-* / build）
→ 固定参数、路径、网络与资源安全检查
```

Hub 进行身份与资源归属判断；Node 使用本地 Workspace 和运行时事实进行最终裁决。浏览器、截图、读取等常用能力不再额外叠一层独立 grant。任何外部协议仍不能以绝对路径、Node 名称或 UI 选择状态替代 Workspace 授权。

## 2. 身份实体

| 实体 | 标识 | 认证方式 | 说明 |
|---|---|---|---|
| Owner Account | `ownerId` | 本地用户名/密码 + Web Session | 当前实例唯一的人类控制主体 |
| OAuth Client / MCP Client | `clientId` | OAuth Authorization Code + PKCE Token | 经 Owner 登录批准后映射回同一 Owner，不形成第二套用户权限 |
| Machine | `machineId` | 设备密钥/凭据 | 逻辑设备，不等于连接 |
| Device Credential | `credentialId` | 私钥证明，可选 mTLS | 每台设备可轮换多个凭据 |
| Node Connection | `connectionId` | 认证后的短期连接上下文 | 带 generation，不持久充当设备身份 |
| Workspace | `workspaceId` | Node 本地 Registry 解析 | 对外 opaque，不暴露路径 |
| Local Connection | `connectionId`（临时） | 当前用户 data-dir 下 AF_UNIX/UDS | 只用于运行日志，不形成长期权限主体 |
| Session | `sessionId` | 继承 Machine/Workspace 边界 | 只是 Provider 会话，不扩大权限 |

## 3. Owner 模式与未来多用户

### MVP Owner 模式

- 一个实例至少有一个 Owner。
- Owner 可以配对/吊销机器、查看审计和配置策略。
- Owner 对已配对 Machine 和已启用 Workspace 拥有正常使用权；写入、Shell、Git 网络/副作用和 Build 仍受 Node 本地开关约束。
- 外部 MCP Client 使用当前单一 `fast-spider` scope；它只证明“已由 Owner 授权连接”，Machine/Workspace 归属和 Node 本地边界仍逐次校验。

### 未来多用户

当前不实现 Organization/RBAC。未来如果真的进入多人共享场景，再根据实际需求决定是否需要 Organization、Owner/Admin/Operator/Viewer 和细粒度资源授权；当前代码不为这些角色提前设计执行分支。

## 4. 认证域

### 4.1 Owner Account

- 首次启动时 Hub 生成短时一次性 `bootstrap-token`。`spiderctl setup-url` 把它放入 `/setup#code=...` 的 URL fragment，浏览器自动填入，值不会进入普通 HTTP/Nginx 请求日志。
- 首次设置页面创建当前实例唯一的本地管理员用户名和密码；密码使用随机盐 PBKDF2-SHA256 哈希，数据库不保存明文。
- 登录后 Hub 签发独立 HttpOnly、SameSite Web Session Cookie，并对后台写操作校验 CSRF；登录失败按真实客户端 IP 做小型内存限速，不引入账户锁定表。
- 后台可以创建连接令牌：明文只展示一次，数据库只保存哈希，可设置有效期、查看最近使用并随时撤销。连接令牌属于当前 Owner，可在有效期内重复登记多台 Node，不绑定具体设备；它只允许机器登记，不能访问 MCP 或管理 REST。
- 修改管理员密码会保留当前浏览器、撤销该 Owner 的其他 Web Session，但不误删已经明确批准的 OAuth Authorization。
- 当前不实现多用户、密码找回邮件或外部 OIDC；个人自托管实例通过备份恢复。出现真实多人需求后再扩展。

### 4.2 MCP Client

- 公网 MCP 使用 Authorization Code + PKCE S256，并支持 Dynamic Client Registration。
- Redirect URI 必须来自已注册精确 URI，注册阶段又受本机配置的 redirect host allowlist 约束；公网 redirect 必须 HTTPS。
- authorize endpoint 复用 Owner Web Session：未登录先跳转登录，登录后显示客户端并点击允许/取消，不输入 Token。
- Access Token 1 小时；Refresh Token 30 天并轮换；数据库只保存 Token 哈希。
- 每次批准形成稳定 Authorization 记录，绑定 `clientId + ownerId + resource + scope`。后台可查看创建、最近使用、到期和撤销状态；撤销会使该授权下所有 Access/Refresh Token 失效。
- OAuth 只适配客户端认证，不替代 Node Workspace/危险能力权限，也不扩散为长期 Node WSS 设备认证。

### 4.3 Node

- Owner 在 Web 后台创建连接令牌；Node 默认打开本地控制界面，填写 Hub、连接令牌和设备名称完成登记。CLI `connect` 仅作为高级/无界面入口。
- Node 本机生成设备私钥，并用连接令牌向唯一的机器登记接口提交设备公钥、名称、OS、架构和版本；连接令牌只决定新设备归属哪个 Owner，本身不与设备绑定，也不写入 Node 状态或本地配置文件。
- 登记成功后 Node 只保存 machineId、credentialId、Hub 公钥/指纹和本机设备私钥。后续连接使用设备证明和短期设备凭据，不依赖连接令牌或 OAuth。
- 同一有效连接令牌可以登记多台独立 Node；每台 Node 获得自己的 machineId 和设备私钥。令牌撤销只阻止后续设备登记，不会静默撤销已经登记的机器；设备撤销仍在后台设备列表单独执行。
- 设备被后台撤销后，再执行 `connect` 可创建新的 machineId；Node 本机 Workspace Registry 不需要删除或重建。

### 4.4 本机 Local Bridge

- Windows/Linux 使用当前用户 data-dir 下的 AF_UNIX/UDS；不监听 localhost TCP。
- 不注册 `localClientId`、Token、公钥或独立 Capability Grant；连接只生成临时 connectionId 用于日志。
- Local Bridge 请求仍必须携带真实 workspaceId，并复用 Workspace 状态、路径检查和 `write/shell/git-*/build` 等现有本机权限。
- 如不需要本机入口，用户可直接用 Node 启动参数整体关闭 Local Bridge。

## 5. 当前个人模式权限模型

MVP 不实现通用 Grant 表、策略 DSL、Capability Lease 或逐 Action Approval。实际判断只保留几层：

- Owner/Client 身份有效，Machine 属于该 Owner 且在线。
- 请求必须使用 opaque `machineId + workspaceId`；Workspace 在 Node 本机存在且启用。
- 读取、搜索、隔离浏览器和一次性截图直接使用 Workspace 基础授权。
- `write`、`shell`、`git-write`、`git-network`、`git-hooks`、`build` 是当前少量额外本机权限。
- 每个 Capability 仍执行自己的参数白名单、路径限制、URL/SSRF、并发、大小、deadline 和幂等检查。
- Node 永远可以拒绝请求；Hub 不能绕过 Node 本地边界。

这套模型的目标是让个人机器“登录一次、配置一次就正常用”，而不是复制 Token、每次操作都弹确认或维护短期授权。

## 6. 未来多人模式

只有出现以下真实需求时，再通过 ADR 引入 Grant/Lease/Approval：

- 多个互不信任用户或团队共享同一 Hub/Node。
- 需要按 Client、角色、时间窗口精细授权。
- 需要合规审批、双人确认或临时第三方访问。

未来扩展不得改变两个基础原则：Workspace 路径仍只由 Node 本机解析；Node 仍拥有最终拒绝权。当前代码不为未来模式预埋长期双路径执行逻辑。

## 7. 权限校验流程

```mermaid
flowchart TD
    A[收到外部请求] --> B{Client/Owner 认证有效?}
    B -- 否 --> X1[拒绝 AUTH_REQUIRED]
    B -- 是 --> C{Machine 属于当前 Owner 且在线?}
    C -- 否 --> X2[拒绝 MACHINE_NOT_FOUND/OFFLINE]
    C -- 是 --> D[路由到认证 Node]
    D --> E{Workspace 存在且启用?}
    E -- 否 --> X3[拒绝 WORKSPACE_NOT_FOUND/DISABLED]
    E -- 是 --> F{该动作需要额外本机权限?}
    F -- 是且未开启 --> X4[拒绝 PERMISSION_DENIED]
    F -- 否或已开启 --> G[路径/参数/网络/资源校验]
    G --> H{校验通过?}
    H -- 否 --> X5[返回结构化错误]
    H -- 是 --> I[Capability Engine 执行]
```

## 8. 会话上下文

MCP/Web 后续可以保存“当前 Machine/Workspace”以减少重复参数，但它只是易用性上下文：

- 不包含绝对路径。
- 不扩大 Workspace 或额外本机权限。
- Workspace 禁用/删除、Machine 吊销或 Session 结束后失效。
- 显式覆盖目标时仍重新检查 Machine/Workspace 归属。

UI 中的“已打开 Workspace”不是新的权限对象。

## 9. Action 权限建议

### 文件

- `file.read`、`code.search`：Workspace 启用即可。
- `file.write`、`file.edit`、`file.applyPatch`：统一使用 `write` 本机权限。
- 删除能力若后续加入，优先可恢复删除；永久删除再单独评估，不提前做审批系统。

### Shell

- `shell.run.argv` 与 `shell.run.profile` 分开。
- Profile 白名单可以允许常规 test/build。
- 任意 shell string、后台任务、网络工具和系统目录访问提升风险。
- 提权不进入普通远程执行链路；Hub 不能把普通 Shell 升级成管理员/root。

### Git

- 读操作使用 Workspace 基础授权。
- 写操作统一 `git-write`，联网操作统一 `git-network`，可能执行 hooks/filter/driver 的操作使用 `git-hooks`。
- 不再为 commit/pull/push/worktree-delete 各建独立权限或短期 Lease。

### 浏览器/截图

- 当前隔离浏览器和一次性截图使用 Workspace 基础授权，不再要求额外 `browser`/`screenshot` 权限。
- 公网浏览默认可用；localhost/私网 Origin 只需本机加入一次持久白名单。
- 接管用户现有浏览器 Profile、持续录屏或远控仍不进入 MVP；如果未来加入，再作为独立高风险能力设计。

### Agent

- provider/model 发现与 Session/Turn 执行分开。
- `session.create/send` 直接复用 Workspace 现有 `write + shell` 权限，不新增 `agent` 权限。
- 当前只实现 bridge-owned Codex；不实现 session share/handoff、desktop-owned 或通用 Agent→Agent 递归。

## 10. 本机可见性

当前个人 MVP 不做逐次 Approval 弹窗，但本机仍应让用户看得懂正在发生什么：

- 能查看已授权 Workspace、实际 Root 和额外本机权限。
- 能看到当前运行中的 Shell/Build/Git 网络/Browser 等长任务。
- 错误和日志不隐藏真实目标，但敏感 Token/凭据必须脱敏。
- 紧急情况下可以禁用 Workspace、停止 Node 或吊销 Machine。

未来多人模式如引入 Approval，再单独定义确认界面与期限，不把这套交互提前塞进个人模式。

## 11. 撤销与失效

| 事件 | 立即影响 |
|---|---|
| OAuth Authorization 吊销 | 该授权下所有 Access/Refresh Token 和新请求立即失效；运行 Job 按策略取消 |
| Web Session 退出/失效 | 后台登录态失效，不直接替代或延长已签发 OAuth Token |
| Machine 吊销 | 关闭连接，拒绝重连和新事件 |
| Device Credential 吊销 | 对应凭据失效；machine 可用新凭据重连 |
| Workspace 禁用/删除 | 新请求立即拒绝；运行中的相关能力按各自取消/清理规则结束 |
| 本机额外权限收紧 | 新请求立即生效；长 Job 按现有周期 guard 重新检查 |
| Hub 紧急锁定 | 停止新执行，只保留管理员恢复路径 |
| Node 本地暂停 | Hub allow 也不能绕过 |

## 12. Token 与凭据存储

- Hub 数据库中 Refresh Token、设备长期秘密只保存摘要或加密形式。
- 加密主密钥不与数据库备份放在同一文件中。
- Node 私钥优先 OS 凭据库。
- Access Token 不写普通日志、审计 details、URL query 或 Artifact。
- Web Console 不在 localStorage 长期保存高权限 Token。
- 配置导出默认排除所有秘密。

## 13. 审计身份链

每个危险操作的审计条目包含：

```text
userId / clientId
organizationId
machineId / credentialId / connectionId
workspaceId / workspaceRevision
capability / action
requestId / jobId / traceId / correlationId
Hub identity/resource decision + Node local decision
result / side-effect knowledge / timestamps
```

不记录完整 Token、私钥、未脱敏环境变量或不必要的文件内容。

## 14. 防止 Confused Deputy

- Hub 不接受 Client 声称的 userId/clientId；由认证层注入。
- Node 不接受 Hub 任意指定本机绝对路径；只解析 workspaceId。
- Local Agent Adapter 不把 Provider 身份当作 Fast Spider 用户身份。
- Artifact 下载再次检查当前主体权限，不因知道 artifactId 即允许。
- Job cancel、watch、result 都检查 Job 所属资源和主体权限。
- 当前不实现跨 Session handoff/share；sessionId 只用于已绑定 Workspace 内的现有 Session 操作。

## 15. MVP 策略实现

MVP 只使用 Owner/Machine 归属、Node Workspace Registry、少量额外本机权限和固定决策函数。不创建通用 Grant/Lease/Approval 数据表，也不引入 OPA、通用 DSL 或远程策略服务。等真正出现多人共享和细粒度授权需求后再通过 ADR 评估；即使未来扩展，Node 本地最终裁决和固定安全底线不可外包。
