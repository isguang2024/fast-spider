# 17 测试策略

## 1. 目标

测试必须证明 Fast Spider 的权限和状态语义在真实跨平台环境中成立，而不只是函数能返回 200。重点风险：

- Hub/Node 边界被绕过。
- Workspace 路径逃逸或并发替换。
- 写操作因重试/断线重复执行。
- 取消只杀父进程或虚报成功。
- Event 缺失、乱序、重复或无限堆积。
- Windows/Linux 编码、路径、进程和权限差异。
- Local Bridge、浏览器、截图和更新链路扩大攻击面。
- 日志、Artifact、临时文件长期增长。

Phase 0 只定义测试，不创建云资源或运行付费测试平台。

## 2. 测试层级

| 层级 | 目标 | 运行频率 |
|---|---|---|
| Contract/Schema | 单一契约、兼容、样例 | 每次提交 |
| Unit | 纯逻辑、状态、策略、解析 | 每次提交 |
| Component | Hub/Node 模块 + 本地依赖 | 每次提交 |
| Integration | Hub↔Node、SQLite、文件、进程、Git | 每次 PR/主线 |
| E2E | 真实 Windows/Linux、MCP、断线、安装 | 里程碑/发布 |
| Security/Fuzz | 协议、路径、授权、供应链 | 持续/夜间/发布 |
| Performance/Soak | 容量、泄漏、清理、长期稳定 | 里程碑/发布 |
| Recovery/Chaos | 崩溃、断网、磁盘、备份、回滚 | 发布前 |

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

- 每种 Request/Event/Error/Artifact/Approval/Capability Descriptor 有有效样例。
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
- Workspace revision 更新使旧 Context/Lease/Session 失效。
- Approval 参数 digest、subject、target、Action、次数、期限不匹配。
- Local Client 不能继承远程 Owner 权限。
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
- `workspace_open` 不扩大 grant，权限变化失效。
- 长任务返回 jobId；watch cursor 正常恢复。
- 绝对路径、任意 flags、未知 action 被拒绝。
- OAuth/PKCE、Redirect URI、scope、audience、Token rotation。
- REST idempotency Header、分页、HTTP error映射。
- body/JSON 深度/数组限制和 413/429。

测试内部 FSWP 不直接暴露在 MCP/API。

## 13. Local Bridge 测试

- 默认关闭，无端口/socket。
- Named Pipe 当前用户 ACL；不同用户拒绝。
- UDS 权限、stale socket、路径替换。
- 每 Local Client 独立 Token/公钥和 Workspace/Action。
- loopback 只监听 127.0.0.1/::1。
- 非法 Host、Origin、CORS、CSRF、DNS rebinding 模拟。
- 本地 Client 吊销和 Session 共享权限。
- Local 与 Remote 同时调用的资源锁/审计一致。

## 14. Agent/Codex 测试

- Provider/model/project 发现与 policy 过滤。
- Project 绝对路径只在 Node 匹配 workspaceId，不外泄。
- Session create/get/send/watch/cancel/result。
- 一个 Session active Run 冲突。
- bridge_owned owner/phase 真实映射。
- desktop_owned 未提交保持 dispatching；UI 打开不算 running。
- Hook 不可信/请求过期/Session 不匹配拒绝 attach。
- cancel ack 与真实 Turn 终态分离。
- recover idempotency；不能把 desktop_owned 偷换 bridge_owned。
- correlationId/hopLimit/循环检测。
- Provider Token 不出 Node。

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
- Job 终态、Event sequence、Lease 次数等不变量。
- 清理任务批次/cursor/崩溃续跑。
- PostgreSQL 迁移不在 MVP 实现，但存储语义测试避免依赖非必要 SQLite 行为。

## 18. 更新与安装测试

- Manifest/package 签名、hash、size、os/arch、过期和 key 撤销。
- 下载断点、缓存污染和截断。
- Hub drain、备份、migration、health、回滚。
- Windows 每用户安装、升级、卸载、自启动、无管理员权限。
- Linux user systemd 安装/升级/卸载。
- 更新中断在每个状态恢复。
- minimumSafeVersion 和防回滚。
- 更新后只有一个实例/服务/启动入口。
- 卸载不留进程、端口、service、计划任务或 Local Bridge。

## 19. 安全测试与 Fuzz

Fuzz 目标：

- FSWP JSON envelope、每种 params Schema。
- 二进制帧头、长度、offset、压缩。
- Path Guard 和跨平台路径 parser。
- Patch/unified diff parser。
- Git/rg/命令参数映射。
- MCP/API input 和 cursor。
- Artifact manifest、release manifest。

安全工具：依赖/SBOM/secret scan、静态分析、Go race detector、平台安全 API review。高风险 native helper 需要内存安全/边界专项审查。

## 20. 性能测试

### 负载模型

- 50 registered/10 online Node。
- 每 Node 心跳、轻量 read/watch。
- 并发文件搜索/读取。
- 2 exec + 1 write + 4 read 默认资源组。
- 持续 stdout/stderr 与 Artifact 上传。
- Hub 重启后的同时重连（带抖动）。

### 指标

- Hub/Node CPU、RSS、goroutine、文件句柄。
- WSS 连接内存。
- Job queue/dispatch/Event 延迟。
- SQLite query、WAL、checkpoint、busy。
- Event/Artifact 磁盘增长与清理回收。
- 取消时间和进程残留。

性能优化不得移除安全校验或把队列改成无限。

## 21. Soak 测试

至少 24–72 小时：

- Node 周期上下线。
- 小 Job、长 Job、日志流、取消。
- Event watch 断开恢复。
- Artifact 创建/过期/清理。
- Browser/Provider Session 创建关闭。
- 定时清理、备份、checkpoint。

验收：内存/句柄/goroutine/磁盘无持续无界增长；日志不会因正常心跳和重试洪泛。

## 22. 故障与恢复演练

注入：

- Hub kill -9、Node kill、服务器重启。
- WSS 半断开、延迟、包重复/乱序。
- SQLite busy/corruption 副本、Artifact 不可写、磁盘满。
- 系统时间跳变。
- Provider/Browser/helper 崩溃。
- 更新服务器不可用/签名错误。
- 设备/Workspace/Client 在运行中吊销。

验证：安全降级、状态不虚报、无重复写、可恢复、告警不过载。

## 23. 关键 E2E 验收场景

发布前必须演示并保存证据：

1. 德国服务器单 Hub 部署。
2. Windows Node 一次性配对，公网无监听端口。
3. Node 本机授权代码 Workspace。
4. MCP 列出机器/Workspace。
5. 指定机器搜索、读取、编辑并返回 Diff。
6. 查看 Git Diff。
7. typecheck/test/build 持续日志和结果。
8. 取消长命令并终止完整进程树。
9. 受管浏览器访问本地开发页、点击/输入/截图。
10. Node 调用本机 Codex Session。
11. 另一 Local Client 访问同 Workspace 但受自己的权限限制。
12. Node 断线重连不重复已完成写操作。
13. Hub 吊销 Node 后不能继续接任务。
14. Workspace 取消授权后旧 Session/ID不能继续访问。
15. 备份、升级失败回滚和恢复演练。

## 24. 测试数据与隔离

- 测试 Workspace 使用临时目录/仓库，不指向真实用户项目。
- 破坏性测试在虚拟机/隔离用户中运行。
- Secret 使用测试凭据，不复制生产 Token。
- Artifact/截图可能包含测试数据，运行后自动清理。
- E2E 机器有明确标签和所有者，禁止误连生产 Node。

## 25. CI 门禁

每次 PR：

- format/lint/typecheck/build。
- unit/component/contract。
- race-sensitive 核心测试（按耗时分组）。
- dependency/license/secret scan。
- Linux integration。

主线/夜间：

- Windows integration。
- fuzz corpus、长路径/进程测试。
- MCP conformance。
- migration/backup/restore。
- 浏览器基础矩阵。

发布：

- 全平台 E2E、安装/升级/回滚。
- security threat model gate。
- performance/soak/recovery。
- SBOM、签名和发布物重验。

## 26. 缺陷分级

- P0：越权、路径逃逸、远程代码执行越界、恶意更新、秘密泄露、重复写。
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
