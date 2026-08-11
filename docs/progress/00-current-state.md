# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `fast-spider-0.4.0-to-0.4.2`
- targetVersion: `0.4.2`
- branch: `main`
- baselineHead: `45303385e7b45e7b0171746cd85b88197bfcf567`
- phase: `0.4.0 baseline closeout`
- productionNode: `PCa / 0.3.14 / online / ready / generation 42`
- worktree: dirty；0.4.0 多 AI Harness、CC Switch 只读 Routing、Claude Code Adapter 和文档尚未提交。
- protectedParallelChanges: `internal/hub/server/web/app.css`、`app.html`、`web_test.go` 的“下载最新版 Windows 客户端”入口改动已单独对账，必须保留。
- currentTask: `FS-040-005 0.4.0 baseline commit + push`
- workingContextRevision: `sha256:2b92fb54d5f910bec4aef9d5bd548fd18738a6aff2c8ba4c2f02a87220148349`
- releaseGate040: `PASS / Windows Git Bash / full`
- nextGate: `0.4.0 baseline pushed before any 0.4.1 code change`

### Guardrails

- 开发测试不得停止正式 PCa Node 或生产 Hub。
- 不允许测试写正式 Node data-dir，不允许测试替换正式 Node EXE，不启动第二正式 Node。
- 测试统一使用 in-process Hub/Node、临时 data-dir、临时 Local Bridge、临时组件目录和临时端口。
- 写操作遇到 `CONNECTION_LOST` 时不得自动重放；先重新读取文件、Git、Machine/Job 事实。
- 0.4.1 只作为连续开发阶段，不做半成品发布；最终只发布 0.4.2。
<!-- fast-spider:managed:current-state:end -->

## Manual Notes

此区域保留给人工设计记录。Fast Spider 自动同步不得覆盖本区域。
 
