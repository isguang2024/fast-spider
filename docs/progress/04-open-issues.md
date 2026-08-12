# Fast Spider Open Issues

<!-- fast-spider:managed:open-issues:start -->
## Active

| ID | 类型 | 状态 | 说明 |
|---|---|---|---|
| — | — | none | 当前没有已知的内部或外部阻塞；0.4.10 full gate、Claude/Codex 产品 E2E 与生产自举均已通过。 |

## Internal Blockers

当前无内部阻塞。若后续出现外部依赖问题，应先完成所有不依赖该问题的任务，并在本表记录影响范围与恢复条件。

## Resolved

- `EXT-CLAUDE-AUTH`：早期 revoked-token 401 已不再构成当前问题；后续真实 Claude E2E 在最终 full gate 中通过。
- 主动优化待办：无。进入稳定使用阶段，仅由真实故障、可复现性能瓶颈或明确新需求触发下一计划。
<!-- fast-spider:managed:open-issues:end -->

## Manual Issues

人工新增问题写在本区；自动同步不得覆盖。
 
