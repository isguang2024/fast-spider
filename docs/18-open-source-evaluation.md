# 18 开源组件评估

## 1. 研究范围与方法

研究快照日期：**2026-08-08**。

本阶段只使用项目官方文档、官方仓库和系统厂商文档作为主要事实来源。评估结果不是一次性许可结论；新增高风险依赖时仍需固定版本并读取对应 LICENSE/NOTICE。SBOM、集中漏洞扫描等发布工具当前不作为个人自用 release gate 的硬前置，准备公开分发或接受外部贡献时再正式加入。

“体积”使用定性等级，因为多数项目未发布可横向比较的单一安装体积，浏览器、平台运行库和构建选项会显著改变结果：

- 小：通常为库或单一小工具，不捆绑大型 runtime。
- 中：包含原生库/较多依赖或单独可执行程序。
- 大：完整远控系统、Node.js 服务或浏览器二进制集合。

## 2. 许可证原则

- Fast Spider 核心优先 Apache-2.0、MIT、BSD、ISC 等宽松许可证。
- AGPL 项目可以研究架构和功能，但在许可证策略未明确前不复制代码、不链接、不派生实现。
- GPL 工具若作为用户/系统已有的独立进程调用，需要保留清晰的分发和 NOTICE 边界；不把其源码静态链接进核心。
- MPL 组件为文件级 copyleft，只有确实需要时引入并隔离修改；MVP 当前不需要。
- 浏览器本体、安装包和平台 SDK 有各自许可证，不能只看 Adapter 仓库许可证。
- 每个依赖必须有替代方案、移除路径和版本升级策略。

## 3. 远程管理/桌面项目

| 项目 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| RustDesk | 完整远程桌面、P2P/relay；Rust 等 | AGPL-3.0 | 活跃；Windows/Linux/macOS/移动端 | 远控攻击面很大；大 | 很高 | **仅参考架构**。功能远超范围，AGPL 会显著影响派生/网络服务策略，不复制或链接 |
| MeshCentral | Web 设备管理、终端、文件、桌面；Node.js + Agent | Apache-2.0 | 活跃；服务器 + Windows/Linux/macOS Agent | 成熟远管系统，入口和功能面广；大 | 很高 | 参考设备注册、Agent 生命周期和管理 UI；不直接依赖完整系统 |
| Apache Guacamole | 浏览器端 RDP/VNC/SSH 网关；Java/C/JS | Apache-2.0 | 成熟；服务端跨平台、浏览器客户端 | 多协议远程访问面；大 | 很高 | 非目标。参考协议隔离/网关思路，不引入 guacd/完整栈 |
| noVNC | 浏览器 VNC Client；JavaScript | MPL-2.0 | 活跃；现代浏览器 | 依赖 VNC/RFB 远控面；中 | 中 | 当前无实时桌面需求，不直接依赖；未来若改变产品边界需新 ADR |

结论：Fast Spider 不应以“裁剪一个远程桌面项目”起步。现有系统可帮助理解设备连接、会话和安全提示，但它们都围绕连续桌面/通用远控，和本项目的结构化 Capability、Machine 与 Job 模型不同。

## 4. 浏览器自动化

| 项目 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| Playwright | Chromium/Firefox/WebKit 高层自动化；TypeScript/Node.js | Apache-2.0 | 高活跃；Windows/Linux/macOS | 浏览器供应链和页面输入风险需独立控制；浏览器集合为大 | 中高 | **Phase 5 推荐 Adapter**。以受管 sidecar/driver 隔离，核心不暴露原始 API |
| Chrome DevTools Protocol | Chrome/Chromium 调试命令与事件；协议定义 | BSD-3-Clause（官方协议仓库） | 随 Chromium 更新；Chromium | tip-of-tree 可能变化，稳定域能力有限；无独立大二进制 | 中 | 作为底层协议参考与兼容边界；不直接公开原始 CDP |
| chromedp | Go 的 CDP/Chromium 控制 | MIT | 活跃；依赖可用 Chrome/Chromium | 继承浏览器/CDP 风险；中 | 中 | Playwright 的 **Chromium-only 退出路径**。先做原型，不与 Playwright 并行维护全功能 |
| Chrome DevTools MCP | 面向 coding agent 的 Chrome 调试 MCP；TypeScript | Apache-2.0 | 活跃；Chrome | 明确会向 MCP 暴露浏览器内容；中/大 | 中 | 参考工具语义和风险提示；不嵌套 MCP Server，Fast Spider 自己统一授权/Job |

### 浏览器决策

- 首选 Playwright 隔离 Profile，获得稳定等待、Context、跨浏览器和测试 Artifact。
- Go Node 通过私有 IPC/stdio 管理 sidecar；sidecar 无公网监听。
- 浏览器和 driver 版本固定并形成兼容矩阵。
- 若安装体积和运维成本实际过高，可缩减为 chromedp + Chromium；该切换通过 Browser Adapter 接口完成，不改变公共 Capability Contract。

## 5. 文件搜索

| 项目 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| ripgrep | 高速递归正则搜索、gitignore/binary 处理；Rust | MIT OR Unlicense | 成熟活跃；Windows/Linux/macOS | 作为外部工具仍需限制 pattern/flags/输出；小到中 | 低 | **历史候选，当前未采用**；现有 `code.search` 使用有界 Go 实现，只有真实性能需求出现时再评估外部可执行程序 |

退出策略：`code.search` 契约不绑定 ripgrep flags。若版本、许可证或平台出现问题，可替换为 Go 搜索实现或其他工具，结果仍归一化为相同 Match Schema。

## 6. Git

| 项目 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| 系统 Git | 完整 Git 行为、凭据、LFS、hooks；C | GPLv2 | 高活跃；全平台 | 仓库配置/hooks/credential 是主要风险；外部安装，中 | 低 | **MVP 首选外部进程**，最大兼容用户环境；不静态链接，严格参数和非交互环境 |
| libgit2 | 可链接 Git 实现；C | GPLv2 with linking exception | 成熟；全平台 | 原生内存安全和 Git 行为差异需关注；中 | 高 | 不作为 MVP 主实现；若未来需要无外部 Git 的窄读能力再评估 |
| go-git | 纯 Go Git 实现 | Apache-2.0 | 活跃；全平台 | 与系统 Git/LFS/hooks/配置存在兼容差异；中 | 中 | 可用于纯内部仓库解析或测试，不替代用户 Git 主链路 |

退出策略：所有 Git Action 经 `GitPort`/固定 Action Contract；默认系统 Git Adapter。任何替换必须通过相同兼容和安全测试。

## 7. 终端与 PTY

| 项目/接口 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| xterm.js | 浏览器终端渲染；TypeScript | MIT | 高活跃；浏览器 | 只负责 UI，不提供 shell；中 | 中 | 仅在后续 Web PTY UI 使用；当前非交互 Shell 不需要 |
| Windows ConPTY | Windows 伪控制台系统 API | Windows SDK/API | Windows 10+ | 管道/线程处理不当会死锁；无第三方包体 | 中高 | 后续 PTY 的 Windows 正式路径；MVP 非交互执行先不依赖 |
| creack/pty | Unix PTY Go 接口 | MIT | 成熟；Unix 系统 | 示例不等于生产终端管理，需自行处理进程/信号；小 | 低中 | 后续 Linux/macOS PTY 候选；与 Job/取消/限额封装 |
| portable-pty | 跨平台 PTY trait；Rust | MIT | 活跃；Windows/Unix | 适合 Rust Node；小中 | 中 | 仅在选择 Rust Node 时考虑，Go MVP 不引入 Rust runtime |

PTY 会扩大交互、转义序列、会话保持和资源风险，因此不阻塞 Phase 1–4。

## 8. 可观测性

| 项目 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| OpenTelemetry Go | Trace/Metric API、SDK 与 Exporter；Go | Apache-2.0 | CNCF 生态活跃；跨平台 | 错误埋点会泄露数据/造成高基数；小到中 | 低中 | **推荐直接依赖 API/SDK**，Exporter 可选；MVP 不强制部署 Collector |

退出策略：核心使用小型内部 telemetry port；无 Exporter 时使用 no-op/内置指标。OpenTelemetry 不成为运行前置条件。

## 9. MCP SDK

| 项目 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| 官方 MCP Go SDK | MCP Server/Client、JSON-RPC、Auth/OAuth；Go | 新代码 Apache-2.0，既有代码 MIT 过渡 | 官方高活跃；跨平台 | 规范快速演进，必须固定版本、body limit 和 conformance；中 | 中 | **Go Hub 直接依赖**。MCP 只在 Adapter；版本升级不影响 FSWP |
| 官方 MCP Rust SDK（RMCP） | MCP Server/Client；Rust/tokio | Apache-2.0/MIT 许可过渡 | 官方高活跃；跨平台 | 同样需固定规范/版本；中 | 中 | 仅 Hub/Node 改用 Rust 时采用；Go MVP 不同时引入两套 SDK |
| MCP 规范仓库 | 协议规范和 JSON Schema | MIT/Apache 许可过渡 | 官方活跃 | 协议版本变化是兼容风险 | 低 | 作为权威规范来源；编码时记录 commit/version |

2026-08-08 调研时，官方 SDK 已围绕新的 MCP 规范继续演进。因此 Fast Spider 必须固定 SDK/规范版本、运行官方 conformance，并通过 Adapter 隔离变化，不能把 MCP 生命周期直接变成内部 Job/Node 协议。

## 10. 截图与窗口枚举

| 项目/方案 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| `kbinani/screenshot` | Go 多显示器截图 | MIT | 维护中；Windows/macOS/Linux（平台实现不同） | 桌面权限、Wayland/锁屏/窗口能力需验证；小中 | 低中 | 仅作为屏幕截图原型候选；窗口捕获和权限可能仍需平台 API |
| XCap | Rust 屏幕/窗口捕获 | Apache-2.0 | 活跃；Windows/macOS/Linux，Wayland有特殊限制 | 跨平台依赖较多；中 | 中高 | Rust helper/Node 候选；Go MVP 不为一个能力引入完整 Rust 边界 |
| 原生系统 API | Windows Graphics Capture、X11/Portal/PipeWire、macOS ScreenCapture | 平台 API | 随 OS | 最能符合权限模型，但实现/测试成本高；无统一包体 | 高 | **最终可靠路径**。先原型库，缺口使用窄 helper，不在 C++/Rust 内放业务策略 |

退出策略：`ScreenshotPort` 只返回统一像素/元数据和结构化错误；后端可按平台替换。不得把具体库的 monitor/window handle 作为公共 ID。

## 11. WebSocket 与 HTTP/2

| 项目/方案 | 解决的问题 / 语言 | 许可证 | 活跃度与平台 | 安全历史与体积 | 集成复杂度 | 结论 / 许可证影响 |
|---|---|---|---|---|---|---|
| `coder/websocket` | Go WebSocket Client/Server | ISC | 活跃；跨平台 | 仍需应用层限额/背压/Origin；小、零依赖 | 低 | **WSS 候选首选**，先做 Autobahn/断线/限额原型 |
| Gorilla WebSocket | Go WebSocket | BSD-2-Clause | 生态成熟 | API/维护路线需在固定版本时复核；小 | 低 | 备选，避免同时维护两套 |
| Go `net/http` / `x/net/http2` | HTTP/1.1、HTTP/2、TLS | BSD-style Go License | 官方维护；跨平台 | 依赖正确 timeout/header/body 限制；标准库级 | 低 | REST/MCP/下载基础；FSWP MVP 不改用自定义 HTTP/2 流 |
| tokio-tungstenite / hyper | Rust WebSocket/HTTP | MIT 等 | Rust 生态活跃 | 仅 Rust Node/Hub 时相关 | 中 | Go MVP 不引入；作为 Rust 方案参考 |

退出策略：FSWP 只依赖抽象 duplex transport 和 frame model；如未来迁移 HTTP/2/QUIC，Capability/Job/Event Contract 不变。

## 12. 数据存储组件

本次指定清单未要求具体 SQLite Driver，但编码前必须单独验证：

- CGO SQLite 驱动与 pure-Go SQLite 驱动的许可证、WAL、backup API、跨平台和发布体积。
- 加密需求是文件系统/备份层还是数据库层。
- Migration 工具是否值得引入，或使用小型内置 migration runner。

在完成 ADR 附录前不锁定具体 Driver；这不影响选择 SQLite WAL 作为 Hub MVP 存储。

## 13. 三套技术组合

### 组合 A：Go/Go 简洁自托管（已采用）

- Hub Go + Node Go。
- WSS 承载 JSON 控制消息；Artifact/Presentation 大文件使用 HTTP 数据面，不在 WSS 内另造二进制文件协议。
- JSON Schema 2020-12 契约源。
- SQLite WAL + Hub 本地 Artifact/Temporary Presentation 存储。
- 当前直接依赖官方 MCP Go SDK、coder/websocket、modernc SQLite、kbinani/screenshot、必要平台包。
- Git 使用系统 Git；当前 `code.search` 使用内置有界 Go 实现，不要求用户安装 ripgrep。
- Playwright 私有 sidecar 作为可选 Browser 组件。

### 组合 B：Go Hub + Rust Node

- Hub Go，Node Rust。
- JSON Schema 或 Protobuf 生成两端类型。
- Rust 在平台能力、资源控制和单文件发布上可能更强。
- 需要 Go/Rust 双语言、两套构建/安全/发布/依赖治理。
- DevSpace 类能力复用和迭代速度较低。

### 组合 C：分布式扩展优先

- Go/Rust、多实例 Hub、PostgreSQL、gRPC/HTTP2、事件总线、S3。
- 横向扩展强，但引入连接归属、分布式事件、对象存储和运维复杂度。
- 与单人自托管和当前规模不匹配。

## 14. 决策矩阵

评分 1（差）到 5（优）：

| 维度 | 权重 | A Go/Go | B Go/Rust | C 分布式优先 |
|---|---:|---:|---:|---:|
| 单人维护与开发速度 | 25% | 5 | 2 | 1 |
| 安全边界可实现性 | 20% | 4 | 5 | 4 |
| Windows/Linux 平台能力 | 15% | 4 | 5 | 4 |
| 部署与运维简单 | 15% | 5 | 3 | 1 |
| DevSpace 同类能力快速落地 | 10% | 5 | 2 | 2 |
| 未来扩展 | 10% | 4 | 4 | 5 |
| 依赖/许可证可控 | 5% | 4 | 4 | 3 |
| **加权结果** | 100% | **4.55** | **3.35** | **2.55** |

推荐组合 A。Rust 只在某个平台能力经原型证明 Go 无法可靠完成时作为窄 helper，而不是现在把整个 Node 改为双语言。

## 15. MVP 依赖建议清单

### 当前直接依赖

- 官方 MCP Go SDK：MCP Adapter。
- coder/websocket：Hub↔Node WSS。
- modernc SQLite：Hub 单机 WAL 数据库。
- kbinani/screenshot + 平台包：桌面/显示器/窗口截图。
- Go/x/sys、Windows UI 相关窄依赖：平台执行和本地 UI。

OpenTelemetry 仍只是后续可选 exporter，不是当前运行时必需依赖；JSON Schema 主契约由仓库 `contracts/v1` + contract generator 维护。

### 独立工具/sidecar

- 系统 Git：Git Adapter。
- Playwright driver/browser + Node runtime：Browser 可选组件包。
- ripgrep 保留为历史评估候选；当前 Code Search 不依赖它，只有真实性能需求出现并通过适配/发布评估后才考虑接入。

### 仅原型/参考

- chromedp、kbinani/screenshot、XCap、creack/pty。
- RustDesk、MeshCentral、Guacamole、noVNC、Chrome DevTools MCP。
- libgit2、go-git（非主链路）。

## 16. 依赖准入清单

引入前必须回答：

1. 固定版本、官方仓库和维护状态是什么？
2. LICENSE/NOTICE/SBOM 是否清楚，传递义务是什么？
3. 是否存在已知未修复高危安全问题？
4. 输入是否来自不可信网络/文件/页面，是否需要沙箱和限额？
5. 二进制/运行时/浏览器实际增量体积是多少？
6. Windows/Linux 构建、交叉编译和签名是否可靠？
7. 可否用系统组件/标准库完成？
8. 替代方案和移除成本是什么？
9. 升级失败如何回滚？
10. 是否污染核心协议、公开 Schema 或许可证？

## 17. 来源索引（官方）

- RustDesk 官方仓库：`github.com/rustdesk/rustdesk`
- MeshCentral 官方仓库：`github.com/Ylianst/MeshCentral`
- Apache Guacamole：`guacamole.apache.org` / Apache 官方仓库
- noVNC：`github.com/novnc/noVNC`
- Playwright：`github.com/microsoft/playwright`
- Chrome DevTools Protocol：`github.com/ChromeDevTools/devtools-protocol`
- chromedp：`github.com/chromedp/chromedp`
- ripgrep：`github.com/BurntSushi/ripgrep`
- libgit2：`github.com/libgit2/libgit2`
- go-git：`github.com/go-git/go-git`
- xterm.js：`github.com/xtermjs/xterm.js`
- ConPTY：Microsoft Learn Console/Pseudoconsole 文档
- creack/pty：`github.com/creack/pty`
- portable-pty：WezTerm 仓库/crate
- OpenTelemetry Go：`github.com/open-telemetry/opentelemetry-go`
- MCP Go/Rust SDK：`github.com/modelcontextprotocol/go-sdk`、`rust-sdk`
- XCap：`github.com/nashaofu/xcap`
- coder/websocket：`github.com/coder/websocket`

## 18. 当前验证状态与仍保留的候选

早期“编码前必须决定”的大部分技术选型已经由实现和 release gate 收敛：

- SQLite 已选择 modernc pure-Go 路线，并有 backup/verify/restore E2E。
- Browser 已采用 Node 管理的 Playwright sidecar + 可选组件包，并进入 real Browser E2E。
- Windows/Linux 截图路径已有平台实现和测试；Windows 额外支持窗口枚举/窗口截图。
- coder/websocket 已进入真实 Hub↔Node 长连接，并有断线、generation、in-flight/重连测试。
- Code Search 当前使用有界 Go 实现；ripgrep 不属于发布前置条件。

仍保留为后续真实需求触发的评估项：更完整的安装器/代码签名、可选 telemetry exporter、macOS/更多架构，以及 Code Search 在数据证明有性能瓶颈后是否切换外部搜索引擎。具体开放问题以 [20-open-questions.md](20-open-questions.md) 为准。
