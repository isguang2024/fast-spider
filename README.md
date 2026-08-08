# Fast Spider

Fast Spider 是一个自托管、跨平台、多节点的远程开发与自动化执行平台。

它通过长期部署在公网服务器上的 Hub，将 GPT、Claude、Codex、Web Console、CLI 或其他自动化客户端的请求，安全路由到用户明确授权的 Windows、Linux，未来也包括 macOS 节点。Node 只主动建立 HTTPS/WSS 443 出站连接，不默认开放局域网或公网端口。

> 当前状态：Phase 1–8 已形成完整可运行闭环。Node 本机入口使用当前用户数据目录下的 AF_UNIX socket，Windows/Linux 共用同一实现，不监听 TCP，也不维护本地 Client 注册/Token/Grant。Codex 直接使用本机 `codex app-server --stdio`。运维主线已收敛为版本检查和 `spiderctl backup / backup-verify / restore`；发布前使用 `scripts/release-gate.sh` 复验，不建设安装器、托盘或自动更新服务。

## 核心定位

Fast Spider 不是远程桌面，也不是通用内网穿透软件：

- 不提供任意 TCP 端口转发。
- 不实现持续桌面视频、音频或通用鼠标键盘远控。
- 不允许 Hub 直接访问节点文件系统或执行命令。
- 不把绝对路径作为远程授权依据。
- 不允许 AI 或 Hub 绕过 Node 本地权限裁决。

它聚焦于受控的远程开发与自动化能力：

- 节点、Workspace 与权限管理。
- 文件读取、搜索、原子编辑和 Diff。
- Shell、构建、测试、日志流、取消和进程树终止。
- Git 状态、Diff、提交及受控远程操作。
- Artifact、截图与浏览器测试。
- Provider-neutral 的本地 AI / Codex Session 桥接。
- MCP、Web Console、CLI、REST/SDK 等多个 Adapter 共用同一 Capability Engine。

## 推荐 MVP 技术组合

| 区域 | 决策 |
|---|---|
| Hub | Go 模块化单体，一个常驻进程 |
| Node | Go；平台能力不足时使用窄接口原生辅助模块 |
| 核心协议 | 版本化 JSON Schema；与 MCP 解耦 |
| Node 通道 | WSS 443，JSON 控制消息 + 二进制分块 |
| Hub 数据库 | SQLite WAL，预留 PostgreSQL 迁移接口 |
| Artifact | Hub 本地内容寻址文件存储，元数据入库 |
| Web Console | 静态资源嵌入 Hub |
| Local Bridge | Windows/Linux 统一 AF_UNIX socket；无 TCP 端口，loopback HTTP 不进入首版 |
| 浏览器 | 隔离 Profile，优先 Playwright Adapter |
| MCP | 固定常用工具 + 动态能力发现的混合模式 |

## 第一批实现范围

完成 Phase 0 审核后，第一批代码优先复用 DevSpace 已验证的产品边界：

1. 设备注册、心跳、在线状态和能力发现。
2. Workspace 注册、授权、禁用和路径边界校验。
3. 文件读取、分段读取、搜索、原子写入、小范围编辑和 Diff。
4. 非交互 Shell Job、流式 stdout/stderr、超时、取消和进程树终止。
5. Git status、diff、log、show 与当前分支。
6. Job watch、事件游标、结果和 Artifact。
7. 稳定的 MCP 工具面，以及独立的内部 Capability Request。

浏览器、截图、Local Bridge、Codex Adapter、简单备份恢复和可重复 Release Gate 均已落地；当前不再为了路线图扩产品功能，后续只根据真实使用问题修复/优化。

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
- [架构决策记录](docs/adr/)

## 设计约束

- 机器与 Workspace 是主要授权边界；只有文件写入、Shell、Git 网络/副作用和 Build 等真正危险操作保留独立本机权限，避免个人使用时层层授权。
- Hub 与 Node 双重校验，Node 是最终裁决者。
- 所有危险操作可审计、可取消、可超时、可限制输出。
- Workspace ID、Machine ID、Session ID 均为 opaque 标识。
- UTF-8 是文本、协议、日志和源码默认编码。
- MVP 不引入 Kubernetes、Redis、NATS、Kafka 或复杂消息队列。
- 不做长期双协议、双写或兼容层堆叠；版本升级使用明确窗口与迁移规则。

## 本地运行

要求 Go 1.26+。

```bash
# 1. 启动 Hub；默认仅监听 127.0.0.1:8787
go run ./cmd/hub --data-dir ./data

# 2. 首次 Owner bootstrap
# Hub 会在 ./data/bootstrap-token 写入一次性 bootstrap token
go run ./cmd/spiderctl bootstrap \
  --hub http://127.0.0.1:8787 \
  --allow-insecure \
  --bootstrap-token-file ./data/bootstrap-token

# 3. 保存上一步只返回一次的 ownerToken
export FAST_SPIDER_OWNER_TOKEN='<owner token>'

# 4. 创建 Node enrollment token
go run ./cmd/spiderctl enrollment-create \
  --hub http://127.0.0.1:8787 \
  --allow-insecure \
  --name dev-node

# 5. 在 Node 机器执行一次性配对
go run ./cmd/node enroll \
  --hub http://127.0.0.1:8787 \
  --allow-insecure \
  --token '<enrollment token>' \
  --name dev-node

# 6. 在 Node 本机授权代码目录；真实绝对路径只保存在 Node 本机
go run ./cmd/node workspace-add \
  --path 'V:/repos/GitHub/example' \
  --name example

# 7. 新 Workspace 默认只有 read；危险能力必须在 Node 本机显式授权
# 本机 workspace-list 会显示真实授权 Root 供用户核对；远程 MCP workspace_list 永远不返回绝对路径。
# go run ./cmd/node workspace-list
# 文件编辑 + Shell：
# go run ./cmd/node workspace-permission --workspace '<workspaceId>' --allow read,write,shell
# Git 写 + 网络 + hooks/filter + Build Profile：
# go run ./cmd/node workspace-permission --workspace '<workspaceId>' --allow read,write,shell,git-write,git-network,git-hooks,build
# Browser/桌面截图无需再单独授权。普通公网网页默认可访问；只有 localhost/局域网开发地址需要在 Node 本机加一次持久白名单：
# go run ./cmd/node workspace-browser-allow --workspace '<workspaceId>' --origin 'http://127.0.0.1:3000'
# go run ./cmd/node workspace-browser-list --workspace '<workspaceId>'
# 不再使用 TTL；只有地址变化时才需要 remove/allow。
#
# Build/Test Profile 的真实 argv 也只能在 Node 本机登记：
# go run ./cmd/node workspace-profile-set --workspace '<workspaceId>' --profile test --name Test --argv-json '["go","test","./..."]' --timeout-seconds 600
# go run ./cmd/node workspace-profile-list --workspace '<workspaceId>'
#
# go run ./cmd/node workspace-disable --workspace '<workspaceId>'
# go run ./cmd/node workspace-enable  --workspace '<workspaceId>'
# go run ./cmd/node workspace-remove  --workspace '<workspaceId>'

# 8. 可选：安装隔离浏览器 Sidecar（仅需要 browser_control 时）
# cd sidecar/browser
# npm install --no-package-lock
# npx playwright install chromium
# cd ../..
#
# 启动 Node；Local Bridge 默认同时启动，不占 TCP 端口。
# 如明确不需要本机入口，可增加 --disable-local-bridge。
# Codex AI 能力直接探测本机 codex CLI；无需单独启动 daemon/agent-service。
go run ./cmd/node run --allow-insecure --browser-sidecar-dir ./sidecar/browser
#
# 本机可直接复用同一 Capability Engine，例如读取 Workspace：
# go run ./cmd/node local-call --workspace '<workspaceId>' --capability file.read --action read --params-json '{"path":"README.md","limit":4096}'

# 9. 查看机器
go run ./cmd/spiderctl machine-list \
  --hub http://127.0.0.1:8787 \
  --allow-insecure

# 10. Hub 运维：备份 → 校验 → 恢复到新的空目录
# 备份包包含 Hub 私钥，请按敏感数据保存。
go run ./cmd/spiderctl backup --data-dir ./data --out ../fast-spider-backup.zip
go run ./cmd/spiderctl backup-verify --file ../fast-spider-backup.zip
# go run ./cmd/spiderctl restore --file ../fast-spider-backup.zip --data-dir ./data-restored

# 版本检查
go run ./cmd/hub --version
go run ./cmd/node version
go run ./cmd/spiderctl version
```

远程 MCP 当前固定工具面为 16 个：`machine_list`、`machine_get`、`capability_list`、`workspace_list`、`file_read`、`code_search`、`file_edit`、`shell_run`、`job_watch`、`job_cancel`、`git_control`、`build_control`、`artifact_get`、`browser_control`、`screenshot_take`、`ai_control`。这些都是 Fast Spider MCP Server 的真实能力；GPT、Claude 或其他 MCP Host 只是调用工具，实际文件修改、Shell、Git、Build、Browser 和 AI 执行都发生在 Node，并继续受 Node 本机 Workspace/危险能力边界约束。工具通过标准 MCP annotations 标记 read-only/destructive/idempotent/open-world 语义，不因某个客户端类型主动删减工具。

公网 MCP 同时支持现有 Owner Bearer Token 和标准 OAuth 授权。生产建议配置 `--public-base-url https://host.example/path-prefix`；OAuth 使用动态 Client Registration + Authorization Code + PKCE S256，授权页只要求输入现有 Owner Token，随后签发 1 小时 Access Token 和可轮换的 30 天 Refresh Token。当前单 Owner 模式只使用一个 `fast-spider` scope；OAuth 只是 MCP 客户端认证适配层，不建立第二套用户/权限系统。带 path-prefix 部署时，反向代理还应把标准 `/.well-known/oauth-protected-resource/<resource-path>` 与 `/.well-known/oauth-authorization-server/<issuer-path>` 路由到同一个 Hub。

`browser_control` 仍是一个固定工具，通过 action 选择 launch/open/navigate/click/type/press/wait/snapshot/screenshot/events/close；不暴露任意 JavaScript、CDP 或 Playwright API。公网浏览默认可用，本地/私网 Origin 由 Node 本机持久白名单控制。`screenshot_take` 当前开放 `listDisplays/desktop/display/listWindows/window`，不再要求独立截图权限；窗口只需先列出一次拿 opaque `windowId`，结果同样只返回 Artifact。具体 Node 只有在 Sidecar、Playwright npm 包和受管 Chromium 都安装完成时才宣告 `browser.automation`。`ai_control` 当前实现本机 Codex 的 `providers/models/projects/session.*`；创建或继续 Codex Run 复用 Workspace 现有 `write + shell` 权限，不新增单独 `agent` 权限。未指定模型时从当前 `codex model/list` 自动选择可用模型，避免本机默认模型与旧 CLI 不兼容导致 Session 直接失败。

本机 HTTP/WS 仅用于开发验证；生产仍按文档要求使用 HTTPS/WSS 443，并建议 Hub 只监听 loopback，由 TLS 反向代理暴露公网入口。Shell/Git 仍以 Node 普通 OS 用户权限运行，不是 chroot/container 沙箱。Git commit/pull/push/worktree 会显式检查 hooks/filter 风险；受管 worktree 创建在 Node 数据目录并注册成新的默认只读 Workspace，不在原仓库目录里嵌套。

## 开发与发布验证

```bash
# 不依赖 Browser/Codex 外部 runtime 的基础门禁
bash scripts/release-gate.sh

# 发布前完整门禁：再跑重复 Node 回归、真实 Browser/Codex 和产品 smoke
bash scripts/release-gate.sh --full
```

`core` 会检查 gofmt、Git whitespace、tracked secret pattern、`go mod verify/tidy -diff`、`go vet`、全量 tests、当前/Windows/Linux builds、恢复后 Hub 健康 E2E 和 Local Bridge E2E。`--full` 再运行 3 轮 Node、真实 Chromium、真实 Codex 和 Local Bridge→Codex 产品 smoke；支持的工具链还会跑短时 random fuzz 与 race。当前开发环境是 `windows/386 + CGO=0`，因此 random fuzz/race 会明确显示 `SKIP`，Fuzz seeds 仍随普通 `go test ./...` 执行。

当前验证链覆盖 Owner bootstrap → enrollment → Node 上线 → Workspace/文件/搜索/编辑 → Git/Build/Artifact → Shell/Job → 隔离 Chromium → 页面/桌面/窗口截图 Artifact → Local Bridge → `agent.control` → 本机 Codex Session/Turn → Hub backup/verify/restore → 恢复后真实 Hub health。Phase 8 把这些真实链路收口到同一个 release gate，而不是再增加服务或权限层。

## 仓库状态

Phase 0 文档保留为设计历史，但实现以当前代码和已更新 ADR 为准。Phase 1–8 已形成可运行闭环；仍保持单进程 Hub/Node，不引入复杂队列、微服务、第二套本地权限系统、安装器状态机、自动更新服务或通用远控能力。
