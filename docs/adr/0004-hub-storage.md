# ADR 0004：Hub 存储

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：Hub 控制面数据库、Artifact 和备份

## 背景

Fast Spider MVP 是一个单人自托管、单 Hub 实例的平台。Hub 需要保存身份、机器、Workspace 逻辑目录、权限、Job、Event、Approval、Audit 和 Artifact 元数据，同时存放日志、Diff、报告和截图等 Artifact。

需求要求比较 SQLite WAL 与 PostgreSQL，并避免为假设的大规模场景提前引入 Redis、消息队列、对象存储或 Kubernetes。

## 决策驱动因素

- 单机部署、备份、升级和恢复简单。
- 数据一致性和事务约束可靠。
- 低空闲资源、少常驻服务。
- 适合 50 台注册、10 台在线 Node 的初始目标。
- Job/Event 高频写入可控，不形成单行事务风暴。
- 未来迁移 PostgreSQL/S3 时公共 Contract 不变。

## 考虑的方案

### 方案 A：SQLite WAL + Hub 本地 Artifact 文件

优点：

- 无独立数据库服务，部署/备份最简单。
- 单 Hub 进程与 Owner 负载非常匹配。
- 支持事务、外键、唯一约束和可靠本地文件存储。
- 可将 Web 静态资源和控制面收敛为一个 Hub 进程。

缺点：

- 单写入协调；长事务或错误 Event 写入模式会造成 busy/WAL 膨胀。
- 多实例 Hub 不适合共享同一 SQLite。
- 备份必须正确处理 WAL 和 Artifact 一致性。

### 方案 B：PostgreSQL + 本地/S3 Artifact

优点：

- 更高并发、多实例和运维生态成熟。
- 适合未来组织/多用户和横向扩展。

缺点：

- 增加常驻服务、备份、升级、权限和故障面。
- 当前规模没有真实收益。
- 仍需单独处理 Artifact。

### 方案 C：SQLite + Redis/消息队列/对象存储

优点：可拆分事件、缓存和 Blob。

缺点：组件数量、状态一致性和运维复杂度显著增加，与 MVP 原则冲突。

## 决策

MVP 使用：

1. **SQLite WAL** 保存 Hub 控制面和 Artifact 元数据。
2. **Hub 本地内容寻址文件系统**保存 Artifact Blob。
3. **进程内有界连接注册表和事件扇出**，不引入 Redis/NATS/Kafka。
4. Web Console 静态资源嵌入 Hub，不增加独立常驻前端服务。

具体 SQLite Driver 在 Phase 1 前通过 CGO/pure-Go 原型决定；本 ADR 决定存储模型，不提前绑定 Driver。

## SQLite 规则

- 只有 Hub 进程写数据库；不允许多个进程直接共享写入。
- `journal_mode=WAL`、`foreign_keys=ON`、合理 `busy_timeout`。
- 所有写事务短小；网络、WSS、文件 hash、大文件移动、Node 调用和外部认证不在事务内。
- Job Event 分批持久化；终态、Approval、Audit 和 Artifact 完成优先提交。
- 使用唯一键、CHECK、foreign key 和 revision/CAS 维护不变量。
- Migration 版本化、带 checksum；启动前验证，变更前一致性备份。
- 定期 checkpoint 有上限和指标，不每请求强制执行。
- 数据库不得放在不可靠网络共享。

## Artifact 布局

```text
data/artifacts/sha256/ab/cd/<full-sha256>
data/uploads/<opaque-upload-id>.part
```

- logicalName 不参与物理路径。
- 完成上传后校验 size/hash，再原子移动。
- 元数据包含 Job/机器/Workspace/权限、Content-Type、敏感级别和过期时间。
- 下载重新授权，使用附件和 `nosniff`。
- 可按 hash 去重物理 Blob，但引用和权限独立。
- 临时上传、过期 Artifact 和孤儿 Blob 由内置清理任务处理。

## 备份

备份集合：SQLite 一致性快照、配置、密钥的独立加密备份、Artifact manifest 和文件。规则：

- 使用 SQLite backup API 或受控 checkpoint/停写；不能只复制 `.db` 忽略 WAL。
- 记录 DB schema、应用版本、Artifact 快照点和 hash。
- 定期隔离恢复验证，不把命令退出码当作可恢复证据。
- 迁移前自动备份；不可逆 Migration 的回滚依赖该备份，不维护长期双写。

## 容量与保护

- Event、Job、Audit、Artifact、临时上传均有保留和硬配额。
- 70/85/95% 磁盘水位触发告警、限制和保护模式。
- 磁盘高水位先拒绝新大 Artifact/Job，再受控清理。
- 清理使用批次、cursor 和时间预算，不做长时间全表事务。

## PostgreSQL 迁移触发条件

只有出现一项或多项真实需求才迁移：

1. 多 Hub 实例或无中断切换成为明确需求。
2. SQLite 写入/busy/WAL 在优化后仍是实际瓶颈。
3. 团队/多租户查询和数据量显著超过单机设计。
4. 运维环境已经标准化 PostgreSQL，额外服务成本不再成立。

迁移方式：停机/维护窗口导出、校验、导入 PostgreSQL并切换，不进行长期双写。公共 ID、状态和 Contract 不依赖 SQLite 特性。

## 后果

### 正面

- Hub 可以作为一个易部署、易备份的进程运行。
- 空闲资源和运维故障面最小。
- 符合个人自托管和阶段路线。

### 负面

- 多实例和大规模写入后续需要迁移。
- 必须认真实现 Event 批处理、WAL、备份和磁盘水位。
- Artifact 与数据库需要一致性恢复器。

## 不采用的做法

- 不让每个 Event 一个独立数据库事务。
- 不在内存保留无限 Event/连接队列。
- 不引入 Redis 仅作为“可能以后有用”的缓存。
- 不把 Artifact base64 存入数据库。
- 不在 SQLite 与 PostgreSQL 之间长期双写。

## 相关文档

- [Hub 设计](../03-hub-design.md)
- [数据模型](../13-data-model.md)
- [部署与运维](../14-deployment-and-operations.md)
- [更新与恢复](../16-update-and-recovery.md)
