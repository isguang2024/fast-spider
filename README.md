# Fast Spider

Fast Spider 是一个自托管、跨平台、多节点的远程开发与自动化执行平台。Hub 提供公网身份、路由、Job/Event、审计和 Artifact 控制面；Node 是真实机器执行面，只主动通过 HTTPS/WSS 443 连接 Hub。

## Current 当前事实

- 当前源码版本为 `0.4.20`。MCP 顶层仍固定 17 个工具，继续使用 `fsprobe` 按需恢复长会话工具物化；本版在 0.4.19 的能力地图与发布安全基础上，加入直接访问密钥能力，并收敛 Windows Node 的隐藏启动、开机自启动、桌面快捷方式和默认窗口行为。OAuth/PKCE、Node/WSS 协议和工具数量不变。
- Machine 是唯一远程资源边界。Fast Spider 不再维护旧目录对象、目录列表工具、目录授权、目录白名单或路径注册表。
- Node 以启动它的当前 OS 用户运行，直接使用该用户对整台电脑的操作系统权限；Fast Spider 不把文件系统再切成一层目录权限。
- 同一 OS 用户只允许运行一个 Fast Spider Node 主实例；重复双击、开机自启动与手动启动、不同 EXE 位置或不同 `--data-dir` 都不能建立第二条 Node 连接。重复启动只打开现有本地界面后退出。
- Windows 默认启动、桌面快捷方式和开机自启动都使用隐藏的 UI 启动路径；不会留下命令窗口。首次启动会在当前用户桌面补建 `Fast Spider Node.lnk`（已存在时不覆盖），关闭 UI 窗口后 Node 仍由托盘保持运行。
- `file_read`、`file_edit`、`code_search` 使用绝对 `path`；`shell_run` 和 `build_control` 使用绝对 `cwd`；`git_control` 使用绝对 `repositoryPath`；`ai_control.session.create` 使用绝对 `workingDirectory`。Git 子目录和 linked worktree 会自动归到主工作树对应的 Codex Desktop 项目，实际执行目录保持不变；非 Git 临时目录不会自动注册成项目。
- 浏览器在 Node 可访问的公网、localhost 和私网中运行，不需要 Fast Spider Origin/DNS/IP 白名单，也不对页面子资源执行逐请求 DNS 审查；Agent 优先使用 snapshot 返回的短期 ref，并可用 batch 一次完成多步交互。显式 `page.open/page.navigate` 仍拒绝非 HTTP(S) 危险 scheme。
- Windows Node 的 `shell_run/build_control` 接受 `runtime={kind:"host"|"wsl",distribution?}`；WSL cwd 仍由调用方提供 Windows 绝对路径，Node 使用目标发行版的 `wslpath` 安全映射。每个发行版至多一个轻量 keepalive、全局最多 8 个，Node 退出只结束自己创建的 keepalive，不执行 `wsl --shutdown`。
- MCP 当前固定提供 17 个工具，包含 `thinking_team` 与 `working_context`，不包含旧目录列表能力。`thinking_team.providerInvocation=false`，只返回调用侧角色协作配置。
- `working_context` 已扩展为同一套 Plan/Task + Markdown Task Workspace：保留 `get/set/clear` 默认 plan 兼容入口，并提供 `plan.init/plan.get/plan.list/plan.sync/task.update/markdown.list/markdown.read/markdown.append/progress.watch`；状态按 Machine 路由、`projectPath + planId` 隔离。
- `code_search` 2.1 支持 content/files、include/exclude glob 与 bounded context；优先使用 data-dir 中已验证的 Managed `search-ripgrep`，缺失或真实失败时才回退 Go native，返回扫描/匹配/跳过/不完整与分段耗时事实。默认遵守 VCS ignore 并跳过通用生成目录，显式 include 优先。
- `file_read` 2.0 保留 byte range，并支持 line/head/tail/around/statOnly/line numbers；校验、原文件 SHA 和选择在一次流式扫描内完成。`file_edit` 2.1 在同一工具内提供 legacy edit、create、replace、editMany、preview，现有文件写入使用 SHA CAS 与原子替换；mutation 不回显正文/diff，preview 仅返回 bounded hunk。
- Node 自更新启动时先处理 `ready.json`；仅在没有待应用更新且 Ready 检查成功后清理 `updates/<currentVersion>` 已消费 staging。Ready/apply 失败和 future pending staging 均保留，正式 EXE 的 `.previous` 回滚副本不受 data-dir 清理影响。0.4.14 的发布推送复用同一 `Ready → StartApply → .previous → restart` 链，不引入第二套 updater；Node 心跳会按真实活动状态上报 `ready|busy`。
- Windows Node 在上述更新维护完成后，会对当前 `fast-spider-node.exe` 同级目录执行一次 fail-closed legacy cleanup：只清理旧手工安装器严格命名的临时 EXE、marker 和直接位于安全 `backups` 目录内的旧备份；未知项、子目录、reparse/junction、当前 EXE 与 `.previous` 永不删除。非 Windows 不执行该迁移。
- Hub release backup rotation 由显式运维命令执行：新标准 backup 通过 Verify 且正式升级成功后，先运行 `spiderctl backup-prune --dir <absolute-backup-dir> --keep 3` 查看计划，再追加 `--apply` 执行。它只识别直接子级 `pre-<semver>-<commit>.zip`，先验证全部候选再按 manifest `CreatedAt` 清理；应用期间与同进程 backup 创建串行，并在每次删除前即时复核候选；历史异名、Hub binary backup、目录与 symlink/reparse 永不自动删除。
- Release staging `--apply` 会先把候选原子隔离到同盘临时 quarantine，再复核身份/内容后递归删除；删除失败恢复原名，进程崩溃留下的 `.fast-spider-prune-*` 目录保留待下一次人工 plan/apply 对账，不会被宽泛扫描误删。
- Browser 孤儿 Session 目录启动时和每分钟维护时都按有界批次清理；组件中心按语义版本选择最新已验证安装，避免字符串排序误选旧版本。
- `ai_control` 1.1 已是多 AI Harness 控制面：当前支持 `codex` 与 `claude_code`；`provider.readiness` 分开报告 route/provider/harness/session backend/create readiness，Codex `session.create` 使用持久幂等记录防止重试重复 Thread。
- Codex 保留 Provider/Model 能力发现、Skills/Hooks/Permission Profiles/Plugins/MCP 状态、Thread/Goal/Settings/Review、steer/respond、原生 Turn input 与 `outputSchema`；app-server 重启后按需 resume 持久 Thread。
- Claude Code 使用原生 Session UUID + `stream-json` + `--resume`，Prompt 通过 stdin 传入；Fast Spider 只保存小型 Session 控制索引，不复制完整对话。模型和有效能力以 CC Switch Route + Harness 能力共同解释，不把 `sonnet`/`opus` 等别名直接当真实上游模型。
- CC Switch 数据库只读；Fast Spider 不返回 raw `settings_config`/`meta`/API Key/Token，也不通过 `ai_control` 修改 Provider、Takeover 或凭据。Codex 0.141.0 app-server 未公开 Automation API，因此仍不映射 Automations。

Fast Spider 不是远程桌面，也不是通用内网穿透软件：不提供任意 TCP 转发、持续桌面视频、通用鼠标键盘远控或自动提权。Hub 不直接访问 Node 的文件和进程，所有实际执行都发生在 Node 当前 OS 用户上下文中。

## 核心能力

- 多台 Windows/Linux/macOS Node 的登记、在线状态、能力发现和撤销。
- 绝对路径文件读取、精确编辑、代码搜索和 Diff。
- 使用绝对 cwd 的 Shell、构建、测试、日志流、取消和进程树终止。
- 使用绝对 `repositoryPath` 的 Git 状态、Diff、提交及受控远程操作。
- Artifact、隔离浏览器、页面/桌面/窗口截图。
- Provider-neutral AI 控制：Codex + Claude Code；CC Switch 作为只读 Routing SSOT，返回 RouteSnapshot、模型映射和 EffectiveCapabilities。
- Codex 支持原生 Skill/Image/Mention、结构化输出、steer/respond 与持久 Thread 自动恢复；Claude Code 支持 create/send/watch/cancel/result/resume、stream-json 与结构化输出。
- MCP、Web Console、CLI、Local Bridge 共用同一 Capability Engine。
- Node 本地 Edge App Window + loopback UI 提供概览/连接、任务与进度、AI 与路由、组件、诊断页面；组件中心只允许手动管理 Browser 与 `search-ripgrep`，搜索/文件自检只使用隔离临时目录。

## 技术组合

| 区域 | 决策 |
|---|---|
| Hub | Go 模块化单体，一个常驻进程 |
| Node | Go；平台差异通过窄接口处理 |
| 核心协议 | 版本化 JSON Schema，与 MCP 解耦 |
| Node 通道 | WSS 443 承载 JSON 控制消息；大文件走 Artifact/Presentation HTTP 数据面 |
| Hub 数据库 | SQLite WAL |
| Artifact | Hub 本地内容寻址文件存储 |
| Node 本地 UI | 同一 Node 进程提供 loopback 管理页；Windows 提供托盘，macOS 支持 LaunchAgent 开机启动 |
| Local Bridge | Windows/Linux AF_UNIX/UDS，不使用本地 HTTP/MCP |
| 浏览器 | 隔离 Profile，Playwright Adapter |

## 本地运行

要求 Go 1.26+。

```bash
# 启动 Hub
go run ./cmd/hub --data-dir ./data

# 首次设置并创建 Owner
go run ./cmd/spiderctl setup-url \
  --public-url http://127.0.0.1:8787 \
  --allow-insecure \
  --bootstrap-token-file ./data/bootstrap-token

# 管理员后台（创建用户、查看用户列表）
# 地址：http://127.0.0.1:8787/admin/login
# 用户名：admin，默认密码：AA@@123456

# Windows 双击 fast-spider-node.exe，或源码运行本地 UI
go run ./cmd/node ui

# macOS：下载与 CPU 对应的客户端后赋予执行权限
# Apple 芯片：/fast-spider/api/v1/node/releases/darwin-arm64/download
# Intel：/fast-spider/api/v1/node/releases/darwin-amd64/download
chmod +x ./fast-spider-node
./fast-spider-node ui

# 在“连接”页填写 Hub 地址、后台创建的连接令牌和设备名称；
# 登记后 Node 只保存设备身份，不保存连接令牌。
go run ./cmd/node connect --hub http://127.0.0.1:8787 \
  --allow-insecure --token '<连接令牌>' --name dev-node

# 无界面 Node
go run ./cmd/node run --allow-insecure \
  --browser-sidecar-dir ./sidecar/browser

# 可选浏览器运行时
cd sidecar/browser
npm install --no-package-lock
npx playwright install chromium
cd ../..

# Hub 运维：普通时间戳备份不会进入 release rotation
go run ./cmd/spiderctl backup --data-dir ./data --out ../fast-spider-backup-<timestamp>.zip
go run ./cmd/spiderctl backup-verify --file ../fast-spider-backup-<timestamp>.zip

# 正式升级备份使用 pre-<semver>-<commit>.zip；先预览，再明确应用轮换
go run ./cmd/spiderctl backup-prune --dir <absolute-backup-dir> --keep 3
go run ./cmd/spiderctl backup-prune --dir <absolute-backup-dir> --keep 3 --apply
go run ./cmd/spiderctl staging-prune --dir <absolute-staging-root> --layout local --through <last-completed-version>
go run ./cmd/spiderctl staging-prune --dir <absolute-staging-root> --layout local --through <last-completed-version> --apply
go run ./cmd/spiderctl restore --file ../fast-spider-backup-<timestamp>.zip --data-dir ./data-restored
```

Node 不需要添加目录、配置目录权限或维护浏览器私网白名单。文件、Shell、Git、Build 和 AI Harness 请求直接携带目标绝对路径；Node 按当前 OS 用户权限、参数安全检查、资源限制和 Job 规则执行。CC Switch Provider/模型/Takeover 由 CC Switch 自己管理，Fast Spider 只读其数据库事实。

## MCP 工具

Current 固定提供 17 个工具：

```text
machine_list
machine_get
capability_list
file_read
file_edit
code_search
shell_run
job_watch
job_cancel
git_control
build_control
artifact_get
browser_control
screenshot_take
thinking_team
ai_control
working_context
```

`file_read`、`file_edit`、`code_search` 的目标字段是绝对 `path`；`shell_run`/`build_control` 使用绝对 `cwd`；`git_control` 使用绝对 `repositoryPath`；`ai_control.session.create` 使用绝对 `workingDirectory`。`working_context` 使用 `projectPath + planId`。`thinking_team` 不需要 `machineId`，只返回调用侧角色、部门、流程和资料室协议，不创建本机 AI Session。`providerId` 选择 AI Harness；`routing.status` 独立返回 CC Switch Route。Codex 会把 Git worktree 归并到主工作树展示项目；Claude Code Session 固定其创建时工作目录并使用原生 Session UUID。`browser_control` 允许 Node 能访问的公网、localhost 和私网地址，不额外维护 Origin 白名单。远程权限只绑定 `machineId`，Node 是最终执行边界。

首次连接或不清楚工具选择时使用 `capability_list(view=overview)`；只在真正执行某项能力时读取一个 `tool`、`workflow` 或 `error` 指南，不要一次加载全部详情。无参数调用仍返回 Hub Capability Catalog 并附带 overview；`machineId` + 省略 `view` 仍只返回该 Machine 的能力目录。ChatGPT App 若已经缓存旧工具 Schema/描述，Hub 升级后仍需在 App 管理中 Refresh。

## 文档导航

- [产品愿景](docs/00-product-vision.md)
- [需求与范围](docs/01-requirements-and-scope.md)
- [系统架构](docs/02-system-architecture.md)
- [Hub 设计](docs/03-hub-design.md)
- [Node 设计](docs/04-node-design.md)
- [Node 能力](docs/05-node-capabilities.md)
- [线路协议](docs/06-wire-protocol.md)
- [任务与事件模型](docs/07-job-and-event-model.md)
- [身份与权限](docs/08-identity-and-permissions.md)
- [安全威胁模型](docs/09-security-threat-model.md)
- [公共 API 与 MCP](docs/10-public-api-and-mcp.md)
- [Local Bridge 与 AI 控制](docs/11-local-bridge-and-ai-control.md)
- [浏览器与截图](docs/12-browser-and-screenshot.md)
- [数据模型](docs/13-data-model.md)
- [部署与运维](docs/14-deployment-and-operations.md)
- [可观测性](docs/15-observability.md)
- [更新与恢复](docs/16-update-and-recovery.md)
- [测试策略](docs/17-test-strategy.md)
- [开源组件评估](docs/18-open-source-evaluation.md)
- [路线图](docs/19-roadmap.md)
- [开放问题](docs/20-open-questions.md)
- [Thinking Team 角色协作](docs/22-thinking-team.md)
- [缓存与生命周期维护](docs/23-cache-and-lifecycle.md)
- [架构决策记录](docs/adr/)

## 设计约束

- Machine 是唯一远程边界；Hub 与 Node 都校验请求，Node 是最终裁决者。
- Node 不自动提权，以当前 OS 用户权限操作整台电脑；Fast Spider 不实现目录授权层。
- 所有高风险操作可审计、可取消、可超时、可限制输出。
- 目标路径必须是绝对路径，并由对应能力按平台规则校验。
- UTF-8 是文本、协议、日志和源码默认编码。
- 不引入 Kubernetes、Redis、NATS、Kafka 或复杂消息队列。
- 0.3.0 删除旧路径，不维护兼容目录 API 或双协议执行层。

## 开发与发布验证

```bash
bash scripts/release-gate.sh
bash scripts/release-gate.sh --full
go run ./cmd/secretscan
go run ./cmd/secretscan --history
```

统一秘密扫描器默认覆盖 tracked/untracked nonignored 工作树和 Git index，包含原始二进制与有界 ZIP 展开；`--history` 追加扫描 Git 全对象库，`--tree <dir>` 用于校验实际导出树。`.local/public-private-markers.txt` 默认只应用于当前 worktree/index 和 `--tree` 导出检查；历史扫描默认只使用内置凭据规则，只有显式 `--markers <file>` 才把本地 marker 应用于历史。命中只输出 source、path/object、line 和 rule，不回显值或整行；locator 自身含敏感值时改为哈希标识，读取、对象枚举、ZIP 展开或资源上限异常均失败关闭。门禁还覆盖格式、模块校验、`go vet`、测试、构建、恢复后 Hub 健康检查；完整模式额外运行历史扫描、合成脱敏自检及各产品专项。具体平台限制以门禁输出为准。
