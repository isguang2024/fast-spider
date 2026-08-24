# 免费本地部署（中文）

[English](free-local-deployment.md)

本指南说明如何不租服务器，在自己的 Windows、macOS 或 Linux 电脑上运行
Fast Spider。Hub 可以只运行在本机，也可以在需要云端 MCP 时通过免费或
免费额度 Tunnel 暴露 Hub 的 HTTPS 地址。

## 推荐：一键 Project mode

在 Fast Spider 源码目录运行：

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```

这条路径会：

- Hub 监听 `127.0.0.1:8787`；
- 使用临时 data-dir 和随机强密码；
- 自动创建首个 owner 和临时 Node Connection Token；
- 输出 Node 登记命令、Console URL 和 MCP URL；
- 让 Node 使用 `--project-root` 绑定当前项目。

`share` 不会启动 Node；在第二个终端执行输出命令。MCP 使用 OAuth，输出的
Bearer credential 仅用于 Node 登记。按 Ctrl-C 会结束临时 Hub 和 Tunnel。

## 三种本地方案

| 方案 | 费用 | 适合场景 |
|---|---:|---|
| `--tunnel none` | 0 | 本机 Console、本地测试、同机 MCP 客户端 |
| `--tunnel cloudflare` | 0 或免费 | Cloudflare Quick Tunnel、短期公网 HTTPS 演示 |
| `--tunnel ngrok` | 0 或免费额度 | 快速公网演示、需要 ngrok 账号或地址的测试 |

### Cloudflare Quick Tunnel

先安装 `cloudflared` 并加入 `PATH`，再运行：

```bash
go run ./cmd/spiderctl share --project . --tunnel cloudflare
```

CLI 会启动 `cloudflared tunnel --url http://127.0.0.1:8787`，等待返回
`trycloudflare.com` 地址，并把这个地址写入 Hub 的 `public-base-url` 和
`allowed-hosts`。Quick Tunnel 地址是临时的，结束后会失效。

### ngrok

先安装 `ngrok`、完成所需账号配置并加入 `PATH`：

```bash
go run ./cmd/spiderctl share --project . --tunnel ngrok
```

CLI 通过本机 ngrok API 读取 HTTPS public URL。免费计划的域名、并发和流量
限制可能变化，适合短期测试。

## Windows 用户

在 PowerShell 的源码目录中运行：

```text
go run .\cmd\spiderctl share --project . --tunnel none
```

保持第一个窗口打开，再在第二个 PowerShell 窗口执行输出的 Node 命令。若
`cloudflared` 或 `ngrok` 已安装但提示不在 PATH，请把安装目录加入 PATH 后
重新打开终端。

如果是安装在源码目录外的 `spiderctl`，需要把 `fast-spider-hub` 放入 PATH，
或设置：

```text
set FAST_SPIDER_SOURCE_ROOT=C:\path\to\fast-spider
```

CLI 不会自动下载 Hub、Node 或 Tunnel。

## 安全边界

- 只让 Tunnel 指向 `http://127.0.0.1:8787`，不要绑定或转发 Node UI。
- `--data-dir` 必须在项目目录之外，避免 Project mode 读到 Hub 数据库和私钥。
- 公开地址加入 Hub `allowed-hosts`，并与 `public-base-url` 使用同一 hostname。
- Bootstrap token、Connection Token、OAuth token 和 Direct Key 不要发到公开
  Issue、Prompt 或日志。
- Project mode 只是路径约束，不会限制任意 Shell 解释器、浏览器网络、Git
  remote 或 provider 凭据；Machine mode 的风险更高。
- 演示结束后按 Ctrl-C，并在 Web Console 撤销未使用的设备、Token、Key 或 OAuth
  authorization。

## 常见失败

### Tunnel 客户端不在 PATH

安装对应客户端，确认当前终端能执行 `cloudflared --version` 或
`ngrok version`，再重试。`share` 不会静默安装依赖。

### public URL 或 allowed-hosts 不匹配

Cloudflare/ngrok 地址变化后应重新运行 `share`。手动启动 Hub 时，公开
hostname 必须同时出现在 `--allowed-hosts` 和 `--public-base-url`。

### ChatGPT 连接 MCP 失败

`--tunnel none` 只适合本地客户端。需要 ChatGPT 等云端客户端时使用 HTTPS
Tunnel，并在 MCP URL 完成 OAuth。Node Connection Token 不能替代 OAuth。

### Project 路径被拒绝

确认 Node 命令的 `--project-root` 与 `share --project` 指向同一个目录。不要
把它改成兄弟目录，也不要在公开试用中删掉这个参数切换到 Machine mode。
