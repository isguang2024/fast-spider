# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `fs-0.4.19-release`
- targetVersion: `0.4.19`
- branch: `main`
- sourceReleaseCommit: `8efbdfc624bdfdfbc6eece527a55b8f022cdeacf`
- phase: `0.4.19 PRODUCTION DEPLOYED / HEALTHY`
- finalReleaseGate: `core PASS / go vet PASS / go test 595 passed in 26 packages / Linux amd64 Hub+spiderctl build PASS；full gate 在既有 private-marker history policy 处按设计阻断（80 个历史命中），未绕过`
- productionHub: `0.4.19 / SHA256 76ea227ce95898c0a6caf98380bce32c9c23726d5f84d78008b37ff0010b6a6e / PID 2598437 / systemd active / local+public livez+readyz 200`
- productionSpiderctl: `0.4.19 / SHA256 160a7bf2fb82996231ba4388fedcaa5cf982e3d7d5290cf32fc959325c1eb751`
- productionNode: `PCa / 0.4.18 / windows-amd64 / generation=89；本次发布未构建、未修改 Node release/version.txt/push.json`
- hubRollback: `版本化回滚副本保留 0.4.18 / Hub SHA256 2665b635ef898a3aebb6861ad9e0525ffe5f30110acfe4db5cff18c847334a13 / spiderctl SHA256 1e2911bc223fb79f830b64b9f12574c55e8d0065c53a1b4ff6021f8b47c1f1b3`
- verifiedBackups: `pre-0.4.19-8efbdfc624bdfdfbc6eece527a55b8f022cdeacf.zip / SHA256 c33ce40aefa9e15971bf4add6ee6655d0148fd624a668fa0028b5cf95f24a5ae / size=11235070 / valid=true / manifest source version=0.4.18`
- workingContext11: `fs-0.4.19-release completion=100%；Hub/spiderctl 部署与健康检查 done`
- schemaStatus: `0.4.19 Hub MCP 分层 capability/tool guide 已上线；公网 OAuth discovery=200、MCP 未认证路由=401、livez/readyz=200；认证 tools/list/capability_list 冷调用待 FS/OAuth 入口补验`
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
- completed0417: `根因证据、过滤式按需发现、Schema 体积预算、请求到达诊断、full gate、commit/push、备份、Hub/spiderctl 生产发布及 Refresh 后同会话恢复均 PASS`
- completed0419: `两层 MCP 能力地图、17 工具摘要、底层 capability 映射、Windows PowerShell/cmd 指引、Hub/spiderctl 0.4.19 生产部署与公网健康检查 PASS；认证 MCP 冷调用待补验`
- currentTask: `none`
- nextGate: `进入稳定使用阶段；仅在新的真实故障、可复现性能瓶颈或明确新需求出现时开启下一计划`

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

### 2026-08-15 — 0.4.18 缓存与生命周期审计、发布验收

- 本地工作树完成 10 项缓存、长期运行资源、Artifact、组件、Browser、Agent 索引、Release manifest、备份清理和维护文档优化；删除策略继续排除用户数据、原生 Agent 历史、未知项、活动项与 `in_doubt`。
- 效率与恢复重点：内存缓存有界且返回值隔离；不同 Artifact 上传可并发，删除失败进入持久重试队列；Release manifest 复用 hash/sign；临时资源采用严格命名、有界扫描和失败恢复。
- 安全清理重点：`backup-prune` 默认 plan-only，同进程创建/清理串行，Windows/Linux 删除前比较已冻结的句柄身份；同尺寸、同 mtime 原子替换不会误删。
- 验证：`go test ./... -count=1` 592 项通过（26 包），`go vet ./...`、`git diff --check`、current/history secretscan、`bash -n` 与 `scripts/release-gate.sh --full` 均 PASS；Windows/386 原生工具链无 CGO C 编译器，已在同一工作树的 WSL Linux/amd64 + CGO 环境补跑 `go test -race ./...`，全量 PASS。
- 生产事实：release commit `a8934c8` 已推送并部署；Hub/spiderctl 0.4.18、PCa Node 0.4.18 generation=89，Hub 本机/公网 livez/readyz 均 200；升级前备份已 Verify，旧 Hub/spiderctl/Node 回滚副本均保留。
- 清理边界：`backup-prune --keep 3` 已完成 plan-only，候选 6、保留 3、计划删除 3、实际删除 0；资料室清理继续只做 dry-run，没有创建内置定时任务。可由 Codex Automations 外部定时触发 dry-run，实际删除仍需显式授权。
