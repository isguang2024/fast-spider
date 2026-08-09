# ADR 0006：Node Local Bridge

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：本机 MCP/API、CLI、编辑器和 AI Client 接入

## 背景

Node 需要提供本地入口，供本机 Codex、其他 AI 编程软件、CLI 或编辑器调用同一 Capability Engine。当前项目是单 Owner、个人开发机优先，因此目标是“当前 OS 用户能直接用”，而不是再实现一套企业级 Local Client 权限系统。当前 Windows 10/11 与 Linux 都可由 Go 原生 AF_UNIX/Unix Domain Socket 覆盖，因此不再为 Windows 单独维护 Named Pipe/DACL 实现。

## 决策驱动因素

- 默认无 TCP 监听端口和最小攻击面。
- 直接利用当前 OS 用户 data-dir 与 Socket 文件边界。
- 本机 Client 无需注册、配对、Grant、Lease 或逐次 Approval。
- 兼容 CLI/Provider Adapter，并复用现有 Capability Engine。
- 不重复实现文件、Shell、Git、Job 和权限逻辑。
- 用户可以明确整体关闭 Local Bridge。

## 考虑的方案

### 方案 A：跨平台 AF_UNIX / Unix Domain Socket

优点：

- 不占网络端口。
- Windows/Linux 可共用同一 Go `net.Unix*` 传输实现。
- DNS rebinding 和普通网页跨站攻击面显著降低。
- 适合长期本机 Client。

缺点：

- 跨语言/SDK 接入比 HTTP 稍复杂。
- Windows 与 Unix 的文件权限语义仍不同，需要平台实测。

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

- **Windows/Linux：当前用户 data-dir 下的 AF_UNIX / Unix Domain Socket**。
- **macOS：后续沿用 Unix Domain Socket**。
- **stdio：允许作为单 Client/Provider Adapter 的专用模式**。
- **loopback HTTP/MCP Capability Adapter：当前不实现；以后只有真实第三方兼容需求时再单独 ADR 评估**。

Local Bridge 在 Node 正常运行时默认启用，但只创建本机 IPC，不监听 TCP。用户可使用 `--disable-local-bridge` 关闭。调用链为：OS ACL → schema/size 校验 → Workspace/现有危险权限/路径/网络/资源检查 → 同一 Capability Engine。

## 本地身份

Phase 6 个人模式不建立长期 Local Client 身份表。当前 OS 用户就是本地信任边界：

- Socket 位于当前用户私有 data-dir；Windows 继承该目录 ACL，不额外维护 SID/SDDL 代码。
- Unix Socket 目录/文件使用严格的 `0700/0600` 权限。
- 每个连接可生成临时 connectionId 用于日志，但它不参与授权。
- 不保存本地 Client Token、公钥、Capability 列表、过期时间或吊销记录。

未来只有出现多个互不信任本地用户共享同一个 Node 的真实需求时，才通过新 ADR 评估独立 Client 身份；当前实现不预埋双认证链路。

## AF_UNIX / Unix Domain Socket

- endpoint 固定为 Node data-dir 下的 `local/bridge.sock`，方便本机发现，不依赖名称保密。
- Windows 使用当前用户 data-dir 的现有 Windows ACL；个人 MVP 不额外生成 SID/SDDL 规则。
- Unix 目录/Socket 使用 `0700/0600`。
- 启动时检测 stale socket：已有活跃监听则拒绝重复实例，失效 socket 才清理后重建。
- 消息有长度、deadline 和并发限制；不叠加应用层配对握手。
- Node 关闭后移除 socket；不留下 helper/端口服务。

## Loopback HTTP / MCP Adapter

Phase 6 对 Local Bridge 的决定保持不变：不实现 loopback HTTP/MCP Capability Adapter，本地 AI/CLI 调用继续只走 AF_UNIX/UDS。后续产品阶段增加了一个独立的 `127.0.0.1` Node 本地控制 UI，用于连接、每机配置和 Workspace/权限管理；它不暴露 MCP/Capability，也不是第三方 Client Adapter。因此 Local Bridge 仍没有 Host/CORS/HTTP Token 认证链，本地 UI 自己使用 same-origin + 进程随机 UI secret 的轻量边界。

## 权限与 Workspace

- Local Client 仍只使用 opaque workspaceId。
- 不能传绝对路径临时扩大 Workspace。
- 同机不代表拥有全部本地目录。
- Workspace 禁用/删除立即阻止新请求；普通 revision 变化不制造额外 Local Session/Lease 状态机。
- Local 与远程请求共享资源组和并发限制，防止互相耗尽 Node。
- 本地 Client 发起的文件、Shell、Git、浏览器和 Agent 操作使用相同 Job/Event/Result 语义。

## AI 与 Provider

- Local Bridge 可以暴露 provider-neutral `agent.control`。
- Provider Token 只在 Node/Provider 本机，不进入 Local Client普通响应或 Hub。
- 同一 Provider Session 只允许一个 active Run；本机连接本身不形成新的权限主体或分享列表。
- 首版不开放通用 AI→AI 递归调用；同一 Provider Session 只允许一个 active Run，`correlationId/parentRunId` 用于追踪即可。
- 当前 Phase 6 只实现 `bridge_owned`；desktop-owned handoff 只有出现真实需求时再单独评估。

## 可见性与控制

Node UI/CLI 必须显示：

- Local Bridge 是否启用、实际传输和 endpoint 类型。
- Local Bridge endpoint 类型、最近活动和 active Job。
- 当前 Workspace 与既有危险权限状态。
- 整体关闭 Local Bridge。

不提供隐藏监听、无日志模式或让 Hub 静默开启 Local Bridge 的功能。Hub 可以建议配置，但最终开关由 Node 本机用户决定。

## 后果

### 正面

- 默认无网络端口，攻击面最小。
- 本地 Client 与远程 Client 使用同一安全和执行语义。
- 当前 OS 用户开箱即用，配置和运维负担小。
- 保留 HTTP/MCP 生态兼容而不把它设为默认。

### 负面

- Windows 与 Unix 仍要分别验证 Socket 文件/目录权限和 stale socket 行为。
- 某些现有工具只支持 HTTP，需要兼容模式或小型 stdio Adapter。
- Windows/Linux IPC 权限实现不同，需要分别测试。

## 不采用的做法

- 不因本机 IPC 跳过 Workspace、路径和现有危险操作权限。
- 不默认监听固定公网可猜端口。
- 不允许 CORS wildcard 或 Host 任意值。
- Local Bridge 不需要 Hub Token，也不创建新的本地 Bearer Token。
- 不让多个 AI 在没有 owner、hopLimit 和共享边界时自动互调。

## 重新评估触发条件

只有真实常用客户端无法通过 AF_UNIX/UDS/stdio Adapter 接入，并且因此显著影响个人使用体验时，才评估 loopback HTTP 兼容层。即使加入，也保持可选，不替代默认本机 IPC。

## 相关文档

- [Local Bridge 与 AI 控制](../11-local-bridge-and-ai-control.md)
- [身份与权限](../08-identity-and-permissions.md)
- [安全威胁模型](../09-security-threat-model.md)
- [路线图](../19-roadmap.md)
