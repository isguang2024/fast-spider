# Fast Spider Decisions

<!-- fast-spider:managed:decisions:start -->
## Active Decisions

1. **双轨任务系统**：Working Context 保存结构化摘要；项目内 Markdown Task Workspace 保存完整设计、决策和验收证据。Git、文件、测试和运行状态始终是最终事实源。
2. **不增加第 17 个 MCP 工具**：扩展现有 `working_context / working.context` actions，计划包括 `plan.init`、`plan.get`、`plan.list`、`plan.sync`、`task.update`、`markdown.list`、`markdown.read`、`markdown.append`、`progress.watch`。
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
<!-- fast-spider:managed:decisions:end -->

## Manual Decisions

人工新增决策写在本区；自动同步不得覆盖。
 
