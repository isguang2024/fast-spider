# Fast Spider 0.4 Roadmap

<!-- fast-spider:managed:roadmap:start -->
## Plan `fast-spider-0.4.x-through-0.4.18`

状态枚举：`pending` / `in_progress` / `blocked` / `done`。0.4.1 是连续开发阶段，不做独立发布；本文件记录 0.4.x 至 0.4.18 的发布与验收事实，后续未版本化维护方向见 `docs/19-roadmap.md`。

### 0.4.0 基线收口

| Task | 状态 | 验收 |
|---|---|---|
| FS-040-001 工作树与并行变更对账 | done | Git/Working Context/关键文件已重新读取；并行 UI 下载入口改动已单独确认并保留 |
| FS-040-002 创建 docs/progress Task Workspace | done | 六个固定 Markdown 文件存在且可读，均保留人工非受管区域 |
| FS-040-003 将完整计划写入 Working Context | done | 结构化目标、约束、任务摘要和关键文件已持久化，revision 已记录 |
| FS-040-004 0.4.0 全量 Gate | done | Windows Git for Windows `scripts/release-gate.sh --full` 终态 PASS |
| FS-040-005 0.4.0 commit + push，形成唯一基线 | done | `0de4f6286942ccd1a7432651292ea7c8069cd33e` 已推送，HEAD = origin/main |

### 0.4.1 任务系统 + AI 运行时收敛 + 本地客户端

| Task | 状态 |
|---|---|
| FS-041-001 扩展 Working Context Plan/Task 数据结构 | done |
| FS-041-002 Markdown 单文件/目录绑定与隔离 | done |
| FS-041-003 受管区块同步、CAS、冲突处理 | done |
| FS-041-004 plan/task/markdown/progress actions | done |
| FS-041-005 本地客户端“任务与进度”页面 | done |
| FS-041-006 拆分 Agent Manager | done |
| FS-041-007 Provider 静态 Registry | done |
| FS-041-008 CC Switch 独立 routing 模块 | done |
| FS-041-009 CC Switch Schema 指纹与 fail-closed | done |
| FS-041-010 CLI/Route/Model 短 TTL 缓存 | done |
| FS-041-011 Codex/Claude/CC Switch 并行 discovery | done |
| FS-041-012 AI Provider 统一错误分类 | done |
| FS-041-013 本地客户端“AI 与路由”页面 | done |
| FS-041-014 本地客户端诊断中心 | done |
| FS-041-015 0.4.1 全量测试 | done |

### 0.4.2 高性能搜索与文件能力

| Task | 状态 |
|---|---|
| FS-042-001 Managed ripgrep Component | done |
| FS-042-002 code_search content/files | done |
| FS-042-003 include/exclude glob + context | done |
| FS-042-004 native Go fallback | done |
| FS-042-005 file_read 行范围/head/tail/around/stat | done |
| FS-042-006 file_edit create | done |
| FS-042-007 file_edit replace | done |
| FS-042-008 file_edit editMany | done |
| FS-042-009 file_edit preview | done |
| FS-042-010 本地客户端组件中心 | done |
| FS-042-011 搜索/文件诊断 | done |
| FS-042-012 全仓文档同步 | done |
| FS-042-013 完整 Release Gate | done |
| FS-042-014 commit + push | done |
| FS-042-015 Hub 0.4.2 部署 | done |
| FS-042-016 Node 0.4.2 更新 + 自动重连 | done |
| FS-042-017 生产 / 本机 / GitHub 三边最终对账 | done |

### 0.4.3 Node 更新文件生命周期

| Task | 状态 |
|---|---|
| FS-043-001 独立 consumed-current staging cleanup API | done |
| FS-043-002 Ready/apply 启动顺序、fail-safe 与生命周期测试 | done |
| FS-043-003 版本/文档/release gate 与全量验证 | done |
| FS-043-004 完整 Release Gate | done |
| FS-043-005 commit + push | done |
| FS-043-006 Hub / Node 0.4.3 更新 + staging 清理验收 | done |
| FS-043-007 发布后文件 / 组件 / 更新链路继续审计 | done |

### 0.4.4 Windows legacy install artifacts cleanup

| Task | 状态 |
|---|---|
| FS-044-001 Windows legacy bin cleanup API + Win32 reparse fail-closed | done |
| FS-044-002 严格命名/非递归/current+previous/幂等/reparse 测试 | done |
| FS-044-003 NodeUI startup 接线、版本/文档/专项 Gate 与全量验证 | done |
| FS-044-004 完整 Release Gate | done |
| FS-044-005 commit + push | done |
| FS-044-006 Hub / Node 0.4.4 更新 + legacy artifact 清理验收 | done |
| FS-044-007 发布后文件 / 组件 / 更新链路继续审计 | done |

### 0.4.5 Hub release backup retention

| Task | 状态 |
|---|---|
| FS-045-001 全候选 Verify + CreatedAt retention prune API | done |
| FS-045-002 `spiderctl backup-prune` + 严格路径/keep/JSON DTO | done |
| FS-045-003 版本/文档/专项 Gate 与全量验证 | done |
| FS-045-004 完整 Release Gate | done |
| FS-045-005 commit + push | done |
| FS-045-006 Hub / Node 0.4.5 更新 + `backup-prune --keep 3` 生产验收 | done |
| FS-045-007 发布后长期增长继续审计 | done |

### 0.4.6 Release staging lifecycle

| Task | 状态 |
|---|---|
| FS-046-001 安全 release staging prune 核心 API | done |
| FS-046-002 `spiderctl staging-prune` plan/apply CLI + bounded JSON | done |
| FS-046-003 local/server/future/unknown/reparse/limits/TOCTOU 测试 + version/docs/Gate | done |
| FS-046-004 完整 Release Gate | done |
| FS-046-005 commit + push | done |
| FS-046-006 Hub / Node 0.4.6 更新 + local/server staging-prune 生产验收 | done |
| FS-046-007 最终长期增长审计 | done |

### 0.4.7 / 0.4.8 Browser 与 Codex runtime 收敛

| Task | 状态 |
|---|---|
| FS-047-001 Browser snapshot refs、batch 与 locator 交互 | done |
| FS-047-002 Browser 网络策略简化与危险 scheme 边界 | done |
| FS-048-001 Browser sidecar 组件协议 1.62.1 与旧组件拒绝 | done |
| FS-048-002 Codex Desktop/CLI runtime 漂移修复与真实 E2E | done |

### 0.4.9 / 0.4.10 性能与稳定性

| Task | 状态 |
|---|---|
| FS-049-001 file_edit 固定元数据响应、bounded preview 与 CAS | done |
| FS-049-002 file_read 单次扫描 | done |
| FS-049-003 managed rg 稳定 reason、ignore/include 与 bounded fallback | done |
| FS-049-004 Agent readiness、持久 create 幂等与 delete 续做 | done |
| FS-049-005 host/WSL Runtime、Job 生命周期与 timing | done |
| FS-049-006 Browser readiness、共享启动状态与 timing | done |
| FS-049-007 完整 Gate、0.4.9 发布与 FastSpider_FS 自举 | done |
| FS-0410-001 静态 include 前缀下推与独立审计 | done |
| FS-0410-002 0.4.10 full gate、发布、自更新与生产验收 | done |
| FS-0410-003 文件/搜索/WSL/Agent/Browser 自举基准与清理 | done |

### 0.4.11–0.4.16 MCP 原生体验与可观测性

| Task | 状态 |
|---|---|
| FS-0416-001 17 个工具的单一分层能力指南与短描述 | done |
| FS-0416-002 capability_list overview/catalog/tool/workflow/error 兼容查询 | done |
| FS-0416-003 每 owner 64 条内存 MCP 调用诊断与隐私 allowlist | done |
| FS-0416-004 Web 诊断面板、登录门禁、启动单次加载与手动刷新 | done |
| FS-0416-005 冷客户端、错误分类、容量与敏感信息专项测试 | done |
| FS-0416-006 full gate、发布、Hub/spiderctl 部署与生产 MCP smoke | done |

### 0.4.17 ChatGPT 长会话 MCP 稳定性

| Task | 状态 |
|---|---|
| FS-0417-001 生产日志复现与根因分层：会话未发请求 vs Hub/Node/OAuth | done |
| FS-0417-002 OAuth refresh 契约核验并排除误修 offline_access | done |
| FS-0417-003 `fsprobe` 单入口、按需 Tool Search 与 Schema 体积硬预算 | done |
| FS-0417-004 最近已认证 MCP 请求到达诊断与 Web 展示 | done |
| FS-0417-005 Hub 专项、full release gate 与跨产品 E2E | done |
| FS-0417-006 commit/push、生产备份与 Hub/spiderctl 部署 | done |
| EXT-CHATGPT-0417-REFRESH 一次 Refresh 后同会话恢复验收 | done |

### 0.4.18 缓存、生命周期与发布安全

| Task | 状态 |
|---|---|
| FS-0418-001 OAuth 注册孤儿配额与历史授权保留边界 | done |
| FS-0418-002 Owner 删除共享 OAuth Client 的隔离事务 | done |
| FS-0418-003 可信反向代理来源与 DCR 限额 | done |
| FS-0418-004 Presentation/Artifact 清理失败可重试 | done |
| FS-0418-005 Artifact 清理锁、共享 Blob 与 `.part` 崩溃恢复 | done |
| FS-0418-006 Release manifest singleflight 取消与发布替换失效 | done |
| FS-0418-007 staging prune 原子隔离、TOCTOU 与失败恢复 | done |
| FS-0418-008 secretscan 密集输入有界扫描、路径检测与脱敏 | done |
| FS-0418-009 Codex generation 与 Node runtime 关闭生命周期 | done |
| FS-0418-010 starting Job shutdown、Browser/组件持续清理与语义版本 | done |
| FS-0418-011 全量门禁、版本提交、生产备份、Hub/spiderctl/Node 发布 | done |

### Final Acceptance Matrix

- `go test ./...`
- `go vet ./...`
- `git diff --check`
- Windows build / Linux build
- Real Browser E2E
- Real CC Switch E2E
- Real Claude Code E2E
- Real Codex E2E
- Local Bridge multi-provider E2E
- Task Workspace E2E
- Search ripgrep/native E2E
- file_read 2.0 E2E
- file_edit 2.1 + bounded preview/CAS E2E
- code_search 2.1 managed rg/fallback/timing E2E
- host/WSL Runtime + Job timing/cancel E2E
- update/reconnect E2E
- consumed-current staging cleanup E2E
- Windows legacy install artifacts cleanup E2E
- Hub release backup prune E2E
- Release staging prune local/server E2E
- Agent readiness/create/send/watch/cancel/result/delete E2E
- Browser readiness/packaged component/DOM locator E2E
- FastSpider_FS production self-bootstrap acceptance
- `scripts/release-gate.sh --full`
<!-- fast-spider:managed:roadmap:end -->

## Manual Notes

人工补充路线图写在本区，自动同步不得覆盖。
 
