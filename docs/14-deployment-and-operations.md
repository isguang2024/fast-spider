# 部署与运维（0.4.5）

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

Hub 发布签名 Node manifest。Node 验证签名、SHA-256 和大小。Web 后台首页提供“下载最新版 Windows 客户端”入口，直接复用同一份 `windows-amd64` Release 下载路径，因此后台手动下载与 Node 自动更新共享同一个发布事实源。手动升级可立即替换；自动更新可检查/预下载。Windows 替换助手等待旧 PID 真正退出后替换原 EXE，再按原模式重启。新版本启动时必须先处理 `updates/ready.json`：只有 Ready/apply 成功返回“无需应用”且 marker 已不存在，才删除 `updates/<currentVersion>` 已消费 staging；Ready/apply 报错时保留现场，future pending staging 与 marker 也绝不清理。早于当前版本的 staging 继续由 stale cleanup 删除，未知/manual 目录保留；正式目录的 `.previous` 回滚副本位于 data-dir 之外，不参与这些清理。

0.4.4 在 Ready/consumed/stale update maintenance 全部完成后、runtime/listener 启动前执行一次 Windows legacy install artifacts cleanup。它仅在当前 executable basename 为 `fast-spider-node.exe` 时检查同级目录，只删除严格命名的 `.fast-spider-node.new-<32-hex-guid>.tmp`、普通 `.node-update-backup-path`，以及安全非 reparse `backups` 目录直接子级中符合 `fast-spider-node-<UTC timestamp>.exe` 或 `fast-spider-node-pre-<version>-<UTC timestamp>.exe` 的普通文件。未知文件、嵌套目录、symlink/reparse/junction 全部保留，`backups` 仅在确认为空时删除；当前 EXE 与 `fast-spider-node.exe.previous` 绝不触碰。清理失败只记录 warning，不阻塞 Node 启动。

大型组件按需安装到 Node data-dir 的 `components/<id>/<version>`。0.4.4 继续只支持 `browser` 与 `search-ripgrep` 两个 Managed Component，安装/更新必须由用户在本地组件中心明确触发，不允许输入任意组件 ID、路径或下载 URL。Browser 安装完成后自动写入 Sidecar 路径并重启 Node 运行时；search-ripgrep 安装后由下一次搜索从受管组件目录解析，不要求重启。运行时切换完成后复用组件管理器清理 ZIP 下载缓存和旧版本。

Windows Browser 组件发布包使用 `cmd/browserpack` 生成，组件根目录必须包含 Sidecar、Playwright、当前 Chromium/headless shell/ffmpeg 浏览器缓存以及 `node.exe`。Hub 发布路径为 `release-dir/components/browser/windows-amd64/component.zip` 和同目录 `version.txt`；Hub 会动态生成签名 manifest。

Windows ripgrep 组件发布包使用已准备好的本地可执行文件生成，不联网，也不从 `PATH` 探测：

```bash
go run ./cmd/ripgreppack --rg-exe <prepared rg.exe> --out <release-dir/components/search-ripgrep/windows-amd64/component.zip>
```

发布路径为 `release-dir/components/search-ripgrep/windows-amd64/component.zip` 与同目录 `version.txt`，ZIP 根目录只包含 `rg.exe`。Release operator 必须从官方或其它可信来源取得 ripgrep，在写入 release-dir 前自行固定版本并校验 SHA-256。Hub 根据 `component.zip`、`version.txt` 和发布签名密钥动态生成签名 manifest；Node 只通过组件管理器显式安装/更新，`code_search` 不会在搜索时联网下载，也不会信任系统 `PATH`。

## 备份

```bash
spiderctl backup --data-dir /var/lib/fast-spider --out /srv/backups/fast-spider-<timestamp>.zip
spiderctl backup-verify --file /srv/backups/fast-spider-<timestamp>.zip
```

备份包含 Hub secrets，必须按敏感数据保存。升级生产前必须先生成并校验备份。

正式 release backup 使用严格文件名 `pre-<semver>-<commit>.zip`，其中 semver 为无前导零的三段十进制版本，commit 为 7–40 位十六进制。新 backup 完成 `backup-verify` 且正式升级成功后，执行：

```bash
spiderctl backup-prune --dir <absolute-backup-dir> --keep 3
```

`backup-prune` 默认保留最新 3 份，root 必须是已存在的绝对普通目录且不能是 symlink/reparse。它只枚举直接子级、最多接受 256 个标准候选，并在任何删除前对全部候选执行完整 Verify；任一匹配候选损坏、manifest 无效、不是普通文件或为 reparse 时整次零删除。排序使用 manifest `CreatedAt` 的 UTC 时刻从新到旧，同时间按 basename 升序稳定决胜。`fast-spider-pre-*.zip` 等历史异名、Hub binary backup、未知文件和子目录全部保留；删除阶段若个别文件失败，JSON 结果仍明确列出 bounded kept/deleted basenames 与计数并返回错误。

## 升级验收

每次发布至少确认：

- Git 工作树与目标提交明确。
- `release-gate.sh --full` 通过。
- Hub/spiderctl/Node 构建哈希明确。
- systemd active/running。
- 本机与公网 livez/readyz 均 200。
- Node release manifest 的版本/哈希与正式 EXE 一致。
- ChatGPT OAuth + MCP tools/list 可获取当前工具。

0.3.x 完成权限模型收敛；0.4.2 正式交付 Task Workspace、多 AI Harness/CC Switch 只读 Routing、Managed ripgrep 与文件能力 2.0；0.4.3/0.4.4 收敛 Node 更新文件生命周期；0.4.5 增加显式、先 Verify 后删除的标准 release backup retention，不改其它 retention 常量。
