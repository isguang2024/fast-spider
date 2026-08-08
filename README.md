# Fast Spider

Fast Spider 是一个自托管、跨平台、多节点的远程开发与自动化执行平台。

它通过长期部署在公网服务器上的 Hub，将 GPT、Claude、Codex、Web Console、CLI 或其他自动化客户端的请求，安全路由到用户明确授权的 Windows、Linux，未来也包括 macOS 节点。Node 只主动建立 HTTPS/WSS 443 出站连接，不默认开放局域网或公网端口。

> 当前状态：Phase 0（产品定义、架构、协议、安全模型与实施规划）。本仓库此阶段不包含业务实现、云资源或付费依赖。

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

## 仓库状态

当前仓库仅包含 Phase 0 文档和决策记录。编码开始条件见 [开放问题](docs/20-open-questions.md) 中的“编码前必须决定”。
