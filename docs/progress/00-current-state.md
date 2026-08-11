# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `fast-spider-0.4.3-file-lifecycle`
- targetVersion: `0.4.3`
- branch: `main`
- baselineHead: `4c263b0cbd9712834bca179206ae442e7520ba26`
- phase: `0.4.3 full gate PASS / release rollout`
- productionRelease: `0.4.2 released/deployed / Hub + Node + search-ripgrep PASS`
- worktree: `main@4c263b0cbd9712834bca179206ae442e7520ba26` clean baseline 上形成 0.4.3 文件生命周期与文档/Gate dirty；最终 full gate 已 PASS，待 release commit。
- protectedParallelChanges: 后台首页“下载最新版 Windows 客户端”改动已纳入 0.4.0 baseline，不再是未归属 dirty 改动。
- currentTask: `FS-043-005 release commit + push`
- completed041: `FS-041-001..015 PASS / no 0.4.1 release`
- completed042: `FS-042-001..017 PASS / 0.4.2 formally released and deployed`
- completed043: `FS-043-001..004 PASS`
- workingContextRevision: `sha256:356f2dd1adf517347a49addf65e96b6c70468919451605e29ac223e2f8a40b65`
- releaseGate041: `PASS / Windows Git Bash / full / clean scan`
- releaseGate042: `PASS / scripts/release-gate.sh --full / exitCode=0`
- releaseGate043: `PASS / scripts/release-gate.sh --full / exitCode=0`
- validation043: `nodeupdate + nodeui / go test ./... / go vet ./... / cross-platform build / Real E2E / full gate PASS`
- nextGate: `FS-043-005 commit/push, then Hub/Node 0.4.3 rollout and production cleanup verification`

### Guardrails

- 开发测试不得停止正式 PCa Node 或生产 Hub。
- 不允许测试写正式 Node data-dir，不允许测试替换正式 Node EXE，不启动第二正式 Node。
- 测试统一使用 in-process Hub/Node、临时 data-dir、临时 Local Bridge、临时组件目录和临时端口。
- 写操作遇到 `CONNECTION_LOST` 时不得自动重放；先重新读取文件、Git、Machine/Job 事实。
- 0.4.2 已正式发布部署；0.4.3 测试不得操作该正式 Node/Hub/data-dir，发布动作必须另行明确执行。
<!-- fast-spider:managed:current-state:end -->

## Manual Notes

此区域保留给人工设计记录。Fast Spider 自动同步不得覆盖本区域。
 
