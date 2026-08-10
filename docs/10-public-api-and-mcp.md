# 公共 API 与 MCP（0.3.x）

## 公网 MCP

Fast Spider MCP 通过 `/mcp` 提供 Streamable HTTP，使用标准 OAuth Authorization Code + PKCE。当前固定 16 个工具：

```text
machine_list
machine_get
capability_list
file_read
code_search
file_edit
shell_run
job_watch
job_cancel
git_control
build_control
browser_control
screenshot_take
ai_control
artifact_get
working_context
```

0.3.0 不提供目录列表工具。

## 资源模型

所有需要操作本机的工具首先使用 `machineId`。文件系统和进程位置直接使用本机绝对路径：

- file_read/file_edit: `path`
- code_search: `path`
- shell_run/build_control: `cwd`
- git_control: `repositoryPath`
- ai_control session.create: `workingDirectory`
- working_context: `projectPath`

`working_context` 只保存每个项目当前任务的有界状态快照，不保存聊天原文；`get` 会同时返回实时 Git branch/HEAD/dirty，用于在聊天压缩后重建工作上下文。

Job 后续操作只用 machineId + jobId。Artifact 获取只用 artifactId；Node 上传本机文件时使用 machineId + absolute path。`artifact_get.publishFile` 使用同样的绝对路径，但文件由 Node 直接上传 Hub Temporary Presentation Relay；Relay 不创建 Artifact/数据库记录，只生成 20 分钟短期 `ResourceLink`。

## OAuth

OAuth resource 是公开 MCP URL。授权页复用 Owner Web Session，用户点击允许/取消。授权页 CSP 只允许当前经过后端验证的 callback origin，避免开放任意表单跳转。

Hub 在 loopback 后由 Nginx 反代时，MCP SDK 自带 localhost Host 防护关闭；Fast Spider 自己的 `AllowedHosts` + 反向代理仍负责 Host 边界，避免把合法公网 Host 误判为 DNS rebinding。

## Node HTTP/WSS

Node 首次登记使用 Connection Token 调用机器登记接口；运行时只使用设备凭据/WSS。Connection Token 不具备 MCP 权限，也不作为长期设备凭据。

## 错误原则

- 相对路径在要求本机路径的接口上返回 `ABSOLUTE_PATH_REQUIRED`。
- OS 拒绝访问时返回权限/系统错误，不伪装成目录授权失败。
- MCP 不再返回旧目录对象的不存在、禁用或越界错误。
