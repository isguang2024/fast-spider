# 12 浏览器与截图（0.3.0）

## 1. 范围

Fast Spider 的浏览器能力用于开发页面验证、自动化测试、结构化页面读取和一次性截图，不用于通用远程桌面。

支持 Node 启动/关闭隔离浏览器、页面导航、列表、点击、输入、按键、等待、可访问性摘要、受限动作、页面截图、下载、控制台日志、网络错误以及桌面/显示器/窗口截图。

不支持持续桌面视频/音频、通用鼠标键盘远控、默认接管用户现有浏览器登录态或向 Client 暴露任意 CDP 命令。

## 2. 当前实现

- Go `BrowserManager` + 私有 Playwright Chromium Sidecar。
- Sidecar 只通过 Node 子进程 stdio 通信，不监听 TCP/CDP 端口。
- 每个 Node 最多一个受管 Browser Session、最多 8 个页面；Session 空闲 10 分钟自动关闭。
- Browser 的远程边界是 Machine；不叠加目录授权或 Origin 白名单。
- Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS 目标均可访问；是否可达由 Node 的 OS、网络和浏览器运行时决定。
- 固定动作：`launch/close/page.open/page.navigate/page.close/pages.list/click/type/press/wait/snapshot/screenshot/events`。
- 不公开任意 JavaScript、`evaluate`、CDP、Playwright 原始 API、Trace/HAR/视频。
- 页面截图和 OS 截图直接上传 Hub Artifact；远程结果不返回临时路径。
- Sidecar、Playwright 或受管 Chromium 缺失时，Node 不宣告 `browser.automation`。

## 3. 浏览器控制模式

MVP 选择 Node 管理的隔离 Profile：专用数据目录、无用户 Cookie/扩展/历史，生命周期、下载、网络、Job 和 Artifact 均由 Node 控制。未来若支持连接现有真实浏览器，必须作为独立高风险能力，要求用户显式启用并限定浏览器实例/Profile/域名范围。

## 4. Browser Manager

| 组件 | 责任 |
|---|---|
| Browser Runtime Registry | 引擎、版本、driver 和安装状态 |
| Profile Manager | 隔离 Profile、Context、配额和清理 |
| Browser Session Manager | browserSessionId、页面和生命周期 |
| Action Executor | 固定动作与参数校验 |
| Network Policy | scheme、解析、重定向、下载和 OS 网络条件 |
| Event Collector | console、network error、page crash、download |
| Artifact Bridge | screenshot、报告和受限诊断上传 |
| Cleanup/Reaper | 崩溃进程、过期 Profile、临时下载清理 |

对外只使用 `browserSessionId`、`browserContextId`、`pageId`、`downloadId`、`windowId`、`displayId` 和 `artifactId` 等 opaque 标识，不返回调试端口、OS 句柄、Profile 绝对路径或 CDP URL。

## 5. 生命周期与固定动作

Session 状态为 `created → launching → ready → running_action → closing → closed`，异常进入 `failed/lost`。一个 Browser Session 绑定 Machine、创建 Client 和隔离 Profile；多步骤测试使用长 Job，Node 重启后旧 Browser Session 不恢复。

固定动作：

- 生命周期：`launch`、`close`、`pages.list`。
- 页面：`page.open`、`page.navigate`、`page.close`、`snapshot`、`screenshot`。
- 交互：`click`、`type`、`press`、`wait`。
- 诊断：`events`，返回有界 console/network/download 摘要。

Client 不传可执行回调；Locator 使用 role、label、text、testId 和受限 CSS/XPath。

## 6. 网络策略

浏览器允许 Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS。Fast Spider 不维护私网 Origin 白名单、TTL、pinned IP 或“先加入地址再访问”的目录式授权流程。

仍执行以下固定安全检查：

- 拒绝 `file:`、`javascript:`、危险自定义 scheme 和非浏览器动作。
- 不开放任意 JavaScript、CDP、Playwright 原始 API 或端口转发。
- 重定向、页面子资源和 WebSocket 只接受浏览器固定 Schema 允许的网络动作。
- 限制页面数量、下载大小、动作超时、Job 时长、输出、临时目录和 Artifact 保留。

这意味着浏览器明确拥有 Node 网络视角下的公网、localhost 和私网访问能力；部署者应通过 OS、防火墙、运行账户和实际网络拓扑管理风险。

## 7. 下载、测试 Job 与截图

下载进入 Browser Session 的隔离临时目录，限制类型、数量、大小和时间；不自动执行或解压。若用户要把文件放到任意绝对路径，必须另行调用文件能力并由 Node 以当前 OS 用户处理。

浏览器测试使用固定步骤，不接受任意代码文件：

```json
{
  "steps": [
    {"action": "navigate", "url": "http://localhost:3000"},
    {"action": "click", "locator": {"role": "button", "name": "Sign in"}},
    {"action": "type", "locator": {"label": "Email"}, "textRef": "secret-input-ref"},
    {"action": "wait", "locator": {"text": "Dashboard"}},
    {"action": "screenshot", "name": "dashboard"}
  ]
}
```

桌面截图支持当前桌面、指定显示器、指定窗口和浏览器页面；窗口通过 `listWindows` 返回短期 `windowId` 选择，不暴露 OS 句柄。截图使用 PNG/JPEG、像素/编码大小和单 Node 并发上限。

## 8. 取消与清理

取消时 Node 先中断动作，再关闭 Context/Browser 和 sidecar 子进程；清理不完整不得虚报 `canceled`。启动时扫描并清理无 owner 的过期 Profile、进程、下载、Trace 和截图临时文件。Browser 崩溃不使 Node 退出，返回 `BROWSER_CRASHED` 和有界诊断。

## 9. 平台与验收

- Windows 处理用户会话、锁屏、UAC 安全桌面、最小化窗口、多显示器、缩放和 HDR。
- Linux 处理 X11、Wayland Portal、无图形会话和锁屏；不通过 root 绕过 OS 安全模型。
- 隔离 Profile 无法读取用户日常浏览器 Cookie。
- 可打开 Node 网络可达的公网、localhost 和私网页面并返回摘要/截图 Artifact。
- 危险 scheme、任意脚本、原始 CDP、端口转发和通用远控不可用。
- 无桌面或 OS 权限不足时返回结构化错误，不尝试提权。
