# Fast Spider Acceptance Log

验收记录只保存可复查的任务、Git、测试和运行事实，不保存完整 Prompt、聊天原文或敏感凭据。

<!-- fast-spider:managed:acceptance:start -->
## 2026-08-11 — 0.4.0 Baseline Reconciliation

- `FS-040-001`: PASS。
- PCa 运行事实：online、ready；正式 Node 版本 0.3.14；generation 42。
- Git 事实：branch `main`，HEAD `45303385e7b45e7b0171746cd85b88197bfcf567`，与 origin/main 对齐，工作树 dirty。
- 源码版本事实：`internal/version/version.go` = `0.4.0`。
- Working Context 已恢复，确认 0.4.0 多 AI Harness、CC Switch 只读 Routing、Claude Code 2.1.207 Adapter、真实 Browser/CC Switch/Claude/Codex/Local Bridge E2E 历史完成事实。
- 并行 UI 改动已单独读取 Git diff：后台首页增加 Windows 最新客户端下载入口、样式和测试；保留，不回滚、不覆盖。
- `scripts/release-gate.sh` 已读取；当前 full gate 包含 Browser、CC Switch、Claude Code、Codex、Local Bridge multi-provider 和 product smoke。
- `FS-040-004`: PASS；Git for Windows `bash.exe scripts/release-gate.sh --full` 终态 `PASS: Fast Spider full release gate`，exitCode=0。
- Gate 包含并通过：secret/private-marker scan、go vet、go test、current/Windows amd64/Linux amd64 build、Hub restore E2E、Local Bridge E2E、repeated Node、Real Browser、Real CC Switch、Real Claude Code、Real Codex、Local Bridge multi-provider discovery、Local Bridge→Codex product smoke。
- 当前 Go Windows 工具链为 windows/386 + CGO=0，因此脚本按设计跳过 amd64/CGO 专属 fuzz/race；该 skip 已由 full gate 明确记录，不影响本次脚本终态 PASS。

## Task Workspace Bootstrap

- `FS-040-002`: 六个固定 Markdown 文件已经创建；均位于 `docs/progress`，内容不包含 machine opaque ID、本机仓库绝对路径、Token、Cookie、API Key、完整 Prompt 或聊天原文。
- 文件通过当前 Fast Spider `file_edit` expected SHA/CAS 写入；未发生 `CONNECTION_LOST`。
- `FS-040-003`: PASS；完整计划已写入 Working Context，revision `sha256:2b92fb54d5f910bec4aef9d5bd548fd18738a6aff2c8ba4c2f02a87220148349`。
<!-- fast-spider:managed:acceptance:end -->

## Manual Acceptance Notes

人工验收补充写在本区；自动同步不得覆盖。
 
