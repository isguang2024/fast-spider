# Fast Spider 中文快速开始

[English](getting-started.md)

## 组成

- **Hub**：身份、路由、任务、审计、附件和 API 控制面。
- **Node**：在用户电脑上执行文件、Shell、Git、浏览器和 AI 操作的执行面。
- **MCP / Web / CLI / Local Bridge**：共享同一套 capability 模型的入口。

Hub 不会挂载 Node 文件系统；真实操作始终发生在 Node 进程，并使用启动
Node 的操作系统账户权限。

## 第一条路径：Project mode

在 Fast Spider 源码目录运行：

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```

它会在 loopback 启动临时 Hub、完成首个 owner 初始化、生成 30 天 Node
Connection Token，并输出 Node 命令和 MCP URL。当前窗口保持运行，按 Ctrl-C
会停止临时 Hub 和 Tunnel。不要把 `--data-dir` 放在项目目录内。

在第二个终端执行输出的 Node 命令。Node 命令包含：

```text
--project-root <同一个项目根>
```

这是公开试用的安全边界。第一条请求建议使用：

```text
Inspect this repository and summarize its structure. Do not make changes.
```

MCP URL 的认证是 OAuth；输出的 Bearer credential 只用于 Node 登记。

## 需要公网 MCP 时

```bash
go run ./cmd/spiderctl share --project . --tunnel cloudflare
go run ./cmd/spiderctl share --project . --tunnel ngrok
```

先安装对应 Tunnel 客户端并确保它在 `PATH`。临时 URL 适合演示，不适合
长期地址。Tunnel 只转发 Hub，不要把 Node UI 或 Local Bridge 暴露出去。

## 手动路径

需要长期数据目录或分开管理 Hub 时：

1. 设置强且独立的 `FAST_SPIDER_ADMIN_PASSWORD` 并启动 Hub。
2. 使用 `spiderctl setup-url` 打开一次性 owner 初始化页面。
3. 登录 Web Console，在“连接令牌”页面创建 Connection Token。
4. 用 `go run ./cmd/node connect` 登记 Node。
5. 在设备和凭据不再使用时及时撤销。

Connection Token、MCP OAuth 和 Direct Access Key 是三个不同的信任域，不
能互换。

## 两种 Node 模式

### Project mode

使用 `--project-root`。Node 会在 capability 分发前限制文件读写、代码搜索、
Shell/Build cwd、Git 仓库和 worktree、工作上下文、附件，以及 AI 的明确
working directory、skill、mention 和本地图片路径；原生桌面/窗口截图被禁用。

Project mode 是路径约束，不是完整 OS 沙箱。Shell 解释器、Git remote、浏览器
网络、provider 凭据和子进程仍要单独审查。

### Machine mode

运行 Node 时不传 `--project-root`，保留原有整机 OS 用户边界。适用于私有高级
使用，不适用于不了解风险的公开 Tunnel。

## 开发验证

```bash
go test ./... -count=1
go vet ./...
git diff --check
```

更多用户安全说明见[中文安全模型](security-model.zh-CN.md)，Tunnel 说明见
[中文免费本地部署](free-local-deployment.zh-CN.md)。
