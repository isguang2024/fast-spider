# 部署与运维（0.4.0）

## Hub

生产主线只保留一个 `fast-spider-hub` systemd 服务。Hub 监听 loopback，由 Nginx/TLS 反向代理公网路径。生产关键配置：AllowedHosts、PublicBaseURL、OAuth redirect host allowlist、data-dir、release-dir。

Hub data-dir 与 release-dir 分离：数据库/密钥/Artifact 进入备份，Windows Node EXE 和大型组件不进入 Hub 数据备份。Temporary Presentation Relay 使用系统临时目录，不进入数据库或备份，Hub 启动/退出会清理，单个资源 TTL 为 20 分钟、单次上传上限 64 MiB。反向代理应对 Fast Spider 路径允许至少 64 MiB 请求体；Nginx 建议同时设置 `proxy_request_buffering off`，避免大图/临时文件先完整落到代理缓存。

## Windows Node

对外只交付一个 `fast-spider-node.exe`。第一次运行后正式副本位于 `%LOCALAPPDATA%\FastSpider\bin\fast-spider-node.exe`。

客户端只有“连接”和“本地配置”等本机运行设置，不需要登记目录。连接后的文件/进程权限就是当前 Windows 用户权限。

### AI Runtime

Fast Spider 不捆绑用户的 AI 账号或 Provider：

- Codex Harness 要求本机已有可执行的 Codex CLI/app-server。
- Claude Code Harness 要求本机已有 `claude` CLI；Fast Spider 只探测版本/安全的 auth 配置并运行原生 Session。
- CC Switch 若安装，则 `~/.cc-switch/cc-switch.db` 作为只读 Routing SSOT；Fast Spider 不创建、迁移或修改该数据库，也不负责启动/更新 CC Switch。
- Node data-dir 中的 `agent/claude-code-sessions.json` 只是 Fast Spider 本地控制索引；Claude 原生会话、CC Switch 数据和 Provider 凭据仍由各自产品管理。

### 托盘

- 普通启动：打开 UI + 托盘。
- 关闭窗口：Node 继续运行。
- 托盘打开：重新显示 UI。
- 托盘退出：真正结束 Node。
- 自动启动：HKCU Run 执行同一 EXE 的 `ui --background`，隐藏进入托盘；若已有实例，后台启动静默退出，不唤起已有 UI。

## 更新

Hub 发布签名 Node manifest。Node 验证签名、SHA-256 和大小。Web 后台首页提供“下载最新版 Windows 客户端”入口，直接复用同一份 `windows-amd64` Release 下载路径，因此后台手动下载与 Node 自动更新共享同一个发布事实源。手动升级可立即替换；自动更新可检查/预下载。Windows 替换助手等待旧 PID 真正退出后替换原 EXE，再按原模式重启。Node 启动时清理早于当前版本的 staging 目录和已消费的 `ready.json`，新版本 staging 只保留当前/待升级版本，避免长期累积升级 EXE；正式目录仍保留一个 `.previous` 回滚副本。

大型组件按需安装到 Node data-dir 的 `components/<id>/<version>`。Browser 组件通过本地 UI 的“安装 / 更新 Browser”下载；安装完成后自动写入 Sidecar 路径并重启 Node 运行时，不需要用户手填目录。运行时切换完成后删除组件 ZIP 下载缓存和旧组件版本，只保留当前已启用组件，避免组件包与解压目录双份长期占盘。

Windows Browser 组件发布包使用 `cmd/browserpack` 生成，组件根目录必须包含 Sidecar、Playwright、当前 Chromium/headless shell/ffmpeg 浏览器缓存以及 `node.exe`。Hub 发布路径为 `release-dir/components/browser/windows-amd64/component.zip` 和同目录 `version.txt`；Hub 会动态生成签名 manifest。

## 备份

```bash
spiderctl backup --data-dir /var/lib/fast-spider --out /srv/backups/fast-spider-<timestamp>.zip
spiderctl backup-verify --file /srv/backups/fast-spider-<timestamp>.zip
```

备份包含 Hub secrets，必须按敏感数据保存。升级生产前必须先生成并校验备份。

## 升级验收

每次发布至少确认：

- Git 工作树与目标提交明确。
- `release-gate.sh --full` 通过。
- Hub/spiderctl/Node 构建哈希明确。
- systemd active/running。
- 本机与公网 livez/readyz 均 200。
- Node release manifest 的版本/哈希与正式 EXE 一致。
- ChatGPT OAuth + MCP tools/list 可获取当前工具。

0.3.x 完成权限模型收敛；0.4.0 增加多 AI Harness/CC Switch 只读 Routing，但不新增常驻服务或第二套权限/凭据管理面。
