# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `layered-mcp-guide-0.4.16`
- targetVersion: `0.4.16`
- branch: `main`
- sourceReleaseCommit: `c93373e2ce2d1b7138be8f0f613f4713955509fb`
- phase: `0.4.16 FINAL PASS / PRODUCTION READY`
- finalReleaseGate: `j-ofkr5w / PASS: Fast Spider full release gate / exitCode=0`
- productionHub: `0.4.16 / SHA256 30437a500f398503000f37c70cec05782058d744e93c8c2706301b18d595423e / PID 1113954 / systemd active+running / local+public livez+readyz 200`
- productionSpiderctl: `0.4.16 / SHA256 7dba50690e53d8cbf8881be9825bfdffc49cf8e3af7f194ce5f36b2efe7cb078`
- productionNode: `PCa / 0.4.14 / windows-amd64 / online+ready；本版本未部署 Node`
- nodeRollback: `unchanged; 0.4.16 does not build or deploy Node artifacts`
- verifiedBackups: `pre-0.4.16-c93373e.zip valid=true；真正升级前 pre-0.4.16-cecf3fe.zip valid=true；中间修订备份保留以免误删升级前证据`
- workingContext11: `layered-mcp-guide-0.4.16 completion=100%`
- schemaStatus: `Guide 1.0、17 工具分层能力与 owner 级 MCP 诊断已由 OAuth+PKCE 冷客户端和生产 Dashboard 验收`
- completed041: `FS-041-001..015 PASS / no 0.4.1 release`
- completed042: `FS-042-001..017 PASS / 0.4.2 formally released and deployed`
- completed043: `0.4.3 formally released and deployed`
- completed044: `0.4.4 formally released and deployed`
- completed045: `0.4.5 formally released and deployed`
- completed046: `0.4.6 release + acceptance remediation + production verification PASS`
- completed047048: `Browser Agent refs/batch、网络策略组件协议与 Codex runtime 修复 PASS`
- completed049: `性能稳定性、响应瘦身、WSL runtime、Agent/Browser readiness 正式发布 PASS`
- completed0410: `静态 include 搜索范围修订、重新发布与 FastSpider_FS 自举验收 PASS`
- completed0416: `分层能力指南、MCP 调用可观测性、full gate、Hub/spiderctl 发布与冷客户端生产验收 PASS`
- currentTask: `none`
- nextGate: `仅剩 ChatGPT App 管理页 Refresh FastSpider_FS，并在新会话执行 connection-check；不构成服务端发布阻塞`

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
