# 05 Node 能力设计

## 1. 通用规则

所有 Node 能力共享以下约束：

- `capabilityId` 使用稳定、小写、点分命名；`action` 使用动词或动词短语。
- 请求必须包含 machineId、workspaceId（不适用时为空）、requestId、deadline 和 idempotencyKey。
- Descriptor 声明能力版本、平台、风险、最大输入/输出、是否可取消、是否允许离线继续。
- Hub 与 Node 都校验权限；Node 是最终裁决者。
- 同步小结果最大建议 1 MiB；超过后转 Job Event 或 Artifact。
- 所有文本默认 UTF-8；二进制不嵌入 JSON。
- 高风险操作可由 Hub 策略、本地策略或两者要求确认。
- `deadline` 不能超过 Action 的本地上限。

## 2. 风险等级

| 等级 | 含义 | 默认策略 |
|---|---|---|
| R0 | 发现与只读元数据 | 已授权机器可直接执行 |
| R1 | Workspace 内只读 | 需要 Workspace read 权限 |
| R2 | 可恢复写入或受限执行 | 独立 Action 权限，完整审计 |
| R3 | 外部副作用/删除/真实登录态 | 短期 Lease 或确认 |
| R4 | 提权、永久删除、全局系统修改 | MVP 默认拒绝或必须本机确认 |

## 3. 能力总览

| capabilityId | 代表 actions | 风险 | MVP |
|---|---|---:|---|
| `node.management` | status, capabilities, rotateCredential, disconnect | R0–R3 | Phase 1 |
| `workspace.management` | list, registerLocal, update, disable, revoke | R0–R3 | Phase 2 |
| `file.system` | list, stat, read, write, edit, move, copy, delete | R1–R4 | Phase 2–3 |
| `code.search` | glob, grep | R1 | Phase 2 |
| `shell.process` | run, status, logs, cancel | R2–R4 | Phase 3 |
| `git.repository` | status, diff, log, show, commit, pull, push, worktree | R1–R3 | Phase 4 |
| `build.test` | runProfile | R2 | Phase 4 |
| `artifact.transfer` | create, uploadChunk, complete, getMetadata | R1–R3 | Phase 3–4 |
| `screenshot.capture` | desktop, display, window, page | R3 | Phase 5 |
| `browser.automation` | launch, navigate, click, type, snapshot, close | R2–R3 | Phase 5 |
| `agent.control` | providers, models, projects, session.*, run.* | R1–R3 | Phase 6 |
| `local.client` | register, list, revoke | R2–R3 | Phase 6 |
| `node.update` | check, download, install, rollback | R3–R4 | Phase 7 |

## 4. 节点管理

### 4.1 `node.management.status`

- 请求：空参数或 `includeCapacity`。
- 响应：版本、OS/arch、连接状态、uptime、资源组容量、Workspace/Job 数量。
- 权限：机器可见权限；R0。
- 确认：否。
- 超时：5 秒。
- 最大输出：64 KiB。
- 主要错误：`NODE_OFFLINE`、`PROTOCOL_MISMATCH`。

### 4.2 `node.management.capabilities`

- 响应：Capability Descriptor 列表及版本、平台限制、当前可用状态。
- 权限：机器可见权限；R0。
- 超时：5 秒；最大输出 256 KiB。
- 安全：Node 只能报告本机真实能力；Hub 不根据客户端输入伪造能力。

### 4.3 `node.management.rotateCredential`

- 请求：新公钥/轮换证明、可选 overlap 时间。
- 权限：设备管理；R3。
- 确认：默认 Web 管理确认；异常环境可要求本机确认。
- 超时：60 秒。
- 错误：`CREDENTIAL_REVOKED`、`ROTATION_PROOF_INVALID`。

### 4.4 `node.management.disconnect`

- 请求：reason、duration/indefinite。
- 权限：设备管理；R3。
- 行为：停止接收新 Job，按策略取消或保留当前 Job，然后断开。

## 5. Workspace 管理

远程接口不接受绝对路径创建授权。

### actions

| Action | 请求核心 | 响应 | 风险/确认 |
|---|---|---|---|
| `list` | filter, cursor | Workspace 逻辑信息 | R0 |
| `get` | workspaceId | revision、能力、Git 摘要 | R0 |
| `registerLocal` | 本机 UI/CLI 选择路径和权限 | workspaceId | 仅本地，R3 |
| `update` | workspaceId, expectedRevision, policy patch | 新 revision | R3，收紧可直接；放宽需确认 |
| `disable` | workspaceId, expectedRevision | 状态 | R3 |
| `revoke` | workspaceId, recovery policy | 删除逻辑授权 | R3，本机确认可配置 |

- 超时：列表 5 秒，更新 30 秒。
- 最大输入：64 KiB；输出 256 KiB。
- 错误：`WORKSPACE_NOT_FOUND`、`WORKSPACE_REVOKED`、`REVISION_CONFLICT`、`PATH_UNAVAILABLE`。
- Windows 差异：驱动器、UNC、junction、大小写不敏感、reparse point。
- Linux 差异：symlink、mount point、权限位、大小写敏感。

## 6. 文件系统

### 6.1 请求公共字段

```json
{
  "workspaceId": "ws_opaque",
  "path": "relative/path.txt",
  "expected": {
    "revision": "opaque-file-revision",
    "sha256": "optional"
  }
}
```

`path` 必须为 Workspace 相对路径。

### 6.2 Actions

| Action | 请求 | 响应/事件 | 风险 | 默认上限 |
|---|---|---|---:|---|
| `list` | path, depth=1, cursor, filters | entries, nextCursor | R1 | 10k entries/页受限 |
| `stat` | path, followPolicy | type,size,mtime,revision | R1 | 5 秒 |
| `read` | path, offset, length, encoding | text/metadata | R1 | 单次 1 MiB，最大文件策略 100 MiB |
| `readChunks` | path, chunkSize, cursor | data events | R1 | 64 KiB/chunk |
| `mkdir` | path, mode | metadata | R2 | 30 秒 |
| `write` | path, content/artifactRef, expected | Diff + metadata | R2 | inline 1 MiB；大文件走 Artifact |
| `edit` | path, exact replacements/ranges, expected | Diff + metadata | R2 | 文件默认 5 MiB |
| `applyPatch` | patch, expected roots | per-file Diff | R2/R3 | 100 files，10 MiB patch |
| `move` | from,to,expected | metadata | R2 | 同 Workspace 默认 |
| `copy` | from,to,expected | progress/result | R2 | 大文件为 Job |
| `delete` | path, recoverable=true | recoveryId | R3 | 默认进恢复区 |
| `purge` | recoveryId/path | result | R4 | 默认拒绝/本机确认 |

### 6.3 确认策略

- read/list/stat：否。
- write/edit/patch：获得 Action 权限后默认不逐次确认；可按 Workspace 开启。
- 删除、覆盖敏感文件、批量 patch：可要求确认。
- 永久删除：必须独立授权，MVP 推荐本机确认。

### 6.4 错误码

`PATH_INVALID`、`PATH_OUTSIDE_WORKSPACE`、`PATH_SYMLINK_ESCAPE`、`PATH_RACE_DETECTED`、`FILE_NOT_FOUND`、`FILE_TOO_LARGE`、`FILE_BINARY`、`ENCODING_INVALID`、`REVISION_CONFLICT`、`MATCH_NOT_UNIQUE`、`DISK_QUOTA_EXCEEDED`、`RECOVERY_BIN_FULL`。

### 6.5 安全风险

- TOCTOU、符号链接/junction 逃逸。
- Windows ADS、设备路径和大小写绕过。
- Patch 路径注入。
- 大文件/深目录资源耗尽。
- 编码转换造成源码损坏。

## 7. 代码搜索

### 7.1 `code.search.glob`

- 请求：patterns、exclude、maxResults、includeHidden、respectGitignore。
- 响应：相对路径、类型、可选元数据。
- 权限：Workspace read；R1。
- 超时：默认 30 秒，最大 2 分钟。
- 输出：最多 10,000 条，超过返回 truncated + cursor/建议。

### 7.2 `code.search.grep`

- 请求：pattern、regex、case、globs、contextLines、maxMatches。
- 响应：结构化 match 事件：path、line、column、preview。
- 实现：优先受控调用捆绑/发现的 ripgrep；缺失时有限 Go fallback。
- 安全：不允许客户端直接传任意 ripgrep flags；参数结构化映射。
- 错误：`SEARCH_PATTERN_INVALID`、`SEARCH_LIMIT_REACHED`、`SEARCH_TOOL_UNAVAILABLE`。

## 8. Shell 与进程

### 8.1 `shell.process.run`

请求核心：

```json
{
  "workspaceId": "ws_opaque",
  "cwd": ".",
  "mode": "argv",
  "executable": "go",
  "args": ["test", "./..."],
  "profile": null,
  "command": null,
  "env": {"CI": "1"},
  "timeoutMs": 120000,
  "background": false
}
```

- `mode=argv` 优先；`mode=shell` 必须选择本地允许的 profile。
- 响应：jobId；随后产生 stdout/stderr/progress/result。
- 权限：`shell.process.run`；R2。命中危险策略升级 R3/R4。
- 确认：按 profile、命令风险、Workspace 设置决定。
- 超时：默认 5 分钟；最大 2 小时，可按 profile 收紧。
- 输入：命令 32 KiB、env 64 KiB；单事件 64 KiB。
- 总在线输出默认 10 MiB，超出转 Artifact。

### 8.2 `status`、`logs`、`cancel`

- `status(jobId)`：R1，5 秒。
- `logs(jobId,cursor,limit)`：R1，游标续读。
- `cancel(jobId,reason)`：R2，幂等；默认 10 秒 grace，随后进程树终止。

主要错误：`EXECUTABLE_NOT_FOUND`、`SHELL_PROFILE_DENIED`、`CWD_OUTSIDE_WORKSPACE`、`ENV_DENIED`、`PROCESS_LIMIT_EXCEEDED`、`TIMEOUT`、`CANCEL_INCOMPLETE`、`OUTPUT_LIMIT_REACHED`。

### 8.3 平台差异

- Windows：CreateProcess、Job Object、PowerShell/cmd 参数规则、代码页与 ConPTY。
- Linux：execve、process group/session、信号、权限位、可选 PTY。
- MVP 非交互执行不依赖 PTY；PTY 在后续独立 Action 中加入。

## 9. Git

`git.repository` 默认调用系统 Git。

| Action | 风险 | 确认 | 输出 |
|---|---:|---|---|
| `status` | R1 | 否 | 结构化状态 |
| `diff` / `stagedDiff` | R1 | 否 | 文本或 Artifact |
| `log` / `show` | R1 | 否 | 分页结构 |
| `branches` / `currentBranch` / `worktrees` | R1 | 否 | 结构化列表 |
| `createWorktree` | R2 | 策略可选 | 路径映射和分支 |
| `deleteWorktree` | R3 | 默认确认 | 结果/未提交检查 |
| `add` | R2 | 策略可选 | staged summary |
| `commit` | R3 | 默认短期授权；hooks 时提示 | commit id |
| `fetch` | R2 | 可选 | progress |
| `pull` | R3 | 默认确认 | merge/rebase result |
| `push` | R3 | 默认确认 | remote refs |

- 超时：读 30 秒；网络操作默认 10 分钟。
- 当前实现 Git Diff/Show 在线预览上限 128 KiB；超出后同一次 Git 读取同时落临时文件并上传 Artifact，单 Artifact 上限 100 MiB，避免二次执行导致 Diff 漂移。
- 错误：`GIT_NOT_FOUND`、`NOT_A_REPOSITORY`、`DIRTY_WORKTREE`、`HOOK_RISK_REQUIRES_APPROVAL`、`AUTH_REQUIRED`、`NON_FAST_FORWARD`、`MERGE_CONFLICT`。
- 凭据和 remote URL 脱敏；不返回完整 credential helper 输出。

## 10. 构建与测试

### `build.test.runProfile`

- 请求：profileId、idempotencyKey；当前远端不能覆盖 argv/cwd/timeout。
- Profile 由 Workspace 本地配置，将 typecheck/lint/test/build 映射为受控命令；真实 argv/cwd/timeout 只能在 Node 本机登记。
- 响应：jobId、结构化摘要、报告 Artifact。
- 权限：R2；确认通常继承 shell 策略。
- 不按语言创建复杂调度框架；Profile 只是受控 Shell 模板。
- 错误：`PROFILE_NOT_FOUND`、`ARGUMENT_DENIED`、`TEST_FAILED`、`BUILD_FAILED`。

## 11. Artifact

| Action | 说明 | 风险/限制 |
|---|---|---|
| `create` | 声明 logicalName、type、size、hash、jobId | R2；配额检查 |
| `uploadChunk` | uploadId、offset、bytes | 二进制帧；块默认 1 MiB |
| `complete` | 校验长度与 SHA-256 | 原子完成 |
| `abort` | 清理临时上传 | 幂等 |
| `getMetadata` | 查询 Artifact | R1 |

- 单 Artifact 默认 100 MiB；当前同时限制单机未过期 Artifact 总量 512 MiB、Owner 总量 2 GiB、单机并发上传 4 个。
- 分块默认 1 MiB；创建相同 machine/workspace/job/name/type/size/hash 的上传会恢复未过期 uploadId/offset，不重复建立上传。
- 完成时校验 size + SHA-256；Blob 以 SHA-256 内容寻址并在复用时再次校验已有 Blob 完整性。
- 当前默认保留 30 天；Hub 定时清理过期上传、元数据和无引用 Blob。
- 压缩包不能自动解压到 Workspace。
- 错误：`ARTIFACT_TOO_LARGE`、`HASH_MISMATCH`、`OFFSET_CONFLICT`、`CONTENT_TYPE_DENIED`、`UPLOAD_EXPIRED`。

## 12. 截图

| Action | 请求 | 风险/确认 | 输出 |
|---|---|---|---|
| `desktop` | format, quality, maxDimension | R3，默认可见提示 | Artifact |
| `display` | displayId, crop | R3 | Artifact |
| `window` | windowId, includeFrame | R3 | Artifact |
| `page` | browserSessionId, fullPage | R2/R3 | Artifact |

- 格式：PNG 默认；JPEG/WebP 用于大图压缩。
- 最大原始像素和编码大小必须限制。
- 锁屏、无桌面会话或权限不足返回明确错误。
- 错误：`CAPTURE_PERMISSION_DENIED`、`NO_INTERACTIVE_DESKTOP`、`DISPLAY_NOT_FOUND`、`WINDOW_NOT_FOUND`、`CAPTURE_TOO_LARGE`。

## 13. 浏览器自动化

`browser.automation` 默认只控制 Node 创建的隔离 Profile。

代表 actions：`launch`、`contexts`、`pages`、`open`、`navigate`、`click`、`type`、`key`、`wait`、`snapshot`、`evaluateRestricted`、`screenshot`、`downloads`、`consoleLogs`、`networkErrors`、`close`。

- 权限：隔离浏览器 R2；连接现有真实浏览器 R3。
- 确认：首次访问真实登录态必须明确确认。
- 单动作默认 30 秒；测试 Job 最大 30 分钟。
- 脚本执行只允许受限表达式/预定义动作；不默认提供任意本机 Node.js 执行。
- URL 策略阻止 `file:`、危险 scheme、云元数据和受限内网地址；访问本地开发站点需 Workspace/策略明确允许。
- 错误：`BROWSER_UNAVAILABLE`、`PROFILE_DENIED`、`NAVIGATION_BLOCKED`、`ELEMENT_NOT_FOUND`、`PAGE_CLOSED`、`DOWNLOAD_DENIED`。

## 14. Agent 控制

Provider-neutral actions：

- `providers.list`
- `models.list`
- `projects.list`
- `session.create`
- `session.get`
- `session.send`
- `session.watch`
- `session.cancel`
- `session.result`
- `session.handoff`

请求必须包括 providerId、clientId、workspaceId、correlationId 和 hopLimit。Session 创建后 provider、Workspace 边界和所有权不能被静默改变。

- 权限：发现 R1；创建/发送 R2；跨客户端共享/handoff R3。
- Provider Token 不进入协议或 Hub。
- 默认 hopLimit=1，最大建议 4。
- 事件必须标识真实 owner 和 execution mode，不能把“打开桌面 UI”误报为“任务已运行”。
- 错误：`PROVIDER_UNAVAILABLE`、`MODEL_NOT_ALLOWED`、`SESSION_NOT_FOUND`、`OWNER_CONFLICT`、`HOP_LIMIT_EXCEEDED`、`ACTIVE_TURN_CONFLICT`。

## 15. 通用结构化错误

```json
{
  "code": "PATH_OUTSIDE_WORKSPACE",
  "message": "path resolves outside the authorized workspace",
  "retryable": false,
  "origin": "node",
  "details": {
    "workspaceId": "ws_opaque"
  }
}
```

错误响应不得包含未脱敏绝对路径、Token、环境变量、完整命令凭据或内部堆栈。诊断细节可进入受权限保护的本地/Hub 审计，但公开错误保持稳定。
