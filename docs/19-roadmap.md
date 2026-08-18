# 路线图（持续维护；当前基线 0.4.18）

## 1. 当前产品基线

当前产品沿用 0.3.0 完成的 Machine-only 权限模型；0.4.x 已把 AI 能力从单 Codex 桥升级为多 Harness 控制面：`codex` + `claude_code`，CC Switch 作为只读 Routing SSOT。当前源码基线为 0.4.18。Harness、Routing Runtime、上游 Provider/Model 与 EffectiveCapabilities 分层，不再把客户端模型别名当真实上游模型。

固定路径契约：

| 能力 | 参数 |
|---|---|
| `file_read` / `file_edit` | absolute `path` |
| `code_search` | absolute directory `path` |
| `shell_run` / `build_control` | absolute `cwd` |
| `git_control` | absolute `repositoryPath` |
| `ai_control session.create` | absolute `workingDirectory` |

公网 MCP 当前固定 18 个工具：

```text
machine_list, machine_get, capability_list, audit_log,
file_read, code_search, file_edit,
shell_run, job_watch, job_cancel,
git_control, build_control, browser_control,
screenshot_take, thinking_team, ai_control, working_context, artifact_get
```

不包含已移除的目录列表工具，也不按 Machine、Provider、模型或 Session 动态生成工具。`audit_log` 是 Hub 本地 Owner 隔离的只读审计查询，不需要 Node 在线且不开放到 Direct Access Key；`thinking_team` 是调用侧静态角色协作配置，不启动本机 AI Provider。

## 2. 已完成阶段

### Phase 1：Hub、Machine 与出站连接

- Hub 单进程、SQLite WAL、Web Console、OAuth 和 `/mcp`。
- Node 使用 Connection Token 首次登记，之后使用 Machine Device Key/WSS。
- Node 只主动连接 Hub；Machine 可查看、吊销和断开。
- Job、Event、Artifact、审计、幂等和断线对账形成共同基础。

### Phase 2：文件、搜索与编辑

- `file_read`、`file_edit`、`code_search` 使用绝对路径。
- Node 使用 OS 用户权限，不维护目录 Registry 或 Path Guard。
- 保留 UTF-8/BOM、二进制检测、大小限制、sha256 和并发修改校验。
- 超时、截断、错误码和大结果转 Artifact 的语义固定。

### Phase 3：Shell、Build 与 Job

- `shell_run` 和 `build_control` 使用结构化 argv、绝对 cwd、deadline 和幂等键。
- 进程以当前 OS 用户运行，支持 stdout/stderr、日志游标、进程树取消和断线恢复。
- Build 不再依赖本地目录 Profile；命令模板和参数由当前能力契约约束。

### Phase 4：Git 与 Artifact

- `git_control` 使用绝对 `repositoryPath` 和固定 action 参数。
- 支持 status、diff、log、show、branch、worktree、add、commit、fetch、pull、push 等受控动作。
- 网络副作用、hooks、冲突、凭据脱敏和幂等结果进入审计。
- Artifact 使用内容寻址、hash/size 校验、分块上传和保留清理。

### Phase 5：Browser 与截图

- 受管 Chromium/Playwright Sidecar 通过 stdio 运行，不监听 TCP。
- Browser 可访问当前 Node 可达的公网、localhost 和私网目标。
- 页面动作、下载、截图、桌面/显示器/窗口截图和错误清理形成闭环。
- 隔离 Profile 不连接用户普通浏览器；不开放任意 JS、CDP 或调试端口。

### Phase 6：Local Bridge 与多 AI Harness

- 当前用户 AF_UNIX/UDS Local Bridge 默认启用；本地和远程入口共用 Machine、OS 用户、Job、Event 与 Audit 事实。
- `providers.list` 当前发现 Codex + Claude Code，并返回 Provider-specific `supportedActions`；`routing.status` 只读 CC Switch DB，返回 RouteSnapshot、model mapping、health/takeover 与 EffectiveCapabilities。
- Codex 继续通过 `app-server --stdio` 提供完整 Thread/Turn/Skill/Plugin/Goal/Review/steer/respond 能力，并在 app-server 重启后自动 resume。
- Claude Code 通过原生 UUID、`stream-json`、stdin Prompt 与 `--resume` 提供 session create/send/watch/cancel/result；Fast Spider 只保存小型 Session 控制索引，不复制对话历史。
- Claude/Codex 的 Harness model catalog 与 CC Switch upstream model 分层；actual upstream 只有在可验证 Route/request correlation 下才声明。
- CC Switch Provider/Token/Takeover 不通过 Fast Spider 修改；Codex 第二执行链和 Claude permission bypass 也不映射到 `ai_control`。
- Fast Spider 不把 Codex App 的 Automations 映射进 `ai_control`，也不逆向私有协议；开发资料室的定时检查属于 Codex 外部运维自动化，与 Node Agent Session 分离。

### Phase 7：更新、恢复与发布门禁

- Hub/Node/CLI 版本可观察；Hub 数据可备份、校验和恢复。
- Node 校验签名 manifest、SHA-256 和大小，按原启动模式更新。
- 发布门禁覆盖格式、秘密模式、依赖校验、vet、测试、跨平台构建和核心 E2E。

### Phase 8：缓存与生命周期治理

- 派生内存缓存统一具备 TTL、容量上限、正确的同 key 刷新和返回值隔离。
- Browser/组件/Artifact 临时资源采用严格命名、活动保护、有界扫描和失败可重试清理。
- Release manifest 复用未变化产物的 hash/sign 结果，避免节点集中轮询重复读取大型文件。
- 删除型 CLI 统一为预览优先；用户数据、原生 Agent 历史、未知项和不确定状态不进入自动清理。
- 完整资源矩阵、运维顺序和资料室定时清理边界见[缓存与生命周期维护](23-cache-and-lifecycle.md)。
- 0.4.18 已把清理从“单次扫描”推进到持续、有界、可恢复：Browser 孤儿目录分批持续回收，组件按语义版本选最新安装，Artifact/Presentation 失败保留重试事实，Release/staging 采用取消传播和原子隔离。

## 3. 维护中的主线

当前优先级：

1. 完善 Windows/Linux OS 用户权限、路径格式、进程树和图形会话差异测试。
2. 加固 Machine 吊销、Device Key 轮换、OAuth 撤销和断线对账。
3. 保持 18 个 MCP 工具 Schema、错误码、Job/Event、Artifact、Audit Log、Thinking Team 与 Working Context 语义一致。
4. 完善 Browser 私网访问、下载清理、截图兼容和 Sidecar 版本矩阵。
5. 持续验证 Codex app-server 协议、Claude Code CLI/stream-json/session-resume，以及 CC Switch schema/model mapping/route correlation；新增 Harness 必须复用统一 Routing/EffectiveCapabilities 层，不复制 Provider 解析逻辑。Automations 仅在 Codex 公开协议出现后再评估映射。

## 4. 下一维护周期的进入与退出门禁

下一周期不预先承诺虚构版本号，只在出现真实触发条件时进入：

| 优先方向 | 进入条件 | 退出门禁 |
|---|---|---|
| 生命周期诊断 | 出现清理失败、磁盘水位或索引容量告警 | 最近成功/失败和 bounded 计数可观察；失败可在下一轮恢复 |
| Session 索引治理 | Claude 控制索引或 create 幂等记录接近容量 | active / `in_doubt` 受保护；任何成功响应重启后仍一致；显式 reconcile 可审计 |
| Working Context 归档 | plan.list 截断或历史计划持续占盘 | CAS 确认、跨项目隔离、默认保留 Markdown、返回 total/truncated |
| 大规模清理基准 | Artifact、Job、会话或发布轮询达到千级候选 | 清理固定批次；SQL/磁盘工作不随请求数重复放大；在线请求延迟无明显回归 |
| 跨进程组件恢复 | 同一 data-dir 需要被多个进程管理 | 文件锁、唯一临时路径、崩溃恢复和真实 Windows 文件占用测试通过 |
| 跨进程发布运维 | 同一 backup/release 目录需要被多个 `spiderctl` 或外部发布器管理 | 协作式文件锁、稳定句柄身份、Windows/Linux 同元数据原子替换与文件占用测试通过 |

本周期的量化底线：所有持久缓存/索引都必须同时有容量边界、可见性和安全退出路径；所有删除操作都必须幂等、默认预览或具备明确的自动保留契约；发布/备份/清理文档不再携带会误导未来版本的旧固定参数。

## 5. 明确不进入当前主线

- 多租户、组织 RBAC、多人共享一台 Machine。
- 目录级授权、目录枚举、路径隐藏或远程来源登记模型。
- 任意 TCP 转发、P2P 打洞、实时远程桌面、音频和通用输入。
- 连接用户日常浏览器 Profile、任意 CDP、任意 Node.js 执行。
- `desktop_owned` 执行所有权、Hook 写入/信任绕过、handoff/recover 第二执行链和通用 AI→AI 递归；只读 `hooks.list` 诊断保留在当前主线。
- Kubernetes、独立消息队列、分布式 Hub 和复杂安装器状态机。

## 6. 变更规则

真实需求变化时，先更新范围、威胁模型、协议和数据模型，再改实现。任何新远程边界、第二套授权链或兼容分支都必须有独立 ADR；不得把已移除的目录模型重新作为兼容方案加入主线。
