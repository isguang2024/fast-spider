# ADR 0003：核心契约格式

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：FSWP、Capability、Job/Event、Artifact、Approval 和 Adapter Schema

## 背景

Fast Spider 的核心协议必须独立于 MCP，并且 Hub、Node、MCP Adapter、REST/SDK、Web Console 和未来 CLI 需要共享一致的字段、枚举、错误、状态和上限。即使 MVP 的 Hub/Node 都使用 Go，也不能把 Go struct 当作唯一隐式协议，否则未来跨语言 Node、SDK 和兼容测试会困难。

候选契约源包括 JSON Schema、Protocol Buffers 和纯手写 Go 类型/文档。

## 决策驱动因素

- JSON 控制消息和 MCP JSON Schema 的自然映射。
- 人类可读、可审查和易生成测试样例。
- 能表达长度、枚举、条件、格式和 `additionalProperties` 策略。
- 可生成 Go 类型和校验器。
- 支持版本兼容与未来其他语言。
- 不维护两份或三份手写 Schema。

## 考虑的方案

### 方案 A：JSON Schema 2020-12

优点：

- 与 JSON/WSS、MCP 工具参数和 REST 自然一致。
- 适合描述边界上限和拒绝规则。
- 可用于样例验证、代码生成、兼容测试和文档。
- 人类可直接审阅。

缺点：

- 不同生成器对复杂关键字支持不一致。
- 需要固定规范子集、生成器和规范化方式。
- 二进制帧仍需单独结构定义。

### 方案 B：Protocol Buffers

优点：成熟代码生成、紧凑二进制、字段演进规则明确。

缺点：控制消息可读性下降；MCP/REST 仍需生成/维护 JSON 映射；容易出现 Protobuf、MCP JSON Schema 和文档三层漂移。

### 方案 C：手写 Go 类型 + 文档

优点：开始最快。

缺点：没有语言无关权威源；上限、条件和兼容容易分散；无法可靠生成 Adapter Schema，长期返工最大。

## 决策

使用 **JSON Schema Draft 2020-12** 作为核心业务契约的唯一来源。

建议目录：

```text
contracts/
├─ schema/
│  ├─ common/
│  ├─ protocol/
│  ├─ capabilities/
│  ├─ jobs/
│  ├─ approvals/
│  └─ artifacts/
├─ examples/
├─ generated/
│  └─ go/
└─ compatibility/
```

规则：

1. Schema 定义稳定字段、枚举、上限、必需性和未知字段策略。
2. Go 类型和边界校验代码由固定工具生成；生成文件不手工编辑。
3. MCP/REST Schema 从同一契约映射，Adapter 不能另写一份语义不同的类型。
4. Mermaid、文档和示例引用同一 canonical 字段名和状态枚举。
5. 二进制帧头使用独立、固定的字节级规范和 Golden Test；其元数据仍用相同 ID/枚举定义。
6. Contract 变更必须同时更新有效/无效样例、兼容测试、生成结果和文档。

## Schema 子集

Phase 1 先使用可稳定生成的受控子集：

- object、array、string、integer、boolean、null。
- `$ref`、`$defs`。
- required、enum、const。
- min/max length、items、properties、numeric range。
- `additionalProperties: false` 为边界对象默认值；需要扩展时使用显式 `extensions`。
- `oneOf` 只用于清楚的 tagged union，不构造难以生成的模糊组合。
- format 只作附加校验，关键 ID/time 仍有明确 pattern/parser。

生成器不支持的复杂语义由固定应用校验函数实现，但其规则仍在 Schema annotation 和测试中记录。

## 版本

- FSWP 使用独立 `major.minor`。
- 每个 Capability 有独立版本。
- Schema 文件带 `$id` 和版本目录/metadata。
- minor 可新增可选字段和枚举扩展点；接收方只能忽略明确允许的未知扩展。
- 新增必需字段、改变含义、删除值或收紧到旧合法输入无效时提升 major/action version。
- 不通过“所有对象允许任意字段”假装兼容。

## 确定性摘要

idempotency、Approval risk digest 和 Event payload hash 需要稳定规范化：

- 参数先按所选 Schema 解析和归一化。
- 拒绝重复 JSON key、非有限数字和未声明字段。
- 使用 RFC 8785 兼容的 JSON Canonicalization Scheme 或经 ADR 附录验证的等价确定性编码。
- hash 算法初始为 SHA-256，摘要注明算法前缀。
- 原始客户端 JSON 字节不直接作为业务等价判断，因为键顺序/空白不应改变语义。

## 生成和验证门禁

Phase 1 锁定工具前必须完成原型：

- 同 Schema 生成可用、强类型 Go 代码。
- round-trip 不丢 opaque ID、时间、optional/null 语义。
- 所有上限在网络边界实际执行，不只作为文档。
- 生成结果可重复，CI 检查工作区无未提交漂移。
- fuzz 输入不能导致 panic、无限递归或巨大分配。
- MCP 工具 Schema 与内部 Contract 的字段映射测试通过。

## 后果

### 正面

- 只有一个业务契约事实源。
- MCP、REST、FSWP 和未来 SDK 更容易保持一致。
- 容易建立 Golden、无效样例和兼容测试。
- 保留未来 Rust/其他语言 Node 的可能性。

### 负面

- 需要选择和维护可靠的生成/校验工具。
- JSON 比 Protobuf 大，需要严格大小限制和二进制 Artifact 通道。
- Schema 设计纪律成为开发门禁，不能随手添加字段。

## 不采用的做法

- 不复制一份 Go struct、一份 MCP Schema、一份手写文档各自演进。
- 不把 MCP SDK 类型传入 Capability Engine。
- 不把 `map[string]any` 作为所有请求的永久逃生口。
- 不在 minor 版本静默改变状态/错误语义。

## 重新评估触发条件

- 控制消息吞吐/体积被真实测量证明为主要瓶颈。
- 出现多语言高性能数据流且 JSON 映射成本显著。
- 官方生态要求另一种明确契约格式。

即使切换 Protobuf，外部 MCP/REST 映射和版本规则仍需保留，迁移必须有明确窗口而非长期双协议。

## 相关文档

- [系统架构](../02-system-architecture.md)
- [线路协议](../06-wire-protocol.md)
- [公共 API 与 MCP](../10-public-api-and-mcp.md)
- [测试策略](../17-test-strategy.md)
