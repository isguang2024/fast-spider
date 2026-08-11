# ADR 0001：语言选择

## 状态

Current：已接受。语言决策仍有效；早期文档中的目录授权假设已在 0.3.0 被 Machine-only 模型替代。

## 决策

Hub 与 Node 使用 Go。原因是 Go 同时覆盖 HTTPS/WSS、并发、跨平台进程、SQLite、MCP Adapter、单文件发布和 Windows/Linux 构建；平台差异通过窄接口实现。

## 边界

- 业务规则、Machine 归属、Capability、Job、审计和 Provider 控制留在 Go 层。
- Windows 原生代码只处理必要的窗口、截图或系统集成，不承载策略、协议或权限逻辑。
- Node 以当前 OS 用户运行，不通过语言层引入目录授权或自动提权。
- 任何语言替换都必须保持绝对路径字段、Machine 边界、Job/Event 和 MCP 语义不变。

## 后续触发条件

只有在 Go 平台原型明确失败、或真实发布/性能需求无法满足时，才新增语言 ADR。不得因为某个依赖或示例方便而引入第二套执行语义。
