# Fast Spider Change Log

<!-- fast-spider:managed:change-log:start -->
## 2026-08-11

- 恢复 PCa、Working Context、Git 和关键文档事实，确认继续基线为 `main@45303385e7b45e7b0171746cd85b88197bfcf567` 的 dirty 0.4.0 工作树。
- 单独对账并保留后台首页“下载最新版 Windows 客户端”并行 UI 改动。
- 新建 `docs/progress` Task Workspace 六文件结构。
- 建立 `fast-spider-0.4.0-to-0.4.2` 路线图、决策、验收日志和开放问题记录。
- 明确 0.4.1 不单独发布，连续开发至 0.4.2 后再执行正式 Hub/Node 更新。
- 0.4.0 full release gate PASS 后形成 baseline commit `0de4f6286942ccd1a7432651292ea7c8069cd33e`，并成功推送 `origin/main`；随后正式进入 0.4.1→0.4.2 连续开发。
- 完成 FS-041-001..004：Working Context Plan/Task、Markdown workspace 隔离与 Windows junction 防护、managed block/CAS/原子写、九个 plan/task/markdown/progress actions；保持 MCP 工具总数 16，相关四包测试与 diff check PASS。
- 完成 FS-041-005：本地客户端新增“任务与进度”一级页面和受约束 loopback API，复用 Node Plan 状态；Windows 路径测试改为文件身份比较后独立复测 PASS。
- 完成 FS-041-006..012：Agent Manager/Provider/Session 职责拆分，Codex+Claude 静态 Registry，CC Switch 独立只读 routing + schema fingerprint fail-closed，route/CLI/models 短 TTL、并行 discovery 与八类统一错误分类全部落地；独立 Agent/Node/Hub 回归与 diff check PASS。
- 完成 FS-041-013：本地客户端新增“AI 与路由”一级页面，使用显式 allowlist DTO 展示 Codex/Claude/CC Switch 脱敏事实；页面加载与刷新均不发起模型生成，独立回归 PASS。
- 完成 FS-041-014：本地客户端新增“诊断”一级页面和只读 `/api/diagnostics`，整合 Node/Hub/Agent/Task Workspace/本地能力脱敏状态与统一错误摘要；安全门禁和独立回归 PASS。
- 完成 FS-041-015：全仓 test/vet/diff check、跨平台 build 与完整 Real E2E Gate 两轮 PASS；0.4.1 不发布，继续同一 dirty 工作树进入 0.4.2。
- 修正 release gate public-source 扫描：过滤工作树中已不存在的 tracked 文件，避免重构删除文件产生 grep 噪音；修正后 full gate clean PASS。
- 完成 FS-042-001..004：接入 Managed `search-ripgrep` 解析与安全执行，`code.search` 扩展 content/files、glob、context、engine/fallback/elapsed，保留完整 native Go fallback；协议/MCP 与独立五包回归 PASS。
- 完成 FS-042-005：`file.read` 扩展 byte/line/head/tail/around/stat/行号，保持原文件与返回 chunk hash 语义清晰，大文件流式定位且正文 bounded；协议/MCP 与独立四包回归 PASS。
- 完成 FS-042-006..009：`file.edit` 收敛为单一 Go 原生 create/replace/editMany/preview 内核，CAS/唯一匹配/批量全有或全无/原子替换/BOM换行权限/preview no-write/bounded diff 全部落地；Hub safe-retry/mutation audit 按 action 区分，独立四包回归 PASS。
- 完成 FS-042-010..011：本地客户端“组件”页只管理 Browser/search-ripgrep，并提供脱敏组件状态和手动隔离搜索/文件自检；native/fake managed rg、file_read 2.0、file_edit preview、guard 与临时目录清理测试 PASS。
- 完成 FS-042-012：源码版本更新为 0.4.2，全仓 Current 文档按真实 capability/UI/Agent/安全边界同步；full release gate 新增六组可读的 0.4.2 专项入口，下一任务切换到 FS-042-013。
- 完成 FS-042-013：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，覆盖全仓测试、跨平台构建、0.4.2 专项能力、更新重连与 Real Browser/CC Switch/Claude/Codex/Local Bridge；进入唯一 0.4.2 release commit + push。
- 0.4.2 已由 release commit `4c263b0` 正式发布部署，Hub、Node 与 Managed search-ripgrep 三边生产验收 PASS。
- 完成 FS-043-001..003：新增独立 consumed-current staging cleanup，严格保持 Ready/apply→cleanup 顺序和 marker/error fail-safe；版本更新为 0.4.3，文档、专项 Gate、全仓 test/vet/diff/bash syntax 全部 PASS。
- 0.4.3 已正式发布部署，生产自更新后的 `updates` staging 验证归零；后续审计将旧手工安装遗留物收敛为 0.4.4 小版本任务。
- 完成 FS-044-001..003：新增 Windows-only legacy install artifacts cleanup，以 Win32 reparse attribute、严格文件名、同级/非递归范围保护 current/previous 和未知项；NodeUI 安全启动时机接线、0.4.4 版本/文档/专项 Gate 与全仓验证均 PASS。
- 完成 FS-044-004：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，新增 legacy install artifacts cleanup 专项门禁与全部既有 Real E2E 均通过；无人值守进入 0.4.4 release commit/push 与生产迁移验收。
- 0.4.4 已正式发布部署，生产自动清理 Windows legacy install artifacts 48,819,313 bytes 并保护 current/previous；后续审计将标准 release backup 线性增长收敛为 0.4.5 任务。
- 完成 FS-045-001..003：新增全候选 Verify 后按 CreatedAt UTC retention 的 release backup prune API 与 `spiderctl backup-prune`；严格命名/root/reparse/bounds/零删除失败边界、部分删除事实 DTO、0.4.5 版本/文档/专项 Gate 和全仓验证全部 PASS。
- 完成 FS-045-004：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，新增 release backup prune 专项门禁与全部既有 Real E2E 均通过；无人值守进入 0.4.5 release commit/push、生产升级与 keep=3 轮换验收。
- 0.4.5 已由 release commit `f5cee7c1` 正式发布部署；标准 release backup 从 4 份安全轮换到最近 3 份且逐份 Verify PASS，历史异名/Hub binary backup 保持原 SHA/size；Node/Hub/spiderctl 与组件/搜索文件自检均正常。
- 发布后审计确认 release staging 是剩余明确线性增长点；无进程引用后一次性清理本机/服务器旧 staging 共 665,103,310 bytes。
- 完成 FS-046-001..003：新增 bounded/fail-closed `PruneReleaseStaging` 与 `spiderctl staging-prune` plan/apply CLI；严格 local/server 名称、future/unknown 保留、reparse/limits/TOCTOU/幂等/partial facts 测试、0.4.6 版本/文档/专项 Gate 与全仓 test/vet/diff/bash syntax 全部 PASS。
- 完成 FS-046-004：最终 `scripts/release-gate.sh --full` 终态 PASS / exitCode=0，新增 release staging prune 专项门禁与全部既有 Real E2E 均通过；无人值守进入 0.4.6 release commit/push 与生产 staging lifecycle 验收。
- 0.4.6 正式验收收口：修正 `working_context.goal` Schema 契约描述与 `plan.sync` pre/post Git snapshot 语义，补自动回归；验收修复源码 `b72f13a` 已 push，Windows Git for Windows full release gate 全绿。
- 生产 Hub/spiderctl 已同版本事务式更新并通过验证备份、livez/readyz；PCa Node 因 updater 正确拒绝同版本更新，采用受控原子替换。验收中识别并淘汰一次受持久 `GOARCH=386` 影响的错误构建，最终以显式 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` clean VCS 产物替换，PCa generation=57、online/ready、最终 SHA 对账 PASS。
- Fast Spider 0.4.6 最终验收状态更新为 `FINAL PASS / PRODUCTION READY`；后续仅在出现新的真实生产缺口时开启下一版本。

## 2026-08-13

- 完成 0.4.7/0.4.8 Browser Agent refs/batch、网络策略组件协议和 Codex runtime 收敛。
- 完成 0.4.9 file_edit 响应瘦身、file_read 单次扫描、code_search 2.1、host/WSL runtime、Agent/Browser readiness 与轻量 timing，并完成首轮正式发布。
- 生产自举发现静态 include 根遍历后按不可变发布规则提升到 0.4.10；前缀下推、独立审计、最终 full gate、Hub/Node/spiderctl 部署、自更新、备份轮换与 FastSpider_FS 自举全部 PASS。
- 0.4.10 进入稳定使用阶段；无主动迭代项，仅在真实问题或明确需求出现时继续。

## 2026-08-14

- 完成 0.4.16 分层能力指南与 MCP 调用可观测性：17 个工具共享 Guide 1.0 单一事实源，owner 级 64 条脱敏诊断、Web 面板和冷客户端阻断测试落地。
- 最终源码 commit `c93373e2ce2d1b7138be8f0f613f4713955509fb` 的 full release gate Job `j-ofkr5w` 全绿；生产 Hub/spiderctl 已升级至 0.4.16，验证备份、回滚产物、公开健康检查、OAuth+PKCE 冷 MCP 与 Dashboard 诊断均 PASS。
- PCa Node 保持 0.4.14 online/ready，未修改 Node release、version.txt 或 push.json；最终状态为 `PASS / PRODUCTION READY`，仅剩 ChatGPT 管理页 Refresh 与新会话 connection-check。
- 完成 0.4.18 十项缓存、生命周期、发布安全与资料清理审计修复；增加 Browser 持续孤儿清理、组件语义版本选择、Artifact/Release/staging 生命周期保护、Node/Job 代际关闭保护和 secretscan 全表面脱敏。
- release commit `a8934c8be0ab2482071dc87fe1f7a81d799c321a` 已推送；`go test ./... -count=1` 592 passed / 26 packages，`scripts/release-gate.sh --full`、secret scan、vet、bash syntax、diff check 全部 PASS。
- 0.4.18 已完成生产部署：Hub/spiderctl 原子升级、备份 Verify、PCa Node idle-safe 更新至 generation=89，公网/本机健康检查均 200，版本化回滚副本保留。
- `backup-prune --keep 3` 与资料室 30 天清理均按安全策略只执行 plan/dry-run，实际删除为 0；资料室定时清理可由 Codex Automations 触发 dry-run，team-workspace 本身不内置调度器。
- 发布后在 WSL Linux/amd64 + CGO 环境补跑 `go test -race ./...` 全量通过，原 Windows/386 race 环境限制已转为已验证事实。
<!-- fast-spider:managed:change-log:end -->

## Manual Change Notes

人工补充变更写在本区；自动同步不得覆盖。

### 2026-08-12 — Windows WSL 常驻复用

- 修复 Windows Node 通过短生命周期 `wsl.exe` Job 使用本地 WSL 时，Job 结束后发行版随空闲自动停止、导致 Docker/Gateway/Redis 下一次命令重新启动的问题。
- Node 仅在首次真实 WSL 工作命令时按发行版启动一个轻量 keepalive；后续 Shell/Build 复用同一 WSL VM。管理命令不创建 keepalive，显式 shutdown/terminate 后下一次工作命令可重新按需启动。
- 专项 Windows Node tests、全仓 `go test ./... -count=1`、`go vet ./...`、Windows Node build 与 `git diff --check` 均通过；后续 0.4.10 真实 WSL runtime、keepalive/cancel 与 full gate 验收也已通过，本项关闭。

### 2026-08-13 — 0.4.9 性能与稳定性

- file_edit 2.1 将 mutation 响应收敛为固定元数据，preview 才返回 bounded hunk；保留 SHA CAS/原子替换并增加同路径并发串行化。
- file_read 将 UTF-8 校验、原文件 SHA 与 byte/line/head/tail/around/stat 选择合并为一次有界扫描。
- code_search 2.1 修复不兼容 glob 导致的 managed rg exit 2，新增稳定 RG reason code、VCS ignore/显式 include 语义、静态 include 前缀 target 下推、扫描/跳过/不完整统计与 primary/fallback timing。
- Shell/Build/Job 1.1 正式支持 host/WSL runtime、Windows cwd 安全映射、bounded WSL keepalive、requestId/traceId 与 queue/run timing。
- Agent 1.1 新增分层安全 readiness、Codex/Claude 持久 session.create 幂等、无 Session 的 in-doubt 显式对账释放与可续做 delete intent；Browser 1.2 新增缓存 readiness、稳定原因与 startup/operation/queue/total timing。
- Hub/Node 所有能力响应新增紧凑通用 timing；NodeUI 诊断页增加 Agent/Browser readiness 与 WSL 可用性，仍不自动发送 Prompt。

### 2026-08-13 — 0.4.10 静态 include 搜索范围收敛

- 0.4.9 生产自举识别到大型仓库窄静态 include 的根目录遍历成本；managed rg 现下推静态目录前缀为 search target，宽泛 include 与显式 override 语义保持不变。
- 版本提升为 0.4.10，避免覆盖同版本 Node release artifact，确保 updater 可验证获取修订构建并保留 0.4.9 rollback。

### 2026-08-13 — 0.4.10 正式发布

- `019ade0` 已推送并通过最终 full release gate；Hub、spiderctl、PCa Node 已升级至 0.4.10，公开健康检查、Node online/ready、自更新与回滚链路均通过。
- FastSpider_FS 完成文件、搜索、host/WSL、Agent 生命周期和 Browser DOM 真实自举；窄静态 include 搜索 P50 相对 0.4.9 部署态下降 98.8%，最终验收为 `PASS / PRODUCTION READY`。

### 2026-08-13 — 0.4.11 Artifact/MCP 原生回显

- `artifact_get` 的 uploadFile/uploadJobLog/get 优先返回有界原生 MCP 图片、文本或二进制资源；空 Artifact 保留结构化元数据且不生成 malformed `EmbeddedResource`。
- browser/screenshot 不再暴露 `publicUrl` 或 `ResourceLink`；只有调用方显式执行 `publishFile` 时才创建短期分享链接。

### 2026-08-14 — 0.4.12 Thinking Team

- 新增只读 `thinking_team` MCP Tool，由当前调用 Fast Spider 的 ChatGPT/LLM 使用 9 个部门、17 个角色、独立角色指令和 direct/parallel/review/full 流程进行多视角思考；`providerInvocation=false`，不会创建 Codex、Claude Code 或其它本机 AI Session。
- 新增 `department.get`、`role.get`、`workspace.get` 等按需读取入口，角色职责和部门数量有专项回归保护。
- 复杂多角色任务复用 `working_context`，每个任务使用独立 `planId` 与 `.local/fast-spider/collaboration/<task-id>` Markdown 资料室；主控统一维护简报、已读证据、发现、决策、交接和验证，避免重复读取和旧证据复用。
- 新增 `docs/22-thinking-team.md`，明确 Thinking Team 与 `ai_control` 的边界、调用方式和 ChatGPT MCP Schema 刷新要求。

### 2026-08-14 — 0.4.13 Thinking Team Workspace 收敛

- 0.4.12 生产自举发现 `thinking_team.workspace.files` 使用了第二套 Markdown 文件名，而 `working_context markdown.append` 只对已存在文件执行 CAS 写入；因此原协议无法直接用现有 Working Context 初始化器完整落地。
- 0.4.13 将协作资料室收敛到 Working Context 标准六文件，要求 `plan.init initializeMarkdown=true`，并明确简报/Read Evidence、计划交接、决策、验证、发现风险、变更历史的文件映射。
- 写入协议固定为 `markdown.read → fileRevision → markdown.append(expectedFileRevision)`；真实 `thinking-team-v1` 资料室已完成 CAS append 自举验收。
- 继续保持 9 部门、17 角色、调用侧主控和 `providerInvocation=false`，不新增第二套 AI Provider 或任务数据库。

### 2026-08-14 — 0.4.14 Idle-safe Node Update Push

- 新增 `spiderctl node-update-push`；发布者在 Node artifact + `version.txt` 已部署并校验后显式生成原子 `push.json`，Hub 通过既有 WSS heartbeat ACK 通知旧版在线 Node，不增加新 daemon、消息队列或 MCP Tool。
- Node 收到 release notice 后继续走签名 manifest + SHA 校验并可预下载 Ready 包；Shell/Build Job、Browser Session、AI 活跃 Turn 或 in-flight Capability 任一存在时绝不重启，并通过 heartbeat 上报真实 `busy`。
- 全部活动结束并连续空闲 15 秒后进入 release drain；drain 不取消已有任务，只拒绝新 Capability 并返回可重试 `NODE_UPDATING`，随后复用既有 `Ready → StartApply → .previous → restart` 自更新链。
- `push.json` 是几百字节非敏感发布元数据；release SHA 只在发起 push 时计算一次，Hub heartbeat 只读取 marker，避免周期性读取大型 Node 二进制。

### 2026-08-14 — 0.4.15 MCP Invocation Routing

- MCP initialize 新增 Server Instructions，明确 `@FastSpider_FS` 被选择/提及时优先真实调用只读工具验证，不再仅凭界面文本推断连接器不可用。
- Server Title 对齐为 `FastSpider_FS`；`capability_list` 与 `machine_list` 明确作为连接/机器发现入口，`ai_control` 明确标注 Codex `action=session.list` 及后续 session.get/watch/result 路由。
- `TestMachineBoundaryEndToEnd` 新增 initialize title/instructions 断言，full release gate 新增 0.4.15 MCP invocation routing gate；工具总数与 Node/WSS 协议保持不变。

### 2026-08-14 — 0.4.16 分层能力指南与 MCP 调用可观测性

- initialize instructions 收敛为 2 KiB 内的九类能力地图与安全规则；17 个工具的 description 与详细指南统一由代码内事实源生成。
- `capability_list` 新增 overview/catalog/tool/workflow/error 分层查询，覆盖文件编辑、长任务、Git、Browser、Codex Session 等安全流程和真实错误指导，并保持旧 `machineId` 查询兼容。
- Hub 新增每 owner 64 条、仅内存的 MCP 调用诊断快照；只记录方法、工具、归一化客户端、结果、稳定错误码、版本和时间戳，不记录输入正文、Token、路径或 raw error。
- Web 后台新增登录保护的“MCP 调用诊断”面板，仅首次加载与人工刷新读取；专项测试覆盖冷客户端、大小预算、ring 上限、owner 隔离、隐私 allowlist、错误分类与未登录 401。
- 生产 smoke 修正重启后既有 MCP 会话只出现 Tool Call 时的诊断优先级：以已观测到的最高阶段为准，不因缺失重启前 initialize/tools-list 事件误报“未连接”。
- stateless MCP 后续请求缺少 clientInfo 时，在同 owner 的 5 分钟握手窗口内沿用最近明确识别的客户端类型；窗口外继续显示 other，不记录原始 User-Agent 或会话标识。
- Receiving Middleware 使用 SDK `Request.GetParams()` 识别服务端 InitializeParams，修正误用客户端侧 InitializeRequest 别名导致的归一化失效；冷 MCP E2E 强制断言 mcpcli 归因。

### 2026-08-14 — 0.4.17 ChatGPT 长会话 MCP 恢复

- 生产日志确认一次真实故障窗口：旧 ChatGPT 会话停止向 `/mcp` 发请求，而同一授权的新会话可立即继续；OAuth refresh 同期自动成功，因此将主问题从 Hub/Node/授权故障收敛为会话级工具物化/发现失效。
- 新增唯一发现标记 `fsprobe`，用于 ChatGPT 命名空间缺失时只加载 `machine_list`；确认真实连接后再按当前动作加载其它工具。17 个顶层能力保持不变，不以删功能解决复杂度。
- 增加工具上下文发布预算：initialize <=2 KiB、完整工具目录 <=48 KiB、连接三工具 <=8 KiB、任一单工具 <=8 KiB；首次实现曾因 initialize 达 2076 bytes 被门禁拒绝，最终通过压缩常驻指令而不是放宽预算。
- MCP 诊断增加 `lastMcpRequestAt`，在 OAuth Bearer 验证成功后更新请求到达时间，但不制造虚假 method event；后台可直接识别“ChatGPT 此会话未向 Hub 发请求”。
- release commit `f2c2b14635575ed4459c5a5bf2db3295d11541c0` 已推送，full release gate 全绿；生产 Hub/spiderctl 已部署 0.4.17，PCa Node 保持 0.4.14。发布后 ChatGPT App Refresh 复验已完成：`fsprobe` 精确只物化 `machine_list`，当前会话可继续按需恢复其它工具，无需新会话、重新登录或 OAuth 授权；0.4.17 最终状态为 `FINAL PASS / PRODUCTION READY`。

### 2026-08-15 — 0.4.18 缓存、生命周期与发布安全（实现阶段）

- OAuth 注册回收只删除没有任何授权/Access Token/Refresh Token 的 Client；撤销授权保留至 90 天历史清理，Owner 删除共享 Client 只解绑自己的授权与 Token。
- Presentation 根目录和过期文件删除失败会保持不可用/追踪状态等待维护重试；Artifact 维护领取批次与慢磁盘删除解耦，Blob 引用在删除前复核，`.part` 崩溃残留进入持久队列。
- Release manifest singleflight 支持独立取消与共享哈希取消；staging prune 采用同盘原子 quarantine + 身份/内容复核；Codex/Job/Node 关闭路径加入代际和 starting reservation 保护。
- Browser 孤儿目录每分钟持续分批清理；组件选择按语义版本；secretscan 覆盖文件名、ZIP entry、Git index/history/export 并统一 locator 脱敏。
- 本条待补充 full gate、release commit、推送、备份哈希、生产 Hub/spiderctl/Node 版本与健康验收事实。

### 2026-08-17 — 0.4.19 分层 MCP 能力发现与 Hub 发布

- `capability_list` 现在先返回完整但紧凑的工具摘要和底层 capability 摘要，需要时再按 capability/tool/workflow/error 读取单项细节；静态测试防止工具、能力和映射漏项。
- `shell_run` 的指南和 Server Instructions 明确 Windows 通过显式 argv 调用 PowerShell/cmd，不再让调用方误以为存在独立 PowerShell 工具。
- 0.4.19 Hub/spiderctl 已完成备份、版本化 staging、事务式替换和本机/公网健康验收；PCa Node 保持 0.4.18，认证 MCP 冷调用留待具备 FS/OAuth 入口的会话补验。
