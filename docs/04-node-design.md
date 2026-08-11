# 04 Node 设计

## 1. Node 的角色

`fast-spider-node` 是真实机器执行面，也是 Machine 的最终执行裁决者。Hub、MCP Client 或本机 Local Bridge 不能改变 Node 的设备身份、参数校验、资源上限或当前 OS 用户权限。

Node 默认：

- 作为普通用户桌面/后台应用运行。
- 只建立到 Hub 的 HTTPS/WSS 443 出站连接。
- 不监听公网或局域网端口；本地控制 UI 只绑定 `127.0.0.1`。
- Local Bridge 默认随 Node 启用；不需要时可在本地关闭。
- 不自动提权、不隐蔽运行，显示连接状态和正在执行的高风险操作。

## 2. 进程模型

MVP 使用单 Node 进程。Windows 的 Edge app window、系统托盘、CLI 和 Node 本地控制 UI 都由同一进程提供；Linux 可用当前用户的 systemd unit。Local Bridge、Browser sidecar、Codex `app-server --stdio` 和 Claude Code CLI 子进程都由 Node 管理；CC Switch 仍是用户已有的独立 Routing Runtime，Fast Spider 只读其数据库事实，不启动第二个 CC Switch 服务。

同一 OS 用户只能持有一个 Fast Spider Node 主实例。实例锁与 data-dir 无关，因此复制 EXE、修改启动目录或切换 `--data-dir` 都不能建立第二个 Node；重复 UI 启动会短暂等待现有本地 UI 上线，打开已有界面后立即退出新进程。

## 3. 模块

| 模块 | 责任 |
|---|---|
| Local UI | loopback-only 连接页和本机设置；不承载远程 Capability |
| Connection Manager | Hub 信任、认证、WSS、重连、心跳和协议协商 |
| Request Dispatcher | Schema、deadline、幂等、目标字段和路由校验 |
| Local Policy | 当前 OS 用户权限、参数、网络、并发和资源限制 |
| Job Manager | 并发、状态、事件、取消、恢复和孤儿进程回收 |
| Capability Engine | 统一能力注册和执行接口 |
| Platform Layer | Windows/Linux 文件、进程、截图和凭据差异 |
| Local Bridge | 当前用户 AF_UNIX/UDS Adapter |
| Agent Adapters | Codex/Claude Code 等 AI Harness 的独立适配器；Provider-specific action 不强行统一 |
| CC Switch Inspector | 只读 Routing SSOT，提供脱敏 Provider/Model/Health/Takeover/Request facts 与 EffectiveCapabilities |

Node 不维护目录注册表、目录授权、路径白名单或目录 ID 到根目录的映射。

## 4. 本地状态

建议目录：

```text
<os.UserConfigDir()>\FastSpider\node\
├─ config.json             # 本机 UI/连接设置，不含 connection token
├─ state.json              # machineId / Hub 信任信息
├─ secrets/
├─ jobs/
├─ artifacts/
├─ browser/
├─ agent/claude-code-sessions.json   # 仅 Claude Session 控制索引，不复制完整对话
└─ local/bridge.sock
```

Linux 使用 `~/.local/share/fast-spider/`、`~/.config/fast-spider/` 和 `~/.cache/fast-spider/`。本地状态只保存 Machine 身份、设备凭据引用、Job/事件摘要、Browser/Bridge 设置、更新状态和必要的恢复信息；不保存 Hub 密码、连接令牌或 Provider Token。

## 5. 目标字段与 OS 权限

每个能力直接接收自己的绝对目标：

| 能力 | 目标字段 |
|---|---|
| `file_read` / `file_edit` / `code_search` | 绝对 `path`；搜索根和匹配结果都使用绝对路径 |
| `shell_run` | 绝对 `cwd`、结构化 `argv[]` |
| `build_control` | 绝对 `cwd`、受控构建命令和超时 |
| `git_control` | 绝对 `repositoryPath` |
| `ai_control.session.create` / `session.send` / `session.fork` / `session.settings.update` 与 Skill/Hook/Permission/Plugin discovery | 需要时使用绝对 `workingDirectory`；Skill/Mention/localImage 也使用绝对本机路径 |

Node 按平台语法、目标类型、文件/进程存在性、符号链接行为、大小、编码、网络和资源限制校验这些字段，然后以当前 OS 用户执行。绝对路径不再被改写成相对路径，也不需要先注册或加入目录名单。

路径安全检查仍然必要，但目的变为避免 NUL、设备路径、非法参数、TOCTOU、意外删除和资源耗尽，不是把机器限制在某个目录内。

## 6. Capability Engine 与 Job

Capability Descriptor 声明 capabilityId、版本、actions、输入上限、输出模式、平台条件和风险等级。Dispatcher 先校验协议与本机 OS/资源条件，再调用能力；所有长输出走 EventSink，大结果转 Artifact。

默认资源组：`read`、`write`、`exec`、`browser`、`capture`、`agent`。这些是并发和资源上限，不是目录授权。deadline 不能超过 Node action 上限；cancel 是幂等请求；Shell 使用 Windows Job Object 或 Linux process group/session 管理完整进程树。

## 7. 文件、Shell、Git 与 Build

- 文件写入使用临时文件、flush、expected hash/revision 和原子替换；编辑要求唯一匹配，默认拒绝二进制和过大文件。
- Shell 只接受显式 `argv[]` 与绝对 cwd；需要 `cmd.exe`、PowerShell 或 bash 时，shell executable 本身必须明确出现在 argv 中，不存在另一条任意 command-string 协议。
- Git 使用系统 `git`，`repositoryPath` 必须是绝对路径；读写、网络、副作用、hooks/filter 风险进入 Action 和审计，不额外创建目录权限。
- Build 使用绝对 cwd 和受控 argv/timeout；Windows 仅额外接受纯盘符 `C:`/`D:`/`V:` 作为对应盘符根目录的简写，`V:folder` 等 drive-relative 形式仍拒绝。配置文件可以保存默认值，但远程请求不能以其他相对路径隐式选择目录。

## 8. 浏览器与 AI

浏览器不叠加 Fast Spider Origin 白名单。Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS 目标均可作为导航目标；仍拒绝危险 scheme、任意 CDP/Playwright API 和把浏览器变成通用远控的能力。Browser Session、页面和截图受 Job、大小、超时和清理规则约束。

AI 控制先选择 Harness，再解释 Routing：`providerId=codex|claude_code`；`routing.status` 独立只读 CC Switch SSOT。Harness model、客户端 alias 与真实 upstream model 是不同事实，`EffectiveCapabilities` 按 Harness、转换层、upstream 与 Fast Spider policy 取交集，未知项保持 unknown。

Codex 通过本机 `codex app-server --stdio` 运行，保留 Provider/Model、Skills/Hooks/Permission Profiles/Plugins/MCP 状态、原生多类型 Turn、`outputSchema`、steer/respond、Thread/Goal/Settings/Review 与自动 `thread/resume`。Codex 自带 fs/command/process/MCP tool-call 不通过 Agent Adapter 暴露。

Claude Code 通过原生 CLI `stream-json` 运行：首 Turn 使用 Session UUID，后续 `--resume`；Prompt 走 stdin，Session 同时只允许一个 active CLI 进程。Node 只保存小型 Claude Session 控制索引和脱敏 RouteSnapshot，不复制完整 Prompt/对话。Claude 第一版只映射已验证的 text/session lifecycle，不开放 permission bypass，也不把 Codex Skill/Image/Mention 结构伪装成 Claude 输入。

CC Switch SQLite 使用只读连接，Provider raw settings/meta、凭据和原始 endpoint 不进入 Hub。Provider Token、本机认证和 CC Switch secret 均留在本机。Codex 0.141.0 app-server 当前没有公开 Automation API，因此 Node 仍不逆向其内部存储或桌面 UI。

## 9. Local Bridge 与紧急控制

Windows/Linux 使用当前用户 data-dir 下的 AF_UNIX/UDS。当前 OS 用户和文件系统 ACL 是本机信任边界；不注册 Local Client，不创建独立 Grant/Lease/Approval。Bridge 请求进入与远程请求相同的 Dispatcher、Capability、Job、资源和审计链路。

本地 UI 可以查看 Machine ID、连接、能力、运行 Job、Browser/Bridge 状态和版本；紧急控制包括停止 Node、断开 Machine、关闭单项 Capability 或终止 Job，但不提供目录授权操作。
