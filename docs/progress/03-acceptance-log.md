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
- `FS-040-003`: PASS；完整计划已写入 Working Context，初始 revision `sha256:2b92fb54d5f910bec4aef9d5bd548fd18738a6aff2c8ba4c2f02a87220148349`。
- `FS-040-005`: PASS；0.4.0 baseline commit `0de4f6286942ccd1a7432651292ea7c8069cd33e` 已成功 push 到 `origin/main`，push 后 HEAD 与 origin/main 完全一致。
- 0.4.0 push 后 Working Context baseline 已切换到该 commit，新 revision `sha256:3edc84fd694395370739888c461195e78e6698c7ea171f799a5c582786f89735`。

## 2026-08-11 — FS-041-001..004 Task Workspace Backend

- `FS-041-001`: PASS；现有 `working.context` 扩展 Plan/Task 状态，旧 `get/set/clear` 继续映射默认 plan，共享同一状态写入路径。
- `FS-041-002`: PASS；Markdown workspace 绑定 projectPath，限制普通 `.md`、64 文件、512 KiB/文件、4 MiB 总量，并对 Windows reparse-point/junction 与 symlink 采用 fail-closed 路径校验。
- `FS-041-003`: PASS；task/Markdown 使用 revision/CAS；Markdown 写入采用临时文件、fsync、原子替换，managed block 仅替换受管区块并保留人工区域。
- `FS-041-004`: PASS；同一 `working_context` MCP 工具已贯穿 `plan.init/plan.get/plan.list/plan.sync/task.update/markdown.list/markdown.read/markdown.append/progress.watch`，协议 capability 升至 1.1，MCP 工具总数仍为 16。
- FastSpider_FS 独立执行 `go test ./internal/node ./internal/protocol/v1 ./internal/hub/core ./internal/hub/server`：全部 PASS；Node 约 14.2s，Hub server 约 11.3s。
- FastSpider_FS 独立执行 `git diff --check`：PASS。

## 2026-08-11 — FS-041-005 Local Task & Progress UI

- `FS-041-005`: PASS；本地 Edge App Window 增加一级“任务与进度”页面，复用 Node Working Plan 本地能力，不复制第二套任务状态。
- 页面显示项目/plan/Git branch+HEAD+dirty、完成度、当前/阻塞任务、最近验收、Markdown Workspace 与 revision，并提供初始化/绑定、Markdown 查看、打开受绑定目录、task.update/evidence、plan.sync 与 CAS 冲突提示。
- 首次 FastSpider_FS 独立复测发现两条 Windows 路径断言以原始字符串比较 canonical/long path 与 8.3/temp path，导致假失败；实现保持 realpath/canonical 安全语义，测试改用 `os.Stat + os.SameFile` 验证真实目录身份。
- 修正后 FastSpider_FS 独立执行 `go test ./internal/nodeui ./internal/node`：PASS，终态 exitCode=0；NodeUI 约 1.175s，Node PASS。
- FastSpider_FS 独立执行 `git diff --check`：PASS；测试临时 `.tmp` 已清理，未进入工作树。

## 2026-08-11 — FS-041-006..012 Agent / Routing Backend

- `FS-041-006`: PASS；原集中 `internal/agent/agent.go` 已拆分为 manager/provider/session 等职责文件，旧巨型文件退出工作树。
- `FS-041-007`: PASS；Provider Registry 为代码内静态 Codex + Claude Code 注册，不引动态插件系统。
- `FS-041-008`: PASS；CC Switch route/schema/model mapping/effective/sanitize 已独立到 `internal/agent/routing`，数据库保持只读。
- `FS-041-009`: PASS；对当前唯一支持 schema 逐表执行 `PRAGMA table_info` 并计算稳定 fingerprint；不兼容时明确 `available=false / reason=unsupported_schema`。
- `FS-041-010`: PASS；进程内并发安全 bounded TTL：route 1500ms、CLI version/auth 45s、models 20s，无 Redis/daemon。
- `FS-041-011`: PASS；Codex/Claude/CC Switch 的互不依赖 discovery 使用并行 goroutine + WaitGroup，只执行 runtime/auth/model/route 只读发现，不触发真实模型生成。
- `FS-041-012`: PASS；统一错误分类为 `auth_failed/rate_limited/provider_unavailable/network_failed/invalid_model/runtime_unavailable/route_mismatch/unknown`，公开错误使用脱敏文案，不返回 raw upstream error。
- FastSpider_FS 独立执行 `go test ./internal/agent/... ./internal/node ./internal/hub/core ./internal/hub/server`：终态 exitCode=0；Agent、routing、Node、Hub 全部 PASS。
- FastSpider_FS 独立执行 `git diff --check`：PASS；仅有 Git CRLF→LF 提示，无 whitespace error；Agent 测试 `.tmp` 已清理。
- 当前 Windows Go 目标为 386，`-race` 不受支持；未用该可选项替代任何本轮要求的标准测试。

## 2026-08-11 — FS-041-013 AI & Routing UI

- `FS-041-013`: PASS；本地客户端新增一级“AI 与路由”页面，复用共享 AgentManager 只读 discovery，显式 allowlist DTO 展示 Codex / Claude Code / CC Switch 安全状态。
- 页面展示 runtime/version/models/supportedActions/effectiveCapabilities/route、Claude auth configured、CC Switch schema/takeover/model mapping/selectionConsistent/health 等受控字段；不返回 Token/API Key/Cookie/email/orgId/raw settings/meta/完整 endpoint/raw upstream error/敏感绝对路径。
- 页面初始 GET 不触发 Agent action；刷新仅调用只读发现，不调用 `session.create/session.send`，真实模型健康测试明确要求用户从会话手动执行，不后台消耗额度。
- FastSpider_FS 独立执行 `go test ./internal/nodeui ./internal/agent/... ./internal/node`：终态 exitCode=0；独立 `git diff --check`：PASS。

## 2026-08-11 — FS-041-014 Local Diagnostics Center

- `FS-041-014`: PASS；本地客户端新增一级“诊断”页面与 `/api/diagnostics`，展示 Node、Hub、Agent、Task Workspace、本地 Bridge/Browser/Component/Tray 状态和脱敏摘要。
- Working Context 诊断只调用本地 `plan.get`、`markdown.list`；Agent 只调用 `providers.list`、`routing.status`；不执行任何任务写入或模型生成。
- Hub 只暴露解析后的 host；错误只暴露统一 `errorClass + publicMessage`；测试主动注入 Token/device key/email/原始 endpoint/绝对路径并确认响应不泄漏。
- Loopback API 已覆盖 UI token、Origin、HTTP method 门禁；页面初始加载不触发诊断 discovery，切换页面或手动刷新才读取。
- FastSpider_FS 独立执行 `go test ./internal/nodeui ./internal/agent/... ./internal/node`：终态 exitCode=0；独立 `git diff --check`：PASS。

## 2026-08-11 — FS-041-015 0.4.1 Full Test / No Release

- `FS-041-015`: PASS；0.4.1 仅完成连续开发阶段验收，没有 commit、push 或部署。
- FastSpider_FS 独立执行 `go test ./...`：exitCode=0；`go vet ./...`：exitCode=0；`git diff --check`：exitCode=0。
- Windows Git for Windows `scripts/release-gate.sh --full` 首轮 exitCode=0；包含 current-platform/Windows amd64/Linux amd64 build、Hub restore E2E、Local Bridge E2E、Repeated Node、Real Browser、Real CC Switch、Real Claude、Real Codex、Local Bridge multi-provider 与 product smoke。
- 首轮 Gate 暴露 public-source scan 会对“tracked 但当前工作树已删除”的文件调用 grep，产生无害 stderr；已将扫描列表收敛为 `git ls-files` 后仅保留工作树实际存在的文件，不对具体 Agent 文件做特例。
- 修正后的 `scripts/release-gate.sh --full` 再次终态 `PASS: Fast Spider full release gate` / exitCode=0，secret/private marker scan 无 deleted-file 噪音。
- Windows/386 + CGO=0 环境按 Gate 设计跳过随机 fuzz/race；Fuzz seeds 已包含在 `go test ./...`，该平台 skip 不替代其它任何验收项。

## 2026-08-11 — FS-042-001..004 Search Backend

- `FS-042-001`: PASS；`search-ripgrep` 复用现有 Managed Component 生命周期，搜索时只解析 Node data-dir 下当前平台已验证组件版本内 `rg/rg.exe`，不信任 PATH，也不会在每次搜索时自动联网下载。
- `FS-042-002`: PASS；同一 `code.search/search` capability 扩展 `mode=content|files`，返回 `engine/fallbackReason/elapsedMs/truncated`，保持现有匹配 DTO 兼容；MCP 工具总数仍为 16。
- `FS-042-003`: PASS；include/exclude glob 数量与长度 bounded，拒绝 flag/traversal 形态；支持 bounded before/after/context 行并与 limit/truncation 协作。
- `FS-042-004`: PASS；现有 Go 搜索扩展为完整 native fallback，组件缺失/无效/平台不支持/启动失败/命令失败/输出过大或 JSON 无效时安全回退；不返回 raw stderr。
- ripgrep 固定包含 `--json --no-config --color=never --no-heading --line-number --column`，清空 `RIPGREP_CONFIG_PATH`，不使用 `--pre/--search-zip/--follow/--unrestricted`；stdout 8 MiB、JSON 单行 1 MiB bounded。
- FastSpider_FS 独立执行 `go test ./internal/node ./internal/componentmgr ./internal/protocol/v1 ./internal/hub/core ./internal/hub/server`：终态 exitCode=0；独立 `git diff --check`：exitCode=0。

## 2026-08-11 — FS-042-005 file_read 2.0

- `FS-042-005`: PASS；保持同一 `file.read/file_read` 能力和 MCP 工具，保留旧 byte `offset/limit`，新增 `lineStart+lineCount/headLines/tailLines/aroundLine+contextLines/statOnly/includeLineNumbers`。
- Byte range 与行选择器严格互斥；line/head/tail 最大 2000 行、context 最大 1000、正文返回最大 128 KiB；大文件 tail 使用固定容量行起点环 + 流式扫描，around/line range 流式定位，不把整文件载入内存。
- `fileSha256` 始终针对原文件；`chunkSha256` 针对实际返回的 chunk bytes，开启行号时包含渲染行号；`statOnly` 返回原文件 hash/stat 但不序列化 content/chunk。
- 专项测试覆盖旧 byte 兼容、line/head/tail/around/line numbers、statOnly、CRLF/LF、UTF-8 BOM、无末尾换行、UTF-8 chunk boundary、长行/大文件 bounded、参数互斥、binary/invalid UTF-8/non-regular 拒绝。
- FastSpider_FS 独立执行 `go test ./internal/node ./internal/protocol/v1 ./internal/hub/core ./internal/hub/server`：终态 exitCode=0；独立 `git diff --check`：exitCode=0；MCP 工具总数仍为 16。

## 2026-08-11 — FS-042-006..009 file_edit 2.0

- `FS-042-006`: PASS；同一 `file.edit/file_edit` 能力新增 create，要求 `expectedAbsent=true`，目标存在则失败且不覆盖，父目录必须已存在。
- `FS-042-007`: PASS；replace 对原文件 `expectedFileSha256` 做 CAS，oldText 必须在原版本唯一匹配；zero/duplicate/CAS mismatch 都零写入；旧 `edit` 入口映射到同一 replace 内核。
- `FS-042-008`: PASS；editMany 在同一原版本对所有 oldText 做唯一匹配并计算原始 ranges，拒绝重叠；任一失败整个操作不写，成功只生成一次最终 bytes 并执行一次原子替换。
- `FS-042-009`: PASS；preview 通过 `previewOf=create|replace|editMany` 复用同一 planner，绝不写磁盘，返回 before/after SHA、changed、bounded diff；Hub 将 preview 判为 safe-retry 且不进入 mutation audit，其余写 action 不自动重试并纳入审计。
- 文件编辑完全 Go 原生；同目录临时文件写完后 fsync。Windows 使用 `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)` 原子覆盖，create 不带 replace flag；其它平台使用 `os.Rename`/原子 create + parent fsync，不存在先删除目标再 rename 的断电窗口。
- BOM、主换行风格、目标权限、no-op mtime、临时文件清理与 2MiB/64 edits/64KiB diff 等 bounded 规则均有专项测试。
- FastSpider_FS 独立执行 `go test ./internal/protocol/v1 ./internal/hub/core ./internal/hub/server ./internal/node`：终态 exitCode=0；独立 `git diff --check`：exitCode=0；MCP 工具总数仍为 16。

## 2026-08-11 — FS-042-010..011 Component Center / Search & File Diagnostics

- `FS-042-010`: PASS；本地 Edge App Window 新增一级“组件”页，只列 Browser 与 `search-ripgrep`；组件状态 DTO 只返回 id/name/installed/version/platform/status/readiness，ensure 服务端先校验 allowlist，再复用 component manager，Browser 保留配置刷新，ripgrep 不重启 runtime。
- `FS-042-011`: PASS；组件页展示 Managed ripgrep/native、file_read 2.0 与 file_edit 2.0 摘要；自检仅在 NodeUI 临时 data-dir 子目录中通过同一 local capability 执行 code.search、file.read 与 file.write preview，preview bytes 不变且临时目录清理。
- 页面初始加载不自动下载组件或运行自检；loopback token/origin/method guard、unknown component 拒绝、敏感字段 allowlist、native 与 fake managed rg 均有测试。
- FastSpider_FS 独立执行 `go test ./internal/nodeui ./internal/componentmgr ./internal/node`：终态 exitCode=0；独立 `git diff --check`：exitCode=0。

## 2026-08-11 — FS-042-012 Documentation / Version / Gate Entry

- `FS-042-012`: PASS；源码版本切换为 `0.4.2`，README、Node capability、Local Bridge/AI、本地 UI、公共 MCP 与测试策略文档按当前实现同步；MCP 工具总数仍为 16，0.4.1 保持不发布。
- `scripts/release-gate.sh --full` 显式增加 Task Workspace、Managed ripgrep/native、file_read 2.0、file_edit 2.0、updater temp E2E 与 reconnect temp E2E 专项入口；Real Browser/CC Switch/Claude/Codex/Local Bridge 门禁保留。
- 本轮 `go test ./...`、`go vet ./...`、`git diff --check` 与 Git for Windows Bash 语法检查均通过；耗时的 `--full` Gate 留给 FS-042-013 执行。
- 发布物生成链路补验：Hub 组件 release endpoint 已确认通用支持 `release-dir/components/<id>/<platform>/component.zip + version.txt` 并动态签名 manifest；无需为 search-ripgrep 新增服务端接口。
- 新增 `cmd/ripgreppack`：仅接受显式本地非空普通 rg 文件，拒绝 symlink、输出链接和输入/输出别名；ZIP 只包含根目录 `rg`/`rg.exe`。`docs/14-deployment-and-operations.md` 已同步为 0.4.2，明确 Browser + search-ripgrep 发布路径、可信来源与 SHA 校验门禁。
- FastSpider_FS 独立执行 ripgreppack/componentmgr/node tests、ripgreppack vet、Git for Windows Bash `-n` 与 `git diff --check`：全部 exitCode=0；release gate 已纳入 ripgreppack 专项测试。

## 2026-08-11 — FS-042-013 Final 0.4.2 Release Gate

- `FS-042-013`: PASS；Windows Git for Windows 执行 `scripts/release-gate.sh --full` 终态 `PASS: Fast Spider full release gate`，Job exitCode=0。
- Gate 通过：public secret/private scan、module verify/tidy、go vet、go test 全仓、current/Windows amd64/Linux amd64 build、Hub restore、Local Bridge、Task Workspace、Managed ripgrep/native、ripgreppack、file_read 2.0、file_edit 2.0、updater temp E2E、reconnect temp E2E、Repeated Node、Real Browser、Real CC Switch、Real Claude、Real Codex、Local Bridge multi-provider 与 product smoke。
- 当前 Windows/386 + CGO=0 仍按脚本设计跳过随机 fuzz/race；Fuzz seeds 已包含于全仓测试，不替代其它任何 Gate。
- Final Gate 全程未停止或替换正式 PCa Node/生产 Hub，正式 Node 在 Gate 后仍为 0.3.14 online/ready generation 42。

## 2026-08-11 — 0.4.2 Formal Release / Production Reconciliation

- 0.4.2 已由 release commit `4c263b0` 正式发布并完成生产部署；Hub、Node 与 Managed `search-ripgrep` 三边验收均为 PASS。

## 2026-08-11 — FS-043-001..003 Node Update File Lifecycle

- `FS-043-001`: PASS；新增独立 `CleanupConsumedCurrent`，只处理可解析的 `updates/<currentVersion>` 目录；marker 存在、目录不存在、future/unknown/manual 均不删除，旧版本仍由 `CleanupStale` 处理。
- `FS-043-002`: PASS；NodeUI 启动保持 Ready/apply 在先，仅 `(applied=false, err=nil)` 调用 consumed-current cleanup；Ready/apply 错误或已开始替换时不清理 current staging，正式 `.previous` 不在 data-dir 清理范围。
- `FS-043-003`: PASS；源码版本切换到 0.4.3，README、部署/恢复/测试文档和 release gate 已同步；新增 `0.4.3 updater consumed staging gate`，原 Real Browser/CC Switch/Claude/Codex/Local Bridge Gate 保持不变。
- 验证终态：`go test ./internal/nodeupdate ./internal/nodeui`、`go test ./...`、`go vet ./...`、`git diff --check` 与 Git for Windows Bash 语法检查全部 PASS。

## 2026-08-11 — FS-043-004 Final 0.4.3 Release Gate

- `FS-043-004`: PASS；Windows Git for Windows 执行 `scripts/release-gate.sh --full` 终态 `PASS: Fast Spider full release gate`，exitCode=0。
- Gate 明确通过新增 `0.4.3 updater consumed staging gate`，并继续通过全仓 test/vet、Windows/Linux build、Task Workspace、Managed ripgrep/native、file_read/file_edit 2.0、updater/reconnect temp E2E、Repeated Node、Real Browser、Real CC Switch、Real Claude、Real Codex 与 Local Bridge multi-provider/product smoke。
- full gate 未操作正式 0.4.2 Node/Hub/data-dir；正式升级继续复用 0.4.2 已验证的 Hub 独立替换 + Node 内置签名 updater 流程。
<!-- fast-spider:managed:acceptance:end -->

## Manual Acceptance Notes

人工验收补充写在本区；自动同步不得覆盖。
 
