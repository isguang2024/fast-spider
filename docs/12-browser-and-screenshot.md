# 12 浏览器与截图（Current）

## 1. 范围

Fast Spider 的浏览器能力用于开发页面验证、自动化测试、结构化页面读取和一次性截图，不用于通用远程桌面。

支持 Node 启动/关闭隔离浏览器、页面导航、列表、可访问性快照、短期元素 ref、点击、输入、按键、等待、受限批量动作、页面截图、下载、控制台日志、网络错误以及桌面/显示器/窗口截图。

不支持持续桌面视频/音频、通用鼠标键盘远控、默认接管用户现有浏览器登录态或向 Client 暴露任意 CDP 命令。

## 2. 当前实现

- Go `BrowserManager` + 私有 Playwright Chromium Sidecar。
- Browser 作为签名可选组件按需安装；Windows 组件自带 Sidecar、Playwright、Chromium/headless shell、ffmpeg 和 `node.exe`，不要求用户另装 Node.js。安装完成后本地 UI 自动把组件路径接入 Node 并刷新运行时能力。
- Sidecar 只通过 Node 子进程 stdio 通信，不监听 TCP/CDP 端口。
- 每个 Node 最多一个受管 Browser Session、最多 8 个页面；Session 空闲 10 分钟自动关闭。
- Browser 的远程边界是 Machine；不叠加目录授权或 Origin 白名单。
- Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS 目标均可访问；是否可达由 Node 的 OS、网络和浏览器运行时决定。
- `browser.automation` 1.2 固定动作：`readiness/launch/close/page.open/page.navigate/page.close/pages.list/click/type/press/wait/batch/snapshot/screenshot/events`。
- 不公开任意 JavaScript、`evaluate`、CDP、Playwright 原始 API、Trace/HAR/视频。
- 页面截图和 OS 截图由 Node 直接上传到 Hub Temporary Presentation Relay 的 attachment 模式；Relay 不写数据库，MCP/Direct 只返回临时 URL 元数据，不再把图片内容或 ResourceLink 展开到聊天界面。新 Node 的截图附件最长保留 48 小时，由 Hub 生命周期维护自动删除。
- Node 始终宣告 `browser.automation`，使缺失运行时也能调用 `readiness` 得到 `not_configured/node_runtime_missing/sidecar_files_missing/protocol_mismatch/chromium_missing/probe_timeout/sidecar_start_failed` 等稳定原因。正缓存 30 秒、负缓存 5 秒，不重复探测/下载。

## 3. 浏览器控制模式

MVP 选择 Node 管理的隔离 Profile：专用数据目录、无用户 Cookie/扩展/历史，生命周期、下载、网络和 Job 均由 Node 控制。截图临时文件只在 Node 本地存在到上传完成；未来若支持连接现有真实浏览器，必须作为独立高风险能力，要求用户显式启用并限定浏览器实例/Profile/域名范围。

## 4. Browser Manager

| 组件 | 责任 |
|---|---|
| Browser Runtime Registry | 引擎、版本、driver 和安装状态 |
| Profile Manager | 隔离 Profile、Context、配额和清理 |
| Browser Session Manager | browserSessionId、页面和生命周期 |
| Action Executor | 固定动作与参数校验 |
| Network Policy | 主动导航 scheme、下载和 OS 网络条件；不做逐请求 DNS/pinned-IP 审查 |
| Event Collector | console、network error、page crash、download |
| Presentation Relay | screenshot 等临时资源由 Node 直传 Hub 临时目录，MCP/Direct 只返回最长 48 小时的 URL 元数据 |
| Cleanup/Reaper | 崩溃进程、过期 Profile、临时下载清理 |

对外只使用 `browserSessionId`、`browserContextId`、`pageId`、`downloadId`、`windowId`、`displayId` 等 opaque 标识；截图完成后 Hub 会把内部 `presentationId` 收敛为 `url/fileName/contentType/sizeBytes/expiresAt`，不向 MCP/Direct 暴露内部 relay ID、调试端口、OS 句柄、Profile 绝对路径或 CDP URL。

## 5. 生命周期与固定动作

Session 状态为 `created → launching → ready → running_action → closing → closed`，异常进入 `failed/lost`。一个 Browser Session 绑定 Machine、创建 Client 和隔离 Profile；多步骤测试使用长 Job，Node 重启后旧 Browser Session 不恢复。

固定动作：

- 生命周期：`launch`、`close`、`pages.list`。
- 页面：`page.open`、`page.navigate`、`page.close`、`snapshot`、`screenshot`。
- 交互：`click`、`type`、`press`、`wait`；优先使用 `snapshot` 返回的短期 `ref`。
- 批量：`batch` 在 Node 内连续执行 1-32 个 `click/type/press/wait`，可用 `snapshotAfter=true` 一次返回新快照，减少 MCP 往返。
- 诊断：`events`，返回有界 console/network/download 摘要。
- 预检：`readiness` 不创建 Browser Session，返回 ready/state/reasonCode/cacheHit/checkedAt/totalMs。

`snapshot` 同时返回完整 `ariaSnapshot`、面向 Agent 的 `agentSnapshot` 和结构化 `refs`。每次新 snapshot 会轮换当前页面 ref；页面导航或元素脱离 DOM 后旧 ref 立即返回 `BROWSER_REF_STALE`，不等待普通 Locator 超时。Client 不传可执行回调；当没有可用 ref 时，Locator 仍可使用 role、label、text、testId 和受限纯 CSS，不支持 XPath。

所有真实 Browser 动作返回 `timing={startupMs,operationMs,coldStart,queueMs,totalMs}`；sidecar 内已有 locator/action/wait 等细分计时会原样保留。计时只在响应中出现，不建立长期高频 trace。

## 6. 网络策略

浏览器允许 Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS。Fast Spider 不维护私网 Origin 白名单、TTL、pinned IP 或“先加入地址再访问”的目录式授权流程，也不再为每个页面子资源执行 `dns.lookup`、route 拦截或 WebSocket DNS 审查。网络请求直接使用 Chromium + OS 网络栈。

仍执行以下固定安全检查：

- 拒绝 `file:`、`javascript:`、危险自定义 scheme 和非浏览器动作。
- 不开放任意 JavaScript、CDP、Playwright 原始 API 或端口转发。
- 页面子资源、重定向和 WebSocket 由隔离 Chromium 按正常浏览器网络语义处理；Fast Spider 不额外增加逐请求 DNS 审查。
- 限制页面数量、下载大小、动作超时、Job 时长、输出、临时目录和展示资源大小。

这意味着浏览器明确拥有 Node 网络视角下的公网、localhost 和私网访问能力；部署者应通过 OS、防火墙、运行账户和实际网络拓扑管理风险。

## 7. 下载、测试 Job 与截图

下载进入 Browser Session 的隔离临时目录，限制类型、数量、大小和时间；不自动执行或解压。若用户要把文件放到任意绝对路径，必须另行调用文件能力并由 Node 以当前 OS 用户处理。

浏览器测试使用固定动作，不接受任意代码文件。Agent 应先取 snapshot，再直接使用返回 ref；多步表单优先合并为 batch：

```json
{"action":"snapshot","browserSessionId":"brs_x","pageId":"pg_x"}
```

快照示例：

```text
- textbox "Email" [ref=e_1]
- textbox "Password" [ref=e_2]
- button "Sign in" [ref=e_3]
```

随后一次执行：

```json
{
  "action":"batch",
  "browserSessionId":"brs_x",
  "pageId":"pg_x",
  "steps":[
    {"action":"type","ref":"e_1","text":"user@example.com"},
    {"action":"type","ref":"e_2","text":"..."},
    {"action":"click","ref":"e_3"}
  ],
  "snapshotAfter":true
}
```

桌面截图支持当前桌面、指定显示器、指定窗口和浏览器页面；窗口通过 `listWindows` 返回短期 `windowId` 选择，不暴露 OS 句柄。截图使用 PNG/JPEG、像素/编码大小和单 Node 并发上限。用于 AI 分享的截图在上传前自动优化：不超过 1 MiB、宽度不超过 2560 且总像素不超过 400 万时保持原图；超过任一阈值时按比例缩放到宽度最多 2560/总像素最多约 400 万并以 JPEG quality 82 编码。若只因文件大小触发且 JPEG 没有变小则保留原图；因尺寸触发时始终使用缩放后的 JPEG。该保护可覆盖显式请求的 PNG，因为它约束的是临时附件结果而不是原始截图文件。Node 只负责把最终图片以设备凭据直接上传 Hub Presentation Relay；Hub 校验大小和 SHA-256，并以 attachment 语义返回 `url/fileName/contentType/sizeBytes/expiresAt`，最长 48 小时后删除。

## 8. 取消与清理

取消时 Node 先中断动作，再关闭 Context/Browser 和 sidecar 子进程；清理不完整不得虚报 `canceled`。启动时扫描并清理无 owner 的过期 Profile、进程、下载、Trace 和截图临时文件。Browser 崩溃不使 Node 退出，返回 `BROWSER_CRASHED` 和有界诊断。

## 9. 平台与验收

- Windows 处理用户会话、锁屏、UAC 安全桌面、最小化窗口、多显示器、缩放和 HDR。
- Linux 处理 X11、Wayland Portal、无图形会话和锁屏；不通过 root 绕过 OS 安全模型。
- 隔离 Profile 无法读取用户日常浏览器 Cookie。
- 可打开 Node 网络可达的公网、localhost 和私网页面并返回摘要；截图通过 Hub Temporary Presentation Relay 返回最长 48 小时的 URL-only 临时附件，不返回 `ImageContent` 或 `ResourceLink`。
- 危险 scheme、任意脚本、原始 CDP、端口转发和通用远控不可用。
- 无桌面或 OS 权限不足时返回结构化错误，不尝试提权。
