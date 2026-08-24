# Fast Spider

[English](README.md) | [简体中文](README.zh-CN.md)

Fast Spider 是一个自托管、跨平台的远程开发与自动化平台。它把一个或多个
用户自己的 Node 通过出站 HTTPS/WSS 连接到 Hub，再通过 MCP、Web Console、
CLI 或 Local Bridge 调用结构化能力。

Fast Spider **不是远程桌面，也不是通用内网穿透**。Hub 负责身份、路由、任务、
审计和 API；真正的文件、Shell、Git、浏览器和 AI 操作发生在 Node，并使用
启动 Node 的操作系统账户权限。

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

## 许可证

Fast Spider 使用 Apache License 2.0，见 [LICENSE](LICENSE)。
