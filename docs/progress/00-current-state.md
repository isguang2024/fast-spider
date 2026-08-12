# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `perf-stability-0.4.9`
- targetVersion: `0.4.10`
- branch: `main`
- sourceReleaseCommit: `019ade0903b89e109b00118e725752d9b5f3fe3a`
- phase: `FINAL PASS / production ready / stable-use mode`
- finalReleaseGate: `j-u62icj / PASS: Fast Spider full release gate / exitCode=0`
- productionHub: `0.4.10 / SHA256 8b0f297c896ee3d7cb1dea175f7bd59c4723fcbf683c8499e82567e2836fe7b8 / livez+readyz PASS`
- productionSpiderctl: `0.4.10 / SHA256 71bd762003c36d14fb28eee0a58a7b93274e8504b6a84938469ff453277fbfbb`
- productionNode: `PCa / 0.4.10 / windows-amd64 / generation 68 / SHA256 d2a2be3e0e65743a56939c768196e001bdb84287383d8dc6f3f0628e7da3e9c9 / online+ready`
- nodeRollback: `0.4.9 / SHA256 81056767d05df3344369b64526c4d4efde573e3f0cad12f8d051508b0dc80c0e`
- verifiedBackups: `pre-0.4.10-019ade0.zip / pre-0.4.9-46ef762.zip / pre-0.4.6-0de7bf1.zip / valid=true`
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
- currentTask: `none`
- nextGate: `仅在出现新的真实生产缺口时启动下一版本，不为版本号本身扩功能`

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
