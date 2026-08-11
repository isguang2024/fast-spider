# ADR 0004：Hub 存储

## 状态

Current：已接受。SQLite WAL + 本地 Artifact 存储决策仍有效；Hub 不保存目录授权或本机路径注册事实，0.3.0 的旧迁移只负责清除历史关联字段。

## 决策

MVP 使用单 Hub 实例 + SQLite WAL + Hub 本地内容寻址 Artifact 存储。数据库保存 Owner、Web Session、OAuth、Connection Token、Machine、Device Credential、Job、Event、Artifact 和 Audit 等真实控制面聚合。

## 事务与备份

- 事务短小，不在事务内执行 WSS、文件 I/O、Shell、Git、Browser 或 Provider 调用。
- Job 创建、幂等查找、Event 序号、Artifact 完成和审计使用明确事务边界。
- Artifact 完成前校验 size/hash，之后原子移动到内容寻址路径。
- 在线备份使用 SQLite 一致性机制；`backup-verify` 校验 manifest 和每个文件哈希。
- Restore 只接受不存在或空目录，并先在临时目录完成校验。

## 数据边界

Artifact 元数据只关联 Owner、Machine、可选 Job、类型、大小、哈希、保留期和来源；不保存 Node 文件系统目录清单。Node 的绝对路径只在某次能力请求中出现，并按日志脱敏策略处理。

## 后续迁移

只有多 Hub 实例、持续高写入或 Artifact 容量成为真实瓶颈时，才评估 PostgreSQL、对象存储或事件总线。迁移不维护长期双写。
