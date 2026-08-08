# Fast Spider

Fast Spider 是一个自托管、跨平台、多节点的远程开发与自动化执行平台。

它通过长期部署在公网服务器上的 Hub，将 GPT、Claude、Codex、Web Console、CLI 或其他自动化客户端的请求，安全路由到用户明确授权的 Windows、Linux，未来也包括 macOS 节点。Node 只主动建立 HTTPS/WSS 443 出站连接，不默认开放局域网或公网端口。

> 当前状态：Phase 3 核心开发闭环已落地。除 Phase 1/2 的设备身份、WSS、Workspace、文件读取和代码搜索外，Node 已支持本机显式授权的精确文件编辑、乐观并发校验、非交互 Shell Job、stdout/stderr 事件游标、幂等启动、超时和完整进程树取消。Workspace 默认仍为只读，`write` / `shell` 必须在 Node 本机单独开启；Git 写操作仍未开放。

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
| Local Bridge | Windows Named Pipe / Unix Domain Socket 优先；loopback HTTP 默认关闭 |
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

浏览器、截图、Local Bridge、Codex Adapter 和安装包按后续阶段推进。

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

- 默认拒绝；用户、机器、Workspace、Capability、Action 五级授权。
- Hub 与 Node 双重校验，Node 是最终裁决者。
- 所有危险操作可审计、可取消、可超时、可限制输出。
- Workspace ID、Machine ID、Session ID 均为 opaque 标识。
- UTF-8 是文本、协议、日志和源码默认编码。
- MVP 不引入 Kubernetes、Redis、NATS、Kafka 或复杂消息队列。
- 不做长期双协议、双写或兼容层堆叠；版本升级使用明确窗口与迁移规则。

## Phase 1/2/3 本地运行

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

# 7. 新 Workspace 默认只有 read；需要远程编辑/Shell 时必须在 Node 本机显式授权
# go run ./cmd/node workspace-list
# go run ./cmd/node workspace-permission --workspace '<workspaceId>' --allow read,write,shell
# go run ./cmd/node workspace-disable --workspace '<workspaceId>'
# go run ./cmd/node workspace-enable  --workspace '<workspaceId>'
# go run ./cmd/node workspace-remove  --workspace '<workspaceId>'

# 8. 启动 Node；本地 HTTP 验证必须再次显式允许明文 Hub
go run ./cmd/node run --allow-insecure

# 9. 查看机器
go run ./cmd/spiderctl machine-list \
  --hub http://127.0.0.1:8787 \
  --allow-insecure
```

远程 MCP 当前固定工具面为：`machine_list`、`machine_get`、`capability_list`、`workspace_list`、`file_read`、`code_search`、`file_edit`、`shell_run`、`job_watch`、`job_cancel`。远程文件工具只接受 `machineId + workspaceId + 相对路径`，不会接受或返回 Node 本机授权目录的绝对路径。`file_edit` 必须携带 `file_read` 返回的完整文件 SHA-256；`shell_run` 只接受显式 argv 数组并要求 idempotency key，不做隐式命令字符串拼接，同时默认只继承基础系统/工具链环境变量，不把任意 API Key/Token 传给子进程。

本机 HTTP/WS 仅用于开发验证；生产仍按文档要求使用 HTTPS/WSS 443，并建议 Hub 只监听 loopback，由 TLS 反向代理暴露公网入口。需要注意：Phase 3 的 `shell` 是以 Node 普通 OS 用户权限启动受控进程，并不是 chroot/container 文件系统沙箱；启用 `shell` 权限意味着该进程仍可能访问该 OS 用户本来就能读取的其他本机资源，因此默认保持关闭并要求本机显式授权。

## 开发验证

```bash
go vet ./...
go test ./... -count=1
go build ./cmd/hub ./cmd/node ./cmd/spiderctl ./cmd/contractgen
```

当前 E2E 覆盖 Owner bootstrap → enrollment → Node 上线 → `workspace_list` → `file_read` → `code_search` → 本机提升 Workspace 权限 → `file_edit` → `shell_run` → `job_watch` → 长任务 `job_cancel` → Node 吊销。核心测试还覆盖并发 enrollment/Shell 幂等、token 重放、路径/符号链接逃逸、禁用 Workspace、UTF-8 分块、二进制拒绝、编辑 revision 冲突，以及撤销 `shell` 权限后运行中 Job 自动终止。

## 仓库状态

Phase 0 文档仍是设计基线；Phase 1 已完成节点身份与控制通道，Phase 2 已完成 Workspace/只读文件/搜索，Phase 3 已完成精确编辑、Shell Job、事件游标和完整取消。下一阶段按路线图进入 Git、构建/测试 Profile 与 Artifact，继续保持单进程 Hub/Node，不引入复杂队列或微服务。
