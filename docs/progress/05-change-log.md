# Fast Spider Change Log

<!-- fast-spider:managed:change-log:start -->
## 2026-08-11

- 恢复 PCa、Working Context、Git 和关键文档事实，确认继续基线为 `main@45303385e7b45e7b0171746cd85b88197bfcf567` 的 dirty 0.4.0 工作树。
- 单独对账并保留后台首页“下载最新版 Windows 客户端”并行 UI 改动。
- 新建 `docs/progress` Task Workspace 六文件结构。
- 建立 `fast-spider-0.4.0-to-0.4.2` 路线图、决策、验收日志和开放问题记录。
- 明确 0.4.1 不单独发布，连续开发至 0.4.2 后再执行正式 Hub/Node 更新。
- 0.4.0 full release gate PASS 后形成 baseline commit `0de4f6286942ccd1a7432651292ea7c8069cd33e`，并成功推送 `origin/main`；随后正式进入 0.4.1→0.4.2 连续开发。
- 完成 FS-041-001..004：Working Context Plan/Task、Markdown workspace 隔离与 Windows junction 防护、managed block/CAS/原子写、九个 plan/task/markdown/progress actions；保持 MCP 工具总数 16，相关四包测试与 diff check PASS。
- 完成 FS-041-005：本地客户端新增“任务与进度”一级页面和受约束 loopback API，复用 Node Plan 状态；Windows 路径测试改为文件身份比较后独立复测 PASS。
- 完成 FS-041-006..012：Agent Manager/Provider/Session 职责拆分，Codex+Claude 静态 Registry，CC Switch 独立只读 routing + schema fingerprint fail-closed，route/CLI/models 短 TTL、并行 discovery 与八类统一错误分类全部落地；独立 Agent/Node/Hub 回归与 diff check PASS。
- 完成 FS-041-013：本地客户端新增“AI 与路由”一级页面，使用显式 allowlist DTO 展示 Codex/Claude/CC Switch 脱敏事实；页面加载与刷新均不发起模型生成，独立回归 PASS。
- 完成 FS-041-014：本地客户端新增“诊断”一级页面和只读 `/api/diagnostics`，整合 Node/Hub/Agent/Task Workspace/本地能力脱敏状态与统一错误摘要；安全门禁和独立回归 PASS。
- 完成 FS-041-015：全仓 test/vet/diff check、跨平台 build 与完整 Real E2E Gate 两轮 PASS；0.4.1 不发布，继续同一 dirty 工作树进入 0.4.2。
- 修正 release gate public-source 扫描：过滤工作树中已不存在的 tracked 文件，避免重构删除文件产生 grep 噪音；修正后 full gate clean PASS。
- 完成 FS-042-001..004：接入 Managed `search-ripgrep` 解析与安全执行，`code.search` 扩展 content/files、glob、context、engine/fallback/elapsed，保留完整 native Go fallback；协议/MCP 与独立五包回归 PASS。
- 完成 FS-042-005：`file.read` 扩展 byte/line/head/tail/around/stat/行号，保持原文件与返回 chunk hash 语义清晰，大文件流式定位且正文 bounded；协议/MCP 与独立四包回归 PASS。
- 完成 FS-042-006..009：`file.edit` 收敛为单一 Go 原生 create/replace/editMany/preview 内核，CAS/唯一匹配/批量全有或全无/原子替换/BOM换行权限/preview no-write/bounded diff 全部落地；Hub safe-retry/mutation audit 按 action 区分，独立四包回归 PASS。
- 完成 FS-042-010..011：本地客户端“组件”页只管理 Browser/search-ripgrep，并提供脱敏组件状态和手动隔离搜索/文件自检；native/fake managed rg、file_read 2.0、file_edit preview、guard 与临时目录清理测试 PASS。
- 完成 FS-042-012：源码版本更新为 0.4.2，全仓 Current 文档按真实 capability/UI/Agent/安全边界同步；full release gate 新增六组可读的 0.4.2 专项入口，下一任务切换到 FS-042-013。
- 完成 FS-042-013：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，覆盖全仓测试、跨平台构建、0.4.2 专项能力、更新重连与 Real Browser/CC Switch/Claude/Codex/Local Bridge；进入唯一 0.4.2 release commit + push。
- 0.4.2 已由 release commit `4c263b0` 正式发布部署，Hub、Node 与 Managed search-ripgrep 三边生产验收 PASS。
- 完成 FS-043-001..003：新增独立 consumed-current staging cleanup，严格保持 Ready/apply→cleanup 顺序和 marker/error fail-safe；版本更新为 0.4.3，文档、专项 Gate、全仓 test/vet/diff/bash syntax 全部 PASS。
- 0.4.3 已正式发布部署，生产自更新后的 `updates` staging 验证归零；后续审计将旧手工安装遗留物收敛为 0.4.4 小版本任务。
- 完成 FS-044-001..003：新增 Windows-only legacy install artifacts cleanup，以 Win32 reparse attribute、严格文件名、同级/非递归范围保护 current/previous 和未知项；NodeUI 安全启动时机接线、0.4.4 版本/文档/专项 Gate 与全仓验证均 PASS。
- 完成 FS-044-004：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，新增 legacy install artifacts cleanup 专项门禁与全部既有 Real E2E 均通过；无人值守进入 0.4.4 release commit/push 与生产迁移验收。
- 0.4.4 已正式发布部署，生产自动清理 Windows legacy install artifacts 48,819,313 bytes 并保护 current/previous；后续审计将标准 release backup 线性增长收敛为 0.4.5 任务。
- 完成 FS-045-001..003：新增全候选 Verify 后按 CreatedAt UTC retention 的 release backup prune API 与 `spiderctl backup-prune`；严格命名/root/reparse/bounds/零删除失败边界、部分删除事实 DTO、0.4.5 版本/文档/专项 Gate 和全仓验证全部 PASS。
- 完成 FS-045-004：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，新增 release backup prune 专项门禁与全部既有 Real E2E 均通过；无人值守进入 0.4.5 release commit/push、生产升级与 keep=3 轮换验收。
- 0.4.5 已由 release commit `f5cee7c1` 正式发布部署；标准 release backup 从 4 份安全轮换到最近 3 份且逐份 Verify PASS，历史异名/Hub binary backup 保持原 SHA/size；Node/Hub/spiderctl 与组件/搜索文件自检均正常。
- 发布后审计确认 release staging 是剩余明确线性增长点；无进程引用后一次性清理本机/服务器旧 staging 共 665,103,310 bytes。
- 完成 FS-046-001..003：新增 bounded/fail-closed `PruneReleaseStaging` 与 `spiderctl staging-prune` plan/apply CLI；严格 local/server 名称、future/unknown 保留、reparse/limits/TOCTOU/幂等/partial facts 测试、0.4.6 版本/文档/专项 Gate 与全仓 test/vet/diff/bash syntax 全部 PASS。
- 完成 FS-046-004：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，新增 release staging prune 专项门禁与全部既有 Real E2E 均通过；无人值守进入 0.4.6 release commit/push 与生产 staging lifecycle 验收。
- 0.4.6 正式验收收口：修正 `working_context.goal` Schema 契约描述与 `plan.sync` pre/post Git snapshot 语义，补自动回归；验收修复源码 `b72f13a` 已 push，Windows Git for Windows full release gate 全绿。
- 生产 Hub/spiderctl 已同版本事务式更新并通过验证备份、livez/readyz；PCa Node 因 updater 正确拒绝同版本更新，采用受控原子替换。验收中识别并淘汰一次受持久 `GOARCH=386` 影响的错误构建，最终以显式 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` clean VCS 产物替换，PCa generation=57、online/ready、最终 SHA 对账 PASS。
- Fast Spider 0.4.6 最终验收状态更新为 `FINAL PASS / PRODUCTION READY`；后续仅在出现新的真实生产缺口时开启下一版本。
<!-- fast-spider:managed:change-log:end -->

## Manual Change Notes

人工补充变更写在本区；自动同步不得覆盖。

