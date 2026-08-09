# 20 开放问题

## 1. 使用方式

本文件把尚未最终锁定的问题分为三类：

1. **Phase 1 编码前必须决定**：不决定会造成核心契约、身份、存储或发布边界返工。
2. **进入对应阶段前决定**：当前不阻塞 Hub/Node 基础链路，但必须在引入相关高风险能力前关闭。
3. **以后再决定**：已有明确扩展点，当前不需要为假设场景增加复杂度。

每项都给出推荐默认值。Owner 可以一次性接受推荐默认值，也可以只覆盖有异议的项目。文档确认前不进入业务代码阶段。

## 2. Phase 1 编码前必须决定

### Q1. 项目许可证 — 仍真正开放

当前仓库没有已确认的 `LICENSE` 文件，因此不能把 Apache-2.0 写成既成事实。个人自用阶段先保持当前状态即可；只有准备公开仓库、接受外部贡献或对外分发源码时，再明确选择 Apache-2.0/MIT 等许可证并补 LICENSE/NOTICE。

SBOM/许可证报告也不进入当前本机 release gate，更不会自动联网安装生成工具。公开分发成为真实需求后，再把许可证确认、第三方 NOTICE/SBOM 作为该发布流程的前置门槛。

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

### Q12. 仓库治理 — 当前已收敛

- `main` 为唯一正式主线；Phase 使用短生命周期 feature branch，合并后删除。
- Contract 变更同时更新实现、测试和文档。
- 不提交秘密、运行数据、浏览器 Profile、Artifact、本地数据库或构建副产物。
- 当前单人项目不强制建设 GitHub Actions/商业 CI；`scripts/release-gate.sh` 是唯一门禁命令，覆盖 format、secret pattern、module verify、vet、tests、Windows/Linux builds 和核心 E2E。
- 未来需要远程 CI 时直接调用同一 release gate，不复制第二套检查清单。

是否接受外部贡献仍取决于 Q1；在此之前不创建 CLA/多维护者流程。

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

## 6. Phase 5 已关闭的决策

### Q22. Playwright sidecar 形态 — 已决定

当前使用固定版本的 Node.js Playwright Sidecar，通过 stdio 与 Go Node 通信，不监听网络。实际 Windows 开发机已完成 Chromium 真 E2E；Playwright `node_modules` 约 18 MiB，受管 Chromium 约 428 MiB。当前成本可接受，因此不同时维护 chromedp 第二套实现。

### Q23. 浏览器网络策略 — 已决定

公网 HTTP/HTTPS/WS/WSS 默认可访问；`file:`、危险 scheme、云元数据、link-local 等仍阻止。localhost、RFC1918、ULA、CGNAT 等本地/私网目标通过 Node 本机 Workspace Origin 持久白名单开放，一次配置后持续有效直到删除；不使用 TTL。私网白名单固定解析 IP，Go 与 Sidecar 双层检查 DNS rebinding。

### Q24. 截图后端 — 已决定当前范围

桌面/显示器使用当前 Go 截图库；Windows 窗口截图使用进程内 Win32 `PrintWindow`，不再启动 helper 子进程。三类截图都直接转 Hub Artifact，不增加独立 Workspace 权限。Linux/Wayland 的实际桌面权限差异继续作为平台兼容测试，不为此引入 root/helper 常驻服务。

## 7. Phase 6 已关闭的决策

### Q25. Codex 官方接入边界 — 已决定

当前 Windows 实机使用本机 `codex app-server --stdio`，不启动 Codex daemon，也不增加独立 agent-service。已用真实 Codex CLI 0.141.0 验证 `model/list → thread/start → turn/start → thread/read/watch → archive`。Provider 凭据和 ChatGPT/Codex 本地认证状态始终留在 Node 本机。

未指定 model 时以当前 `model/list` 为事实源自动选择可用模型；显式不可用模型提前拒绝。这样避免本机 Codex 默认配置指向当前 CLI 不支持的模型时，让 Session 无提示失败。

### Q26. desktop-owned Hook — 不进入当前 MVP

Phase 6 只实现 `bridge_owned`。desktop-owned、可信 Hook、handoff/recover 只有出现明确实际需求时再单独评估，不为未来可能性维护第二条执行链。

### Q27. Local Client 配对 — 已取消

个人单 Owner 模式不实现 Local Client 注册、配对 Token、公钥、Grant、Lease 或逐次 Approval。Windows/Linux 当前统一使用 Node 当前用户 data-dir 下的 AF_UNIX/UDS Local Bridge；请求直接复用 Workspace 和现有 `write/shell/git-*/build` 等真实边界。Local Bridge 默认启用，可用本机开关整体关闭。

## 8. Phase 7 当前决策

### Q28. 自动更新/代码签名 — 已按个人模式轻量落地

Windows Node 已支持本地 UI 手动升级，以及“后台检查/预下载、下次干净启动自动替换”。不建设独立 root/release key 状态机：release manifest 直接由当前 Hub Ed25519 身份签名，Node 用登记时已固定的 Hub 公钥验证，再校验 SHA-256/大小。当前仍不做 Windows Authenticode、自动提权、独立常驻 updater 或复杂安装器状态机。

### Q29. RPO/RTO — 保持简单

单人自托管先以“关键升级前必备份 + 用户自行安排周期备份”为主。`spiderctl backup` 不引入后台 scheduler；如果后续实际需要每日备份，优先用现有 systemd timer/任务计划调用同一个命令，而不是在 Fast Spider 内增加第二套调度器。

### Q30. 升级审批 — 不需要状态机

Hub 升级仍由 Owner 人工执行，不存在 Hub 内部 update approval；不可逆 migration 前的实际门槛仍是已生成并通过 `backup-verify` 的升级前备份。Windows Node 自更新只作用于本机 EXE，不需要 Hub 审批状态机。

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

## 10. 当前实际状态

Phase 1–8 已经完成实现与真实门禁，早期“一次性确认包”不再作为后续开发输入。当前代码事实以 README、ADR 和 [19-roadmap.md](19-roadmap.md) 为准：

1. Hub/Node 使用 Go，Hub 为单进程模块化单体，SQLite WAL + 本地 Artifact。
2. Node 只主动连接 Hub；远程能力必须落到明确 Machine/Workspace。
3. Local Bridge 默认随 Node 启用，Windows/Linux 统一 AF_UNIX/UDS；当前 OS 用户是本地信任边界，不存在独立 Local Client Token/Grant/Approval。
4. Browser/截图不再额外叠 Workspace 权限；私网 Browser Origin 只需本机持久白名单。
5. Codex 使用本机 `app-server --stdio`，当前只实现 bridge-owned；`write + shell` 是 Session create/send 的现有危险操作边界。
6. Hub 运维继续使用手工版本检查和 `backup / backup-verify / restore`；Windows Node 使用本地签名 manifest 自更新，不建设独立 updater 服务或安装器状态机。
7. `scripts/release-gate.sh` / `--full` 是当前统一发布前门禁；不为 checklist 自动安装外部扫描工具。
8. 当前真正仍开放的主要是许可证/公开分发、macOS、多用户、Authenticode/独立 Release Key 等未来需求；它们不应反向改变个人自用主线。

后续若真实使用需求改变，再按“先修改范围/ADR，再改代码”的方式推进，不重新启用历史 Phase 0 的企业级假设。
