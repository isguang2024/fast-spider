# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `fs-0.4.22-url-only-attachments`
- targetVersion: `0.4.22`
- branch: `main`
- sourceReleaseCommit: `2ff08335361269faa9b56265235ccf43c74df492`
- phase: `0.4.22 PRODUCTION DEPLOYED / HEALTHY`
- finalReleaseGate: `worktree/index secret scan PASS；内置 history scan PASS；go test ./...、go vet、diff-check、跨平台 build、post-history 专项、fuzz、race、Windows Browser、CC Switch、Claude、MCP 与 multi-provider discovery PASS；本机额外 private-marker overlay 仅命中既有 Git history 并使原始 --full 提前停止；Local Bridge→Codex product E2E 300 秒外部 Agent 等待超时，未伪装为 PASS`
- productionHub: `0.4.22 / SHA256 6792a5747a418d07e938fbc3e0a23aa1f68229392d4eea617d0281b140f62e25 / PID 3989323 / systemd active / local+public livez+readyz 200`
- productionSpiderctl: `0.4.22 / SHA256 4b5a174660465722ee4cfe19a3718c45c216a22f1d699d186015337b2da45941`
- productionNode: `PCa / 0.4.22 / windows-amd64 / generation=123 / online+ready / release SHA256 e5fd6f820001022c4ed554d3df038fd6482d4f1153fa24984de59ecdb3d2696e`
- hubRollback: `0.4.21 rollback copies / Hub SHA256 dc68ebff7620b0218046e81615b742e9fe5b39d61a6c1d20fe1c9a39f584f560 / spiderctl SHA256 1588c1bbcb46adf5ed21096add05e352c5b29b992e019337f1c47768c9ce9a7e / Windows Node SHA256 66d3ea1ee61a0312dc542484a749714a81aba61891fe32c11721874f46f0005e`
- verifiedBackups: `pre-0.4.22-2ff08335361269faa9b56265235ccf43c74df492.zip / SHA256 d89163794add000e45d1ad5bf4e212190f61b0a5add9ae50f1229a71ec0b2a64 / size=14714987 / valid=true / manifest source version=0.4.21`
- workingContext11: `0.4.22 源码、Hub/spiderctl、三平台 Node release、Windows PCa 自更新、48h URL-only attachment 生产 smoke 与公网健康检查 done`
- schemaStatus: `0.4.22 URL-only attachment 行为已上线；live artifact_get.publishFile 只返回 url/fileName/contentType/sizeBytes/expiresAt，公网临时 URL=200 image/png，约 48h expiry；当前长会话工具描述可能仍受 ChatGPT schema cache 影响，但运行时行为已切换`
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
- completed0421: `Node operation.log/query、MCP operation_log、敏感字段投影、19 工具目录、Hub/Node 0.4.21 发布与健康检查 PASS`
- completed0422: `publishFile/Browser/OS screenshot URL-only attachment、48h TTL/自动清理、三平台 Node release、Hub/spiderctl 0.4.22 与 PCa 自更新完成；公网 attachment smoke PASS`
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
