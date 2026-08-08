# ADR 0006：Node Local Bridge

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：本机 MCP/API、CLI、编辑器和 AI Client 接入

## 背景

Node 需要可选择性提供本地入口，供本机 Codex、其他 AI 编程软件、CLI 或编辑器调用同一 Capability Engine。需求明确：本地接口默认关闭；启用后只绑定本机；优先 Named Pipe/Unix Domain Socket；必须防止 DNS rebinding、浏览器跨站和“localhost 等于可信”的错误假设。

## 决策驱动因素

- 默认无监听端口和最小攻击面。
- 利用 OS 用户边界和文件/管道 ACL。
- 每个本地 Client 独立身份、权限和审计。
- 兼容 MCP/CLI/Provider Adapter。
- 不重复实现文件、Shell、Git、Job 和权限逻辑。
- 能明确关闭、吊销和观察。

## 考虑的方案

### 方案 A：Windows Named Pipe / Unix Domain Socket

优点：

- 不占网络端口。
- 可以使用 SID/文件权限限制当前用户。
- DNS rebinding 和普通网页跨站攻击面显著降低。
- 适合长期本机 Client。

缺点：

- 跨语言/SDK 接入比 HTTP 稍复杂。
- Windows/Linux 分别实现和测试。

### 方案 B：127.0.0.1 HTTP/MCP

优点：生态兼容最好，调试简单。

缺点：

- localhost 并非天然安全，存在 DNS rebinding、Origin/CORS、端口占用和 Token 泄露风险。
- 容易被误配置到 `0.0.0.0`。

### 方案 C：stdio

优点：生命周期与父进程绑定，无监听入口，适合单个 Adapter。

缺点：不适合多个长期 Client、发现、独立 Session 和托盘管理。

## 决策

Local Bridge 的默认实现：

- **Windows：Named Pipe**。
- **Linux/macOS：Unix Domain Socket**。
- **stdio：允许作为单 Client/Provider Adapter 的专用模式**。
- **loopback HTTP/MCP：默认关闭，仅作为明确启用的兼容模式**。

Local Bridge 整体默认关闭。启用后仍必须经过：transport authentication → localClientId → Workspace/Capability/Action grant → 可选 Approval → 同一 Dispatcher/Job Manager/Capability Engine → Audit。

## 本地身份

每个 Client 独立注册，至少保存：

- localClientId、显示名。
- 独立公钥或 Token 摘要。
- OS 用户/SID/UID 约束。
- 允许的 Workspace、Capability 和 Action。
- 状态、最近使用和吊销时间。

OS ACL 是第一层，不是唯一认证。进程路径/签名可作为风险提示，但不能成为唯一身份，因为路径和进程可以被替换或代理。

首次注册使用本机可见的一次性配对流程。一个 Client 的凭据不能复制成另一个 Client 的身份；吊销只影响对应 Client。

## Windows Named Pipe

- Pipe 名称包含不可猜测实例部分或由安全本机发现机制提供。
- DACL 默认只允许当前用户 SID 和必要的 Node 服务身份。
- 拒绝 Everyone/Authenticated Users 的宽泛写权限。
- 校验连接者 token/SID；服务模式不能因 SYSTEM 身份自动信任所有本地用户。
- 消息有长度、deadline、并发和认证握手。
- Node 卸载/关闭后无遗留 pipe/helper。

## Unix Domain Socket

- Socket 目录和文件仅 Node 用户可访问，默认权限 `0700/0600`。
- 使用安全创建和原子替换，处理 stale socket。
- 校验 peer credentials（平台支持时）并结合独立 Client 凭据。
- 不放在其他用户可替换父目录。
- 容器/挂载场景不自动扩大 ACL。

## Loopback HTTP 安全底线

兼容模式启用时：

- 只绑定 `127.0.0.1` 和可选 `::1`；不得监听 `0.0.0.0`。
- Host allowlist 只接受配置的 loopback host/port。
- 严格 Origin；默认不允许浏览器跨域，不使用 `Access-Control-Allow-Origin: *`。
- 独立短期 Token/Client 凭据，不复用 Hub Owner/MCP Token。
- 写操作使用 nonce/CSRF 防护；GET 不产生副作用。
- 管理页面使用 CSP、frame-ancestors 和安全 Cookie。
- 启动时检测端口占用；失败即禁用，而不是自动换到未知暴露地址。
- 日志不打印完整 URL Token、Authorization 或 Cookie。

## 权限与 Workspace

- Local Client 仍只使用 opaque workspaceId。
- 不能传绝对路径临时扩大 Workspace。
- 同机不代表拥有全部本地目录。
- Workspace revision 变化使旧 Local Session/Lease 失效。
- Local 与远程请求共享资源组和并发限制，防止互相耗尽 Node。
- 本地 Client 发起的文件、Shell、Git、浏览器和 Agent 操作使用相同 Job/Event/Result 语义。

## AI 与 Provider

- Local Bridge 可以暴露 provider-neutral `agent.control`。
- Provider Token 只在 Node/Provider 本机，不进入 Local Client普通响应或 Hub。
- 每个 Client 的 Session 创建者、共享列表和 active Run owner 明确。
- correlationId、hopLimit 和调用链阻止多个 AI 自动递归。
- desktop-owned handoff 需要可信本机 Hook/官方事件；打开 UI 不等于运行。

## 可见性与控制

Node UI/CLI 必须显示：

- Local Bridge 是否启用、实际传输和 endpoint 类型。
- 已注册 Client、权限、最近访问和 active Job。
- 新 Client 配对和高风险 Approval。
- 暂停、逐 Client 吊销和整体关闭。

不提供隐藏监听、无日志模式或让 Hub 静默开启 Local Bridge 的功能。Hub 可以建议配置，但最终开关由 Node 本机用户决定。

## 后果

### 正面

- 默认无网络端口，攻击面最小。
- 本地 Client 与远程 Client 使用同一安全和执行语义。
- 可逐 Client 授权、审计和吊销。
- 保留 HTTP/MCP 生态兼容而不把它设为默认。

### 负面

- 需要实现和测试两种平台 IPC。
- 某些现有工具只支持 HTTP，需要兼容模式或小型 stdio Adapter。
- OS peer identity 在不同平台行为不同，仍需独立凭据。

## 不采用的做法

- 不因地址是 localhost 跳过 Workspace/Action 权限。
- 不默认监听固定公网可猜端口。
- 不允许 CORS wildcard 或 Host 任意值。
- 不复用 Hub 高权限 Token。
- 不让多个 AI 在没有 owner、hopLimit 和共享边界时自动互调。

## 重新评估触发条件

只有主流 MCP/编辑器无法通过 Named Pipe/UDS/stdio Adapter 接入，且安全 HTTP 兼容层被证明更低成本时，才考虑把 loopback HTTP提升为默认。即使提升，上述 Host/Origin/Token/权限底线不变。

## 相关文档

- [Local Bridge 与 AI 控制](../11-local-bridge-and-ai-control.md)
- [身份与权限](../08-identity-and-permissions.md)
- [安全威胁模型](../09-security-threat-model.md)
- [路线图](../19-roadmap.md)
