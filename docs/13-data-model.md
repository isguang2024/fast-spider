# 数据模型（0.4.0）

Hub 只持久化远程控制真正需要的事实：Owner、Web Session、OAuth Client/Authorization/Token、Connection Token、Machine、Device Credential、Job/Artifact 元数据和审计。

## Machine

Machine 是当前唯一远程执行资源边界。设备记录包含 machineId、Owner、客户端名称、管理员备注、OS/架构/版本、在线状态、能力摘要、撤销/删除状态等。

Node 的本机绝对路径不需要提前登记到 Hub，也没有目录授权表。

## Artifact

Artifact 关联 Owner、Machine 和可选 Job。0.3.0 migration 008 删除历史目录关联字段。Artifact 内容仍使用内容寻址存储，元数据在 SQLite。

## Node 本地数据

Node 本地保存：

- config.json
- state.json
- Device Key
- Job/Browser/更新/组件等受管运行数据
- `agent/claude-code-sessions.json`：有界 Claude Session 控制索引，只保存 session/status/model/result/usage/RouteSnapshot，不复制完整 Prompt/对话

CC Switch `~/.cc-switch/cc-switch.db` 与 Claude/Codex 原生会话存储属于外部产品事实，不复制进 Fast Spider Hub 数据库；Inspector 只读并返回脱敏摘要。0.3.x 起不再读取目录授权注册表。旧版本遗留的目录授权文件不是当前产品事实，也不会影响新 Node 权限。

## 隐私

Hub 不需要保存 Node 文件系统目录清单。只有某次能力请求明确携带绝对路径时，路径会作为该请求参数参与执行；普通机器列表不会枚举本机文件系统。
