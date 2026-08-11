# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `fast-spider-0.4.0-to-0.4.2`
- targetVersion: `0.4.2`
- branch: `main`
- baselineHead: `0de4f6286942ccd1a7432651292ea7c8069cd33e`
- phase: `0.4.2 release commit and production rollout`
- productionNode: `PCa / 0.3.14 / online / ready / generation 42`
- worktree: 0.4.0 baseline 已 clean + pushed；0.4.1→0.4.2 连续开发已完成最终 full release gate，待形成唯一 0.4.2 release commit。
- protectedParallelChanges: 后台首页“下载最新版 Windows 客户端”改动已纳入 0.4.0 baseline，不再是未归属 dirty 改动。
- currentTask: `FS-042-014 commit + push 0.4.2`
- completed041: `FS-041-001..015 PASS / no 0.4.1 release`
- completed042: `FS-042-001..013 PASS`
- workingContextRevision: `sha256:cbc5aaf060d7c7e10803ed210b115ad280c3d6af5607b0011c0d40087452c636`
- releaseGate041: `PASS / Windows Git Bash / full / clean scan`
- releaseGate042: `PASS / scripts/release-gate.sh --full / exitCode=0`
- nextGate: `FS-042-014 release commit + push, then Hub/Node production rollout`

### Guardrails

- 开发测试不得停止正式 PCa Node 或生产 Hub。
- 不允许测试写正式 Node data-dir，不允许测试替换正式 Node EXE，不启动第二正式 Node。
- 测试统一使用 in-process Hub/Node、临时 data-dir、临时 Local Bridge、临时组件目录和临时端口。
- 写操作遇到 `CONNECTION_LOST` 时不得自动重放；先重新读取文件、Git、Machine/Job 事实。
- 0.4.1 只作为连续开发阶段，不做半成品发布；最终只发布 0.4.2。
<!-- fast-spider:managed:current-state:end -->

## Manual Notes

此区域保留给人工设计记录。Fast Spider 自动同步不得覆盖本区域。
 
