# ADR 0005：浏览器控制

## 状态

Current：已接受。隔离 Browser + fixed actions 决策仍有效；网络能力按 Node 当前 OS 和网络可达性执行，不维护 Fast Spider Origin 白名单。

## 决策

MVP 使用 Node 管理的隔离 Browser Profile，通过 Go Browser Manager 和 Playwright Sidecar 控制。Sidecar 只走 stdio，不监听公网、局域网、TCP 或 CDP 端口。

## 网络与动作

- 允许 Node 可访问的公网、localhost 和私网 HTTP/HTTPS/WS/WSS。
- 不开放 `file:`、危险 scheme、任意 JavaScript、CDP、Playwright 原始 API 或端口转发。
- 页面、下载、动作、事件和 Artifact 受固定 Schema、超时、大小、并发和清理策略限制。
- Browser Session、Context、Page、Download 和 Window 使用 opaque ID，不返回调试端口、OS 句柄、Profile 路径或 CDP URL。

## 截图与生命周期

页面截图、桌面截图、显示器截图和窗口截图通过 Hub Temporary Presentation Relay 的 attachment 模式返回 URL-only 临时附件，不创建普通 Artifact 记录；MCP/Direct 只返回 `url/fileName/contentType/sizeBytes/expiresAt`，不返回原生 `ImageContent` 或 `ResourceLink`。新 Node 的截图附件最长保留 48 小时并自动删除。窗口先由 `listWindows` 取得短期 `windowId`。取消时先中断动作，再清理 Context、Browser、Sidecar 和临时文件；清理不完整不得报告成功。

## 后续

接管用户真实浏览器、任意脚本、Trace/HAR、视频或更细网络控制只有出现真实需求时才新增 ADR，并明确秘密、审计和清理边界。
