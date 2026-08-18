# Fast Spider Decisions

<!-- fast-spider:managed:decisions:start -->
## Active Decisions

1. **双轨任务系统**：Working Context 保存结构化摘要；项目内 Markdown Task Workspace 保存完整设计、决策和验收证据。Git、文件、测试和运行状态始终是最终事实源。
2. **MCP 工具演进**：0.4.2 的 Task Workspace 继续扩展 `working_context / working.context` actions 而不拆工具；0.4.12 因“调用侧角色协作”具有独立语义正式增加只读 `thinking_team`；当前固定 18 个工具，新增 `audit_log` 作为 Hub 本地、Owner 隔离的只读审计查询入口，并明确不扩展 Direct Access Key 权限面；仍不按 Machine、Provider、模型或 Session 动态生成工具。
3. **计划隔离键**：`machineId + projectPath + planId`；Markdown 必须位于 projectPath 内且为普通 `.md` 文件，防 symlink/junction 逃逸。
4. **Markdown 同步**：受管区块以 `<!-- fast-spider:managed:<name>:start/end -->` 标记；自动同步只替换受管区块，不覆盖人工内容。写入使用 revision/CAS、临时文件、fsync 和原子替换。
5. **任务上限**：Markdown 文件最多 64；单文件 512 KiB；目录 4 MiB；单计划最多 500 个任务；每任务最多 32 条验收证据。
6. **敏感信息边界**：Task Workspace 和 Working Context 不保存 Token、Cookie、API Key、完整 Prompt、聊天原文或原始上游错误。
7. **Agent 结构**：静态 Provider Registry；不做动态插件系统。CC Switch 拆为独立 routing 模块并继续只读数据库。
8. **CC Switch fail-closed**：用 `PRAGMA table_info`/schema fingerprint 校验当前唯一支持 schema；不兼容时返回 `available=false / reason=unsupported_schema`，不写多版本兼容路径。
9. **缓存**：Route 1–2 秒；CLI version/auth 30–60 秒；models 10–30 秒；不新增 Redis。
10. **错误分类**：统一为 `auth_failed`、`rate_limited`、`provider_unavailable`、`network_failed`、`invalid_model`、`runtime_unavailable`、`route_mismatch`、`unknown`。
11. **本地客户端**：继续 Edge App Window + loopback UI；一级页面为概览、连接、任务与进度、AI 与路由、组件、诊断。
12. **真实 AI 健康测试**：仅用户明确点击时运行，后台不得自动消耗模型额度。
13. **ripgrep**：作为 Node Managed Component 安装到 data-dir 固定组件目录，不信任 PATH；固定安全 flags，清除 `RIPGREP_CONFIG_PATH`，保留原生 Go fallback。
14. **搜索引擎范围**：0.4.2 不引入 fd、Zoekt、Bleve、Tree-sitter；Tree-sitter 等真正需要 symbol/AST 搜索时再评估。
15. **file_read 2.0**：保持同一 MCP 工具，扩展 byte/line/head/tail/around/stat/line-number 读取，不新增工具。
16. **file_edit 2.0**：保持同一 MCP 工具，以 Go 原生实现 `create/replace/editMany/preview`；CAS、原子写、fsync、BOM/换行/权限保留；0.4.2 不增加 delete/move/copy。
17. **正式连接保护**：开发与测试禁止停止正式 PCa Node/生产 Hub、写正式 data-dir、替换正式 EXE或启动第二正式 Node；仅最终 0.4.2 更新验收允许正式 Node 短暂断线并要求自动恢复/必要时 `.previous` 回滚。
18. **发布策略**：0.4.0 先形成唯一 baseline commit；0.4.1 不独立发布；全部 Gate 通过后直接提交、推送并部署 0.4.2。
19. **组件中心 allowlist**：本地 UI 只管理 `browser` 与 `search-ripgrep`，拒绝任意 componentId/path/URL；安装继续复用 component manager 且必须由用户手动触发，搜索/文件自检不联网下载组件。
20. **Node staging 生命周期**：`CleanupConsumedCurrent` 与 `CleanupStale` 职责分离；启动必须先处理 Ready/apply，只有 `(applied=false, err=nil)` 才尝试清理 current staging，且 `ready.json` 存在时 API 自身 fail-safe 不删。future/unknown 目录与正式 `.previous` 回滚副本不属于 consumed-current cleanup。
21. **Windows legacy install cleanup**：独立 API 只在当前 executable basename 为 `fast-spider-node.exe` 时检查同级目录；使用 Win32 reparse attribute fail-closed，只删除严格命名的旧 temp/marker/直接 backup 文件，未知项和嵌套目录保留。当前 EXE 与 `.previous` 是保护对象，`.previous` 继续作为唯一正式 rollback SSOT；非 Windows no-op。
22. **Release backup retention**：仅显式 `backup-prune` 管理标准 `pre-<三段 semver>-<7..40 hex commit>.zip`；root 必须是绝对 non-reparse 普通目录。全部候选先执行现有 `Verify`，任一无效/特殊文件则零删除；按 manifest `CreatedAt` UTC 新到旧、basename 升序 tie-break，默认保留 3。历史异名、Hub binary backup、未知项与子目录永不自动处理，不改其它 retention 常量。
23. **Release staging 生命周期**：仅显式 `staging-prune` 管理本机 `release-<semver>[-<commit>]` 与服务器 `fast-spider-<semver>[-<commit>]` 直接子目录；默认 plan-only，`--apply` 才写。只处理 version `<= through`；future/unknown/legacy deploy/普通文件保留。root/candidate/tree reparse、扫描 bounds 或删除前身份/内容变化时 fail-closed，递归删除逐项 `Lstat`/reparse 校验，不使用无边界 `RemoveAll`。
24. **file_edit 2.1 响应边界**：mutation 默认只返回固定元数据，不回显正文或 diff；只有 preview 返回 16 KiB 内 bounded hunk。SHA CAS、原子替换、权限/编码/换行保留和同目标串行化不因瘦身降低。
25. **code_search 2.1**：正常路径优先 managed ripgrep；fallback 使用稳定 reason code、统一匹配行统计与实际 JSON 预算。VCS ignore 和通用生成目录默认生效，显式 include 优先；静态目录前缀下推为搜索 target。
26. **Execution Runtime**：Shell/Build 只增加轻量 `host|wsl` runtime，不扩展 Git 默认执行环境。Windows 绝对 cwd 由选定发行版 `wslpath` 映射；每发行版一个、全局最多八个 Node 自有 keepalive，退出不执行全局 WSL shutdown。
27. **Agent readiness 与幂等**：route/provider/harness/session backend/create readiness 分层报告；create 幂等状态持久、确定失败释放、未知副作用保持 in-doubt 并显式对账，delete intent 先落盘且 Provider not-found 可续做。
28. **Browser readiness 与组件协议**：readiness 缓存并共享并发启动状态；sidecar 必须声明组件协议 1.62.1，旧策略组件不得误报可用。HTTP(S) 允许公网、localhost 和私网，显式导航拒绝内嵌凭据与危险 scheme。
29. **轻量可观测性**：沿 MCP/Hub/Node/Job 传递紧凑 `requestId/traceId/timing`，只报告可测事实；不引入 OpenTelemetry 集群、长期高频日志或额外调度系统。
30. **不可变发布**：已发布 Node artifact 不做同版本覆盖；0.4.9 自举发现静态 include 根遍历后提升为 0.4.10，重新完成 build/deploy/self-update，并保留 0.4.9 rollback。
31. **Thinking Team 执行边界**：`thinking_team.executionTarget=calling_chat_model`、`providerInvocation=false`；角色/部门/流程只指导当前调用侧 ChatGPT/LLM 的多视角分析，绝不因该工具启动 Codex、Claude Code 或其它本机 AI Session。
32. **Thinking Team 资料室复用**：复杂协作直接使用 `working_context` 标准六文件与 `initializeMarkdown=true`；共享简报和 Read Evidence 写入 `00-current-state.md`，其它阶段事实映射到既有 roadmap/decisions/acceptance/open-issues/change-log。所有写入必须先 read 取得 fileRevision，再用 expectedFileRevision CAS append，不建立第二套 Workspace 存储或初始化器。
33. **Node 发布推送**：发布完成后由 `spiderctl node-update-push` 在 release-dir 当前 Node artifact/version.txt 上生成原子 `push.json`；Hub 只借现有 heartbeat ACK 发送 release notice，不新增消息队列、推送 daemon 或第 18 个 MCP 工具。Node 仍通过签名 manifest + SHA 获取真实更新。
34. **更新不得中断任务**：推送允许在任务期间预下载，但 Shell/Build Job、Browser Session、AI 活跃 Turn、in-flight Capability 任一存在时不得重启。全部结束并连续空闲 15 秒后才能进入 release drain；drain 不取消旧任务，只拒绝新任务并返回可重试 `NODE_UPDATING`，随后复用既有 Ready/StartApply/.previous/restart 链。
35. **0.4.16 分层能力指南**：initialize instructions 只承载九类能力地图和安全黄金规则；17 个工具的用途、必需输入、安全顺序、返回、下一步、真实错误与短示例由 `capability_list` 的 overview/catalog/tool/workflow/error 视图按需展开。指南和工具 description 使用同一代码内事实源，不新增独立 Manifest，也不改变既有 `machineId` 查询兼容行为。
36. **MCP 调用诊断边界**：只在 Hub 进程内按 owner 保存最近 64 条 initialize/tools/list/tools/call 归一化事件与当前快照；不持久化、不记录参数、正文、Token、路径或完整错误。Web API 复用现有 session 登录，仅在页面首次加载和人工刷新时读取，不轮询。
37. **0.4.17 长会话恢复边界**：ChatGPT 会话缺少 FastSpider_FS 命名空间时不等同于 Hub/Node/OAuth 断线；先以唯一 `fsprobe` 过滤发现并只物化 `machine_list`，真实连通后再按当前动作加载其它工具。健康检查禁止一次加载全部 17 个 Schema；发布预算固定为 initialize <=2 KiB、完整工具目录 <=48 KiB、连接入口 <=8 KiB、单工具 <=8 KiB。
<!-- fast-spider:managed:decisions:end -->

## Manual Decisions

人工新增决策写在本区；自动同步不得覆盖。
 
