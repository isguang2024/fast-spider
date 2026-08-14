# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `layered-mcp-guide-0.4.16`
- targetVersion: `0.4.16`
- branch: `main`
- sourceReleaseCommit: `7f085f31cdadecabb54307bc3f024d0bc795543a (0.4.16 implementation baseline; release commit pending)`
- phase: `0.4.16 implementation complete / full gate PASS / release and production deployment pending`
- finalReleaseGate: `j-h6x1dh / PASS: Fast Spider full release gate / exitCode=0`
- productionHub: `0.4.15 / SHA256 1824cc9f7ebb9b4ebe9fc9e5fbbcc75a049747509355849d7ccaa9ce3acc68b1 / livez+readyz PASS`
- productionSpiderctl: `0.4.15 / SHA256 48207c5e520d708a5851f00c9a8573d73acd4de3150444709faa45c11214ff0d`
- productionNode: `PCa / 0.4.14 / windows-amd64 / registered active; online+ready acceptance pending`
- nodeRollback: `unchanged; 0.4.16 does not build or deploy Node artifacts`
- verifiedBackups: `pre-0.4.16 backup pending; existing production backups unchanged`
- workingContext11: `perf-stability-0.4.9 completion=100%；最终交付版本为 0.4.10`
- schemaStatus: `file.write/code.search/shell/build/job/agent/browser 新能力已由生产 Node 协商并完成真实自举`
- completed041: `FS-041-001..015 PASS / no 0.4.1 release`
- completed042: `FS-042-001..017 PASS / 0.4.2 formally released and deployed`
- completed043: `0.4.3 formally released and deployed`
- completed044: `0.4.4 formally released and deployed`
- completed045: `0.4.5 formally released and deployed`
- completed046: `0.4.6 release + acceptance remediation + production verification PASS`
- completed047048: `Browser Agent refs/batch、网络策略组件协议与 Codex runtime 修复 PASS`
- completed049: `性能稳定性、响应瘦身、WSL runtime、Agent/Browser readiness 正式发布 PASS`
- completed0410: `静态 include 搜索范围修订、重新发布与 FastSpider_FS 自举验收 PASS`
- currentTask: `FS-0416-006 commit/push → verified backup → Hub/spiderctl production deployment → MCP smoke`
- nextGate: `release commit and origin/main alignment`

### Guardrails

- Git、文件、测试结果和真实运行状态始终是最终事实源；Working Context/Markdown 只保存可恢复的任务事实。
- 写操作遇到 `CONNECTION_LOST`、`MACHINE_OFFLINE` 或 `JOB_NOT_FOUND` 时不得盲重放；先重新读取 Machine/Job/文件/Git 事实。
- 正式 Hub 更新继续使用独立服务器事务式替换 + 验证备份；Node 正常版本升级继续使用签名 updater，同版本修复不得伪造版本绕过 updater。
- Windows Node 正式产物必须显式构建并核验 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` 与 VCS revision，禁止依赖本机持久 GOARCH 默认值。
<!-- fast-spider:managed:current-state:end -->

## Manual Notes

此区域保留给人工设计记录。Fast Spider 自动同步不得覆盖本区域。

### 2026-08-13 — 0.4.10 性能与稳定性

- 已完成范围：file_edit 响应瘦身、file_read 单次扫描、code_search managed rg/fallback、Agent readiness/create 幂等、host/WSL Runtime、Browser readiness/timing 与 requestId/traceId。
- 版本：0.4.9 部署后自举发现大型仓库窄静态 include 仍从根遍历；修订按不可变发布规则提升并最终发布 0.4.10。
- 工作树：此前保留的 Node UI 窗口尺寸改动经确认与升级无关后已撤销；文档同步前工作树 clean。
- 最终状态：0.4.10 源码修订 `019ade0` 已推送并完成 full release gate；生产 Hub/spiderctl/PCa Node 均为 0.4.10，Node generation=68、online/ready，FastSpider_FS 文件、搜索、host/WSL、Agent 与 Browser 自举验收全部 PASS。
- 回滚与备份：Hub、spiderctl、Node `.previous` 均保留 0.4.9；`pre-0.4.10-019ade0.zip`、`pre-0.4.9-46ef762.zip`、`pre-0.4.6-0de7bf1.zip` 为最近三份标准验证备份。
