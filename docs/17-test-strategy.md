# 17 测试策略

## 1. 目标

测试必须证明 Fast Spider 的权限和状态语义在真实跨平台环境中成立，而不只是函数能返回 200。重点风险：

- Hub/Node 边界被绕过。
- Workspace 路径逃逸或并发替换。
- 写操作因重试/断线重复执行。
- 取消只杀父进程或虚报成功。
- Event 缺失、乱序、重复或无限堆积。
- Windows/Linux 编码、路径、进程和权限差异。
- Local Bridge、浏览器、截图和备份/恢复链路扩大攻击面。
- 日志、Artifact、临时文件长期增长。

Phase 0 只定义测试，不创建云资源或运行付费测试平台。

## 2. 测试层级

| 层级 | 目标 | 运行频率 |
|---|---|---|
| Contract/Schema | 单一契约、兼容、样例 | 每次提交 |
| Unit | 纯逻辑、状态、策略、解析 | 每次提交 |
| Component | Hub/Node 模块 + 本地依赖 | 每次提交 |
| Integration | Hub↔Node、SQLite、文件、进程、Git | 每次 PR/主线 |
| E2E | Hub/Node、Browser、Local Bridge、Codex、恢复 | 里程碑/发布 |
| Security/Fuzz | 密钥编码、Git 输入、备份路径等纯输入边界 | 每次提交运行 seeds；支持环境再做随机 fuzz |
| Resource regression | 重复 Node 测试、清理、取消、临时文件回收 | full release gate |
| Recovery/Fault | 断线、取消、Provider/Browser 故障、备份恢复 | 发布前/相关变更 |

不追求虚高覆盖率数字；Critical 安全不变量和状态转换必须完整覆盖。

## 3. 平台矩阵

### Hub

- Linux amd64：主生产平台，必测。
- Linux arm64：发布时支持则必测。
- Windows amd64：开发/诊断支持，至少集成测试。

### Node

- Windows 11 amd64：必测。
- Windows 10 受支持版本：发布兼容测试。
- Ubuntu LTS amd64：必测。
- 另一主流 Linux（Debian/Fedora 系）：发布抽样。
- Linux X11、Wayland、无图形会话：截图/浏览器能力分别测试。
- macOS：进入支持阶段后加入，不在 MVP 假装支持。

### 文件系统

- Windows NTFS：大小写不敏感、junction、reparse point、长路径、ADS。
- Linux ext4：symlink、权限、mount point、大小写敏感。
- 可选网络/特殊文件系统只做“不支持/保守拒绝”测试，未验证前不声明兼容。

## 4. 契约测试

`contracts/schema` 是唯一源：

- 每种 Request/Event/Error/Artifact/Capability Descriptor 有有效样例。
- 生成 Go 类型与 Schema checksum 一致。
- 缺失必需字段、未知必需枚举、超长字符串、深 JSON、巨大数组拒绝。
- 可选新字段在旧 minor 接收端被安全忽略。
- major 不兼容握手拒绝。
- Golden JSON 在不同实现/版本 round-trip 后语义不变。
- MCP Schema 与内部 Capability Contract 映射测试，避免 Adapter 字段漂移。

发布包必须附带协议版本兼容矩阵和 conformance 结果。

## 5. 状态机与属性测试

### Job

使用 table/property test 验证：

- 只有允许的状态转换。
- 终态不可逆。
- running 必须有 Node 开始证据。
- cancel ack 不会直接变 canceled。
- completed 必须有固定 Result。
- expired/lost 的副作用字段不会被省略。

### Event

- sequence 从 1 递增。
- 重复同 hash 幂等；同 sequence 不同 hash 告警/拒绝。
- gap 对账后可恢复。
- cursor 重复读取允许重叠但不漏事件。
- cursor 过期返回快照与可恢复位置。

### Idempotency

- 同 scope/key/params 返回同 Job。
- 同 key 不同 params 永远冲突。
- ack、result、连接在所有断点丢失时不重复执行写操作。
- 去重记录清理时间覆盖最大 Job/重连窗口。

随机生成消息顺序和断线点，验证不变量。

## 6. 身份与权限测试

建立授权矩阵：

```text
subjects × machines × workspaces × capabilities × actions × origins × risk
```

至少覆盖：

- 无 Token、过期、错误 audience/scope、被吊销 Client/User。
- Client A 访问 Client B/其他组织资源。
- machineId/workspaceId 替换和不可见 ID 枚举。
- Hub allow + Node deny → 必须拒绝。
- Workspace revision/启用状态变化后，后续请求与已有 Session 不能继续使用旧边界。
- 写入、Shell、Git 网络/Hook、Build 等危险本机开关变化后立即按最新状态判断。
- Local Bridge 与远程 MCP 进入同一 Workspace/Capability 校验，不形成第二套权限语义。
- Artifact/job watch/cancel/result 的二次授权。
- 提权、永久删除、真实浏览器 Profile 默认拒绝。

安全测试以“默认拒绝”为验收，不只测试 happy path。

## 7. Path Guard 测试

### 通用 corpus

- 空、`.`、`..`、重复分隔符、NUL、控制字符。
- 绝对路径、编码/Unicode 规范化、超长路径。
- Workspace 内/外 symlink 链、循环链接、悬空链接。
- 检查后替换文件/目录的 TOCTOU 竞争。
- 深目录、海量目录项、权限变化。

### Windows

- `C:\`、`C:relative`、UNC、`\\?\`、`\\.\`。
- junction、mount point、各类 reparse point。
- `name:stream` ADS。
- 保留设备名、尾随点/空格、大小写变化、8.3 名称（环境支持时）。
- 跨卷 move 和原子替换差异。

### Linux

- symlink、bind mount/mount point、权限位、sticky bit。
- 文件在操作中被 rename/unlink/recreate。
- `/proc`、`/dev` 等特殊文件通过链接进入 Workspace。

测试应尽可能使用真实文件句柄和竞态循环，不只 mock path 库。

## 8. 文件能力测试

- UTF-8、UTF-8 BOM、CRLF/LF 保留策略。
- 无效 UTF-8、二进制、超大文件明确拒绝/Artifact，不产生乱码。
- 分段读取 offset、文件变化和 revision。
- 原子写入在进程崩溃/磁盘满时不留下半文件。
- expected revision/hash 冲突。
- exact edit 唯一/零/多匹配。
- Patch 多文件路径注入、超限和部分失败策略。
- Recovery-bin 容量、恢复、过期和永久删除权限。
- 文件权限、mtime 和 executable bit 的明确保留/改变语义。

## 9. Shell 与进程测试

### 参数/编码

- executable+argv 不经 shell 重新解释。
- PowerShell、cmd、bash profile 的引号、换行、管道和特殊字符。
- `--` 参数终止、防 option injection。
- Windows 中文路径/输出、不同代码页、无效字节。
- env 白名单、敏感变量脱敏和大小限制。

### 生命周期

- stdout/stderr 并发持续输出，不发生 pipe deadlock。
- 无输出长任务 heartbeat。
- timeout 与 cancel 竞争。
- 父进程派生多层子进程、脱离尝试、后台进程。
- Windows Job Object、Linux process group 完整终止。
- 取消失败返回 `CANCEL_INCOMPLETE`，不虚报。
- Node 崩溃/重启后的孤儿识别和回收。
- 输出超限切 Artifact，内存保持有界。

测试使用专门 helper 可执行程序构造进程树、编码、信号和输出，不依赖随机系统命令。

## 10. Git 测试

- 普通仓库、bare、submodule、worktree、detached HEAD。
- status/diff/staged/log/show 结构化解析。
- 大 Diff 转 Artifact。
- 系统 Git 不可用/版本过低。
- credential helper、pager/editor 禁用，输出脱敏。
- pre-commit/commit-msg/post-commit hooks 存在与失败。
- dirty worktree 创建/删除受管 worktree。
- fetch/pull/push auth、非快进、冲突、网络 timeout。
- 不修改全局 config/safe.directory。
- 恶意分支名/路径/remote 防参数注入。

网络测试使用本地受控 Git server/仓库，不依赖公网稳定性。

## 11. Hub↔Node 集成测试

测试进程内/本机真实 WSS：

- enrollment 一次性、并发消费、过期、重放。
- 设备 challenge、credential 轮换、overlap、吊销。
- 同 machine 双连接 generation 替换。
- 协议/Capability 版本协商。
- 心跳抖动、suspect/offline、重连。
- Node 发送队列背压和 Hub 慢消费者。
- dispatch ack/result/Event 在每个网络断点丢失。
- Hub 重启、Node 重启、双方同时重启。
- 旧 connection generation 事件拒绝。
- Event 批量写入和 SQLite busy 处理。

使用可编程故障代理注入延迟、断开、重复、乱序和帧截断。

## 12. MCP/API 测试

- 使用官方 MCP SDK conformance 测试固定版本。
- 工具列表不随机器数重复增长。
- 工具 Schema 大小、描述和必需字段。
- 工具直接使用 machineId/workspaceId；不依赖 `workspace_open` 或短期 Context 才能保证权限收紧生效。
- 长任务返回 jobId；watch cursor 正常恢复。
- 绝对路径、任意 flags、未知 action 被拒绝。
- OAuth/PKCE、Redirect URI、scope、audience、Token rotation。
- REST idempotency Header、分页、HTTP error映射。
- body/JSON 深度/数组限制和 413/429。

测试内部 FSWP 不直接暴露在 MCP/API。

## 13. Local Bridge 测试

- 默认随 Node 启用，`--disable-local-bridge` 时不创建 endpoint。
- Windows/Linux AF_UNIX/UDS round-trip、stale socket、路径替换和退出清理。
- endpoint 位于当前用户 data-dir；Unix 权限收紧，Windows 继承当前用户目录 ACL。
- 不监听 TCP/loopback HTTP，不存在 Local Client Token/注册状态。
- 非法 JSON、未知字段、超大消息、并发连接和取消退出均有界。
- Local 与 Remote 同时调用时复用同一 Workspace/Capability 规则。

## 14. Agent/Codex 测试

- Provider/model/project 发现与当前 Codex `model/list` 真实结果一致。
- Project 绝对路径只在 Node 匹配 workspaceId，不外泄。
- Session list/get/create/send/watch/cancel/result/rename/archive。
- 一个 Session active Turn 冲突。
- bridge_owned owner/phase 真实映射。
- 未指定 model 自动使用当前 CLI 实际可用模型；不存在的显式 model 提前拒绝。
- cancel ack 与真实 Turn 终态分离。
- stdio JSON-line 并发写入必须串行，不能交叉破坏 RPC。
- Provider Token 不出 Node；desktop-owned/handoff/recover 不属于当前测试矩阵。

Provider 不可用时返回结构化状态，不阻塞文件/Shell能力。

## 15. 浏览器与截图测试

### 浏览器

- 隔离 Profile 无用户 Cookie/扩展。
- Chromium/Firefox/WebKit（选择的支持矩阵）。
- navigate/click/type/key/wait/snapshot/screenshot。
- 页面关闭、浏览器崩溃、超时、取消和清理。
- console/network error、下载配额和 Artifact。
- 本地开发 URL 明确授权。
- `file:`、云元数据、loopback/内网、DNS rebinding、重定向绕过阻止。
- 密码/支付字段不出页面摘要/日志。
- 真实 Profile 未授权不可访问。

### 截图

- 多显示器、DPI、窗口、锁屏、无桌面会话。
- Windows 普通桌面/UAC安全桌面；Linux X11/Wayland Portal。
- PNG/JPEG/WebP、最大像素、缩放说明、Artifact hash。
- 权限和本机提示。
- 取消/失败后临时图像清理。

视觉 E2E 只验证能力和 Artifact，不引入持续视频。

## 16. Artifact 测试

- 创建、分块、断点、重复 chunk、offset 冲突。
- size/hash/CRC 错误。
- 临时上传过期与清理。
- logical name 路径穿越。
- Content-Type/nosniff/下载附件。
- HTML、可执行、压缩包不自动执行/解压。
- Blob 去重/ref_count/孤儿对账。
- 磁盘水位保护和并发配额。
- 下载重新授权，不凭 artifactId 越权。

## 17. 数据库与 Migration 测试

- 从每个受支持旧 Schema 升级到当前。
- migration checksum、重复运行、部分失败。
- WAL、busy、崩溃恢复、磁盘满。
- 一致性备份包含 WAL；恢复 integrity check。
- Job 终态、Event sequence、Workspace revision 等不变量。
- 清理任务批次/cursor/崩溃续跑。
- PostgreSQL 迁移不在 MVP 实现，但存储语义测试避免依赖非必要 SQLite 行为。

## 18. 运维、备份与恢复测试

- `backup → backup-verify → restore` 完整闭环。
- 备份期间文件集合/内容变化时失败且不发布临时包。
- ZIP 内容篡改、重复 entry、路径逃逸、缺失 `hub.db` 均拒绝。
- Restore 只接受不存在或空目录；失败不修改原 data-dir。
- 恢复后的 DB、secrets、Artifact 与源数据逐文件一致。
- Hub/Node/spiderctl 版本查询。
- 手工升级后的 `/livez`、`/readyz`、Node 重连和关键 smoke。
- 正式运行保持一个 Hub/每机一个 Node，不出现 updater/helper/重复启动入口。

## 19. 安全测试与 Fuzz

当前 Phase 8 只对已经证明高收益、且无需外部服务的纯输入边界做 Fuzz：

- `security`：Ed25519 public key/signature 编码解码，保证错误输入不 panic、成功长度严格。
- `node`：Git ref/path 参数验证，保证接受后的路径仍不是绝对路径/父目录逃逸/NUL。
- `opsbackup`：跨平台备份路径验证，保证接受值不被静默改写且再次验证稳定。

普通 `go test ./...` 会始终运行这些 Fuzz seeds。`release-gate.sh --full` 在工具链支持时再各跑 2 秒随机 fuzz；当前 `windows/386` fuzz runtime 会因工具链/内存映射限制失败，因此明确跳过随机 fuzz，不伪报通过。

内建安全门禁还包括：

- `go vet ./...`。
- `go mod verify` 和 `go mod tidy -diff`。
- tracked 文件的常见 private-key/token 模式扫描。
- 当前平台 + Windows/Linux amd64 构建。
- `git diff --check` 和全仓 Go `gofmt` 检查。

Race detector 仅在 `amd64 + CGO_ENABLED=1` 时运行；当前 Windows/386 环境明确标记为外部门槛。`govulncheck`、SBOM、商业 secret scanner 等外部工具当前未安装，也不会由 release gate 自动联网安装；以后真正进入公开分发/外部贡献流程时再按需要加入。

## 20. 资源回归

当前个人项目不为“规模指标”搭建专门压测集群。Full release gate 采用更贴近真实使用的轻量资源回归：

- `internal/node` 连续运行 3 轮，覆盖 Job、Browser、Artifact、Workspace、Git、截图等已有清理/并发测试。
- Local Bridge E2E 验证取消后 socket 清理。
- Browser E2E 验证 Sidecar/Chromium 生命周期和临时 Artifact。
- Codex E2E 验证 app-server 启停、Turn 完成和 Session archive。
- Hub 恢复 E2E 验证临时 Hub 二进制/目录退出后可清理。

已有运行时仍必须保持有界 semaphore、事件窗口、日志/Artifact 保留和超时；发现 CPU、RSS、goroutine、句柄或磁盘持续增长时，再针对真实泄漏增加专门 soak，而不是预先维护 50 Node/72 小时固定场景。

## 21. 长时间 Soak

24–72 小时 soak **不作为当前 Phase 8 的硬门槛**。单 Owner 当前规模下，长 soak 的成本高于日常收益，而且会把发布流程变成形式化等待。

只有以下情况出现时再启用定向 soak：

- 实际发现疑似内存/句柄/goroutine 泄漏；
- Node 长连接/断线恢复出现小时级问题；
- Artifact/Event/日志清理出现持续增长；
- Browser/Codex 连续创建关闭出现残留进程；
- 实际 Node 数量或并发明显上升。

届时 soak 应围绕已观察到的资源指标与失败模式设计，不固定成大厂式时长 KPI。

## 22. 故障与恢复演练

当前优先演练已经有真实执行链、且失败后用户会直接感知的故障：

- Hub/Node 进程退出与重连。
- Shell timeout/cancel 与完整进程树回收。
- Browser Sidecar/Chromium 关闭、超时和清理。
- Codex app-server 不可用、Session active Turn 冲突、Turn materialization 短窗口。
- Local Bridge 取消/退出后 endpoint 失效。
- SQLite/WAL 备份、篡改备份、非空恢复目录和恢复后 Hub 健康检查。
- Workspace/设备在运行中禁用或吊销。

网络乱序、磁盘满、系统时间跳变等只有相关模块出现真实故障或进入更高规模后再做定向注入。验收原则不变：安全降级、状态不虚报、无重复写、可恢复、错误有界。

## 23. 当前关键 E2E 验收场景

当前不维护“为了凑数量”的 15 场景清单。Release gate 需要覆盖这些真实链路：

1. Hub/API/WS 的 bootstrap、enrollment、Node 会话和固定 MCP 工具测试随 `go test ./...` 通过。
2. Workspace 搜索/读取/编辑、权限收紧和路径边界通过 Node 集成测试。
3. Shell Job、watch/cancel、完整进程树和输出边界通过 Node 测试。
4. Git/Build/Artifact 的权限、副作用和上传/清理测试通过。
5. 真实 Chromium Browser E2E 完成 launch → 页面操作 → Artifact/close。
6. Local Bridge AF_UNIX/UDS E2E 完成调用并验证取消后 endpoint 清理。
7. 真实 Codex E2E 完成 model/list → Session/Turn → result/archive。
8. 完整 Local Bridge → `agent.control` → Codex 产品 smoke 最终返回 `OK`。
9. Hub data-dir 完成 backup → verify → restore，并用恢复目录启动真实 Hub，`/livez`、`/readyz` 返回 200。
10. Windows/Linux amd64 均可构建；当前主机全量测试和静态分析通过。

涉及真实 Browser/Codex 的场景只在 `--full` 中运行；core gate 不把缺少本机外部 runtime 误报为产品代码失败。

## 24. 测试数据与隔离

- 测试 Workspace 使用临时目录/仓库，不指向真实用户项目。
- 破坏性测试在虚拟机/隔离用户中运行。
- Secret 使用测试凭据，不复制生产 Token。
- Artifact/截图可能包含测试数据，运行后自动清理。
- E2E 机器有明确标签和所有者，禁止误连生产 Node。

## 25. 当前 Release Gate

仓库提供一个开发/发布前门禁，不作为生产启动脚本：

```bash
bash scripts/release-gate.sh
bash scripts/release-gate.sh --full
```

`core` 固定执行：

- 全仓 Go `gofmt` 检查和 `git diff --check`。
- tracked 文件常见私钥/Token 模式扫描。
- `go mod verify`、`go mod tidy -diff`。
- `go vet ./...`、`go test ./... -count=1`。
- 当前平台、Windows amd64、Linux amd64 构建。
- 恢复后真实 Hub `/livez`/`/readyz` E2E。
- Local Bridge E2E。

`--full` 再增加：

- Node 测试连续 3 轮。
- 工具链支持时各 2 秒随机 Fuzz；不支持时明确 SKIP，seeds 仍随普通 tests 执行。
- `amd64 + CGO_ENABLED=1` 时运行 race detector；当前 Windows/386 明确 SKIP。
- 真实 Browser E2E。
- 真实 Codex E2E。
- Local Bridge → Codex 产品 smoke。

当前没有强制建设 GitHub Actions/商业 CI。单人项目先让同一脚本在本机和未来 CI 复用；需要远程 CI 时直接调用它，不复制第二套命令清单。Release gate 不自动安装 `govulncheck`、SBOM 或 secret scanner，也不自动联网；这些只有公开分发/外部贡献带来真实需求时再加入。

## 26. 缺陷分级

- P0：越权、路径逃逸、远程代码执行越界、秘密泄露、重复写或备份/恢复破坏原数据。
- P1：取消不完整被误报、状态/结果丢失、备份不可恢复、严重资源泄漏。
- P2：非关键兼容、性能、UI/诊断问题。

P0/P1 未解决不得正式发布；不能通过文档说明规避实际缺陷。

## 27. 完成定义

一个阶段完成需要：

- 功能和拒绝路径均有测试。
- 协议/文档/错误码/状态一致。
- Windows/Linux 适用测试通过。
- 资源和保留策略验证。
- 安全模型更新。
- 故障/回滚方式实际演练。
- 可演示场景有可重复步骤与证据。
