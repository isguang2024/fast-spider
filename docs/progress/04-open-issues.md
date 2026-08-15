# Fast Spider Open Issues

<!-- fast-spider:managed:open-issues:start -->
## Active

| ID | 类型 | 状态 | 说明 |
|---|---|---|---|

## Internal Blockers

当前无内部阻塞。若后续出现外部依赖问题，应先完成所有不依赖该问题的任务，并在本表记录影响范围与恢复条件。

## Resolved

- `EXT-CHATGPT-0417-REFRESH`：发布后 Refresh 已完成；`query=fsprobe` 精确只物化 `machine_list`，当前会话随后按需恢复 `capability_list` 与 `working_context`，无需新会话、重新登录或 OAuth 授权。0.4.17 宿主侧最终门禁 PASS。
- `FS-0417-SERVER`：根因证据、按需 Tool Search、Schema 预算、MCP HTTP 到达诊断、full gate、提交推送、验证备份、Hub/spiderctl 0.4.17 部署与生产 MCP smoke 全部 PASS。
- `FS-0418-001..010`：OAuth 历史保留/Owner 隔离/DCR 来源、Presentation/Artifact 可恢复清理、Release manifest/staging 生命周期、秘密门禁、Codex/Job 关闭代际、Browser/组件持续清理均已实现；等待 0.4.18 full gate、推送和生产验收闭环。
- `FS-0416-006`：0.4.16 full gate、提交推送、验证备份、Hub/spiderctl 部署与生产 MCP smoke 全部 PASS。
- `EXT-CLAUDE-AUTH`：早期 revoked-token 401 已不再构成当前问题；后续真实 Claude E2E 在最终 full gate 中通过。
- 主动优化待办：无。进入稳定使用阶段，仅由真实故障、可复现性能瓶颈或明确新需求触发下一计划。
<!-- fast-spider:managed:open-issues:end -->

## Manual Issues

人工新增问题写在本区；自动同步不得覆盖。

### 2026-08-15 — 缓存与生命周期审计

- 本轮 10 项问题均已实现；主控已补做 Hub/Node/Artifact/Release/secret 关键路径复核，当前无已知本地代码阻断。
- 后续门禁：在 Linux amd64 + CGO 环境补跑 `go test -race ./...`；当前 windows/386 工具链不支持 race detector。
- 后续仅在真实进入条件出现时启动：清理可观测性、Session 显式 reconcile、Working Context 归档、大规模清理背压，以及组件/发布目录的跨进程协作锁。详见 `docs/19-roadmap.md` 与 `docs/23-cache-and-lifecycle.md`。
