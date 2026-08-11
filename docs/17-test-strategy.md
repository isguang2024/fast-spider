# 测试策略（0.4.2）

发布门禁必须验证新的 Machine 边界，而不是旧目录授权模型。

## 必测主链

1. Connection Token 可重复登记多台 Node；Node 后续只用 Device Key。
2. MCP OAuth 完成后 tools/list 返回 16 个固定工具，包含 `working_context` 且不包含旧目录列表能力。
3. machine_list/machine_get 不泄露秘密。
4. file_read/file_edit 使用绝对路径；相对路径拒绝。
5. code_search 使用绝对目录。
6. shell_run/build_control 使用绝对 cwd，并验证 timeout、idempotency、取消和进程树清理。
7. job_watch/job_cancel 只依赖 jobId。
8. git_control 使用绝对 repositoryPath，覆盖读写和受控网络动作。
9. Artifact 上传绝对路径文件和 Job 日志；数据库不再有目录授权关联字段。
10. Browser 在隔离 Profile 中可访问 Node 网络可达的公网/localhost/私网；危险地址仍拒绝。
11. Screenshot window token 不依赖目录授权对象。
12. `routing.status` 真实只读 CC Switch DB：三类 app route、current Provider、takeover/live takeover、model mapping、selectionConsistent、sanitizer 和 request-log Session correlation 均通过。
13. `providers.list` 经 Local Bridge 同时发现 Codex + Claude Code，并验证每个 Harness 的 `supportedActions`、route 与 provider-specific capabilities。
14. Codex E2E 继续覆盖 Provider capabilities、Hooks、Permission Profiles、Installed Plugins、MCP status、原生多类型 Turn、`outputSchema`、Goal/Review/respond/steer 和 app-server auto-resume。
15. Claude Code E2E 覆盖 CLI availability、stdin Prompt、原生 UUID、stream-json init/result、Session index、终态归一化和 RouteSnapshot；upstream 认证失效时允许正确终止为 failed，但 Runtime/lifecycle 必须完整。
16. Windows 托盘、隐藏自启动、自更新 PID 等待链路通过。
17. Task Workspace 覆盖默认 plan 兼容、Plan/Task CAS、Markdown managed block、symlink/junction 边界、progress.watch 与本地页面。
18. code_search 2.0 同时覆盖受管 ripgrep 安全 argv/environment/JSON parser 与 native content/files/glob/context fallback。
19. file_read 2.0 覆盖 byte/line/head/tail/around/stat/hash/bounds；file_edit 2.0 覆盖 create/replace/editMany/preview、CAS、全有或全无和原子替换。
20. 组件中心只接受 Browser/search-ripgrep allowlist；本地自检必须使用临时 data-dir 并确认 preview 不落盘、临时目录已清理。

## Release Gate

`bash scripts/release-gate.sh --full` 继续作为发布前硬门槛，覆盖：

- git whitespace / secret scan
- module checksum / tidy
- static analysis
- `go test ./...`
- Windows amd64 / Linux amd64 构建
- backup/restore E2E
- Local Bridge E2E
- repeated Node regression
- Task Workspace 专项
- Managed ripgrep/native 搜索专项
- file_read 2.0 专项
- file_edit 2.0 + MCP/Hub policy 专项
- Node updater staging/cleanup 与 reconnect/backoff 临时 E2E
- real Browser E2E
- real CC Switch routing read-only E2E
- real Claude Code CLI E2E
- real Codex E2E（model/provider capabilities、skills/hooks/permissions/plugins/MCP status、Goal、`outputSchema`、Session/Turn、app-server restart auto-resume）
- Local Bridge multi-provider discovery E2E
- Local Bridge → Codex product smoke

测试中不得重新引入旧目录对象或目录白名单来让旧断言通过。
