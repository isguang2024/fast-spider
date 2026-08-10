# 05 Node 能力设计

## 1. 通用规则

所有 Node 能力共享以下约束：

- 请求包含 `machineId`、`requestId`、`deadline` 和 `idempotencyKey`；目标字段按能力定义。
- Machine 是唯一远程资源边界；Node 是最终执行裁决者。
- Node 以当前 OS 用户运行，直接使用该用户对整台电脑的操作系统权限。
- 绝对路径是目标，不是 Fast Spider 授权对象；不维护目录注册、目录白名单或目录 ID。
- Descriptor 声明能力版本、平台、风险、最大输入/输出、是否可取消和是否允许离线继续。
- 同步小结果最大建议 1 MiB；超过后转 Job Event 或 Artifact；文本默认 UTF-8。

## 2. 能力总览

| capabilityId | 代表 actions | 风险 | 状态 |
|---|---|---:|---|
| `node.management` | status, capabilities, rotateCredential, disconnect | R0–R3 | 已实现 |
| `file.system` | read, readChunks, write, edit, move, copy, delete | R0–R4 | 已实现 |
| `code.search` | glob, grep | R0–R1 | 已实现 |
| `shell.process` | run, status, logs, cancel | R2–R4 | 已实现 |
| `git.repository` | status, diff, log, show, commit, pull, push, worktree | R1–R3 | 已实现 |
| `build.test` | list, run | R2–R3 | 已实现 |
| `artifact.transfer` | create, uploadChunk, complete, getMetadata | R1–R3 | 已实现 |
| `working.context` | get, set, clear | R0–R1 | 已实现 |
| `screenshot.capture` | listDisplays, desktop, display, listWindows, window | R1–R3 | 已实现 |
| `browser.automation` | launch, page.open/navigate/close, pages.list, click, type, press, wait, snapshot, screenshot, events, close | R2–R3 | 已实现 |
| `agent.control` | providers.list, models.list, projects.list, session.* | R1–R3 | 已实现 |
| `node.update` | check, download, install, rollback | R3–R4 | 已实现 |

## 3. Machine 管理

### `node.management.status`

请求为空或带 `includeCapacity`；响应包含版本、OS/arch、连接状态、uptime、资源组容量和运行 Job 数量。权限是 Machine 可见性，默认不需要逐次确认。

### `node.management.capabilities`

返回 Node 当前真实可执行的 Capability Descriptor。Node 不因为客户端输入伪造能力；缺少 Browser/Codex runtime 时只报告不可用状态。

### `node.management.disconnect`

请求包含 reason 和 duration/indefinite。Node 停止接收新 Job，按策略取消或保留当前 Job，然后断开连接。

## 4. 文件系统

### 公共请求

```json
{
  "machineId": "mach_opaque",
  "path": "C:/dev/example/internal/app.go",
  "expected": {
    "sha256": "optional-file-hash"
  }
}
```

`path` 必须是绝对路径。Windows 使用盘符、UNC、设备路径和大小写规则；Linux 使用绝对 POSIX 路径。Node 按当前 OS 用户权限检查目标，不把路径映射到其他逻辑根目录。

### actions

| Action | 请求/响应 | 风险 |
|---|---|---:|
| `read` | absolute path, offset, length, encoding → text/metadata | R0–R1 |
| `readChunks` | absolute path, chunkSize, cursor → data events | R0–R1 |
| `write` | absolute path, content/artifactRef, expected → Diff + metadata | R2 |
| `edit` | absolute path, exact replacements/ranges, expected → Diff + metadata | R2 |
| `mkdir` | absolute path, mode → metadata | R2 |
| `move` / `copy` | absolute from/to paths → progress/result | R2 |
| `delete` | absolute path, recoverable=true → recoveryId | R3 |
| `purge` | recoveryId/absolute path → result | R4 |

编辑要求唯一匹配或明确 range + expected hash；默认拒绝二进制和超大文件。删除默认进入 Node recovery-bin，永久删除是独立高风险 Action。

错误包括 `PATH_INVALID`、`PATH_NOT_FOUND`、`PATH_RACE_DETECTED`、`FILE_TOO_LARGE`、`FILE_BINARY`、`ENCODING_INVALID`、`MATCH_NOT_UNIQUE`、`DISK_QUOTA_EXCEEDED` 和 `RECOVERY_BIN_FULL`。错误不得隐式声称存在目录授权边界。

## 5. 代码搜索

`code.search.glob` 和 `code.search.grep` 接受绝对 `path` 或绝对搜索根、patterns、exclude、regex、case、contextLines、maxMatches 和 `respectGitignore`。结果中的 `path`、line、column、preview 都是结构化字段，最多返回 10,000 条并带 `truncated`。

优先使用受控的 ripgrep；缺失时使用有限 Go fallback。客户端不能直接传任意 ripgrep flags。搜索超时、取消和资源上限由 Node 执行。

## 6. Shell 与进程

```json
{
  "machineId": "mach_opaque",
  "cwd": "C:/dev/example",
  "mode": "argv",
  "executable": "go",
  "args": ["test", "./..."],
  "env": {"CI": "1"},
  "timeoutMs": 120000,
  "idempotencyKey": "idem_opaque"
}
```

`cwd` 必须是绝对路径；`mode=argv` 优先，`mode=shell` 必须显式选择 Node 支持的 profile。环境变量按显式字段和 Node 安全规则处理，不在远程请求中接受隐式继承开关。

返回 `jobId`，随后通过 Job Event 提供 stdout/stderr/progress/result。主要错误为 `EXECUTABLE_NOT_FOUND`、`SHELL_PROFILE_DENIED`、`CWD_INVALID`、`ENV_DENIED`、`PROCESS_LIMIT_EXCEEDED`、`TIMEOUT`、`CANCEL_INCOMPLETE` 和 `OUTPUT_LIMIT_REACHED`。

## 7. Git

`git.repository` 使用系统 Git，所有操作请求都包含绝对 `repositoryPath`。固定 action 包括 status、diff、stagedDiff、log、show、branches、currentBranch、worktrees、add、commit、fetch、pull、push、createWorktree 和 deleteWorktree；MCP 不接受任意 Git flags 字符串。

Node 使用当前 OS 用户的 Git 配置、凭据、LFS、签名和 hooks。remote URL、凭据 helper 输出和环境变量脱敏；commit/pull/push/worktree 等副作用进入 Job、审计和取消规则。主要错误为 `GIT_NOT_FOUND`、`NOT_A_REPOSITORY`、`DIRTY_WORKTREE`、`GIT_HOOKS_DISABLED`、`AUTH_REQUIRED`、`NON_FAST_FORWARD` 和 `MERGE_CONFLICT`。

## 8. 构建与测试

`build.test.run` 使用绝对 `cwd`、受控 `argv`、timeout 和 `idempotencyKey`；不通过相对路径或本机目录配置隐式选择目标。返回 Job、结构化摘要和报告 Artifact。`list` 只返回当前 Node 可用的构建模板和限制，不返回秘密或不必要的环境。

## 9. Artifact 与截图

Artifact 仍用于显式文件/日志传输，使用大小、类型、SHA-256、分块、偏移、恢复和保留策略；Hub 保存其元数据和内容寻址 Blob。截图走独立的 AI 展示通道：Node 本地生成后直接上传到 Hub Temporary Presentation Relay，不写数据库；Hub 在系统临时目录中最多保留 20 分钟，并由 MCP 直接返回 `ImageContent`，同时生成短期 `ResourceLink` 供下载。截图支持显示器、桌面、窗口和浏览器页面，窗口目标使用短期 opaque `windowId`，不返回 OS 句柄或临时路径。

## 10. Working Context

`working.context` 是项目级的轻量任务状态，不是长期 AI Memory。每个项目只保存一份当前开发任务快照，存放在 Node data-dir，不写入项目目录、不污染 Git；单份 JSON 最大 64 KiB。

固定 actions 为 `get`、`set`、`clear`。`set` 保存 goal、baseline branch/commit、completed、constraints、pending、keyFiles 和 facts；各列表最多 64 项，禁止放聊天原文、凭据、Token 或其它秘密。未显式提供 baseline 时，Node 会用当前 Git branch/HEAD 固化任务起点。`get` 同时实时读取当前 Git `isRepository/branch/HEAD/dirty`，因此保存的任务状态与当前代码事实保持分离。`clear` 只删除 Node 本地任务快照，不修改项目文件。

恢复上下文时推荐组合：聊天压缩摘要 + `working_context.get` + 当前 Git 事实 + 必要 keyFiles 重新读取。Git/文件始终是最终事实源，Codex Thread 只负责 Codex 自己的执行历史。

## 11. 浏览器自动化

Browser 使用 Node 管理的隔离 Profile，动作固定为 `launch`、`close`、`page.open`、`page.navigate`、`page.close`、`pages.list`、`click`、`type`、`press`、`wait`、`snapshot`、`screenshot`、`events`。

- Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS 目标均可访问。
- 不维护 Origin 白名单，不要求先登记地址。
- 仍拒绝 `file:`、危险 scheme、任意 JavaScript、CDP 和 Playwright 原始 API。
- 每动作、Session、Job、下载和结果都有超时、大小、并发和清理上限。

## 12. Agent 控制

Provider-neutral actions 为 `providers.list`、`models.list`、`projects.list`、`session.list`、`session.get`、`session.create`、`session.send`、`session.watch`、`session.cancel`、`session.result`、`session.rename` 和 `session.archive`。

当前实现本机 Codex `bridge_owned`：Node 直接启动 `codex app-server --stdio`，`session.create` 使用绝对 `workingDirectory`，Provider 凭据和本机认证状态不进入 Hub。若该目录属于 Git 仓库，Node 自动解析主工作树作为 `projectDirectory`，复用/注册对应的 Codex Desktop 本地项目，并把 linked worktree 会话绑定到该项目；`cwd` 仍保持实际执行目录。非 Git 目录不自动注册成项目。未指定 model 时从当前 `model/list` 选择实际可用模型；一个 Session 只允许一个 active Run。
