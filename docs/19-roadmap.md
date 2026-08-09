# 19 路线图

## 1. 路线原则

- 先证明安全链路，再增加高风险能力。
- 首批优先复用 DevSpace 已被实际验证的产品形态：Workspace、文件、搜索、Shell、Git、Job/Event 和 AI Session，但重新按 Fast Spider 的 Hub/Node 双重授权与独立协议实现。
- 每个 Phase 都必须有可演示闭环、拒绝路径、资源上限和回滚方案。
- 不为未来规模提前引入微服务、Kubernetes、Redis、NATS、Kafka、S3 或长期双写。
- 公开 Contract、状态机和权限语义先于 Adapter/界面。
- 浏览器、截图、Local Bridge 在核心链路稳定后进入；自动更新不作为当前 MVP 既定阶段。
- 一个阶段未达到安全门禁，不用“后面再补”进入正式发布。

## 2. 阶段总览

| Phase | 主题 | 主要成果 |
|---|---|---|
| 0 | 产品、架构、协议、安全、调研 | 完整文档与 ADR，不写业务代码 |
| 1 | Hub、注册、出站连接、心跳 | 一台 Node 安全上线并可列出 |
| 2 | Workspace、文件只读、搜索 | GPT 可安全读取/搜索授权项目 |
| 3 | 文件编辑、Shell Job、日志、取消 | DevSpace 同类核心执行闭环 |
| 4 | Git、构建、测试、Artifact | 完整代码修改与验证闭环 |
| 5 | 浏览器控制与截图 | 本地开发页面自动测试闭环 |
| 6 | Local Bridge、MCP、Codex Adapter | 本机与远程 AI 共用能力与 Session |
| 7 | 简单运维、备份恢复、版本检查 | 启动方式收敛、数据可备份恢复 |
| 8 | 安全强化、审计、故障演练、正式发布 | 发布门禁和稳定版本 |

## 3. Phase 0：文档与技术决策

### 目标

固定产品边界、MVP/目标架构、核心协议、状态机、权限、安全、数据、运维、测试、开源依赖和实施顺序。

### 范围

- README 与 21 份主题文档。
- 6 份初始 ADR。
- Mermaid 系统/模块/时序/权限/部署图。
- 三套技术组合和决策矩阵。
- 当前官方资料的开源组件评估。
- 编码前问题清单。

### 非目标

- 不创建业务代码、云资源、服务、安装包、证书或付费依赖。
- 不部署 Hub/Node。
- 不创建隐藏的兼容实现。

### 依赖

- 产品需求源。
- DevSpace 同类能力边界参考。
- 官方协议、组件和系统 API 资料。

### 风险

- 文档术语/状态/错误码漂移。
- 过度设计或把目标架构混入 MVP。
- 对第三方许可证/平台能力判断不足。

### 验收标准

- 指定文档全部存在并为 UTF-8。
- Hub 不执行、Node 只出站、Node 最终裁决贯穿全部文档。
- Job 状态、Event、ID、权限维度一致。
- 所有要求的 Mermaid 图存在。
- 开放问题区分“编码前必须决定”和“以后决定”。
- 仅文档变更，无业务实现/部署。

### 可演示场景

从 README 导航到架构、协议、威胁模型、路线图，能够完整解释一条 file_read、Shell cancel、浏览器测试和 Codex Session 调用链。

### 回滚

文档尚未形成运行时依赖；通过 Git 回退提交。ADR 被替代时新增 superseding ADR，不静默改写历史决定。

## 4. Phase 1：Hub、节点注册、出站连接与机器列表

### 目标

建立最小安全“德国 Hub ↔ Windows/Linux Node”控制通道，Node 无入站端口，MCP/API 可列出真实在线机器。

### 工作包

1. Go Module、目录、构建和配置骨架。
2. Contract v1：握手、Machine、Capability Descriptor、Error。
3. Hub SQLite migration 和存储最小集。
4. Owner bootstrap 与最小认证（不先做完整多用户）。
5. 后台连接令牌与 Node 首次登记。
6. Node 设备密钥、Hub 信任和后续设备证明。
7. WSS 连接、协议协商、generation、心跳、重连。
8. Hub connection registry、在线状态和 machine list/get。
9. MCP 固定 `machine_list/machine_get/capability_list`。
10. 审计、结构化日志、指标、清理和 E2E。

第一编码阶段工作量以 **10 个独立任务包** 管理；每个任务包必须包含拒绝路径与测试，不以“先连通再补安全”拆成无边界原型。

### 范围

- 单 Owner、单 Hub、SQLite WAL。
- Windows/Linux Node 单进程模式；Windows 当前已提供轻量本地控制 UI，安装包/托盘服务仍后置。
- WSS JSON 控制消息。
- Node status/capability discovery。
- 基础 Web/CLI 管理设备登记和吊销。

### 非目标

- Workspace、文件、Shell、Git。
- 浏览器、Local Bridge、Agent。
- 多实例 Hub、团队 RBAC、自动更新。自动更新只有真实需求出现时再单独立项，不阻塞当前路线。

### 依赖

- ADR 0001–0004。
- SQLite Driver、WebSocket 与 MCP SDK 引入前验证。
- Hub 公网测试可先用本机 TLS/测试证书；生产资源不在编码前自动创建。

### 风险

- 设备身份/登记重放。
- 双连接 generation 竞争。
- 反向代理长连接配置。
- 心跳/重连形成风暴。
- Schema 快速变化。

### 验收标准

- Node 对 Hub 只建立出站 443；不开放公网/局域网监听，本地控制 UI 只绑定 loopback。
- Connection Token 可重复登记同一 Owner 下多台 Node，支持过期/撤销且不落盘；每台 Node 最终使用独立设备密钥。
- 被吊销设备立即不能继续接收/发送有效消息。
- Hub 重启后 Node 带抖动重连。
- 同 machine 双连接只有新 generation 有效。
- MCP 工具不随机器数量重复增长。
- 空闲 CPU/内存和心跳写入保持低水平。

### 可演示场景

启动 Hub → Web 后台创建 Connection Token → Windows Node 本地 UI 填写 Token → Node 上线 → MCP 列出机器、OS、版本、能力和 `connection_token/local_node/device_key` 模式 → Hub 吊销设备 → Node 断开且重连失败。

### 回滚

- Feature flag 关闭公网 MCP，只保留本机管理。
- 数据库只含身份/机器，可从升级前备份恢复。
- Node 删除/恢复本地设备身份后使用任一有效 Connection Token 重新登记。
- 协议不兼容时回退共同 v1，不维护两个执行层。

## 5. Phase 2：Workspace、文件只读与代码搜索

### 目标

让远程 Client 在 Node 本机明确授权的 Workspace 内安全列出、读取和搜索代码，证明路径边界不可绕过。

### 范围

- Node 本机 Workspace 注册/启用/禁用/删除。
- opaque workspaceId、revision、逻辑目录同步。
- Path Guard 跨平台实现。
- file list/stat/read/readChunks。
- glob/grep，ripgrep Adapter。
- `workspace_list/open`、`file_read`、`code_search`。
- 分页、cursor、大小/编码/二进制限制。

### 非目标

- 写文件、删除、Shell、Git 写。
- 远程通过绝对路径授权新 Workspace。
- 文件内容索引/Elasticsearch。

### 依赖

- Phase 1 稳定连接/身份。
- Path Guard 平台原型和恶意路径 corpus。
- ripgrep 引入决策。

### 风险

- symlink/junction/reparse point/TOCTOU 逃逸。
- 大目录/正则造成 CPU、内存、输出耗尽。
- Windows 编码、大小写和特殊路径。

### 验收标准

- 远程只看到 workspaceId/显示名，不见绝对路径。
- 所有穿越、链接逃逸和竞态 corpus 被拒绝。
- Workspace 禁用后旧 context/session 立即失效。
- 大文件、二进制、无效 UTF-8 不产生乱码或无界返回。
- 搜索有超时、结果上限、取消和截断说明。

### 可演示场景

Node 本机授权仓库 → MCP 列出 Workspace → 搜索符号 → 分段读取文件 → 尝试 `../`/外部链接被拒绝 → 禁用 Workspace 后旧 context 失效。

### 回滚

Workspace 默认只读，可整体关闭 `file.system`/`code.search`。Node 保留本地 Registry，Hub 删除逻辑缓存不影响文件。若 Path Guard 有疑问，立即禁用受影响平台的文件能力。

## 6. Phase 3：文件编辑、Shell Job、日志流与取消

### 目标

完成首个可实际修改与验证项目的核心闭环，并证明写入幂等、并发安全、长任务可观察和完整取消。

### 范围

- 原子 write、exact/range edit、patch、mkdir/move/copy。
- recoverable delete；永久删除默认不开放。
- expected revision/hash、Diff。
- Job/Event 状态机、cursor、断线续读。
- Shell argv/profile、stdout/stderr、timeout、cancel、进程树。
- `file_edit`、`shell_run`、`job_watch`。
- Node 本地 idempotency 和事件缓冲。

### 非目标

- PTY/ConPTY 交互终端。
- 自动提权、系统级任意命令。
- 复杂通用队列/调度 DSL。

### 依赖

- Phase 2 Path Guard。
- Windows Job Object、Linux process group 原型。
- Job/Event Contract 和 Hub Event 持久化。

### 风险

- 断线重复写/执行。
- 半写文件、并发覆盖、Patch 路径注入。
- 取消留下孤儿子进程。
- 输出/事件/日志无限增长。

### 验收标准

- 同 idempotencyKey 不重复执行；不同参数冲突。
- 写入原子，冲突返回 revision error。
- 所有修改返回 Diff 和审计。
- Shell 持续输出可从 cursor 恢复。
- 取消完整进程树后才显示 canceled；失败明确报告。
- Hub/Node 在 ack/result 任意断点后重连不重复执行。
- Event、日志、恢复区有硬上限和清理。

### 可演示场景

读取文件 → 精确编辑 → 查看 Diff → 运行长测试命令 → watch 日志 → 断开/重连继续读取 → cancel → 验证全部子进程退出。

### 回滚

分别按 capability/action feature flag 关闭 write、delete、shell。只读 Phase 2 仍可运行。数据迁移新增表需可从备份恢复；不保留两个写实现。

## 7. Phase 4：Git、构建、测试与 Artifact

### 目标

形成远程代码修改后的版本控制、构建和证据交付闭环。

### 范围

- 系统 Git status/diff/staged/log/show/branches/worktrees。
- 受管 add/commit/fetch/pull/push/worktree。
- Workspace build/test profiles。
- Artifact create/chunk/resume/hash/complete/download/cleanup。
- 测试报告、覆盖率、完整日志和大 Diff Artifact。
- `git_control`、`artifact_get`。

### 非目标

- 自建 Git 实现或远程 Git hosting。
- 为每种语言建立构建系统。
- 任意压缩包自动解压/执行。

### 依赖

- Phase 3 Job/进程/文件。
- Git Adapter 风险策略。
- Artifact 本地内容寻址存储。

### 风险

- Git hooks/config/credential 泄露。
- 网络副作用和 merge conflict。
- Artifact 路径/HTML/压缩炸弹。
- 磁盘增长。

### 验收标准

- Git 读写权限分离，commit/pull/push 可审批。
- hooks 存在时风险可见；凭据/remote 脱敏。
- typecheck/test/build Profile 参数受控。
- 大输出稳定转 Artifact。
- 分块重试不损坏，hash/size 不匹配拒绝。
- Artifact 过期清理、权限复核和磁盘水位保护通过。

### 可演示场景

编辑 → git diff → test/build → 报告 Artifact → commit（审批）→ 可选 push（独立审批）。

### 回滚

关闭 Git write/network 和 Artifact 上传，保留 Git read/小结果。物理 Blob 通过 manifest/ref_count 安全清理，不直接删除数据库引用。

## 8. Phase 5：浏览器控制与截图

### 目标

在 Node 本机用隔离浏览器测试开发页面，返回结构化页面信息、日志和截图；支持一次性桌面/窗口捕获。

### 范围

- Playwright Adapter 原型/正式化。
- 隔离 Profile/Context/Page。
- 固定 navigate/click/type/key/wait/snapshot/screenshot。
- console/network error/download。
- 本地开发 URL 策略和 SSRF 防护。
- Windows/Linux 截图/窗口枚举平台实现。
- `browser_control`、`screenshot_take`。

### 非目标

- 用户真实浏览器 Profile 默认接管。
- 持续桌面视频、音频、通用鼠标键盘。
- 原始 CDP/Playwright任意执行。

### 依赖

- Phase 3/4 Job、Artifact、进程。
- ADR 0005 和 Playwright/截图平台原型。
- URL/网络安全测试环境。

### 风险

- SSRF、内网/元数据访问。
- 登录态/截图泄露。
- 浏览器体积、版本和崩溃清理。
- Wayland/锁屏权限差异。

### 验收标准

- 隔离 Profile 无用户 Cookie。
- 明确授权的本地开发 URL 可测试。
- 危险 scheme、云元数据和未授权内网被阻止。
- 取消后 Browser/driver/Profile 临时资源清理。
- 桌面/窗口截图单独权限和本机提示。
- 无桌面/锁屏返回结构化错误，不提权绕过。

### 可演示场景

启动受管浏览器 → 打开 Node 本地开发页面 → 点击/输入/等待 → 获取可访问性摘要、console/network error 和截图 Artifact。

### 回滚

浏览器和截图是可选 capability，可整体禁用/卸载 sidecar，不影响文件/Shell/Git。保留公共工具但返回 capability unavailable，或按版本化工具发现隐藏。

## 9. Phase 6：Local Bridge 与 Codex Adapter

### 目标

让本机 Codex/其他 AI 和远程 MCP 共用同一 Workspace/Capability/Job/权限，实现可追踪的 Provider-neutral Session。

### 范围

- Windows/Linux 统一 AF_UNIX/UDS，Node 运行时默认启用，可显式关闭。
- 当前 OS 用户即本地信任边界，不实现 Local Client 注册/Grant/Approval。
- 首版不做 loopback HTTP/MCP；只有真实兼容需求出现时再评估薄 Adapter。
- Provider/model/project 发现。
- Agent session create/get/send/watch/cancel/result。
- bridge_owned Codex Adapter。
- owner/executionMode/phase 字段。
- 当前阶段只维护 bridge_owned 一条执行链；desktop-owned/handoff/recover 不进入路线图，只有出现真实需求时再单独立项。

### 非目标

- Provider Token 经 Hub 传播。
- 多 AI 无限递归。
- 无边界共享所有 Session。
- Hook 信任绕过。

### 依赖

- Phase 3 Job/Event、Phase 4 Artifact。
- ADR 0006。
- 本机官方 Codex `app-server --stdio` 的稳定适配层。

### 风险

- 当前用户 data-dir/Socket ACL 或 stale socket 处理错误。
- Session owner/phase误报。
- Provider Turn 重复启动和资源消耗。
- Codex stdio 并发写入或进程退出造成 RPC 状态异常。

### 验收标准

- Local Bridge 默认无 TCP 端口，只创建当前用户 data-dir 下的 AF_UNIX/UDS，并可显式关闭。
- OS ACL +现有 Workspace/危险本机权限即可，不维护独立 Local Client 权限系统。
- Session 绑定 Workspace/Provider/owner。
- 取消 ack 不算 canceled；终态以真实 Codex Turn 事实为准。
- 同一 Session 单 active Turn；首版不开放通用 AI→AI workflow。
- Provider Token 不离开 Node。
- 未指定 model 时按当前 `model/list` 自动选可用模型，避免本机默认模型与 CLI 版本不兼容。

### 可演示场景

本机 Codex/CLI 通过 Local Bridge 读取同一 Workspace → 远程 Client 创建 bridge_owned Codex Session → 本机或远程继续 watch/result；两条入口看到同一 Session/Job 事实，不需要再做本地 Client 分享授权。

### 回滚

使用 Node 本机开关关闭 Local Bridge/Agent capability；文件/Shell/Git 远程链路继续。Provider Adapter 独立版本，不修改核心 Job/Contract；不存在需要清理的 Local Client 凭据表。

## 10. Phase 7：简单运维、备份恢复与版本检查

### 目标

把当前可运行程序收敛成个人项目真正容易维护的形态：启动方式唯一、版本看得见、Hub 数据可验证备份并安全恢复。

### 范围

- Hub 正式入口固定为一个构建后二进制；Linux 可由一个 systemd unit 管理。
- Node 正式入口固定为一个当前用户 `fast-spider-node run` 进程。
- Hub/Node/CLI 版本查询。
- `spiderctl backup / backup-verify / restore`。
- Hub `hub.db + secrets + artifacts` 单 data-dir 备份边界。
- 手工可观察升级与失败回退文档。
- 明确卸载只移除程序，数据是否删除由用户决定。

### 非目标

- Windows 安装包、托盘 UI、SYSTEM service。
- 自动更新下载/安装和自动提权。
- Release Key/Root Key/签名 manifest 状态机。
- Recovery Mode 常驻恢复器。
- 多版本 `current/previous` 进程管理。
- Kubernetes/多实例无停机升级。

### 依赖

- Phase 1–6 Contract 与数据目录已经稳定。
- SQLite、secrets、Artifact 均已收口到 Hub data-dir。

### 风险

- Hub 活跃写入期间生成混合时间点备份。
- 备份包包含 Hub 私钥，被当普通文件泄露。
- 恢复覆盖已有 data-dir 导致新旧数据混杂。
- 不兼容 migration 后直接切旧二进制失败。

### 验收标准

- 备份过程中源数据变化会使备份失败，不发布半成品。
- 备份 manifest 对每个文件记录 SHA-256，篡改后 `backup-verify` 拒绝。
- Restore 只接受不存在或空目录，并以临时目录完成后再发布。
- 恢复后的 DB/secrets/Artifact 与源数据一致。
- Hub/Node/spiderctl 都能查询版本。
- 正式运行仍只有一个 Hub 和每机一个 Node，不增加 updater/helper/daemon。
- Windows/Linux build 与 Phase 1–6 全量测试继续通过。

### 可演示场景

准备测试 Hub data-dir → `backup` → `backup-verify` → 篡改副本验证拒绝 → `restore` 到新空目录 → 用恢复目录启动 Hub → `/livez`、`/readyz` 正常。

### 回滚

升级前备份并校验。新版本失败时先停新版本；数据库未发生不兼容变化时切回旧二进制，发生不可逆 migration 时用升级前备份恢复到新 data-dir 后再启动旧版本。

## 11. Phase 8：可重复 Release Gate 与安全/故障回归

### 目标

不再扩产品功能，把 Phase 1–7 已经真实跑通的能力收成一个每次发布前都能重复执行的门禁，并明确区分“已验证”和“当前环境无法验证”。

### 范围

- `scripts/release-gate.sh` 的 core/full 两档门禁。
- 全仓 gofmt、`git diff --check`、tracked secret pattern、`go mod verify/tidy -diff`、`go vet`。
- 全量 tests 与当前/Windows/Linux amd64 builds。
- 恢复后 Hub、Local Bridge、Browser、Codex 和 Local Bridge→Codex 真实 E2E。
- Node 连续 3 轮回归，观察现有清理/并发路径是否出现 flake。
- Ed25519 编码、Git ref/path、backup path 三个高收益 Fuzz target；普通 tests 永远运行 seeds。
- Threat Model、测试策略、Roadmap 与实际实现收口。

### 非目标

- 为 checklist 建设 GitHub Actions、商业 CI 或外部测试平台。
- 自动联网安装 `govulncheck`、SBOM、secret scanner 等工具。
- 24–72 小时固定 soak、50 Node 压测或大规模 SaaS 场景。
- 为当前 Windows/386 工具链强行改代码以运行 race/random fuzz。
- 发布签名、自动更新或安装器体系。

### 依赖

- Phase 1–7 全部完成。
- Full gate 需要本机真实 Chromium/Playwright Sidecar 与 Codex runtime。
- Race/random fuzz 依赖支持它们的 Go/OS/arch 环境。

### 风险

- Release gate 自己与真实命令漂移。
- 外部 runtime 变化导致 Browser/Codex E2E 失败。
- 当前 386 工具链无法覆盖 race/random fuzz，造成验证盲区。
- 历史文档继续描述已取消的企业级设计，诱导后续重新实现。

### 验收标准

- `bash scripts/release-gate.sh` 通过。
- `bash scripts/release-gate.sh --full` 在当前真实 Browser/Codex 环境通过。
- 当前 Windows/386 对 random fuzz/race 明确输出 `SKIP`，而不是伪报 PASS；Fuzz seeds 仍通过。
- Windows amd64/Linux amd64 构建通过。
- 无未解决 P0/P1；关键输入边界无 panic/路径静默改写。
- 恢复、Local Bridge、Browser、Codex 和完整产品 smoke 继续通过。
- 文档与当前个人模式实现一致，不把自动更新、Local Client Grant、desktop-owned 等未来能力写成当前事实。

### 可演示场景

运行 `bash scripts/release-gate.sh --full`，一次完成静态检查、全量测试、双平台构建、恢复 Hub、Local Bridge、真实 Browser/Codex 与产品 smoke；最后输出 `PASS`，同时清楚列出当前环境跳过的 fuzz/race 门槛。

### 回滚

Phase 8 只增加测试、门禁和文档收敛；若门禁本身误判，回滚对应测试/脚本即可，不改变 Phase 1–7 运行时数据或权限语义。

## 12. 分支与提交策略

- `main` 始终保持可构建、可测试的权威主线。
- 每个任务使用短生命周期分支/worktree；合并后删除已合并临时分支。
- Contract/Schema 变更与实现/测试/文档同提交或同一个可审查变更集。
- 不长期保留 `legacy`/`v2-new` 双业务分支。
- 每个 Phase 保持可回滚提交；当前自用发布不强制维护签名/tag 状态机，未来公开分发时再单独定义发布签名流程。
- Codex/其他 AI 可协助测试、审查和平台验证，但主实现边界、权限与状态由 Fast Spider 文档和代码保持权威。

## 13. 决策门禁

### 编码开始门禁

- Phase 0 文档一致性验证通过。
- [20-open-questions.md](20-open-questions.md) 中“编码前必须决定”关闭。
- ADR 0001–0004 接受。
- 许可证策略和最小依赖允许列表接受。

### 高风险能力门禁

- 文件写/Shell：Path Guard、idempotency、process tree 原型通过。
- 浏览器/截图：SSRF/隐私/平台原型通过。
- Local Bridge/Agent：当前 OS 用户边界、Workspace 复用、owner/phase、单 active Turn 通过。
- 运维恢复：backup-verify、恢复到空目录、health/smoke 回退流程通过。

## 14. 路线变更规则

出现真实需求变化时：

1. 先更新范围和威胁模型。
2. 新增/替代 ADR，说明为什么原决策失效。
3. 评估对 Contract、权限、数据和升级的影响。
4. 不因单个组件方便而偷偷改变产品边界，例如加入通用端口转发或远程桌面。
5. 优先缩小范围而不是增加中间件。
