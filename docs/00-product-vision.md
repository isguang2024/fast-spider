# 00 产品愿景

## 1. 产品定义

Fast Spider 是面向个人开发者、小型团队和自托管场景的跨平台远程开发与自动化执行平台。它把公网控制面与真实机器执行面分离：Hub 负责身份、路由、任务状态和审计，Node 负责在当前 OS 用户权限下执行文件、Shell、Git、构建、浏览器、截图和本地 AI 操作。

当前远程边界只有 Machine。Fast Spider 不建立目录授权、路径注册或白名单层；Node 直接使用启动它的 OS 用户权限操作整台电脑。文件类请求使用绝对路径，执行类请求使用绝对工作目录或仓库路径。0.3.0 是完成该模型迁移的历史版本。

## 2. 要解决的问题

远程桌面缺少结构化和可审计语义，通用内网穿透又容易暴露过大的端口边界。Fast Spider 提供固定能力、Job/Event、取消、审计和结果 Artifact，让 GPT、Claude、Codex 或其他客户端可以操作多台异构机器，而不要求 Hub 直接进入机器。

## 3. 目标用户

- 单人 + AI 的开发者。
- 拥有公网服务器和若干 Windows/Linux 开发机的自托管用户。
- 需要远程读取、编辑、构建、测试和浏览器验证，但不愿开放 SSH、SMB 或任意端口的人。
- 需要可观察、可取消、可复核自动化执行的人。

MVP 不以大规模多租户 SaaS 为目标。

## 4. 核心价值

### 4.1 边界清晰

Node 只出站连接；Hub 不直接执行；Machine 是唯一远程资源边界；Node 以当前 OS 用户权限进行最终执行。绝对路径是请求目标，不是额外授权对象。

### 4.2 能力统一

MCP、Web Console、CLI 和 Local Bridge 共用同一 Capability Engine、Job、事件和审计链路，避免入口之间的语义漂移。

### 4.3 可运营

Hub 是一个 Go 模块化单体，使用 SQLite WAL 和本地 Artifact 存储。单机部署、备份、升级和恢复优先于横向扩展。

### 4.4 适合 AI 协作

MCP 固定 16 个工具；`ai_control` 以 Harness 为执行对象，当前支持 Codex + Claude Code，并把 CC Switch 作为只读 Routing SSOT。Harness 模型、客户端 alias 与真实 upstream Provider/model 分层，最终能力按实际 Harness/转换/upstream/policy 派生。长任务、Diff、日志、Artifact 与轻量 `working_context` 继续使用 Fast Spider 自己的可观察事实，Git/文件仍是最终代码事实源。

## 5. 产品原则

1. Hub 是控制面与路由层，不触碰 Node 真实资源。
2. Machine 是唯一远程边界，Node 是执行面和最终裁决者。
3. Node 不自动提权，Fast Spider 不实现目录授权层。
4. 文件、Shell、Build、Git 和 AI 请求使用绝对路径字段。
5. 浏览器允许 Node 可访问的公网、localhost 和私网地址；不维护 Origin 白名单。
6. 高风险操作必须可见、可审计、可取消、可超时。
7. 协议与 MCP 解耦。
8. 优先单进程、单数据库、少组件。
9. 旧目录对象模型已在 0.3.0 删除；当前不保留兼容执行路径。
10. AI Harness、Routing Runtime 与 upstream Provider/model 必须分层；第三方模型能力不靠品牌名猜测。
11. UTF-8、结构化错误和版本化契约是基础要求。

## 6. 成功指标

- 新 Node 在 5 分钟内完成安装、登记和首次上线。
- 远程客户端可以在指定 Machine 上读取、编辑、搜索和构建任意当前 OS 用户可访问的路径。
- Shell、Build、Git 和 AI 长任务具备稳定 Job/Event、取消和幂等语义。
- 浏览器可以验证 Node 可访问的公网、localhost 和私网页面。
- Hub/Node 断线后不重复执行已接受的副作用操作。
- 备份、校验、恢复和 release gate 可以在无云资源条件下重复运行。
