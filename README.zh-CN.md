# Fast Spider

[English](README.md) | [简体中文](README.zh-CN.md)

**让 MCP 或 AI 编程 Agent 安全地操作你自己的 Windows、Linux 和 macOS
电脑。**

Fast Spider 是一个自托管、跨平台的远程开发与自动化平台。它把用户自己的
电脑转化为明确、可审计的能力：读取和修改代码、运行构建、操作 Git、控制隔离
浏览器、传递产物，以及管理本机 AI 编程会话。

具体工作始终在你的电脑上执行；Fast Spider 通过 MCP、Web Console、CLI 和
Local Bridge 提供统一的身份认证、路由、任务生命周期、审计与控制平面。

Fast Spider **不是远程桌面，也不是通用内网穿透**。Hub 负责身份、路由、任务、
审计和 API；真正的文件、Shell、Git、浏览器和 AI 操作发生在 Node，并使用
启动 Node 的操作系统账户权限。

> **项目状态：** Fast Spider 是持续维护中的早期公开项目。核心平台已经可以
> 使用，但外部贡献者和采用者社区仍在形成中。欢迎提交可复现 Issue、使用反馈
> 和范围清晰的贡献。

## Fast Spider 可以做什么

| 领域 | 能力 |
|---|---|
| 多电脑管理 | 登记、发现、查看、断开和吊销多个 Windows、Linux、macOS Node |
| 代码与文件 | 读取文本、搜索仓库、精确修改文件、校验并发变更并返回 diff |
| 命令与构建 | 把 Shell、构建和测试作为可取消任务运行，持续读取日志并取得最终结果 |
| Git | 查看状态、diff 和历史；创建提交；管理分支与 worktree；受控执行 fetch、pull、push |
| 浏览器 | 使用隔离 Chromium 打开页面、检查内容、点击、输入、下载和截图 |
| AI 编程会话 | 通过统一接口发现和控制本机 Codex、Claude Code 会话 |
| 项目协作 | 使用带版本校验的计划、任务状态和 Markdown 工作上下文 |
| 产物与证据 | 传递生成文件、提供临时展示资源，并保留操作日志与审计记录 |
| 调用入口 | MCP 客户端、Web Console、`spiderctl` 和 Local Bridge 共用同一套能力模型 |

典型用途包括：

- 让云端或另一台电脑上的编程 Agent 检查、修改你指定的本地项目；
- 从 Linux 或云端 MCP 客户端触发 Windows 专属构建；
- 启动测试、跟踪日志、必要时取消任务并取回产物；
- 在隔离浏览器中验收本地 Web 应用；
- 统一协调 Codex 或 Claude Code，而不复制它们的原生对话历史；
- 在一个地方查看电脑访问、任务状态和操作记录。

## 一次请求如何执行

```text
MCP / Web / CLI / Local Bridge
              |
              v
        Fast Spider Hub
      身份 | 路由 | 任务 | 审计
              |
        出站 HTTPS/WSS
              |
              v
       Fast Spider Node
 文件 | Shell | Git | 浏览器 | AI
              |
              v
       启动 Node 的系统用户
```

1. 客户端请求 Hub 在指定 Node 上执行一项明确能力。
2. Hub 验证调用者身份、记录任务并把请求路由到 Node。
3. Node 校验能力参数，再以启动 Node 的操作系统用户权限执行。
4. 进度、日志、结果、错误和产物通过同一任务生命周期返回并进入审计。

## 3 分钟安全试用

从源码目录运行，第一次上手默认使用 Project mode：

```bash
git clone https://github.com/isguang2024/fast-spider.git
cd fast-spider
go run ./cmd/spiderctl share --project . --tunnel none
```

`share` 会：

1. 在 `127.0.0.1:8787` 启动临时 Hub；
2. 自动创建临时管理员密码和首个 owner；
3. 创建一个临时 Node Connection Token；
4. 输出 Node 连接命令和 MCP URL；
5. 持续运行，直到你在当前窗口按 Ctrl-C。

在第二个终端执行输出的 Node 命令。`share` 不会自动启动 Node UI，避免
进程 owner 和 Local Bridge 的生命周期被隐藏。MCP 使用 OAuth；输出中的
Bearer credential 只用于 Node 登记，不是 MCP 登录凭据。

第一次建议使用的安全请求：

```text
Inspect this repository and summarize its structure. Do not make changes.
```

需要云端 MCP 客户端访问时，将 `--tunnel none` 替换为
`--tunnel cloudflare` 或 `--tunnel ngrok`。对应客户端必须已经安装并在
`PATH` 中。详情见[免费本地部署](docs/free-local-deployment.zh-CN.md)。

## 安装与运行

要求：

- Go 1.26+
- Git
- 浏览器自动化可选安装 Node.js / Playwright 依赖

从源码运行最容易获得 Hub 和 Node 两个入口。如果只安装 CLI：

```bash
go install github.com/isguang2024/fast-spider/cmd/spiderctl@latest
spiderctl share --project .
```

还需要 `fast-spider-hub` 在 `PATH` 中，或设置
`FAST_SPIDER_SOURCE_ROOT` 指向 Fast Spider 源码目录。CLI 不会静默下载或
安装 Hub、Node 或 Tunnel。

## 和远程桌面、内网穿透的区别

- 远程桌面传输屏幕并接管桌面；Fast Spider 通过明确的 capability 调用
  文件、任务、Git、浏览器或 AI，不提供隐藏桌面流。
- Tunnel 只改变 Hub 的网络可达性，不改变 Node 的本地权限，也不会自动
  暴露 Node UI 或 Local Bridge。
- Node 仍是本机强力自动化进程，必须按照凭据、Shell、Git、浏览器和 AI
  风险来使用。

Hub 不会直接挂载 Node 文件系统，也不会获得高于启动 Node 的系统用户权限。
详细契约见[系统架构](docs/02-system-architecture.md)、
[Node 能力](docs/05-node-capabilities.md)和[安全模型](docs/security-model.zh-CN.md)。

## Project mode 与 Machine mode

Project mode 是公开试用的默认模式。它在 Node capability 边界限制文件、
搜索、Shell/Build cwd、Git 仓库和 worktree、上下文、附件以及明确的 AI
输入路径，并禁用原生桌面/窗口截图。

这是一层路径约束，不是操作系统沙箱。Shell 解释器、Git remote、浏览器、
provider 凭据和子进程仍可能产生项目外副作用。Machine mode 保留原有的
整机 OS 用户边界，适合私有高级使用，不应通过公开 Tunnel 暴露。

## 免费本地部署

- [免费本地部署（中文）](docs/free-local-deployment.zh-CN.md)
- [Free Local Deployment (English)](docs/free-local-deployment.md)
- [安全模型（中文）](docs/security-model.zh-CN.md)
- [Security Model (English)](docs/security-model.md)

## 文档入口

- [中文快速开始](docs/getting-started.zh-CN.md)
- [英文快速开始](docs/getting-started.md)
- [文档索引](docs/README.md)
- [配置参考](docs/configuration.md)
- [公共 API 与 MCP](docs/10-public-api-and-mcp.md)
- [部署与运维](docs/14-deployment-and-operations.md)
- [安全漏洞报告](SECURITY.md)

## 凭据提醒

Bootstrap token、Connection Token、OAuth token、Direct Access Key、Node 私钥
和 Hub 私钥用途不同，不要放进 Issue、Prompt、公开仓库或普通日志。Connection
Token 只用于登记 Node；MCP 使用 OAuth，Direct API 使用独立 Direct Key。
暴露后请在 Web Console 撤销对应凭据或设备。

## 开发检查

```bash
go test ./... -count=1
go vet ./...
git diff --check
bash scripts/public-release-check.sh
```

公共源码发布必须使用 `scripts/public-export.sh` 创建新的单 root commit，不能
直接把私有开发 history 当作公开仓库历史。

## 社区

Fast Spider 认可并支持开发者社区 [LINUX DO](https://linux.do)。项目讨论与
贡献仍通过本仓库面向所有人开放。

## 许可证

Fast Spider 使用 Apache License 2.0，见 [LICENSE](LICENSE)。

维护治理、支持边界和公开变更记录分别见 [GOVERNANCE.md](GOVERNANCE.md)、
[SUPPORT.md](SUPPORT.md) 和 [CHANGELOG.md](CHANGELOG.md)。
