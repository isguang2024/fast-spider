# ADR 0003：契约格式（0.3.0 当前有效部分）

## 状态

已接受。本文以当前 Machine-only Capability Contract 为准，旧目录对象字段不属于公共契约。

## 决策

使用 UTF-8 JSON Schema 作为 Request、Event、Error、Job、Artifact 和 Capability Descriptor 的唯一来源。Go 类型、校验器和 MCP Schema 从同一契约生成或映射；Adapter 不手写第二套语义。

## 要求

- 外部 MCP/REST 与内部 Contract 保持字段和错误语义一致。
- 绝对 `path`、`cwd`、`repositoryPath` 和 `workingDirectory` 作为能力参数，`machineId` 作为唯一远程目标。
- 所有上限在网络边界实际执行，不只作为文档。
- Unknown optional fields 可忽略；改变必需字段或语义时提升 major。
- 生成结果可重复，CI 检查 JSON、Schema checksum、round-trip 和未知字段行为。
- Fuzz 输入不能导致 panic、无限递归、巨大分配或路径静默改写。

## 不采用

不同时维护手写 Go struct、手写 MCP Schema、另一份 Protobuf 或兼容目录协议。迁移必须是明确版本窗口中的一次性变更。
