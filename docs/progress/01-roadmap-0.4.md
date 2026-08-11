# Fast Spider 0.4 Roadmap

<!-- fast-spider:managed:roadmap:start -->
## Plan `fast-spider-0.4.0-to-0.4.3`

状态枚举：`pending` / `in_progress` / `blocked` / `done`。0.4.1 是连续开发阶段，不做独立发布。

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
| FS-043-005 commit + push | in_progress |
| FS-043-006 Hub / Node 0.4.3 更新 + staging 清理验收 | pending |
| FS-043-007 发布后文件 / 组件 / 更新链路继续审计 | pending |

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
- file_edit 2.0 E2E
- update/reconnect E2E
- consumed-current staging cleanup E2E
- `scripts/release-gate.sh --full`
<!-- fast-spider:managed:roadmap:end -->

## Manual Notes

人工补充路线图写在本区，自动同步不得覆盖。
 
