# Fast Spider Open Issues

<!-- fast-spider:managed:open-issues:start -->
## Active

| ID | 类型 | 状态 | 说明 |
|---|---|---|---|
| EXT-CHATGPT-0417-REFRESH | external/manual | pending | 生产 Hub/spiderctl 已为 0.4.17；需要一次 ChatGPT App Refresh 吸收新的 `fsprobe` 工具描述。Refresh 后先确认 `query=fsprobe` 只物化 `machine_list`，再在同一长会话验证命名空间缺失后无需新会话、无需重新登录/授权即可恢复。 |

## Internal Blockers

当前无内部阻塞。若后续出现外部依赖问题，应先完成所有不依赖该问题的任务，并在本表记录影响范围与恢复条件。

## Resolved

- `FS-0417-SERVER`：根因证据、按需 Tool Search、Schema 预算、MCP HTTP 到达诊断、full gate、提交推送、验证备份、Hub/spiderctl 0.4.17 部署与生产 MCP smoke 全部 PASS。
- `FS-0416-006`：0.4.16 full gate、提交推送、验证备份、Hub/spiderctl 部署与生产 MCP smoke 全部 PASS。
- `EXT-CLAUDE-AUTH`：早期 revoked-token 401 已不再构成当前问题；后续真实 Claude E2E 在最终 full gate 中通过。
- 主动优化待办：无。进入稳定使用阶段，仅由真实故障、可复现性能瓶颈或明确新需求触发下一计划。
<!-- fast-spider:managed:open-issues:end -->

## Manual Issues

人工新增问题写在本区；自动同步不得覆盖。
 
