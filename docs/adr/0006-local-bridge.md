# ADR 0006：Local Bridge（0.3.0 当前有效部分）

## 状态

已接受。早期 Local Bridge 设计中的目录对象和额外本机权限层已由 0.3.0 的 Machine/OS-user 模型取代。

## 决策

Local Bridge 默认启用，只创建当前用户可访问的 AF_UNIX/UDS IPC，不监听 TCP 或 loopback HTTP。Windows/Linux 共用 Go 传输与请求模型，可用 `--disable-local-bridge` 整体关闭。

## 权限与调用链

- 当前 OS 用户和 data-dir ACL 是本机信任边界。
- 不注册 localClientId、Token、公钥、Capability Grant、Lease 或逐次 Approval。
- 请求使用 `machineId` 和能力所需的绝对 `path`、`cwd`、`repositoryPath` 或 `workingDirectory`，进入同一 Dispatcher、Capability、Job、资源和审计链路。
- Local Bridge 不把 Provider Token、私有 Session 内容或环境变量默认上传 Hub。

## 取舍

AF_UNIX/UDS 已覆盖本机 AI/CLI Client 的核心需求，因此不实现 loopback HTTP/MCP Capability Adapter。Node 本地管理 UI 是独立管理面，只负责连接、本机设置、状态和紧急停止。

## 不实现

不实现 Local Client 分享权限、desktop-owned、Hook、handoff、恢复型第二执行链或通用 AI→AI 递归网络。若未来出现多个互不信任 OS 用户共享同一 Node 的需求，另行设计身份和隔离边界。
