# ADR 0001：Hub 与 Node 语言选择

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：MVP Hub、Node、核心协议与平台适配

## 背景

Fast Spider 需要同时实现公网 Hub、Windows/Linux Node、WSS 长连接、文件与进程能力、SQLite、MCP Adapter、安装更新和少量平台集成。当前由单人 + AI 维护，最重要的是安全边界清晰、迭代和发布简单，而不是追求理论上的最高性能或多语言展示。

需求提出比较 Go 与 Rust：Hub 倾向 Go；Node 可选 Go 或 Rust。Rust 在内存安全、平台系统编程和资源控制方面有优势，但双语言会增加契约生成、构建、CI、调试、签名、依赖治理和人才切换成本。

## 决策驱动因素

1. 单人维护成本和交付速度。
2. Hub 与 Node 共享协议、状态和安全语义的能力。
3. Windows/Linux 交叉构建和单文件发布。
4. WSS、HTTP、SQLite、MCP 与系统进程生态。
5. 文件路径、进程树、截图等平台能力的可靠性。
6. 未来可以替换局部实现，而不是锁死整个系统。

## 考虑的方案

### 方案 A：Hub Go + Node Go

优点：

- 一套工具链、依赖治理、测试和发布流水线。
- 可以共享生成的 Contract 类型、错误和测试工具。
- Go 的网络、并发、HTTP、跨平台进程和单文件发布适合 MVP。
- 最快复用 DevSpace 同类 Workspace、文件、Shell、Git 和 Session 经验。

缺点：

- 某些 Windows 图形/窗口和 Linux Wayland 能力可能需要原生 API/helper。
- Go 不能消除所有内存/并发/系统调用错误，需要严格边界测试。

### 方案 B：Hub Go + Node Rust

优点：

- Node 的内存安全和系统能力表达更强。
- Rust 平台库和 helper 生态可能适合截图、PTY 等能力。

缺点：

- 两套语言、构建、依赖、安全公告和发布链。
- 契约生成和跨语言调试显著增加成本。
- 核心需求尚未证明 Go 无法可靠实现。

### 方案 C：Hub Rust + Node Rust

优点：统一语言、内存安全、系统控制强。

缺点：MCP/Hub 生态和开发效率对当前团队不如 Go；从 DevSpace 同类能力迁移更慢；没有足够收益支持全量换栈。

## 决策

MVP 选择 **Hub Go + Node Go**。

- Hub 与 Node 位于同一 Go Module/仓库，保持模块边界而非复制代码。
- Contract 使用语言无关 Schema，生成 Go 类型；不能因当前同语言而把线路协议写成 Go 私有序列化。
- `native/` 或独立 helper 只在 Go/系统 API 原型证明不足时引入。
- helper 必须接口窄、版本化、无网络/策略/权限逻辑、可独立崩溃隔离和测试。
- 不把主要业务逻辑、Workspace 权限、Job 状态或 Provider 控制放进 C++/Rust helper。

## 后果

### 正面

- 最少组件和维护成本。
- Hub/Node 共享测试、Contract 生成、错误和安全工具。
- Windows/Linux 发布链更直接。
- 可以更快完成 Phase 1–4 的核心闭环。

### 负面

- 需要自行严谨处理平台文件/进程边界。
- 个别图形/窗口能力可能增加 helper。
- 若未来整个 Node 的系统集成复杂度显著上升，迁移成本仍存在。

## 风险控制

- Path Guard、进程树、协议解析必须有 fuzz/property/真实平台测试。
- 使用 Go race detector 和明确 goroutine/队列生命周期。
- 外部工具和 helper 通过固定 Port/Adapter 接口隔离。
- 对不可靠的平台能力宁可声明 unavailable，不通过提权或扩大范围绕过。

## 重新评估触发条件

只有出现以下真实证据才重新打开语言决策：

1. Go 无法在目标 Windows/Linux 平台安全实现关键 Node 能力。
2. 原生 helper 数量和边界增长到比单一 Rust Node 更复杂。
3. Node 的内存/资源隔离缺陷无法通过架构和测试控制。
4. 团队规模或维护能力发生实质变化。

即使重评，Hub 与 Node 仍通过同一语言无关 Contract 连接，不复制维护两份 Schema。

## 相关文档

- [系统架构](../02-system-architecture.md)
- [Node 设计](../04-node-design.md)
- [开源组件评估](../18-open-source-evaluation.md)
- [路线图](../19-roadmap.md)
