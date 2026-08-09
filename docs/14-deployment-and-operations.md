# 14 部署与运维

## 1. 当前原则

Fast Spider 面向单 Owner 自托管使用，运维目标是“少进程、启动方式唯一、坏了容易恢复”，不是建设多套安装体系。

当前只维护：

- 一个 Hub 进程；
- 每台 Node 一个当前用户进程；
- 一个明确的数据目录；
- 一个备份/恢复工具链。

Local Bridge、Browser sidecar 生命周期和 Codex `app-server --stdio` 都由 Node 管理，不再要求人工启动 agent-service、Codex daemon、Local Bridge helper 或额外隧道。

## 2. 开发与生产分开

开发阶段允许：

```bash
go run ./cmd/hub ...
go run ./cmd/node ...
go run ./cmd/spiderctl ...
```

正式长期运行只使用构建后的二进制，不把 `go run`、临时 PowerShell/Bash 脚本或多个启动器当作生产入口。

建议二进制命名：

```text
fast-spider-hub
fast-spider-node
spiderctl
```

## 3. Hub 唯一生产入口

Linux Hub 推荐：**一个二进制 + 一个 systemd unit**。

核心启动命令始终等价于：

```bash
fast-spider-hub \
  --listen 127.0.0.1:8787 \
  --data-dir /var/lib/fast-spider \
  --allowed-hosts sharedservices.example.com,127.0.0.1 \
  --public-base-url https://sharedservices.example.com/fast-spider
```

公网只由现有 Caddy/Nginx/其他 TLS 反向代理暴露 443：

```text
Internet :443
→ TLS reverse proxy
→ 127.0.0.1:8787 fast-spider-hub
```

Hub 本身不需要为了 Fast Spider 再部署数据库服务、Redis、队列或内部微服务。

仓库提供最小 unit 模板：`packaging/systemd/fast-spider-hub.service`。默认二进制路径为 `/usr/local/bin/fast-spider-hub`，运行用户为 `fast-spider`；`StateDirectoryMode=0700` 固化 Hub data-dir 权限，避免 systemd 重启时恢复为默认 0755。生产使用共享域 path-prefix 时，在 `/etc/fast-spider/hub.env` 配置：

```text
FAST_SPIDER_ALLOWED_HOSTS=sharedservices.example.com,127.0.0.1
FAST_SPIDER_PUBLIC_BASE_URL=https://sharedservices.example.com/fast-spider
FAST_SPIDER_OAUTH_REDIRECT_HOSTS=chatgpt.com,localhost,127.0.0.1,::1
```

`FAST_SPIDER_PUBLIC_BASE_URL` 是 Hub 对外的 base，不包含 `/mcp`；它用于 OAuth resource/issuer/redirect discovery，必须与反向代理公开路径一致。

模板不包含安装器或更新逻辑，只负责启动这一个进程。

## 4. Node 本地入口

Windows 日常使用直接双击 `fast-spider-node.exe`。客户端会打开一个简单本地窗口，在“连接”页填写 Hub 地址、后台生成的连接令牌和设备名称即可；无需客户端网页登录。源码/其他桌面环境也可显式运行：

```bash
fast-spider-node ui --data-dir <node-data-dir>
```

连接令牌属于 Owner，不绑定某一台设备；同一个仍有效的令牌可以给当前账户下多台 Node 重复登记。Node 登记成功后不会把令牌写入状态或本地配置，只保存自己的设备身份和 Hub 指纹，随后使用设备私钥持续连接。

CLI 保留给高级或无界面场景：

```bash
fast-spider-node connect \
  --hub https://sharedservices.example.com/fast-spider \
  --token <connection-token> \
  --name <machine-display-name> \
  --data-dir <node-data-dir>
```

无界面常驻仍可直接运行：

```bash
fast-spider-node run --data-dir <node-data-dir>
```

需要 Browser Capability 时再增加：

```text
--browser-sidecar-dir <sidecar/browser>
```

Local Bridge 默认随 Node 启动；桌面客户端可在“本地配置”页直接关闭。无界面模式仍支持 `--disable-local-bridge`。

本地控制 UI 不引入 Electron、Wails、托盘服务或第二套 Agent。Windows 使用当前 Node 进程在 `127.0.0.1` 提供只面向本机的管理页面，并用 Edge application window 打开。同一 data-dir 的 `ui/run/connect` 共用轻量运行锁，避免两个进程用同一设备密钥争抢 Hub generation；重复双击只重新打开已有 UI。迁移时如果旧 CLI `run` 仍在，UI 可以打开但不会启动第二条设备连接，并会提示先停止旧进程。关闭窗口不等于退出由 UI 持有的 Node，需要停止时使用“退出客户端”。Windows 自启动如果实际需要，可由当前用户登录启动项/任务计划管理同一个客户端；Linux Node 可继续用 user-level systemd 运行 `fast-spider-node run`。模板不覆盖 PATH 或 umask。

## 5. 数据目录

### Hub

Hub 的所有持久事实收口到一个目录，例如：

```text
/var/lib/fast-spider/
├─ hub.db
├─ secrets/
├─ artifacts/
└─ bootstrap-token   # 仅初始化阶段可能存在
```

不要把数据库和 Artifact 分散到不同临时脚本目录。备份与恢复以这个 data-dir 为唯一边界。

### Windows Node

默认由 `os.UserConfigDir()` 推导当前用户目录，结构由 Node 自己维护：

```text
FastSpider/node/
├─ config.json        # 仅本机客户端设置，不含连接令牌
├─ state.json
├─ secrets/
├─ workspaces.json
├─ jobs/
├─ artifacts/
├─ browser/
└─ local/bridge.sock
```

Node data-dir 不应放到多人共享网络盘。

## 6. 首次上线

最小流程：

1. 启动 Hub，并确认 `/livez`、`/readyz`。
2. 在服务器本机生成一次性设置链接：

   ```bash
   spiderctl setup-url \
     --public-url https://sharedservices.example.com/fast-spider \
     --bootstrap-token-file /var/lib/fast-spider/bootstrap-token
   ```

3. 直接在浏览器打开输出的 `/setup#code=...` 链接，创建管理员用户名和密码；fragment 不会进入普通 HTTP/Nginx access log，设置完成后 bootstrap code 立即失效并删除。
4. 打开 `/app` 后台，确认 Hub 状态并创建一个连接令牌。
5. 双击 Windows `fast-spider-node.exe`（或运行 `fast-spider-node ui`），在本地“连接”页填写 Hub、连接令牌和设备名称。
6. Node 完成机器登记并自动上线；后台设备列表出现该机器。连接令牌输入值不会保存到 Node。
7. 在本地“工作区”页添加目录并勾选需要的 `write/shell/git-*/build` 权限；这些路径和权限只保存在该客户端。
8. 后续客户端自动使用本机设备身份重连；设备被后台撤销后，可以继续使用任一有效连接令牌重新登记，Workspace Registry 不需要删除或重建。无界面机器仍可使用 `connect/run` CLI。

后台 `/app` 管理设备、MCP OAuth 授权、已授权客户端、连接令牌和账户密码。连接令牌明文只在创建页展示一次，但令牌本身不是一次性消费品：只要仍有效且未撤销，就可以登记该 Owner 下多台 Node；它只允许机器登记。GPT 不使用连接令牌，继续走标准 OAuth。修改密码会注销其他 Web Session，但保留当前会话和已批准的 OAuth 授权。`spiderctl` 只保留 setup URL、备份/校验/恢复和版本命令，不再提供 Owner Token、enrollment 或机器管理兼容命令。任何 Token、密码或 Session Cookie 都不写进仓库或普通日志。

migration `005_connection_tokens.sql` 会清空全部旧 Owner API/PAT Token、清理旧无 authorization 的 OAuth Token 和旧 `device-connect` OAuth 客户端，并删除 `enrollment_tokens` 表；随后把空的历史 `owner_api_tokens` 表重命名为 `connection_tokens`。升级后必须从后台重新生成 `ctk_` 连接令牌，这是一次明确收敛，不保留长期兼容分支。

## 7. 健康检查

Hub：

```text
GET /livez
GET /readyz
```

- `/livez`：进程可响应。
- `/readyz`：Hub 当前能正常提供服务。

Node 没有单独开放健康端口；使用 `fast-spider-node status`、Hub machine 状态和本机日志判断。

健康检查按 Hub/Node 进程粒度，不为每个 Workspace/路由建立探活，避免无意义资源消耗。

## 8. 日志

Hub/Node 当前输出结构化日志到 stdout/stderr。长期运行时交给 systemd/journald 或操作系统现有日志设施收集即可。

原则：

- 不再额外写一套无限增长的应用日志文件；
- 不记录密码、Web Session Cookie、OAuth Access/Refresh Token、Owner/Enrollment/Provider Token；
- 不记录 Node 未脱敏绝对路径到公网接口；
- Debug 日志只在排障时开启；
- 日志保留/轮转由现有系统日志设施负责。

## 9. 反向代理与防火墙

Hub 主机：

- 公网只开放 HTTPS/WSS 443；
- 8787 保持 loopback；
- 共享域只代理 Fast Spider 自己的 path-prefix，不接管根路径或其他服务路径；
- `/node/v1/connect` 允许 WebSocket upgrade，idle timeout 不应过短；
- 带 path-prefix 的 MCP OAuth 额外代理两个标准 discovery 路径：`/.well-known/oauth-protected-resource/<prefix>/mcp` 与 `/.well-known/oauth-authorization-server/<prefix>`；
- 不缓存认证、OAuth、MCP、Node WSS 或私有 Artifact 响应；
- Access log 脱敏 Authorization、Cookie 和敏感 query；首次设置 code 使用 URL fragment，不进入服务器请求行。

Node：

- Fast Spider 不要求公网/局域网入站端口；
- Local Bridge 使用当前用户 AF_UNIX/UDS；
- 出站访问 Hub HTTPS/WSS 及用户实际执行的构建/浏览器网络即可。

## 10. 启停与重启

Hub 停止时应给进程正常 SIGTERM/服务停止信号，让 HTTP server 和 SQLite 正常关闭。

Node 停止时会取消本机 Job、关闭 Browser、Codex stdio 和 Local Bridge，然后退出。

快速重启只重启对应的**一个正式进程**；不需要按顺序人工恢复多个 helper。

## 11. 版本检查

```bash
fast-spider-hub --version
fast-spider-node version
spiderctl version
```

升级前后先记录版本，避免“换了文件但不知道实际运行哪一版”。

## 12. 备份与恢复

Hub 备份：

```bash
spiderctl backup --data-dir /var/lib/fast-spider --out /srv/backups/fast-spider.zip
spiderctl backup-verify --file /srv/backups/fast-spider.zip
```

恢复：

```bash
spiderctl restore --file /srv/backups/fast-spider.zip --data-dir /var/lib/fast-spider-restored
```

详细一致性规则见 [16-update-and-recovery.md](16-update-and-recovery.md)。

Node 的 Workspace Registry/设备状态体积较小；当前不另外建设 Node 云备份。Node 丢失时在后台创建一个新连接令牌并重新执行 `connect`，随后按需重新授权 Workspace。真正需要保存 Node data-dir 时直接使用操作系统现有备份工具即可。

## 13. 升级

当前升级流程保持人工可观察：

```text
检查版本
→ Hub backup + verify
→ 停服务
→ 替换二进制
→ 启动
→ /livez + /readyz
→ Node 重连与关键 smoke
```

不自动下载、不自动安装、不自动提权。数据库 migration 仍由 Hub 内置 runner 在启动时执行。

## 14. 卸载

当前不需要平台专用卸载器：

1. 停止对应进程/systemd/autostart 项。
2. 删除二进制。
3. 根据用户选择保留或人工删除 data-dir。

程序卸载默认不删除用户数据。

## 15. 当前明确不维护的部署方式

为避免运维分叉，MVP 不同时维护：

- Docker/Kubernetes 主路径；
- Windows SYSTEM service + 每用户进程两套方式；
- MSI/EXE 安装器与脚本安装器并存；
- 托盘常驻 UI；
- 独立 agent-service/daemon；
- 自动更新服务；
- 多 Hub 实例、共享数据库或分布式锁。

出现真实需求后再增加一个明确方案，并同时删除被替代的旧主路径。
