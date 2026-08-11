# Fast Spider Current State

本文件是项目内可读的任务事实镜像。Git、文件内容、测试结果和真实运行状态始终是最终事实源；Working Context 保存结构化摘要，本目录保存完整设计、决策和验收证据。

<!-- fast-spider:managed:current-state:start -->
## Managed Current State

- planId: `fast-spider-0.4.6-final-acceptance`
- targetVersion: `0.4.6`
- branch: `main`
- sourceFixCommit: `b72f13ade86b7e147dc86536d6c20b8ca8c73879`
- phase: `FINAL PASS / production ready`
- acceptanceFix: `working_context goal Schema 契约说明 + plan.sync pre/post Git snapshot 语义已修复并补回归测试`
- finalReleaseGate: `job_UHILu4nJG-nz8A286pgl4m-pYI5ttDJP / Windows Git for Windows Bash / PASS / exitCode=0`
- productionHub: `0.4.6 / SHA256 7dca315e29b0ac699bdb460a1dd18aee11db443b2a2fc9017f94b1dce9498d5b / livez+readyz PASS`
- productionSpiderctl: `0.4.6 / SHA256 cf2156ffe24d70a01b9a47421ae16fc5f8d2a0c030a73f4f0df8e9053a5eac9f`
- productionNode: `PCa / 0.4.6 / windows-amd64 / generation 57 / SHA256 617c3e430c3317818641302472ae0873f5ba56384c247923838450bb6667498b / online+ready`
- nodeRollback: `独立原生产 amd64 rollback 已保留 / SHA256 148a89c58fc4d02542edf2d4c1e862db1de232bfe49e0f36054a95373240618b`
- hubBackup: `pre-0.4.6-b72f13a.zip / backup-verify valid=true`
- workingContext11: `生产 Node plan.sync 已实测 dirtyBeforeSync + post-sync currentGit；round4 completion=100%`
- schemaStatus: `服务端新 Schema 回归测试 PASS；当前既有 ChatGPT 会话仍可能显示热更新前的字段描述缓存`
- completed041: `FS-041-001..015 PASS / no 0.4.1 release`
- completed042: `FS-042-001..017 PASS / 0.4.2 formally released and deployed`
- completed043: `0.4.3 formally released and deployed`
- completed044: `0.4.4 formally released and deployed`
- completed045: `0.4.5 formally released and deployed`
- completed046: `0.4.6 release + acceptance remediation + production verification PASS`
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
 
