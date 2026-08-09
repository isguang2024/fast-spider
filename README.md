# Fast Spider

Fast Spider 是一个自托管、跨平台、多节点的远程开发与自动化执行平台。Hub 提供公网身份、路由、Job/Event、审计和 Artifact 控制面；Node 是真实机器执行面，只主动通过 HTTPS/WSS 443 连接 Hub。

## 0.3.0 当前事实

- Machine 是唯一远程资源边界。Fast Spider 不再维护旧目录对象、目录列表工具、目录授权、目录白名单或路径注册表。
- Node 以启动它的当前 OS 用户运行，直接使用该用户对整台电脑的操作系统权限；Fast Spider 不把文件系统再切成一层目录权限。
- 同一 OS 用户只允许运行一个 Fast Spider Node 主实例；重复双击、开机自启动与手动启动、不同 EXE 位置或不同 `--data-dir` 都不能建立第二条 Node 连接。重复启动只打开现有本地界面后退出。
- `file_read`、`file_edit`、`code_search` 使用绝对 `path`；`shell_run` 和 `build_control` 使用绝对 `cwd`；`git_control` 使用绝对 `repositoryPath`；`ai_control.session.create` 使用绝对 `workingDirectory`。
- 浏览器在 Node 可访问的公网、localhost 和私网中运行，不需要 Fast Spider Origin 白名单；仍由 Node 的 OS、网络和浏览器运行时条件决定是否可达。
- MCP 固定提供 15 个工具，不包含旧目录列表能力。

Fast Spider 不是远程桌面，也不是通用内网穿透软件：不提供任意 TCP 转发、持续桌面视频、通用鼠标键盘远控或自动提权。Hub 不直接访问 Node 的文件和进程，所有实际执行都发生在 Node 当前 OS 用户上下文中。

## 核心能力

- 多台 Windows/Linux Node 的登记、在线状态、能力发现和撤销。
- 绝对路径文件读取、精确编辑、代码搜索和 Diff。
- 使用绝对 cwd 的 Shell、构建、测试、日志流、取消和进程树终止。
- 使用绝对 `repositoryPath` 的 Git 状态、Diff、提交及受控远程操作。
- Artifact、隔离浏览器、页面/桌面/窗口截图。
- Provider-neutral 的本地 Codex Session 桥接。
- MCP、Web Console、CLI、Local Bridge 共用同一 Capability Engine。

## 技术组合

| 区域 | 决策 |
|---|---|
| Hub | Go 模块化单体，一个常驻进程 |
| Node | Go；平台差异通过窄接口处理 |
| 核心协议 | 版本化 JSON Schema，与 MCP 解耦 |
| Node 通道 | WSS 443，JSON 控制消息 + 二进制分块 |
| Hub 数据库 | SQLite WAL |
| Artifact | Hub 本地内容寻址文件存储 |
| Node 本地 UI | 同一 Node 进程提供 loopback 管理页和 Windows 托盘 |
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

# Windows 双击 fast-spider-node.exe，或源码运行本地 UI
go run ./cmd/node ui

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

# Hub 运维：备份、校验、恢复到新空目录
go run ./cmd/spiderctl backup --data-dir ./data --out ../fast-spider-backup.zip
go run ./cmd/spiderctl backup-verify --file ../fast-spider-backup.zip
go run ./cmd/spiderctl restore --file ../fast-spider-backup.zip --data-dir ./data-restored
```

Node 不需要添加目录、配置目录权限或维护浏览器私网白名单。文件、Shell、Git、Build 和 Codex 请求直接携带目标绝对路径；Node 按当前 OS 用户权限、参数安全检查、资源限制和 Job 规则执行。

## MCP 工具

0.3.0 固定提供 15 个工具：

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
ai_control
```

`file_read`、`file_edit`、`code_search` 的目标字段是绝对 `path`；`shell_run`/`build_control` 使用绝对 `cwd`；`git_control` 使用绝对 `repositoryPath`；`ai_control.session.create` 使用绝对 `workingDirectory`。`browser_control` 允许 Node 能访问的公网、localhost 和私网地址，不额外维护 Origin 白名单。远程权限只绑定 `machineId`，Node 是最终执行边界。

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
```

门禁覆盖格式、秘密模式、模块校验、`go vet`、测试、构建、恢复后 Hub 健康检查，以及完整模式下的 Browser、Codex、Local Bridge 和产品 smoke。具体平台限制以门禁输出为准。
