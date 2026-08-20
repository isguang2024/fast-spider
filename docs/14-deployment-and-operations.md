# 部署与运维（0.4.23）

## Hub

生产主线只保留一个 `fast-spider-hub` systemd 服务。Hub 监听 loopback，由 Nginx/TLS 反向代理公网路径。生产关键配置：AllowedHosts、PublicBaseURL、OAuth redirect host allowlist、data-dir、release-dir。

Hub data-dir 与 release-dir 分离：数据库/密钥/Artifact 进入备份，Windows Node EXE 和大型组件不进入 Hub 数据备份。Temporary Presentation Relay 使用系统临时目录，不进入数据库或备份；新 Node 的 Browser/OS 截图与 `artifact_get.publishFile` 都使用 attachment 模式，MCP/Direct 只返回 URL 元数据，最长 TTL 为 48 小时，Hub 每分钟按 `expiresAt` 自动删除，单次上传上限均为 64 MiB。旧 Node 未携带 resource kind 时仍按 20 分钟 presentation 兼容语义处理。Hub 启动/退出会清理临时根，因此 48 小时是最长保留上限而不是跨 Hub 重启的持久化承诺。反向代理应对 Fast Spider 路径允许至少 64 MiB 请求体；Nginx 建议同时设置 `proxy_request_buffering off`，避免大图/临时文件先完整落到代理缓存。

0.4.20 增加独立 Direct API。生产 PublicBaseURL 为 `/fast-spider` 时，对外入口为 `GET /fast-spider/direct/v1/tools` 和 `POST /fast-spider/direct/v1/call`，仅接受后台生成的 `fsp_tmp_` Direct Access Key Bearer。Direct Key 与 OAuth、Connection Token 完全隔离；默认只读，高危 Scope 单独授权，高权限最长 24 小时、只读最长 7 天，可绑定单一 Machine 并设置每分钟限速。MCP 与 Direct API 对共有工具复用同一 `toolExecutor`，不得维护两套 Capability 参数映射；MCP-only `audit_log` 不进入 Direct Access Key 工具目录。

## Windows/macOS/Linux Node

Windows 对外交付 `fast-spider-node.exe`；macOS 交付 `darwin-arm64`（Apple Silicon）和 `darwin-amd64`（Intel）两个单文件客户端。macOS 下载后需要执行 `chmod +x fast-spider-node`。Windows 第一次运行后正式副本位于 `%LOCALAPPDATA%\FastSpider\bin\fast-spider-node.exe`；macOS 使用当前用户配置目录保存 Node 状态。

客户端只有“连接”和“本地配置”等本机运行设置，不需要登记目录。连接后的文件/进程权限就是当前 OS 用户权限。

### AI Runtime

Fast Spider 不捆绑用户的 AI 账号或 Provider：

- Codex Harness 要求本机已有可执行的 Codex CLI/app-server。
- Windows 上若 Codex Desktop 提供 `%LOCALAPPDATA%/OpenAI/Codex/bin/<runtime>/codex.exe`，Node 优先使用最近更新且可正常执行的 Desktop runtime，以保证它与 Desktop 写入的 `CODEX_HOME` 配置格式一致；否则回退到 `PATH`。可用绝对路径环境变量 `FAST_SPIDER_CODEX_EXECUTABLE` 明确覆盖该选择。
- 共享 app-server owner 的实验入口（仅测试分支/显式配置）：设置绝对路径环境变量 `FAST_SPIDER_CODEX_APP_SERVER_SOCKET` 后，Node 不再启动独立 `app-server --stdio`，而是通过 `codex app-server proxy --sock <path>` 转发 WebSocket RPC；Node 不写入 Codex SQLite/rollout，也不负责停止 socket owner。该 socket owner 需由外部先以 `codex app-server --listen unix://<path>` 启动。
- 该入口不等于自动接管当前 Codex Desktop。在 Windows 本机，Desktop 当前启动的 app-server 是由 Desktop 父进程管理的 stdio 子进程，没有公开给 FS 的 socket 地址；未获得可连接的 Desktop endpoint 前，不能宣称 FS 会话已经具备 Desktop 原生归档语义。
- Claude Code Harness 要求本机已有 `claude` CLI；Fast Spider 只探测版本/安全的 auth 配置并运行原生 Session。
- CC Switch 若安装，则 `~/.cc-switch/cc-switch.db` 作为只读 Routing SSOT；Fast Spider 不创建、迁移或修改该数据库，也不负责启动/更新 CC Switch。
- Node data-dir 中的 `agent/claude-code-sessions.json` 只是 Fast Spider 本地控制索引；Claude 原生会话、CC Switch 数据和 Provider 凭据仍由各自产品管理。

### 托盘

- 普通启动：通过隐藏启动路径打开 UI + 托盘；Windows 默认窗口大小为 `1280×860`，首次启动会在当前用户桌面创建 `Fast Spider Node.lnk`，已有快捷方式不覆盖。
- 关闭窗口：Node 继续运行。
- 托盘打开：重新显示 UI。
- 托盘退出：真正结束 Node。
- 自动启动：HKCU Run 执行同一 EXE 的 `ui --background`，隐藏进入托盘；若已有实例，后台启动静默退出，不唤起已有 UI。
- Windows 客户端即使使用控制台子系统构建，默认启动、后台启动和隐藏 UI 回退也会隐藏命令窗口；关闭该窗口不会再终止 Node 连接。

### WSL 生命周期

Windows Node 不在启动时无条件拉起 WSL。第一次通过 `shell_run` / `build_control` 执行真实 `wsl.exe` 工作命令时，Node 按发行版懒启动一个轻量 keepalive；同一发行版只保留一个 keepalive，后续命令直接复用现有 WSL VM。普通 Job 完成、超时或被取消只终止该 Job 的进程树，不主动关闭 WSL VM，也不停止已经在 WSL 内运行的 Docker/systemd 服务。

`wsl.exe --status`、`--list`、`--shutdown`、`--terminate`、`--unregister`、`--install`、`--update` 等管理命令不触发 keepalive。若用户显式 shutdown/terminate，keepalive 随发行版退出并从 Node 状态中回收；下一次真实 WSL 工作命令再按需启动。该策略只影响 Windows 上实际调用 `wsl.exe` 的 Job，非 WSL 命令和 Linux Node 不受影响。

## 更新

Hub 发布签名 Node manifest。Node 验证签名、SHA-256 和大小。Web 后台首页提供“下载最新版 Windows 客户端”入口，直接复用同一份 `windows-amd64` Release 下载路径，因此后台手动下载与 Node 自动更新共享同一个发布事实源。手动升级可立即替换；自动更新可检查/预下载。Windows 替换助手等待旧 PID 真正退出后替换原 EXE，再按原模式重启。新版本启动时必须先处理 `updates/ready.json`：只有 Ready/apply 成功返回“无需应用”且 marker 已不存在，才删除 `updates/<currentVersion>` 已消费 staging；Ready/apply 报错时保留现场，future pending staging 与 marker 也绝不清理。早于当前版本的 staging 继续由 stale cleanup 删除，未知/manual 目录保留；正式目录的 `.previous` 回滚副本位于 data-dir 之外，不参与这些清理。

0.4.14 新增轻量发布推送。新的 Node EXE 与 `version.txt` 完成发布对账后执行 `spiderctl node-update-push --release-dir /var/lib/fast-spider-releases --platform windows-amd64`。该命令只校验当前 release 的版本/SHA 并原子写入小型 `push.json`；Hub 通过现有 WSS heartbeat ACK 通知旧版在线 Node，不新增消息队列或常驻推送进程。Node 仍通过签名 manifest 下载并校验更新。Shell/Build Job、Browser Session、AI 活跃 Turn 或正在处理的 Capability Request 任一存在时只保留 Ready 更新并等待；全部任务结束且连续空闲 15 秒后才进入 release drain。Drain 只拒绝新的 Capability 请求并返回可重试 `NODE_UPDATING`，不会取消已经运行的任务；随后复用现有 Ready/StartApply/.previous/restart 链自更新。

0.4.4 在 Ready/consumed/stale update maintenance 全部完成后、runtime/listener 启动前执行一次 Windows legacy install artifacts cleanup。它仅在当前 executable basename 为 `fast-spider-node.exe` 时检查同级目录，只删除严格命名的 `.fast-spider-node.new-<32-hex-guid>.tmp`、普通 `.node-update-backup-path`，以及安全非 reparse `backups` 目录直接子级中符合 `fast-spider-node-<UTC timestamp>.exe` 或 `fast-spider-node-pre-<version>-<UTC timestamp>.exe` 的普通文件。未知文件、嵌套目录、symlink/reparse/junction 全部保留，`backups` 仅在确认为空时删除；当前 EXE 与 `fast-spider-node.exe.previous` 绝不触碰。清理失败只记录 warning，不阻塞 Node 启动。

大型组件按需安装到 Node data-dir 的 `components/<id>/<version>`。0.4.4 继续只支持 `browser` 与 `search-ripgrep` 两个 Managed Component，安装/更新必须由用户在本地组件中心明确触发，不允许输入任意组件 ID、路径或下载 URL。Browser 安装完成后自动写入 Sidecar 路径并重启 Node 运行时；search-ripgrep 安装后由下一次搜索从受管组件目录解析，不要求重启。运行时切换完成后复用组件管理器清理 ZIP 下载缓存和旧版本。

Windows Browser 组件发布包使用 `cmd/browserpack` 生成，组件根目录必须包含 Sidecar、Playwright、当前 Chromium/headless shell/ffmpeg 浏览器缓存以及 `node.exe`。Hub 发布路径为 `release-dir/components/browser/windows-amd64/component.zip` 和同目录 `version.txt`；Hub 会动态生成签名 manifest。组件版本的源码事实源是 `sidecar/browser/package.json` 中的 `fastSpider.componentVersion`，当前为 `1.62.1`，发布时的 `version.txt` 必须与它一致。Browser 网络策略或 Sidecar 协议变化必须提升组件版本并重打包；Node 会拒绝缺少当前 `fastSpider.sidecarProtocol` 的旧组件，发布门禁必须从打包后的安装目录运行真实 Browser E2E。

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

备份包含 Hub secrets，必须按敏感数据保存。升级生产前必须先生成并校验备份。普通 `fast-spider-<timestamp>.zip` 不属于 release rotation，`backup-prune` 永远不会识别或删除它。

正式 release backup 使用严格文件名 `pre-<semver>-<commit>.zip`，其中 semver 为无前导零的三段十进制版本，commit 为 7–40 位十六进制。标准闭环是“升级前创建 → Verify → 升级并验收 → 预览轮换 → 明确应用”：

```bash
# 用即将升级到的版本与当前来源提交生成标准名称
spiderctl backup --data-dir /var/lib/fast-spider --out /srv/backups/pre-<target-semver>-<source-commit>.zip
spiderctl backup-verify --file /srv/backups/pre-<target-semver>-<source-commit>.zip

# 仅在升级成功后执行；第一条只输出计划，不写磁盘
spiderctl backup-prune --dir <absolute-backup-dir> --keep 3
spiderctl backup-prune --dir <absolute-backup-dir> --keep 3 --apply
```

`backup-prune` 默认保留最新 3 份且只生成计划，只有 `--apply` 才删除。root 必须是已存在的绝对普通目录且不能是 symlink/reparse。它只枚举直接子级、最多接受 256 个标准候选，并在任何删除前对全部候选执行完整 Verify；任一匹配候选损坏、manifest 无效、不是普通文件或为 reparse 时整次零删除。排序使用 manifest `CreatedAt` 的 UTC 时刻从新到旧，同时间按 basename 升序稳定决胜。Apply 与同进程 `backup` 创建使用目录级串行化；Windows/Linux 在初检时从打开句柄冻结身份，并在每个 remove 前即时复核，身份改变或取消时保留当前及后续候选并返回准确的部分结果。其它平台在存在候选时明确 fail-closed。`fast-spider-pre-*.zip` 等历史异名、Hub binary backup、未知文件和子目录全部保留；删除阶段若个别文件失败，JSON 结果仍明确列出 bounded kept/planned/deleted basenames 与计数并返回错误。

## Release staging 清理

发布构建和服务器上传 staging 不进入正式 release-dir，也不应长期保留。清理命令显式接收“已经完成、验收且确认不再引用的最高版本”，默认只规划：

```bash
spiderctl staging-prune --dir <absolute-root> --layout local --through <last-completed-version>
spiderctl staging-prune --dir <absolute-root> --layout local --through <last-completed-version> --apply
spiderctl staging-prune --dir /tmp --layout server --through <last-completed-version> --apply
```

`local` 只识别直接子级 `release-<semver>` / `release-<semver>-<7..40hex>`；`server` 只识别 `fast-spider-<semver>` / `fast-spider-<semver>-<7..40hex>`。只规划/删除版本不高于 `--through` 的候选；future、未知目录、legacy deploy 名称和普通文件均保留。root 或匹配 candidate 为 symlink/reparse/junction、候选树内出现 reparse/非普通项、扫描超过 256 candidates / 10000 files / 16 GiB / depth 32，或删除前身份/内容变化时均 fail-closed；`--apply` 前会完整扫描并重新核对，默认无 `--apply` 时绝不写磁盘。

## 周期维护

缓存、临时目录、Artifact、Agent 索引与开发协作资料室的生命周期边界、自动/手动清理方式和定时执行建议统一见[缓存与生命周期维护](23-cache-and-lifecycle.md)。无人值守任务只应执行无副作用检查或 plan-only 命令；任何 `--apply` / `--yes` 删除都必须先固定保留期、绝对根目录、结果留存和失败告警。

## 升级验收

每次发布至少确认：

- Git 工作树与目标提交明确。
- `release-gate.sh --full` 通过。
- Hub/spiderctl/Node 构建哈希明确。
- systemd active/running。
- 本机与公网 livez/readyz 均 200。
- Node release manifest 的版本/哈希与正式 EXE 一致。
- ChatGPT OAuth + MCP tools/list 可获取当前工具。
- Direct API 未认证请求返回 401；临时只读 Key 可读取 `/direct/v1/tools`，高危调用无对应 Scope 返回 403；Machine-bound Key 无法访问其它 Machine；撤销或过期后立即返回 401。
- 已登录 Web 后台“MCP 调用诊断”除 initialize/tools/list/tools/call 外还显示最近一次通过 OAuth 的 MCP HTTP 请求时间；若 ChatGPT 正报告不可用但该时间不变化，优先判定为会话侧未发请求，而不是 Node/Hub 断线。
- ChatGPT App 在工具 Schema/描述变化后执行 Refresh；普通长会话中若 FastSpider_FS 命名空间缺失，先以唯一标记 `fsprobe` 过滤发现并只物化 `machine_list`，真实连接成功后再按当前动作加载 `capability_list`、`machine_get` 或业务工具。禁止为了健康检查一次加载全部 19 个 Schema；完整目录、连接入口与单工具分别不得超过 48 KiB、8 KiB、8 KiB。

0.3.x 完成权限模型收敛；0.4.2 正式交付 Task Workspace、多 AI Harness/CC Switch 只读 Routing、Managed ripgrep 与文件能力 2.0；0.4.3-0.4.6 收敛更新、backup 与 staging 生命周期；0.4.7/0.4.8 收敛 Browser 与 Codex runtime；0.4.9 交付 file_edit 响应瘦身、搜索稳定码/统计、host/WSL runtime、Agent/Browser readiness、持久 Session create 幂等与轻量 timing；0.4.10 收敛大型仓库静态 include 前缀下推；0.4.11 收敛 Artifact/MCP 原生回显与临时分享边界；0.4.12 引入调用侧 Thinking Team；0.4.13 将其协作资料室收敛到 Working Context 标准六文件与 CAS 写入协议；0.4.14 新增任务空闲保护的 Node 发布推送与真实 ready/busy heartbeat；0.4.15 补充 MCP 调用路由提示；0.4.16 将其收敛为 initialize 常驻能力地图、`capability_list` 按需指南和每 Owner 有界 MCP 诊断；0.4.17 针对 ChatGPT 长会话偶发丢失工具物化状态增加过滤式恢复协议与认证请求到达性诊断；0.4.18 补齐 OAuth 历史保留、Presentation/Artifact 可恢复清理、Release manifest 取消传播、staging 原子隔离、Node/Agent 代际生命周期和秘密发布门禁；0.4.19 在不改变 Node/WSS 协议与 17 个 Direct API 顶层工具的前提下，补齐完整工具摘要、底层 capability 映射、按 capability 读取细节和 Windows PowerShell/cmd 调用说明。0.4.20 增加直接访问密钥能力，并完善 Windows Node 隐藏启动、开机自启动、桌面快捷方式、托盘驻留和默认窗口尺寸。
