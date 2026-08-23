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

### 2026-08-17 — 0.4.20 Direct Access Key 与共享 Tool Executor

- 新增独立 `Direct Access Key / 临时直连密钥`：`fsp_tmp_` 明文仅创建时显示，Hub 只持久化哈希/提示；支持过期、最近使用、撤销/删除、每分钟限速、Machine 绑定。默认只读，高危能力按 `files.write`、`shell`、`jobs`、`git`、`browser`、`ai`、`context.write`、`artifacts.write` 独立授权；高权限最长 24 小时、只读最长 7 天。
- 新增 `GET /direct/v1/tools` 与 `POST /direct/v1/call`；生产 PublicBaseURL 下对应 `/fast-spider/direct/v1/*`。Direct Key、MCP OAuth、Node Connection Token 三套凭据互不通用；API/权限测试覆盖 401/403、限速、CSRF、Secret redaction、真实过期、只读 Scope 矩阵与跨 Machine 越权矩阵。
- MCP 与 Direct API 已收敛到单一 `toolExecutor`：17 个顶层工具的参数映射、Capability 调用和结果标准化只维护一份；`mcp.go` / `direct.go` 不再直接调用 `CallCapability`，静态门禁防止未来重新分叉。独立 Codex 只读审查无阻断问题。
- 源提交 `3fc82f85038ce71e3c8d90a783e795d617e87b30` 已推送 `main`；干净 worktree `release-gate.sh --full` 最终 PASS，Linux amd64 `go test -race ./...` PASS，另有全仓 `go test ./... -count=1`、targeted tests、`go vet` 与 `git diff --check` PASS。
- 生产升级前备份 `/srv/backups/pre-0.4.20-3fc82f85038ce71e3c8d90a783e795d617e87b30.zip` Verify PASS，SHA-256 `cbef63897976548b3c5922ca772aa3bed08901f082bd30036ded9bb1da88efbb`。生产 Hub SHA-256 `180bd1ecda9a0c891d7aaa55534bdf54be2ab9738f7a58ecfeb417d27f8384e0`，spiderctl SHA-256 `5ee6863de1bd196a23161e23cdaa99729799fe33d47f9095d95757f6f6611362`，均为 0.4.20；systemd active，本机与公网 livez/readyz 均 200，公网未认证 Direct tools 返回预期 401。首轮部署因版本核验命令兼容性触发安全回滚，随后重新部署和完整验收成功，当前生产运行新 SHA。Node release 未因本次 Hub 补丁改动。
- Web 后台在 0.4.20 内继续收敛为轻量导航式管理台：概览、设备、OAuth、临时直连密钥、连接令牌、账户安全、MCP 诊断与运行状态拆分为独立登录保护页面；原有管理动作与 CSRF 边界保持不变。临时直连密钥列表不再渲染任何 TokenHint，明文只在创建成功页显示一次；服务层同时将历史较长 hint 统一缩短为 `fsp_tmp_…末4位`，避免旧记录通过其它内部列表视图暴露过多提示信息。

### 2026-08-18 — 0.4.21 Node 操作日志与 MCP 查询

- Node 通过共享持久化 `operationlog.Store` 记录能力调用和本地 UI 操作，并新增 `operation.log/query` 能力；MCP 新增只读 `operation_log` 工具，按 `machineId`、级别、分类和游标查询近期记录。
- MCP 输出只保留时间、级别、分类、动作、状态和耗时，省略本地路径、消息、客户端 IP 与额外字段；查询沿用 Owner/Machine 授权和在线 Node 边界。
- 源提交 `13ed5db765112d25f8b106e211dcac116ee10a6d` 已推送 `main`；core gate、全仓测试、vet、Linux amd64 构建、WSL/Browser/CC Switch/Claude/MCP 专项均通过；full gate 最后 Local Bridge→Codex product E2E 因本机会话返回 `AGENT_EXECUTION_FAILED` 未通过，未伪装为通过。
- 生产升级前备份 `/srv/backups/pre-0.4.21-13ed5db765112d25f8b106e211dcac116ee10a6d.zip` Verify PASS，SHA-256 `26655df2757a02fbd99f85ef9f7f36a5c506a55986476f16da02249935bc8a13`。生产 Hub 0.4.21 SHA-256 `dc68ebff7620b0218046e81615b742e9fe5b39d61a6c1d20fe1c9a39f584f560`，spiderctl SHA-256 `1588c1bbcb46adf5ed21096add05e352c5b29b992e019337f1c47768c9ce9a7e`，systemd active，本机与公网 livez/readyz 均 200。
- PCa Node 已从 0.4.20 自更新至 0.4.21 / windows-amd64 / generation=120，Hub 已登记 `operation.log/query`；Windows Node push marker SHA-256 `66d3ea1ee61a0312dc542484a749714a81aba61891fe32c11721874f46f0005e`。

### 2026-08-19 — 0.4.22 URL-only 临时附件中转

- `artifact_get.publishFile`、Browser 页面截图与 `screenshot_take` 统一改为 attachment 中转：Node 直传 Hub Temporary Presentation Relay，MCP/Direct 仅返回 `url/fileName/contentType/sizeBytes/expiresAt`，不再生成聊天内 `ImageContent`、`EmbeddedResource` 或 `ResourceLink`。
- 新 Node 为临时附件显式发送 `X-Fast-Spider-Resource-Kind: attachment`，最长保留 48 小时；Hub 每分钟按 `expiresAt` 自动清理。旧 Node 未携带 resource kind 时保留 20 分钟 legacy presentation 兼容路径；Hub 重启仍可提前清理系统临时根。
- 新增 URL-only 结果收敛、48 小时 TTL/到期删除、截图/`publishFile` attachment kind 与 fail-closed 公网 URL 测试；worktree/index secret scan、内置 history scan、全仓 test/vet/build、post-history 全套专项、fuzz/race、Windows Browser、CC Switch、Claude 与 multi-provider discovery 均 PASS。本机额外 private-marker overlay 仅在既有 Git 历史中命中 80 项并使原始 `--full` 脚本提前停止，当前工作树/索引无命中；Local Bridge→Codex product E2E 在 300 秒外部 Agent 等待后超时并关闭 bridge socket，保持未通过事实，不伪装为 PASS。
- release commit `2ff08335361269faa9b56265235ccf43c74df492` 已推送 `origin/main`。生产升级前备份 `pre-0.4.22-2ff08335361269faa9b56265235ccf43c74df492.zip` Verify PASS，SHA-256 `d89163794add000e45d1ad5bf4e212190f61b0a5add9ae50f1229a71ec0b2a64`，size `14714987`，manifest source version=0.4.21。
- 生产 Hub/spiderctl 已原子升级到 0.4.22：Hub SHA-256 `6792a5747a418d07e938fbc3e0a23aa1f68229392d4eea617d0281b140f62e25`、spiderctl SHA-256 `4b5a174660465722ee4cfe19a3718c45c216a22f1d699d186015337b2da45941`，systemd active，PID 3989323，本机与公网 livez/readyz PASS；0.4.21 Hub/spiderctl/Windows Node 回滚副本均已保留。
- Windows amd64、darwin amd64、darwin arm64 Node release 均已发布并生成 0.4.22 push marker；PCa 已完成 idle-safe 自更新到 0.4.22 / generation=123 / online+ready。生产 `artifact_get.publishFile` smoke 仅返回 URL 元数据，公网临时地址 HTTP 200、Content-Type image/png、expiresAt 约 48 小时，确认新行为真实上线。备份轮换已完成安全 dry-run：14 个标准候选、保留 3、计划删除 11、实际删除 0；自动删除未作为发布必要条件。

### 2026-08-20 — 0.4.23 ChatGPT Cloud CHAT 会话与 MCP 发现

- `chatgpt_cloud` `session.steer` 已真实接入 `/backend-api/f/steer_turn`；MCP `ai_control` 的 tools/list、initialize instructions 与 capability guide 明确支持通过 Codex 创建 visible ChatGPT CHAT 会话，不再保留“ChatGPT cloud creation is explicitly unsupported”的过时描述。
- 源提交 `c724945406f1072086c905a2a8f9b4de200e629b` 已推送 `origin/main`，分支 `codex/release-0.4.23` 与 tag `v0.4.23` 已推送；源码版本为 0.4.23，五个显式构建目标均 `vcs.modified=false`。
- core release gate、全仓 test/vet/build、history 之后的全部专项/WSL/Browser/CC Switch/Claude/multi-provider/Local Bridge→Codex product stages 与真实 `TestChatGPTCloud` live E2E PASS；精确 `--full` gate 在 Git history secretscan 的 325 个既有历史命中处按设计停止，未绕过历史安全门禁；windows/386 按设计 skip fuzz/race。
- 生产备份 `pre-0.4.23-c724945406f1072086c905a2a8f9b4de200e629b.zip` Verify PASS；Hub/spiderctl 已原子升级到 0.4.23，三平台 Node release 与 push marker 已发布，PCa idle-safe 自更新到 0.4.23 / generation=131；本机/公网 livez/readyz=200，未认证 MCP=401，OAuth metadata=200。
- 已通过当前认证 MCP 真实调用 `machine_list` 与 `capability_list(view=overview/view=tool,name=ai_control)`：生产 ServerVersion=`0.4.23`、GuideVersion=`1.3`，返回 ChatGPT CHAT 创建参数；ChatGPT App/客户端如果缓存旧顶层工具 Schema，仍需 Refresh。本次未读取生产 OAuth 凭据执行原始冷 `tools/list`，以本地 tools/list E2E、生产 capability discovery 与公开未认证边界作为服务端证据。

### 2026-08-21 — 0.4.24 发布加固、部署与 AI/MCP 验收

- 修复固定管理员默认凭据、管理员密码轮换/旧会话撤销、ChatGPT Cloud session.create 持久幂等、realtime 订阅关闭/有界空闲回收/精确 waiter 释放与 conversation watch 校验；Nginx Fast Spider 入口覆盖客户端 IP 头，阻断外部伪造。
- 源码 release commit `ceef5843dfb8dd4453eef0141c83ec20ad152f85`；clean reachable clone 的完整 release gate PASS，真实 Codex plugin/MCP、ChatGPT Cloud live、WSL/Browser/CC Switch/Claude/multi-provider 与 Local Bridge→Codex product E2E PASS。Windows/386 按设计 skip fuzz/race。
- 生产已完成备份 Verify、Hub/spiderctl 原子升级、三平台 Node release push 与本机 Windows Node 受控替换；Hub/spiderctl/Node 均为 0.4.24，PCa generation=136，systemd 与本机/公网健康检查正常，回滚副本保留。
- 本机真实 Local Bridge smoke 返回 `pluginMarketplaces=5`、`mcpServers=4`、`fileReadSucceeded=true`；MCP status 读取到 `codex_apps`，可见 222 tools，证明 AI 插件能读取 MCP 并正常调用能力。

### 2026-08-23 — 0.4.25 ChatGPT Cloud 会话路由与 Git remote-helper 修复

- 修复 `chatgpt_cloud` 创建与后续控制不对称：visibility sidecar 现在保存 `backend + workingDirectory`，FS 创建的 cloud conversation 后续只传 `sessionId` 即可自动路由；普通 Codex `session.list` 合并 FS 管理的 cloud conversation，显式 `backend=chatgpt_cloud` 保留完整账号云列表语义。
- 修复 Cloud 创建被 60 秒 HTTP Client 全局超时早于 120 秒 SSE 边界截断的问题；HTTP Client 不再设置该全局短超时。SSE 已返回 conversation ID 后即使流尾异常，manager 仍持久化已创建会话并返回 `created_execution_unknown`，同 idempotency key 继续重放同一 ID，不再把已创建误判为可重试创建。
- 修复 `git_control fetch/pull/push` 的空 remote-helper 根因：不再注入 `-c remote.<name>.vcs=`，避免 Git 解释为 `git-remote-`；仓库真实配置中的非空 `remote.<name>.vcs` 仍由安全检查 fail-closed 拒绝。`remote` 省略时统一按当前分支 upstream → `origin` → 唯一 remote 解析，多 remote 歧义时要求显式指定。
- 回归证据：ChatGPT Cloud 自动 backend 路由、默认列表合并、known-ID stream error 幂等恢复；Git remote 推断、ambiguous remote fail-closed、真实本地 bare remote fetch（省略 remote）均通过。相关 `internal/agent`、`internal/node`、`internal/hub/server`、`internal/hub/core` 包级完整测试与 `git diff --check` PASS。
- 正式版本提升至 `0.4.25`，release commit `7d699615e02889556a35d7ed71996c137ee77688` 与 tag `v0.4.25` 已推送。release commit 上全仓 `go test ./... -count=1` 约45秒 PASS；五个正式跨平台产物均 `vcs.modified=false`。
- 升级前备份 Verify `valid=true`，SHA256=`ce6bab522c5777660ce4e4ccd64a7ec2c74e7730ab3d3bee07ca5560eec61003`。生产 Hub/spiderctl 已升级 0.4.25，本机及 `sharedservices.tibbs.app/fast-spider` 公网 livez/readyz=200，未认证 MCP=401、OAuth metadata=200。
- 三平台 Node release/version/push marker 已发布 0.4.25；PCa 在真实 busy 期间按设计保持旧进程，活动结束后 idle-safe 自动替换并以 `generation=139 / online / ready` 重连，没有为升级强制中断任务。
- 生产故障复验完成：新 Node `git_control fetch` 省略 remote 的网络 Job exit 0；既有 ChatGPT Cloud conversation 的 `session.get` 省略 backend 后自动返回 `backend=chatgpt_cloud / externalIdType=chatgpt_conversation`。
