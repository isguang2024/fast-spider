# 20 开放问题

## 1. 使用方式

本文件把尚未最终锁定的问题分为三类：

1. **Phase 1 编码前必须决定**：不决定会造成核心契约、身份、存储或发布边界返工。
2. **进入对应阶段前决定**：当前不阻塞 Hub/Node 基础链路，但必须在引入相关高风险能力前关闭。
3. **以后再决定**：已有明确扩展点，当前不需要为假设场景增加复杂度。

每项都给出推荐默认值。Owner 可以一次性接受推荐默认值，也可以只覆盖有异议的项目。文档确认前不进入业务代码阶段。

## 2. Phase 1 编码前必须决定

### Q1. 项目许可证

**问题**：Fast Spider 核心仓库采用 Apache-2.0、MIT、双许可证，还是暂时私有不开源？

**推荐默认值**：核心代码使用 **Apache-2.0**；第三方 NOTICE、SBOM 和依赖许可证在发布流程中自动生成。名称、Logo 和商标另行保留，不由代码许可证自动授权。

**原因**：Apache-2.0 包含明确专利授权，适合 Go 基础设施项目，并与候选 MCP SDK、Playwright、OpenTelemetry 等宽松许可组件兼容。

**不决定的后果**：不能安全接受外部贡献，也无法最终确定发布包的 LICENSE/NOTICE；因此在第一行业务代码进入仓库前必须确认。

### Q2. Go 版本和首批平台基线

**推荐默认值**：编码开始时采用当时稳定、受支持的最新 Go 小版本，并在 `go.mod` 和 CI 固定；Hub 生产基线为 Ubuntu 24.04 LTS amd64，Node 为 Windows 11/Windows 10 受支持版本与 Ubuntu 24.04 LTS amd64。每次升级 Go 版本由依赖、安全和发布测试门禁决定，不使用浮动工具链。

**需确认**：是否必须从 Phase 1 同时发布 Linux arm64；推荐先构建验证但不把它列为 MVP 必须发布平台。

### Q3. Hub Owner 身份与首次初始化

**候选**：

A. Hub 内置 Owner 账户 + 密码/恢复码。
B. 只允许外部 OIDC/OAuth 登录。
C. 本机一次性 bootstrap 创建 Owner，之后可选本地账户或外部 OIDC。

**推荐默认值**：选择 **C**。首次启动生成只在本机读取的一次性 bootstrap token；Owner 完成初始化后立即销毁。MVP 支持一个本地 Owner 账户，密码使用现代自适应哈希；外部 OIDC 作为后续可选 Adapter，不阻塞自托管部署。

**必须固定**：账户恢复方式、MFA 是否进入 MVP、Web Session 生命周期。推荐 Phase 1 先完成近期重新认证和恢复码，TOTP MFA 在 Phase 7 前加入。

### Q4. 设备凭据格式

**候选**：

A. 每台 Node 生成 Ed25519 密钥，Hub 颁发短期设备 Token；WSS 握手再做 nonce 签名。
B. Hub 自建 CA，为每台 Node 颁发 mTLS 客户端证书。
C. 只使用长期 bearer token。

**推荐默认值**：Phase 1 选择 **A**；协议和数据库保留 credential type，使未来可切换/增加 mTLS。拒绝 C。

**原因**：A 的配对、轮换、吊销和跨平台部署更简单，同时仍能证明设备持有私钥；mTLS 可在需要更强网络层设备认证时增加，而不把第一阶段证书生命周期做得过重。

**必须固定**：Ed25519、challenge 格式、credential 有效期、轮换 overlap、Hub trust fingerprint 和时钟偏差策略。

### Q5. Contract 的唯一来源与生成链路

**推荐默认值**：使用 **JSON Schema 2020-12** 作为 Request/Event/Error/Capability/Artifact 的唯一契约来源；生成 Go 类型和校验器；MCP Schema 从同一契约映射生成。参数摘要使用确定性规范化 JSON，优先采用 RFC 8785 兼容实现。

**必须验证**：选择的生成/校验库能正确处理 `oneOf`、枚举、长度、additionalProperties、格式和跨版本兼容；生成结果可重复且不会把全部字段变成无类型 `map[string]any`。

**不采用**：第一阶段不同时维护手写 Go struct、手写 MCP Schema 和另一份 Protobuf。

### Q6. SQLite Driver、备份与 Migration Runner

**待选**：CGO SQLite 驱动或 pure-Go SQLite 驱动。

**推荐决策方法**：在 Phase 1 开码前做一个很小的技术验证，比较：

- WAL、busy timeout、foreign key、事务和并发行为。
- 在线 backup API、checkpoint 和异常恢复。
- Windows/Linux 交叉构建、发布体积和启动时间。
- 漏洞响应、许可证和维护活跃度。
- race/soak 下的稳定性。

**推荐默认倾向**：若 pure-Go 驱动通过 backup/WAL/性能验证，优先 pure-Go，降低 Windows CGO 发布复杂度；若关键 SQLite 能力或稳定性不足，则选择成熟 CGO 驱动并正式支持交叉构建链。不能只因“零 CGO”牺牲数据可靠性。

Migration 使用内置、版本化、带 checksum 的小型 runner；不引入需要独立服务的迁移系统。

### Q7. WebSocket 库和连接参数

**推荐默认值**：优先 `coder/websocket`，但在锁定前完成：WSS through reverse proxy、双向大日志、慢消费者、有界发送队列、ping/heartbeat、断线、关闭码和 Autobahn/协议测试。

**必须固定**：

- 单控制消息最大 1 MiB。
- 二进制 chunk 默认 1 MiB。
- 每 Node 最大 inflight 和发送队列字节数。
- 心跳默认 30 秒并带抖动。
- suspect/offline 阈值。
- permessage-deflate 默认关闭。

具体限额可在压测后调整，但第一版字段和拒绝语义必须稳定。

### Q8. opaque ID 与时间格式

**推荐默认值**：所有公开 ID 使用类型前缀 + 128 bit 以上安全随机值；不使用可推导时间或路径的自增 ID。数据库保存完整 ID 文本；API 时间为 UTC RFC 3339 Nano；运行时 deadline 使用 wall clock 传输、接收端转为单调时钟剩余时间。

**必须固定**：随机实现、字符集、最大长度、大小写规则和日志短显示规则。ID 比较大小写敏感，不接受自动修正。

### Q9. Hub 与 Node 的秘密存储

**推荐默认值**：

- Hub：应用加密/签名主密钥放在数据库之外的权限严格文件或系统 secret store；备份时与数据分离加密。
- Windows Node：优先 DPAPI/Credential Manager，绑定当前用户。
- Linux Node：优先 Secret Service；无桌面服务器可使用权限 `0600`、由本机密钥保护的凭据文件，并在 UI/CLI 明示降级。
- Provider Token 只由 Provider/Node 本地持有。

**必须固定**：服务模式与每用户模式切换时秘密迁移规则；禁止因安装模式变化静默复制为明文。

### Q10. Phase 1 公网与本地测试拓扑

**推荐默认值**：开发和 CI 先使用本机/隔离 VM 的真实 TLS+WSS；生产部署文档以现有反向代理 + loopback Hub 为唯一推荐路径。Phase 1 不自动创建 DNS、证书或云资源。

**需确认**：未来正式使用的公网域名和反向代理由用户在部署阶段提供；它不应硬编码进源码或阻塞本地编码。

### Q11. 默认容量与保留值

**推荐初始值**：

| 项目 | 默认值 |
|---|---:|
| 注册 Node | 50 |
| 同时在线 Node | 10 的测试目标，不设低硬限制 |
| Node read/write/exec 并发 | 4 / 1 / 2 |
| 控制消息 | 1 MiB |
| Event payload | 64 KiB |
| 单 Artifact | 100 MiB |
| 单 Job 在线输出 | 10 MiB，超出转 Artifact |
| Job 元数据 | 90 天 |
| 普通 Event | 14 天 |
| stdout/stderr Event | 7 天或转 Artifact |
| Audit | 365 天 |
| Node 本地事件/日志 | 每 Job 50 MiB、全局 1 GiB |

这些是安全起点，不是规模承诺。所有值必须可配置、有合理上下界，并进入清理/水位测试。

### Q12. 仓库治理

**推荐默认值**：

- `main` 为唯一正式主线。
- 短生命周期 feature branch/worktree，合并后删除。
- Contract 变更必须同时包含生成结果、兼容测试和文档。
- 不提交秘密、运行数据、浏览器 Profile、Artifact 或本地数据库。
- Phase 1 建立最小 CI：format、vet/lint、unit、contract、license、secret scan、Linux/Windows build。

是否接受外部贡献取决于 Q1；在此之前不创建复杂 CLA/多维护者流程。

## 3. 进入 Phase 2 前必须决定

### Q13. Workspace 链接策略

**推荐默认值**：Workspace 内 symlink/junction 可以存在，但默认禁止最终解析到 Workspace 外；链接本身可列出，跟随读取/写入需要 Path Guard 证明目标仍在边界内。无法可靠证明的平台/文件系统保守拒绝。

需通过 Windows reparse point、junction、UNC、ADS、大小写和 Linux symlink/mount 的真实原型锁定实现。

### Q14. Git worktree 的外部 `.git` 元数据

**推荐默认值**：允许读取当前 worktree 正常工作所需的 Git 元数据，但这种内部访问不能转化为用户可操作的任意外部路径。Git Adapter 使用系统 Git；File Capability 仍只暴露 Workspace 内文件。

### Q15. ripgrep 分发方式

**候选**：捆绑固定版本、发现系统版本、只实现 Go fallback。

**推荐默认值**：Windows 发布包捆绑经过验证的固定 ripgrep；Linux 优先发现受支持系统版本，可提供可选捆绑包。无 ripgrep 时有限 Go fallback 或明确 `SEARCH_TOOL_UNAVAILABLE`，不静默改变正则/ignore 语义。

### Q16. 文件编码与换行策略

**推荐默认值**：只把有效 UTF-8（可带 BOM）作为文本编辑；保留原 BOM 与主要换行风格；无效 UTF-8/二进制默认拒绝文本编辑。不会自动把本地代码页文件转码后覆盖。后续如需其他编码，必须作为显式转换 Action，并产生 Diff/备份。

## 4. 进入 Phase 3 前必须决定

### Q17. Shell Profile 初始集合

**推荐默认值**：

- 通用 `argv` 执行。
- Windows：PowerShell 7（可用时）与 `cmd` 兼容 Profile；不默认 Windows PowerShell 全权限脚本。
- Linux：`bash --noprofile --norc` 受控 Profile。
- Workspace 可定义 build/test Profile，但参数必须白名单。

禁止把 MCP 任意字符串直接拼接到 shell。需确定默认继承的环境变量最小集合和敏感变量规则。

### Q18. 进程树终止实现

**推荐默认值**：Windows 使用 Job Object 并从创建时绑定；Linux 使用新的 process group/session，发送 TERM 后在 grace period 发送 KILL。cgroup v2 只作为后续增强，不是 MVP 前置。

原型必须证明多层子进程、后台派生、超时和 Node 崩溃恢复。做不到完整确认时返回 `CANCEL_INCOMPLETE`，不能降级为“杀父进程即成功”。

### Q19. 文件 Patch 的原子性

**推荐默认值**：单文件原子；多文件 patch 先全量验证和准备，再逐文件原子替换并记录变更清单。MVP 不承诺跨多个文件的数据库式全局原子事务，但失败时必须返回哪些文件已修改、哪些未修改，并尽可能使用受控备份恢复。禁止模糊部分匹配。

## 5. 进入 Phase 4 前必须决定

### Q20. Git Hooks 策略

**推荐默认值**：默认兼容用户现有 hooks；commit 前检测 hooks 是否存在并纳入风险摘要。提供 Workspace 策略可禁止执行 hooks，但不通过偷偷修改全局 Git 配置实现。commit、pull、push 使用独立权限。

### Q21. Artifact 敏感级别与扫描

**推荐默认值**：MVP 提供 `normal/sensitive` 标签、附件下载、nosniff、短期访问和 hash；不强制引入外部恶意扫描服务。为未来 Scanner 接口预留，但没有扫描时显示 `unknown`，不能伪报安全。

## 6. 进入 Phase 5 前必须决定

### Q22. Playwright sidecar 形态

**候选**：Node.js sidecar、独立 Playwright 服务进程、直接使用 chromedp。

**推荐默认值**：Node 管理的本机私有 Playwright sidecar，通过 stdio/Named Pipe/UDS，固定版本并无网络监听；浏览器作为可选组件包下载。先验证实际安装体积、离线安装、更新兼容和崩溃清理。若代价明显过高，缩减为 chromedp + Chromium，而不是长期维护两套完整实现。

### Q23. 浏览器网络策略

**推荐默认值**：默认只允许公网 HTTP/HTTPS；阻止 `file:`、云元数据、link-local、loopback、RFC1918/ULA。Node 本地开发站点通过绑定 Workspace、主机、端口和期限的明确策略例外。每次 DNS 解析、重定向和连接目标重新校验。

### Q24. 截图后端

**推荐方法**：分别对 Windows Graphics Capture/Desktop Duplication、Linux X11 与 Wayland Portal/PipeWire 做真实原型。Go 库能可靠覆盖则直接使用；不足时增加窄 helper process。不能为了截图把整个 Node 改为双语言或要求 root 绕过 Wayland。

## 7. 进入 Phase 6 前必须决定

### Q25. Codex 官方接入边界

**推荐默认值**：优先官方 App Server/SDK；若通过本地 agent-service，Node 只连接 loopback/Named Pipe/UDS/stdio，不能公网暴露。必须先固定并测试 Session/Turn/owner/phase/approval/cancel 的真实映射，再实现 MCP `ai_control`。

### Q26. desktop-owned Hook

**推荐默认值**：后于 bridge-owned 实现。Hook 必须由用户在本机明确安装/信任；使用一次性 requestId、过期时间和 persisted Session/Turn 证据。未信任时停止真实 desktop-owned 链路，不修改 trusted hash、不绕过官方信任。

### Q27. Local Client 配对

**推荐默认值**：Named Pipe/UDS OS ACL + 每 Client 独立本机一次性配对/公钥。进程路径和签名只作为提示。Local Bridge 管理 UI 显示 Client、Workspace、Action 和撤销按钮。

## 8. 进入 Phase 7 前必须决定

### Q28. 发布签名和 Windows 代码签名

需确定：离线 root key、release key、密钥保管、Windows Authenticode 证书、发布渠道和紧急撤销流程。没有可验证签名与回滚前，不发布自动更新。

**推荐默认值**：Hub/Node release manifest 使用独立 Ed25519 签名；Windows 安装包同时做 Authenticode。Root key 离线，release key 使用受保护的发布环境；发布后从公开地址重新下载验签。

### Q29. RPO/RTO

**推荐初始目标**：单人自托管场景 RPO 24 小时、RTO 4 小时；每日一致性备份、每周完整备份和异机加密副本。Owner 有更严格需求时再提高频率，不先引入连续复制集群。

### Q30. Hub 更新审批

**推荐默认值**：自动检查、管理员确认安装；patch 也不默认无人值守做不可逆 migration。Node 低风险 patch 可在维护窗口自动安装，但 major/minor、权限变化和服务模式变化必须确认。

## 9. 以后再决定

这些问题已有扩展边界，不影响 MVP：

- macOS Node、签名/notarization 和 Screen Recording 权限。
- 组织/团队、多用户角色、邀请和细粒度共享。
- Hub 多实例、PostgreSQL、连接归属租约和事件总线。
- S3 兼容 Artifact 存储。
- HTTP/2/gRPC/QUIC 传输替代 WSS。
- PTY/ConPTY 交互终端和 xterm.js UI。
- 控制用户真实浏览器 Profile。
- Trace/HAR/浏览器测试视频。
- TPM/硬件设备密钥。
- Rust 全量 Node；只有 Go 平台原型明确失败才重新开启 ADR。
- 移动端、P2P、实时远程桌面、音频、通用输入和任意 TCP 转发仍属于非目标，不因“以后决定”自动进入范围。

## 10. 推荐的一次性确认包

Owner 接受以下组合即可进入 Phase 1：

1. Apache-2.0 核心许可证。
2. Hub/Node 都使用 Go。
3. JSON Schema 2020-12 作为唯一 Contract。
4. WSS 443 + JSON 控制消息 + 二进制分块。
5. 单 Hub + SQLite WAL + 本地 Artifact。
6. Ed25519 Node 身份 + 短期设备凭据，未来可选 mTLS。
7. 内置 Owner bootstrap + 本地 Owner 账户；外部 OIDC 后续。
8. `coder/websocket` 与 SQLite Driver 先做窄原型再锁版本。
9. Hub/Node 普通用户权限、Node 只出站、Local Bridge 默认关闭。
10. Phase 1 只实现配对、连接、心跳、机器/能力发现和最小 MCP，不提前加入文件执行。

接受不代表跳过验证；它只是允许按 [19-roadmap.md](19-roadmap.md) 启动 Phase 1 编码与原型门禁。
