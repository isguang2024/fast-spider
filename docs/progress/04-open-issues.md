# Fast Spider Open Issues

<!-- fast-spider:managed:open-issues:start -->
## Active

| ID | 类型 | 状态 | 说明 |
|---|---|---|---|
| EXT-CLAUDE-AUTH | external | open | 本机 Claude Code Runtime/stream 可用，但当前官方 OAuth 上游曾真实返回 revoked-token 401。该问题不阻塞 Adapter/任务系统/搜索文件能力开发；最终真实 Claude E2E 允许正确归一化为 `auth_failed`/failed，但若要求成功模型回答需用户重新认证或切换健康路由。 |

## Internal Blockers

当前无内部阻塞。若后续出现外部依赖问题，应先完成所有不依赖该问题的任务，并在本表记录影响范围与恢复条件。
<!-- fast-spider:managed:open-issues:end -->

## Manual Issues

人工新增问题写在本区；自动同步不得覆盖。
 
