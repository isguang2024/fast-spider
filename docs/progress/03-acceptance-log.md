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

## 2026-08-11 — 0.4.3 Formal Release / Production Verification

- 0.4.3 已由 baseline/release commit `44597ac0` 正式发布部署；生产自更新完成后 `updates` staging 已验证自动归零，当前 EXE 与 `.previous` rollback 状态正常。
- 后续文件审计确认仍存在旧手工安装链路生成的严格命名 backups、legacy temp 与 marker；当前 self-updater 不引用这些文件，0.4.4 以独立 fail-closed 迁移清理处理。

## 2026-08-11 — FS-044-001..003 Windows Legacy Install Cleanup

- `FS-044-001`: PASS；新增 Windows-only legacy install cleanup API，只接受 basename `fast-spider-node.exe` 的绝对当前 executable，并只操作其同级目录。Win32 `FILE_ATTRIBUTE_REPARSE_POINT` 用于 executable、目录与候选项检查；非 Windows no-op。
- `FS-044-002`: PASS；只删除 32-hex GUID legacy temp、普通 marker 与 `backups` 直接子级中严格 UTC timestamp/三段 pre-version 命名的普通 EXE；unknown、嵌套目录、reparse、current 与 `.previous` 保留，空 backups 才删除且重复执行幂等。
- `FS-044-003`: PASS；NodeUI 在 Ready/consumed/stale maintenance 之后、runtime/listener 之前调用一次，错误仅 warning；源码版本更新为 0.4.4，文档与 `0.4.4 legacy install artifacts cleanup gate` 已同步，原 Real E2E Gate 均保留。
- 验证终态：`go test ./internal/nodeupdate ./internal/nodeui`、`go test ./...`、`go vet ./...`、`git diff --check` 与 Git for Windows Bash 语法检查全部 PASS；未运行 full release gate。

## 2026-08-11 — FS-044-004 Final 0.4.4 Release Gate

- `FS-044-004`: PASS；Windows Git for Windows 执行 `scripts/release-gate.sh --full` 终态 `PASS: Fast Spider full release gate`，exitCode=0。
- Gate 明确通过新增 `0.4.4 legacy install artifacts cleanup gate`，并继续通过全仓 test/vet、Windows/Linux build、Task Workspace、Managed ripgrep/native、file_read/file_edit 2.0、0.4.3 consumed staging、updater/reconnect temp E2E、Repeated Node、Real Browser、Real CC Switch、Real Claude、Real Codex 与 Local Bridge multi-provider/product smoke。
- full gate 未操作正式 0.4.3 Node/Hub/data-dir 或真实 legacy artifacts；生产验收留给 0.4.4 自更新后对正式 bin 目录实物检查。

## 2026-08-11 — 0.4.4 Formal Release / Production Verification

- 0.4.4 已由 baseline/release commit `1ec91ac0` 正式发布部署；生产启动自动清理旧手工安装 artifacts 共 48,819,313 bytes，严格保护当前 EXE 与 `.previous`，验收 PASS。
- 发布后增长审计确认标准 release backup 按版本线性累积；历史异名与 Hub binary backups 必须保留，0.4.5 只对标准命名 backup 增加显式 retention。

## 2026-08-11 — FS-045-001..003 Hub Release Backup Retention

- `FS-045-001`: PASS；新增 `PruneReleaseBackups`，root/keep/candidate count bounded；只识别直接标准候选，先检查普通文件/reparse 并对全部执行现有 Verify，再按 manifest CreatedAt UTC 与 basename tie-break 规划删除。任何 planning/verify 错误均零删除，remove 错误返回 bounded kept/deleted 事实与 error。
- `FS-045-002`: PASS；新增 `spiderctl backup-prune --dir <absolute-dir> --keep N`，默认 keep=3，输出 allowlist JSON DTO；空/相对目录拒绝。历史异名、Hub binary backup、未知文件、子目录与 symlink/reparse 不自动处理。
- `FS-045-003`: PASS；源码版本更新为 0.4.5，README、部署/恢复/测试文档与 `0.4.5 release backup prune gate` 已同步，全部既有 Real E2E Gate 保留。
- 验证终态：`go test ./internal/opsbackup ./cmd/spiderctl`、精确 0.4.5 Gate selector、`go test ./...`、`go vet ./...`、`git diff --check` 与 Git for Windows Bash 语法检查全部 PASS；未运行 full release gate。

## 2026-08-11 — FS-045-004 Final 0.4.5 Release Gate

- `FS-045-004`: PASS；Windows Git for Windows 执行 `scripts/release-gate.sh --full` 终态 `PASS: Fast Spider full release gate`，exitCode=0。
- Gate 明确通过新增 `0.4.5 release backup prune gate`，并继续通过全仓 test/vet、Windows/Linux build、Task Workspace、Managed ripgrep/native、file_read/file_edit 2.0、0.4.3 consumed staging、0.4.4 legacy artifacts、updater/reconnect temp E2E、Repeated Node、Real Browser、Real CC Switch、Real Claude、Real Codex 与 Local Bridge multi-provider/product smoke。
- full gate 未操作正式 0.4.4 Hub/Node/backup root；生产轮换只在 0.4.5 新标准备份创建并 Verify、正式升级成功后执行。

## 2026-08-11 — 0.4.5 Formal Release / Production Verification

- 0.4.5 已由 release commit `f5cee7c1` 正式发布部署；Hub、spiderctl、Node 均为 0.4.5，Node 自更新后 `updates` 为空且 `.previous` 精确保留 0.4.4 rollback。
- 新标准升级前 backup 完成 Verify 后，生产 `backup-prune --keep 3` 从 4 个标准候选中只删除最老 0.4.2，保留 0.4.3/0.4.4/0.4.5 三份且逐份 Verify PASS；历史异名与 Hub binary backup 前后 size/SHA 不变。
- 发布后长期增长审计发现本机与服务器累计旧 release staging 共 665,103,310 bytes；确认无进程引用后一次性安全清理，触发 0.4.6 staging lifecycle 收口。

## 2026-08-11 — FS-046-001..003 Release Staging Lifecycle

- `FS-046-001`: PASS；新增 `PruneReleaseStaging`，严格区分 local/server 目录名，只处理 direct child 标准候选与三段 semver，并只规划 version `<= through`。root/candidate/tree reparse、非普通树项、候选/文件/字节/深度 bounds 和删除前身份/内容变化均 fail-closed。
- `FS-046-002`: PASS；新增 `spiderctl staging-prune --dir <absolute> --layout local|server --through <semver> [--apply]`。默认 plan-only，JSON 只返回 basename/version/estimatedBytes/counts；future/unknown/legacy deploy/普通文件保留。
- `FS-046-003`: PASS；源码版本更新为 0.4.6，README、部署/恢复/测试策略与 `0.4.6 release staging prune gate` 已同步；专项覆盖 strict names、plan/apply、local/server、future/unknown、root/candidate/nested reparse、candidate/file/byte/depth limits、TOCTOU、幂等与 partial delete facts。
- 验证终态：`go test ./internal/opsbackup ./cmd/spiderctl -run 'Test(StagingPrune|PruneReleaseStaging)' -count=1`、`go test ./... -count=1`、`go vet ./...`、`git diff --check` 与 Git for Windows `bash -n scripts/release-gate.sh` 全部 PASS。

## 2026-08-11 — FS-046-004 Final 0.4.6 Release Gate

- `FS-046-004`: PASS；Windows Git for Windows 执行 `scripts/release-gate.sh --full` 终态 `PASS: Fast Spider full release gate`，exitCode=0。
- Gate 明确通过新增 `0.4.6 release staging prune gate`，并继续通过全仓 test/vet、Windows/Linux build、Hub restore、Local Bridge、Task Workspace、Managed ripgrep/native、file_read/file_edit 2.0、0.4.3 consumed staging、0.4.4 legacy artifacts、0.4.5 backup prune、updater/reconnect temp E2E、Repeated Node、Real Browser、Real CC Switch、Real Claude、Real Codex 与 Local Bridge multi-provider/product smoke。
- full gate 未操作正式 0.4.5 Hub/Node/data-dir/release-dir/backup root；生产 staging-prune 将在 0.4.6 正式升级成功后先 plan 再 apply。

## 2026-08-11 — 0.4.6 Acceptance Remediation / Final Production Verification

- `FS-046-A01`: PASS；修正 `working_context.goal` MCP Schema 契约说明，明确 `set` 与 `plan.init` 均要求 goal，并在 Hub MCP E2E 中加入 Schema 回归断言。
- `FS-046-A02`: PASS；修正 `plan.sync` Git 快照语义：受管 Markdown 明确写 `dirtyBeforeSync`，调用结果在写入后重新读取实时 `currentGit`，避免受 Git 跟踪 Markdown 对自身 dirty 状态形成自指；新增临时 Git 仓库回归测试。
- `FS-046-A03`: PASS；针对性 `go test ./internal/node ./internal/hub/server`、全仓 `go test ./...`、`go vet ./...`、`git diff --check` 全部通过。
- `FS-046-A04`: PASS；Windows Git for Windows Bash 完整执行 `scripts/release-gate.sh --full`，Job `job_UHILu4nJG-nz8A286pgl4m-pYI5ttDJP` 终态 `PASS: Fast Spider full release gate` / exitCode=0；Real Browser、CC Switch、Claude Code、Codex 与 Local Bridge product smoke 全部通过。
- `FS-046-A05`: PASS；验收修复源码提交 `b72f13ade86b7e147dc86536d6c20b8ca8c73879` 已推送 `origin/main`。
- `FS-046-A06`: PASS；生产 Hub 目标通过旧生产 SHA 精确对账确认后执行事务式替换；升级前 `pre-0.4.6-b72f13a.zip` backup-verify `valid=true`，新 Hub 0.4.6 SHA256=`7dca315e29b0ac699bdb460a1dd18aee11db443b2a2fc9017f94b1dce9498d5b`、spiderctl SHA256=`cf2156ffe24d70a01b9a47421ae16fc5f8d2a0c030a73f4f0df8e9053a5eac9f`，livez/readyz PASS。
- `FS-046-A07`: PASS；同版本 0.4.6 被正常 updater 按设计判定为无需更新，因此采用独立 rollback + 精确 PID + 同卷预置/rename 的人工受控切换。首次构建受本机持久 `GOARCH=386` 影响，验收通过 Machine `arch` 立即识别并拒绝该产物作为最终态；随后显式 `CGO_ENABLED=0 GOOS=windows GOARCH=amd64` 从 `b72f13a` clean VCS 重建并再次切换。最终 PCa Node 0.4.6 / windows-amd64 / generation=57 / SHA256=`617c3e430c3317818641302472ae0873f5ba56384c247923838450bb6667498b` / online+ready，原生产 amd64 rollback SHA256=`148a89c58fc4d02542edf2d4c1e862db1de232bfe49e0f36054a95373240618b` 独立保留。
- `FS-046-A08`: PASS；新生产 Node 对隔离 `round4` 再次执行 `plan.sync`，`00-current-state.md` 已真实输出 `dirtyBeforeSync` 且 completion=100%，返回 `currentGit` 与实时 Git 事实一致。
- **Final Acceptance: PASS / PRODUCTION READY**。当前既有 ChatGPT 会话可能仍缓存热更新前的 `goal` 字段描述；服务端新 Schema 已由源码 E2E + full release gate 验证，刷新连接器/新会话后再核对展示层即可，不作为运行态缺陷。

## 2026-08-15 — 0.4.18 缓存、生命周期与生产发布

- `FS-0418-001..010`: PASS；OAuth/Presentation/Artifact/Release manifest/staging/Node Agent/Job/Browser/component/secretscan 十项审计问题均完成实现、回归测试与门禁接线。Artifact 慢删除与上传解耦、Release hash 支持取消、staging 使用同盘 quarantine、Browser 持续分批清理、组件按语义版本选择，敏感路径/ZIP/Git history/export 均 fail-closed 脱敏。
- `FS-0418-011`: PASS；`go test ./... -count=1` 为 592 passed / 26 packages；`go vet ./...`、`git diff --check`、current/history `secretscan`、脚本语法检查及完整 `scripts/release-gate.sh --full` 均通过。Windows/386 + CGO=0 原生环境无法执行 race，已在同一工作树的 WSL Linux/amd64 + CGO 环境补跑 `go test -race ./...` 并全量通过。
- Git 事实：release commit `a8934c8be0ab2482071dc87fe1f7a81d799c321a` 已推送到 `origin/codex/release-0.4.18`，PR #1 已创建并完成合并前验收。
- 生产 Hub/spiderctl 原子部署：Hub SHA256=`2665b635ef898a3aebb6861ad9e0525ffe5f30110acfe4db5cff18c847334a13`、spiderctl SHA256=`1e2911bc223fb79f830b64b9f12574c55e8d0065c53a1b4ff6021f8b47c1f1b3`，版本均为 0.4.18；systemd active，Hub PID=`1844594`，本机与公网 livez/readyz 均为 200。
- 升级前备份 `pre-0.4.18-a8934c8.zip` SHA256=`2409b522785223e1865d8db6b7c56f8b5ff8608efbda65eab64ba45f959b247e`，Verify `valid=true`、22 files、manifest source version=0.4.17；旧 Hub/spiderctl 版本化回滚副本和 Node 0.4.14 rollback 均保留。
- PCa Node 发布文件与 manifest SHA256=`75cb6b274095ff2dad12bf6a9df5fe8faa1f5ba2a02345532e8722f0da1ed41d`，Node idle-safe 更新后为 0.4.18 / windows-amd64 / generation=89 / active+online+ready。
- 备份清理执行 `backup-prune --keep 3` plan-only：candidate=6、kept=3、planned=3、deleted=0；没有未经确认删除历史备份。资料室 30 天清理仍为 dry-run，未创建定时任务。
- 发布后补充验收：同一工作树在 WSL Linux/amd64 + CGO 环境执行 `go test -race ./...`，全量包通过，exitCode=0；Windows/386 原生 race 限制不再构成未验证项。
- **Final Acceptance: PASS / PRODUCTION READY**。0.4.18 源码、门禁、备份、Hub/spiderctl、Node、健康检查与回滚证据均已闭环。
 
## 2026-08-17 — 0.4.19 分层 MCP 能力发现与生产部署

- 变更范围：`capability_list` 增加 overview/tool summary、底层 capability summary 与 `view=capability` 按需详情；17 个 MCP 工具和 13 个 Node capability 建立静态对账；`shell_run`/`shell.exec` 明确 Windows `powershell.exe`、`pwsh.exe`、`cmd.exe` 调用方式；Node/WSS 协议和 PCa Node 版本不变。
- 验证：核心 release gate、`go vet ./...`、`go test ./... -count=1`（595 passed / 26 packages）、Linux amd64 clean build、MCP Hub 专项回归、`git diff --check` 均 PASS。完整 gate 在既有私有 marker 历史扫描处发现 80 个已知历史命中并按设计停止，未绕过该阻断。
- Git 事实：release commit `8efbdfc624bdfdfbc6eece527a55b8f022cdeacf` 已推送 `origin/main`；构建元数据 `vcs.modified=false`。
- 生产 Hub/spiderctl 已事务式更新为 0.4.19：Hub SHA256=`76ea227ce95898c0a6caf98380bce32c9c23726d5f84d78008b37ff0010b6a6e`、spiderctl SHA256=`160a7bf2fb82996231ba4388fedcaa5cf982e3d7d5290cf32fc959325c1eb751`；systemd active；本机与 shared-services 公网 livez/readyz 均为 200；MCP 未认证 POST 返回预期 401，OAuth discovery 两个端点返回 200。
- 升级前备份 `pre-0.4.19-8efbdfc624bdfdfbc6eece527a55b8f022cdeacf.zip` SHA256=`c33ce40aefa9e15971bf4add6ee6655d0148fd624a668fa0028b5cf95f24a5ae`，Verify `valid=true`；旧 0.4.18 Hub/spiderctl 版本化回滚副本均保留；`backup-prune --keep 3` 仅 plan-only，无删除。
- PCa Node 未构建、未修改、未推送更新；认证后的 MCP `tools/list`、`capability_list(view=overview)` 和 `view=capability` 冷调用需在具备 FS/OAuth 连接器的会话中补验。
- **部署验收：PASS / PRODUCTION HEALTHY；认证 MCP 冷调用：待补验**。
<!-- fast-spider:managed:acceptance:end -->

## Manual Acceptance Notes

人工验收补充写在本区；自动同步不得覆盖。

### 2026-08-13 — 0.4.9 性能/稳定性阶段验收

- 旧 0.4.8 真实 Node 基线：file_edit 单行/50行/editMany/约 500 KiB 文件响应分别 462/1845/1281/486 bytes；大文件 around read P50/P95/max=744/780/780ms；Fast Spider code_search 内部 P50/P95/max=43/59/59ms，Tibbs TypeScript include=127/148/148ms；host shell=1567/1592/1592ms，手工 wsl.exe shell=1521/2880/2880ms；routing.status=986/2238/2238ms，session.list=1000/1128/1128ms，5 次无 Prompt session.create=1040/1138/1138ms 且全部清理；Browser launch/open/snapshot/close 已采集真实旧版基线。
- 实现专项：Node/Agent/NodeUI/Hub/Protocol 定向测试 PASS；真实 Ubuntu-24.04 WSL 使用 V: 盘、空格和中文 cwd 连续 20 次执行，queue P50/P95/max=176/217/230ms、run=107/124/139ms，文件创建/读取/清理和 process-tree cancel PASS。
- 独立审计终态：file/search、WSL/runtime、Browser 网络策略/组件识别均 PASS；Agent create 容量回收、delete 续做与 Claude discovery 三项阻断修复后复验 PASS。
- 最终 `scripts/release-gate.sh --full` Job `j-e6lkxw` exitCode=0，终态 `PASS: Fast Spider full release gate`；覆盖全仓 test/vet、Windows/Linux build、真实 WSL、打包 Browser、真实 CC Switch/Claude/Codex 与 Local Bridge product E2E。当前 Go 为 windows/386 且 CGO=0，因此 race 按 Gate 规则 SKIP；fuzz seeds 已由全仓测试执行。
- 本阶段记录时的剩余项为正式 commit/push、干净构建、生产 backup/deploy、Node self-update、新版同机 benchmark 与 FastSpider_FS 自举验收；这些项目随后均已在 0.4.10 最终验收完成。

### 2026-08-13 — 0.4.10 部署后搜索性能修订

- 0.4.9 Hub/spiderctl/Node 已由 commit `46ef762` 构建并部署，自更新后 PCa 正确协商 file.write/code.search/shell/build/job/Agent/Browser 新能力；升级前 `pre-0.4.9-46ef762.zip` backup Verify PASS，Hub/Node `.previous` 分别保留 0.4.6/0.4.8。
- FastSpider_FS 自举确认 Fast Spider 默认源码搜索继续使用 ripgrep、无 fallback；同时发现 Tibbs 窄静态 include 因显式 override 使用 `--no-ignore` 时仍从仓库根遍历，搜索本体 P50 约 2.4s。宽泛 include 的 `RG_OUTPUT_LIMIT/RG_TIMEOUT` 受控 fallback reason 正确，但窄范围性能未达标。
- 修订将静态 include 目录前缀下推为 managed rg search target；exact file 使用父目录，重叠目标合并，无前缀宽泛 glob 保持根扫描。定向测试、独立搜索审计与第二轮 full release gate Job `j-7ec7b9` 均 PASS。
- 因 Node updater 按版本比较且 release artifact 必须不可变，最终正式版本从 0.4.9 提升到 0.4.10；重新构建、发布、自更新与自举基准结果见下一节，均已完成。

### 2026-08-13 — 0.4.10 最终发布与 FastSpider_FS 自举验收

- release commit `019ade0` 已推送 `origin/main`；最终 full release gate Job `j-u62icj` exitCode=0，终态 `PASS: Fast Spider full release gate`，覆盖全仓 test/vet、Windows/Linux build、Hub restore、Local Bridge、Repeated Node、真实 WSL、打包 Browser、CC Switch、Claude、Codex Product E2E。
- 生产 Hub/spiderctl/PCa Node 均为 0.4.10：Hub SHA256=`8b0f297c896ee3d7cb1dea175f7bd59c4723fcbf683c8499e82567e2836fe7b8`，spiderctl SHA256=`71bd762003c36d14fb28eee0a58a7b93274e8504b6a84938469ff453277fbfbb`，Node SHA256=`d2a2be3e0e65743a56939c768196e001bdb84287383d8dc6f3f0628e7da3e9c9`；公开 livez/readyz PASS，PCa generation=68、online/ready，Browser component=1.62.1。
- 升级前备份 `pre-0.4.10-019ade0.zip` 与既有 `pre-0.4.9-46ef762.zip` 均 Verify PASS；标准备份按 keep=3 保留 0.4.10/0.4.9/0.4.6，Hub、spiderctl、Node `.previous` 精确保留 0.4.9。
- file_edit 新响应为固定元数据：50 行替换由 1845B 降到 576B（-68.8%），20 项 editMany 由 1281B 降到 576B（-55.0%）；约 500KiB 文件响应仍固定约 574B，不随正文增长。
- 最终 code_search：Fast Spider 内部查询 searchElapsed P50/P95/max=33/36/36ms；Tibbs 窄静态 include 从 0.4.9 的 2404/2855/2855ms 降至 0.4.10 的 29/35/35ms（P50 -98.8%），engine=ripgrep、fallback=none。宽泛 include 触发 `RG_OUTPUT_LIMIT`/`RG_TIMEOUT` 时 bounded native fallback 与稳定原因码正确。
- host shell queue P50/P95/max=5/6/6ms、run=9/11/11ms；Ubuntu-24.04 WSL queue=95/118/118ms、run=96/100/100ms，host 与 WSL `go version` 分别报告 windows/386 与 linux/amd64，真实 WSL gate PASS。
- Agent Manager 经 Local Bridge 真实创建 5 个 Codex Session，首个 667ms、后续 350–381ms；相同 idempotencyKey 重放返回 replayed 且 nodeExecutionMs=0。带 Prompt 产品 E2E 完成 create/send/watch/result/delete，终态 `FS_0410_OK`，cancel 专项亦由 full gate 覆盖且独立验收 PASS。
- Browser 真实 DOM 自举不使用截图坐标：launch wall P50/P95/max=936/1968/1968ms，warm type operation=5/17/17ms，warm click=19/25/25ms，snapshot 文本断言 PASS；localhost/RFC1918/WSL/Docker/LAN 可访问，credentialed URL 明确拒绝。
- **Final Acceptance: PASS / PRODUCTION READY**。自举临时 Session、Browser server、验收目录与 release staging 已清理；未触碰用户既有 `internal/nodeui/open_windows.go`、`internal/nodeui/tray_windows_test.go` 改动。

### 2026-08-13 — 最新文档与仓库状态同步

- README、架构/能力/协议/运维/可观测性/测试文档均已核对为 0.4.10；进度受管区块、路线图、决策与开放问题已同步到最终生产事实。
- 此前保留的两个 Node UI 窗口尺寸改动经确认与 0.4.10 无关后已撤销；同步开始前 `main` 与 `origin/main` 一致且工作树 clean。
- 当前策略为稳定使用：没有主动迭代项；仅在真实运行问题、明确性能证据或新需求出现时创建下一计划。

### 2026-08-14 — 0.4.16 分层能力指南与 MCP 调用诊断（发布前）

- 实现完成：17 个既有 MCP 工具共享单一指南事实源；initialize instructions 保持小型，`capability_list` 支持 overview/catalog/tool/workflow/error 按需读取并保持旧机器能力查询兼容。
- 实现完成：SDK receiving middleware 在 Hub 内存中按 owner 保存最多 64 条脱敏 MCP 事件；Web session API 与后台诊断面板只在首次加载或人工刷新时读取。
- 专项回归 `go test ./internal/hub/server -count=1` PASS，共 45 项测试；覆盖冷客户端分层读取、指南大小预算、17 工具完整性、稳定错误分类、ring 上限、owner 隔离、序列化 allowlist、Web 登录 401 与敏感标记不泄漏。
- 发布前生产基线：Hub 0.4.15（SHA256 `1824cc9f7ebb9b4ebe9fc9e5fbbcc75a049747509355849d7ccaa9ce3acc68b1`）与 spiderctl 0.4.15（SHA256 `48207c5e520d708a5851f00c9a8573d73acd4de3150444709faa45c11214ff0d`）；livez/readyz 均为 200。PCa 注册版本 0.4.14；本版本明确不构建、不修改、不部署 Node、version.txt 或 push.json。
- 当前尚未宣告最终 PASS：完整 release gate、release commit/push、标准验证备份、Hub/spiderctl 原子部署及真实 MCP cold-session smoke 仍待执行。
- 最终完整门禁 Job `j-h6x1dh` exitCode=0，终态 `PASS: Fast Spider full release gate`；覆盖 secret/private marker、gofmt、mod verify/tidy、vet、全仓测试、current/Windows amd64/Linux amd64 build、恢复/Local Bridge、全部历史专项门禁、重复 Node、真实 WSL、打包 Browser、CC Switch、Claude、Codex 与 Local Bridge→Codex 产品 E2E。当前 windows/386、CGO=0，因此 fuzz/race 按既有 Gate 规则 SKIP，fuzz seeds 已由全仓测试执行。
- 首次生产 smoke 发现 Hub 重启后的既有 MCP 客户端可直接成功调用工具，但进程内没有重启前 initialize/tools-list 事件；诊断原先按“缺失早期事件优先”会误报未连接。修订为“最新已观测阶段优先”：真实 Tool Call 成功/失败是最高可信证据，其次 tools/list、initialize；专项测试同步覆盖该重启/中途接管边界。
- 修订后第二轮完整门禁 Job `j-g2x2ht` exitCode=0，终态再次为 `PASS: Fast Spider full release gate`；最终发布证据以本轮为准。
- 真实 OAuth+PKCE 冷客户端进一步确认 stateless MCP 的 initialize 能识别客户端，而后续独立 HTTP 请求可能只有通用 User-Agent。诊断增加 5 分钟短握手归因窗口：同 owner 的后续 tools/list/tools/call 继承最近明确识别的 chatgpt/codex/mcpcli，窗口外仍归一为 other；不保存 sessionId、原始 User-Agent 或其它新字段。
- 客户端归因修订后完整门禁 Job `j-aarg07` exitCode=0，终态 `PASS: Fast Spider full release gate`；全仓测试现为 445 项，Hub 专项 45 项，真实 WSL/Browser/CC Switch/Claude/Codex/Local Bridge 产品链均通过。最终发布证据以本轮为准。
- 冷 E2E 强断言进一步定位 SDK 类型边界：Receiving Middleware 收到的是 `ServerRequest[*InitializeParams]`，不是客户端侧 `InitializeRequest` 别名。实现改为只通过公开 `Request.GetParams()` 读取结构化 InitializeParams/ClientInfo，不解析 raw body；真实 cold E2E 将 mcpcli 归因设为发布阻断断言。
- SDK 请求类型修订后最终完整门禁 Job `j-ofkr5w` exitCode=0，终态 `PASS: Fast Spider full release gate`；45 项 Hub 测试、445 项全仓测试以及全部跨平台构建与真实产品 E2E 通过。最终发布证据以本轮为准。

### 2026-08-14 — 0.4.16 最终发布与生产验收

- 最终源码 release commit `c93373e2ce2d1b7138be8f0f613f4713955509fb` 已推送；其 clean VCS 构建通过 Job `j-ofkr5w` 完整门禁，`vcs.modified=false`，覆盖 45 项 Hub 测试、445 项全仓测试、跨平台构建与全部真实产品 E2E。
- 生产 Hub/spiderctl 已事务式更新到 0.4.16：Hub SHA256=`30437a500f398503000f37c70cec05782058d744e93c8c2706301b18d595423e`，spiderctl SHA256=`7dba50690e53d8cbf8881be9825bfdffc49cf8e3af7f194ce5f36b2efe7cb078`；Hub PID=1113954、systemd active/running，本机及公开 livez/readyz 均为 200。`.previous` 分别保留 SHA256=`f63ec91689772fe25298f7d34a6b78399a5e02d57e20e2b0ce7bf831ab23e1d4` 与 `bc3b5c8fc442faaa8a869b670ae1d022bdbf928abbcc497299b4d93793b683e6`。
- 标准备份 `pre-0.4.16-c93373e.zip`（SHA256=`9de87273841376645e1133981b4b21c335ff5bf99b626df4007249faa8519b99`）Verify PASS、17 files、format v1；真正升级前备份 `pre-0.4.16-cecf3fe.zip`（manifest 0.4.15，SHA256=`65be79af93933db6053063f8e3c8a41d2c4db4937fa946cb982edf9fa7067d33`）同样 Verify PASS。中间修订备份有意保留，未执行可能删除真正升级前证据的轮换。
- PCa Node 仍为 0.4.14 windows/amd64 且 online/ready；Node release、version.txt 与 push.json 均未修改或部署，push.json 仍不存在，符合本版本边界。
- 真实 OAuth+PKCE 冷客户端验收通过：initialize 返回 FastSpider_FS 0.4.16（instructions 1308 bytes），tools/list=17，`capability_list(view=overview)` 返回 Guide 1.0，machine_list 返回 PCa online/ready，`ai_control(session.list)` 成功。Dashboard 最终显示 client=mcpcli、last tool=ai_control、result=success、diagnosis=`Tool Call 成功`。
- 冷验收临时 OAuth client、authorization、access/refresh token 均已撤销并精确清理为 0 残留；远端 release staging 已清理。当前已连接会话仍缓存旧 MCP schema，因此只剩外部人工动作：在 ChatGPT App 管理中 Refresh FastSpider_FS，并于新会话执行 connection-check。
- **Final Acceptance: PASS / PRODUCTION READY**。服务端发布、回滚链路、隐私清理与生产 MCP 冷调用全部通过；Refresh 不是服务端发布阻断项。

### 2026-08-14 — 0.4.17 ChatGPT 长会话 MCP 稳定性修复与生产发布

- 真实故障证据来自生产 Nginx：OpenAI `/fast-spider/mcp` 正常连续调用后出现约 24 分钟无请求窗口，而同一授权的新会话随后立即恢复 200；用户未重新登录或授权。故障后 `/oauth/token` 自动 200 并继续 MCP 200，因此主根因定位为 ChatGPT 会话级工具物化/发现状态未发起请求，而不是 Hub、PCa Node 或 OAuth refresh 失效。
- 修复不删除既有 17 个能力。ChatGPT 命名空间缺失时使用唯一 Tool Search 标记 `fsprobe` 只物化 `machine_list`，真实连通后再按当前动作加载其它工具；initialize instructions 保持 <=2 KiB，完整工具目录 <=48 KiB、连接三工具 <=8 KiB、任一单工具 <=8 KiB，防止后续 MCP 功能增长重新放大长会话工具上下文压力。
- Hub 诊断新增 `lastMcpRequestAt`：Bearer Token 验证成功即更新 Owner 的最近 MCP HTTP 到达时间，但不会伪造 initialize/tools/list/tools/call 事件，也不记录 Token、arguments、Prompt、路径或原始 User-Agent。后台增加“最近 MCP 请求”，可直接区分“请求已到 Hub”和“ChatGPT 会话根本未发请求”。
- 最终源码 release commit `f2c2b14635575ed4459c5a5bf2db3295d11541c0` 已推送 `origin/main`。正式 Windows Git Bash full release gate Job `job_OQB48opU06AxylxMZ6DdiPCEGIy_6n68` exitCode=0，终态 `PASS: Fast Spider full release gate`；覆盖全仓测试/静态检查、Windows/Linux build、真实 WSL、打包 Browser、CC Switch、Claude Code、Local Bridge 与 Codex Product E2E。最终暂存版 0.4.17 专项测试随后再次 PASS。
- 生产升级前创建 `pre-0.4.17-f2c2b14.zip`，SHA256=`f0558585cc47baa19233f42eb2dab435aecd81cff95d095a6dc2927f2ecbcd78`，Verify valid=true、18 files、format v1，manifest source version=0.4.16。
- 生产 Hub/spiderctl 已事务式更新至 0.4.17：Hub SHA256=`84df61c5860847cf755c463741e2bc1f4e61141bc6f5f5bea390da12bc0978da`，spiderctl SHA256=`a35b47d76b97f8ce8fbbafc118a917d0716f1e2e94d9408ce94f8123e49d1ce5`，Hub PID=`1157989`，systemd active，本机与公网 livez/readyz 全 PASS。`.previous` 精确保留 0.4.16 两个生产哈希；远端 staging 已清理。
- PCa Node 保持 0.4.14 windows/amd64，generation=82、online/ready；0.4.17 未构建/部署 Node，也未修改 Node release、version.txt 或 push.json。
- 发布后 ChatGPT App Refresh 最终宿主门禁已通过：`api_tool.list_resources(paths=["FastSpider_FS"], query="fsprobe")` 精确只返回 `machine_list`；真实 `machine_list` 返回 PCa online/ready、Node 0.4.14、generation=82。随后按需发现并调用 `capability_list`，connection-check 返回生产 ServerVersion=0.4.17；再以 `revisioned` 过滤发现精确只加载 `working_context`。整个复验在当前会话完成，无需新会话、无需重新登录或 OAuth 授权。
- **Final Acceptance: PASS / PRODUCTION READY**。0.4.17 的服务端发布、回滚/备份、MCP 复杂度预算、请求到达诊断、Refresh 后 `fsprobe` 单工具发现及同会话按需恢复均已验收；当前无剩余内部或宿主侧发布阻断项。
