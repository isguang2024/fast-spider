# ADR 0005：浏览器控制模式

- 状态：Proposed
- 日期：2026-08-08
- 决策者：Fast Spider Owner
- 适用范围：Phase 5 浏览器自动化和页面截图

## 背景

Fast Spider 需要在 Node 本机启动浏览器、访问开发页面、点击、输入、等待、读取结构化页面信息并返回截图和测试结果。它不应默认接管用户日常浏览器，也不能把任意 CDP/Playwright 命令直接暴露给远程 Client。

需求要求评估：

A. Node 启动并管理隔离 Browser Profile。
B. 通过扩展或调试连接控制用户现有浏览器。

同时需要比较 Playwright、CDP 和 chromedp。

## 决策驱动因素

- 用户 Cookie、密码和真实登录态隔离。
- 自动化稳定性、等待语义和跨平台能力。
- 适合结构化 Capability、Job、Event 和 Artifact。
- 安装体积与 Node 运维成本。
- URL/SSRF、下载、脚本和进程可控。
- 可在不改变公共契约的情况下替换底层 Adapter。

## 考虑的控制模式

### 模式 A：Node 管理隔离 Profile

优点：

- 默认不含用户 Cookie、扩展和历史。
- Profile/Context/Page 生命周期可由 Job 管理。
- 下载、网络、截图、Trace 和清理边界清楚。
- 更容易复现测试。

缺点：

- 需要下载/管理浏览器和 driver。
- 登录态测试需要显式注入测试凭据或后续受控能力。

### 模式 B：控制现有浏览器

优点：可以访问用户已有登录态和页面。

缺点：

- 隐私和误操作风险极高。
- 扩展、调试端口、版本和用户操作的兼容复杂。
- 容易把一次开发测试扩大为浏览器账户控制。

## 考虑的实现

### Playwright

- 高层定位、自动等待、隔离 Context、跨 Chromium/Firefox/WebKit。
- 截图、下载、console、network、Trace 等测试能力成熟。
- 需要 Node.js/driver 和浏览器组件，体积较大。

### chromedp

- Go 中直接控制 Chromium/CDP，部署较轻。
- 只覆盖 Chromium，高层测试语义与跨浏览器一致性较弱。

### 原始 CDP

- 能力底层且灵活。
- 版本变化和安全面大；直接公开会绕过 Fast Spider 固定动作和策略。

## 决策

Phase 5 默认选择：

1. **模式 A：Node 管理的隔离 Browser Profile**。
2. **Playwright 私有 Adapter/sidecar** 作为首选实现。
3. Node 使用 stdio、Named Pipe 或 UDS 管理 sidecar；sidecar 不监听公网，也不直接接受 MCP/Local Client。
4. 公共 `browser.automation` 只暴露固定动作和结构化 locator，不透传任意 Playwright/CDP 请求。
5. Browser Session、Context、Page、Download 都使用 opaque ID，不返回 CDP WebSocket URL、调试端口、OS 句柄或 Profile 路径。
6. 页面截图和桌面/窗口截图使用不同 Capability，但个人 MVP 不再要求额外 Workspace 权限；都受 Machine/Workspace、大小、资源和 Artifact 边界约束。

模式 B 不是 MVP。未来加入时作为独立高风险 Capability：用户本机显式启用，限定浏览器/Profile/域名/期限，有明显可见状态，不返回 Cookie 原文，并可立即撤销。

## Sidecar 边界

- Go Node 负责身份、Workspace、权限、URL 策略、Job、deadline、取消、Artifact 和审计。
- sidecar 只负责浏览器引擎适配和动作执行。
- sidecar 协议有版本、输入上限和固定 Action。
- sidecar崩溃不拖垮 Node；Node 回收浏览器子进程和临时 Profile。
- Provider/页面不能让 sidecar访问 Node 文件系统或任意执行本机 JavaScript/Node 模块。
- 浏览器组件是可选包；未安装时能力发现明确报告 unavailable。

## 网络策略

默认：

- 允许经过策略的 HTTP/HTTPS 公网目标。
- 阻止 `file:`、`javascript:`、危险自定义 scheme。
- 阻止云元数据、link-local、危险/特殊地址。
- 公网 HTTP/HTTPS/WS/WSS 默认允许；loopback、RFC1918、ULA、CGNAT 等本地/私网目标必须在 Node 本机加入精确 Origin 白名单。
- DNS 解析后、重定向和子请求重新检查，私网白名单固定解析 IP，防 DNS rebinding。

本地开发站点白名单持久绑定 workspaceId + scheme + host + port；配置一次后一直生效，直到用户本机删除。浏览器在 Node 本机访问；Fast Spider 不做端口转发，也不向 Client 暴露本地服务。

## 页面数据

默认返回有限可访问性树/结构化摘要，不返回完整 DOM：

- role/name/state、landmark、可见错误和受限文本。
- 密码、信用卡、安全码等字段值永不返回。
- 节点、深度和文本长度有上限。
- 任意 `evaluate` 不进入 MVP；后续 `evaluateRestricted` 使用独立 Action、脚本摘要和时限。

## 下载与 Artifact

- 下载进入 Session 隔离临时目录。
- 限制大小、数量、类型和时间。
- 不自动打开、执行、解压或移动到 Workspace。
- 导入 Workspace 是独立文件写 Action并重新授权。
- screenshot、报告、可选 Trace/HAR 通过 Artifact 返回。
- HAR/Trace 可能包含 Header、Cookie 和正文，默认关闭并需脱敏/单独授权。

## chromedp 退出策略

在 Phase 5 前做实际原型测量：安装体积、离线安装、稳定性、跨平台、崩溃清理和版本升级。如果 Playwright sidecar 对自托管 MVP 代价明显过高，可切换到 **chromedp + Chromium-only**。

切换条件：

- 公共 Browser Capability 和 Action Schema 不变。
- 明确缩小支持引擎，不伪报 Firefox/WebKit。
- 通过同一 URL、安全、取消和 Artifact 测试。
- 不长期维护两套完整浏览器实现。

## 后果

### 正面

- 用户真实浏览器和登录态默认安全隔离。
- 自动化测试语义稳定，适合 AI 和 E2E。
- 底层浏览器库不会污染公共协议。
- 浏览器能力可选安装，不影响核心 Node。

### 负面

- Playwright/浏览器安装体积和更新成本较大。
- 需要维护 sidecar 版本和跨进程取消/清理。
- 用户需要在隔离环境重新登录或提供测试凭据。

## 不采用的做法

- 不启动远程调试端口并将地址交给 Hub/Client。
- 不把任意 CDP method 或 Playwright script 暴露为万能工具。
- 不默认复制用户 Browser Profile。
- 不因测试本地页面而实现通用端口转发。
- 不实现持续桌面视频或通用鼠标键盘远控。

## 相关文档

- [浏览器与截图](../12-browser-and-screenshot.md)
- [安全威胁模型](../09-security-threat-model.md)
- [开源组件评估](../18-open-source-evaluation.md)
- [路线图](../19-roadmap.md)
