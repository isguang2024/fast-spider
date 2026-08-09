# 测试策略（0.3.x）

发布门禁必须验证新的 Machine 边界，而不是旧目录授权模型。

## 必测主链

1. Connection Token 可重复登记多台 Node；Node 后续只用 Device Key。
2. MCP OAuth 完成后 tools/list 返回 15 个固定工具。
3. machine_list/machine_get 不泄露秘密。
4. file_read/file_edit 使用绝对路径；相对路径拒绝。
5. code_search 使用绝对目录。
6. shell_run/build_control 使用绝对 cwd，并验证 timeout、idempotency、取消和进程树清理。
7. job_watch/job_cancel 只依赖 jobId。
8. git_control 使用绝对 repositoryPath，覆盖读写和受控网络动作。
9. Artifact 上传绝对路径文件和 Job 日志；数据库不再有目录授权关联字段。
10. Browser 在隔离 Profile 中可访问 Node 网络可达的公网/localhost/私网；危险地址仍拒绝。
11. Screenshot window token 不依赖目录授权对象。
12. ai_control session.create 使用绝对 workingDirectory，Local Bridge → Codex product E2E 通过。
13. Windows 托盘、隐藏自启动、自更新 PID 等待链路通过。

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
- real Browser E2E
- real Codex E2E
- Local Bridge → Codex product smoke

测试中不得重新引入旧目录对象或目录白名单来让旧断言通过。
