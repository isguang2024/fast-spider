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
<!-- fast-spider:managed:change-log:end -->

## Manual Change Notes

人工补充变更写在本区；自动同步不得覆盖。

