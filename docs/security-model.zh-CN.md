# Fast Spider 安全模型

本文面向第一次使用 Fast Spider 的用户，解释各个入口和运行模式的信任
边界。它不是操作系统沙箱承诺，也不替代用于漏洞报告的
[SECURITY.md](../SECURITY.md)。

## 一句话理解

Fast Spider 是一个通过 Hub 访问本机自动化能力的系统。Hub 负责身份、路由
和审计；Node 在启动它的操作系统账户权限下执行真实工作。

公开试用推荐使用 **Project mode（项目模式）**：

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```

项目模式会在 Node 侧限制明确提交的路径，但不是容器、受限 Windows
Token、网络防火墙、密钥保险箱或完整的 OS 沙箱。

## 各入口能做什么

### ChatGPT 和其他 MCP 客户端

MCP 客户端完成 OAuth 授权后，可以发现并调用 Hub 暴露的工具。实际结果还
取决于账户、选择的 Node、能力、权限和 Node 运行模式。Node Connection
Token 不能直接当作 MCP 登录凭据。

在能力已开启时，MCP 请求可能触发本地文件操作、任务、Git、浏览器、附件
或本地 AI provider 调用。请把已授权的 MCP 客户端当成强力的自动化调用方。

### Direct API 客户端

Direct API 使用独立的临时 Direct Access Key。Key 可以是只读，也可以根据
实际需要授予文件写入、Shell、Git、浏览器、AI、上下文写入或附件写入等
scope。Direct Key 不是 OAuth token，Connection Token 也不能调用
`/direct/v1`。

### Hub

Hub 保存账户、设备、令牌、OAuth、审计和路由元数据，并在授权客户端与在线
Node 之间转发能力参数和结果。因此 Hub 可以看到请求元数据以及能力返回的
数据。不要把 Hub 运行在不信任的主机上。

Hub 不会直接挂载 Node 文件系统；本地文件访问发生在 Node 进程中，并使用
启动 Node 的操作系统账户。

### Node

Node 持有本机私钥，并执行文件、Shell、Build、Git、浏览器、截图、附件和
AI 操作。操作系统账户仍是整机最重要的信任边界。这个账户能够访问的本地
文件、进程、凭据和网络，Node 往往也能访问。

## Project mode 与 Machine mode

| 模式 | 推荐场景 | 限制内容 | 不代表什么 |
|---|---|---|---|
| Project mode | 公开上手、演示、普通项目 | 明确的文件/搜索路径、Shell/Build cwd、Git 仓库和 worktree、工作上下文、附件路径、AI 输入路径 | 不是 OS 隔离；不能阻止任意 Shell、浏览器网络、Git 凭据、provider 凭据或子进程行为 |
| Machine mode | 私有高级使用 | 现有 OS 账户、能力和 Hub 授权控制 | 没有项目目录边界 |

项目模式会解析配置的项目根，并在能力分发前检查规范化路径。已有路径必须
位于项目内；新文件必须使用项目内已有父目录；创建 Git worktree 必须明确
指定项目内目标。桌面、显示器和任意窗口截图会被禁用，因为项目路径无法
保护屏幕上的其他窗口。

检查放在 Node 能力边界，因此 MCP、Direct API、Local Bridge 和其他 Node
请求使用同一套路径策略。但普通用户进程仍可能遭遇检查与实际使用之间的
竞态、Windows reparse point 等 OS 风险。如果威胁模型要求机密隔离，应使用
低权限 OS 账户或真正的沙箱。

## 能力风险

### Shell 与 Build

项目模式只限制显式工作目录，不会让命令自动变安全。argv 仍可能调用解释器、
脚本、编译器、包管理器或子进程，并访问其他路径或网络。Build 也可能修改
项目并使用用户环境中的凭据。写权限 Key 必须私密，执行前要审查命令。

### Git

Git 可以修改历史、创建提交，并在明确允许时访问远程。Hooks、filters、凭据
助手和 remote 配置属于本地 Git 信任边界。项目模式限制仓库和明确的 worktree
路径，但不会把 push 变成只读操作。

### Browser

浏览器可以访问公网以及 Node 能访问的 HTTP(S) 服务，包括 localhost 和私网
服务。不要把浏览器当作无害的查看器；不要在 URL 中放凭据，并审查下载、登录
状态、重定向和可能产生副作用的页面。

### Screenshot

Machine mode 在图形环境可用时可以截取桌面、显示器和窗口，画面可能包含聊天、
密码、源代码或个人信息。Project mode 禁止原生桌面/显示器/窗口截图；浏览器
页面截图仍然需要考虑浏览器和附件风险。

### AI Control

AI Control 可以创建或继续 Codex、Claude Code 等本地 provider 会话。provider
和它的 harness 可能读取、修改、执行或发送项目内容。项目模式会检查明确的
working directory、skill、mention 和本地图片，但不能证明上游 provider 或
子进程具有不可绕过的 OS 沙箱。还应使用 provider 自身的审批、数据分享和
账户设置。

## 凭据类型与撤销

- **Bootstrap token**：创建首个 owner 的一次性设置秘密，只放在 setup URL
  fragment 中，并由 Hub 消费。不要放入 Issue、Prompt、截图或日志。
- **Connection Token**：登记 Node。`share` 只在输出中显示一次，用于打印
  的 Node 命令；它不是 MCP 或 Direct API 凭据。暴露后在 Web Console 撤销。
- **Device Token**：Node 运行时的短期凭据，不要复制或分享。
- **OAuth grant/token**：MCP 客户端的认证凭据，在 Web Console 撤销 OAuth
  client 或 authorization。
- **Direct Access Key**：`/direct/v1` 的凭据，具有独立 scope、有效期、可选
  设备绑定和频率限制，在 Web Console 撤销。
- **Node/Hub 私钥**：本地身份材料，数据目录必须私密，备份也要按敏感数据
  处理。

凭据泄露后，应立即停止分享相关 URL 或日志，撤销对应 token、Key、设备或
OAuth authorization，再创建替代凭据。撤销 Connection Token 不会自动撤销
已经登记的 Node；设备还需要单独撤销。

## 免费 Tunnel 风险

Tunnel 只改变网络可达性，不改变 Node 的本地权限。Hub 应保持 loopback，
Tunnel 只转发 Hub；`--allowed-hosts` 只加入精确的公开 hostname，
`--public-base-url` 必须使用同一个外部 URL，非 loopback 公网地址必须是 HTTPS。

Cloudflare Quick Tunnel 和默认 ngrok 地址可能变化。公开 Hub 仍需要有效认证，
但公网地址会受到扫描和凭据攻击。不要把 Node UI、Local Bridge socket 或
Machine-mode Node 通过 Tunnel 暴露出去。

## 推荐默认配置

1. 先运行 `share --project . --tunnel none`。
2. 测试时保持 Hub 和 Node 在 loopback 或可信 LAN。
3. 使用新的临时 profile 和唯一强密码。
4. 没有写入需求时优先只读 Direct Key 或最小 OAuth 权限。
5. 从安全检查 Prompt 开始，审查每个写入、Shell、Git、浏览器、附件和 AI
   动作。
6. 只有云端客户端需要访问时才使用 Cloudflare 或 ngrok，并在演示后撤销凭据。
7. 仅在可以接受整机 OS 账户边界时使用 Machine mode。
