# 测试策略（0.4.18）

发布门禁必须验证新的 Machine 边界，而不是旧目录授权模型。

Browser 发布门禁必须把 Sidecar、Playwright、Chromium 与 Node runtime 打成组件包，解压到临时安装目录，再从该目录执行真实 Chromium E2E；只从仓库源码目录运行不算通过。URL 策略测试必须允许公网、localhost、loopback、RFC1918、WSL/Docker/LAN/开发 hostname，并拒绝非 HTTP(S)、非法 URL 与带 userinfo 的 URL，且不得触发 DNS 审查。

Codex 发布门禁必须在实际选择的 runtime 上覆盖 create/get/list/watch/result/send/fork，确认 completed session 可 fork、新旧 session ID 不同、fork 后可继续发送且原 session 不变；运行时配置不兼容必须返回脱敏的稳定错误分类，不能退化成调用参数 `INVALID_REQUEST`。

Node 发布推送门禁必须验证“忙碌不重启”：先启动一个真实长任务，再发起 `node-update-push`；Node 可完成 Ready 预下载并上报 `busy`，但版本/PID 不得在任务结束前切换，Job 必须自然完成。之后连续空闲达到 grace 后才允许自更新；新任务在 release drain 窗口返回可重试 `NODE_UPDATING`。最终 Node 版本/SHA、generation 和 `.previous` 回滚副本必须对账通过。

MCP 分层能力门禁必须使用冷 Client 验证 initialize 返回 `FastSpider_FS` Server Title 和不超过 2 KiB 的能力地图，覆盖十类能力、`@FastSpider_FS`、`machine_list`、`audit_log`、按需指南与 `session.list`；tools/list 固定 19 个工具。`audit_log` 必须验证 Owner 隔离、无 Node 可读及过滤/limit 边界；`machine_list` 描述中的唯一过滤标记 `fsprobe` 必须只出现一次，默认 20/最大 50 的稳定页必须覆盖 `hasMore/nextCursor`，默认不得读取或返回完整 capability descriptors，`includeCapabilities=true` 才展开。完整 19 工具目录、连接入口三工具和任一单工具序列化体积上限分别为 48 KiB、8 KiB、8 KiB。无参数 Direct 及 Machine 旧调用必须兼容；overview 不超过 8 KiB，单个 tool/workflow/error 不超过 12 KiB；未知 view、缺 name 和未知 name 必须拒绝。注册工具、指南目录和公共文档工具名必须自动对账。

MCP 调用诊断门禁必须通过真实 SDK 请求确认 initialize、tools/list、tools/call、工具名、成功/失败和稳定错误分类；0.4.17 还必须验证通过 Bearer Token 的 MCP HTTP 请求会更新 `lastMcpRequestAt`，但不会伪造一条 method event。每 Owner ring 最多 64 条、Owner 快照空闲 TTL 24 小时、总量最多 1024，并验证过期及最久未触达淘汰；不同 Owner 隔离。序列化结果不得包含 arguments、Prompt、Token、路径或原始 User-Agent。默认工具结果必须省略模型可见的 transport IDs/timing、保留业务续作字段并把诊断放入 `_meta`；`diagnostics=true` 恢复完整结果；普通 structured result 不得再复制成文本 `content`，Artifact 原生内容仍保留。后台 API 未登录返回 401，登录后只能读取当前 Owner 的快照；页面只加载一次并提供手动刷新。

## 必测主链

1. Connection Token 可重复登记多台 Node；Node 后续只用 Device Key。
2. MCP OAuth 完成后 tools/list 返回 17 个固定工具，包含 `thinking_team`、`working_context` 且不包含旧目录列表能力。
3. machine_list/machine_get 不泄露秘密。
4. file_read/file_edit 使用绝对路径；相对路径拒绝。
5. code_search 使用绝对目录。
6. shell_run/build_control 使用绝对 cwd，并验证 timeout、idempotency、取消和进程树清理。
7. job_watch/job_cancel 只依赖 jobId。
8. git_control 使用绝对 repositoryPath，覆盖读写和受控网络动作。
9. Artifact 上传绝对路径文件和 Job 日志；数据库不再有目录授权关联字段。
10. Browser Automation 1.1 在隔离 Profile 中可访问 Node 网络可达的公网/localhost/私网，不做逐请求 DNS/IP 审查；`snapshot` 必须返回可操作 ref，`batch` 必须支持 1-32 个固定动作与 `snapshotAfter`，旧 ref 必须快速返回 `BROWSER_REF_STALE`，显式导航的非 HTTP(S) 危险 scheme 仍拒绝。
11. Screenshot window token 不依赖目录授权对象。
12. `routing.status` 真实只读 CC Switch DB：三类 app route、current Provider、takeover/live takeover、model mapping、selectionConsistent、sanitizer 和 request-log Session correlation 均通过。
13. `providers.list` 经 Local Bridge 同时发现 Codex + Claude Code，并验证每个 Harness 的 `supportedActions`、route 与 provider-specific capabilities。
14. Codex adapter 专项 E2E 覆盖 Provider capabilities、Hooks、Permission Profiles、Installed Plugins、MCP status、Goal 与 app-server auto-resume；产品发布门禁以 Local Bridge 全链路的 create/get/list/watch/result/send/fork/cancel 为准，避免重复消耗同一上游会话造成非产品链路的干扰。
15. Claude Code E2E 覆盖 CLI availability、stdin Prompt、原生 UUID、stream-json init/result、Session index、终态归一化和 RouteSnapshot；upstream 认证失效时允许正确终止为 failed，但 Runtime/lifecycle 必须完整。
16. Windows 托盘、隐藏自启动、自更新 PID 等待链路通过。
17. Working Context 覆盖单文本 `get/set/clear`、revision CAS、旧 schema 文本迁移、敏感内容边界与本地页面。
18. code_search 2.1 同时覆盖受管 ripgrep 安全 argv/environment/JSON parser、稳定原因码、扫描统计与 native content/files/glob/context fallback。
19. file_read 2.0 覆盖单次扫描的 byte/line/head/tail/around/stat/hash/bounds；file_edit 2.1 覆盖元数据-only mutation、create/replace/editMany/preview、CAS、全有或全无和原子替换。
20. 组件中心只接受 Browser/search-ripgrep allowlist；本地自检必须使用临时 data-dir 并确认 preview 不落盘、临时目录已清理。
21. Node updater 必须覆盖 Ready/apply→consumed-current cleanup 启动顺序、Ready error 不清理、marker fail-safe、current 删除以及 future/old/unknown 分治。
22. Windows legacy install cleanup 必须覆盖严格 temp/marker/backup 命名、Win32 reparse fail-closed、非递归、unknown/current/previous 保留、空目录删除、幂等和 NodeUI 启动时机；非 Windows 为 no-op。
23. Release backup prune 必须覆盖严格标准文件名、绝对 non-reparse root、默认 plan-only、显式 apply、全部 Verify 后再删除、同进程 backup/create 目录串行、逐候选删除前身份复核、删除中路径替换停止后续删除、CreatedAt UTC 排序和 filename tie-break、损坏/匹配 symlink 零删除、历史异名/未知/子目录保留、keep/candidate bounds、幂等与部分删除结果。
24. Release staging prune 必须覆盖 local/server 严格目录名、绝对 non-reparse root、plan/apply、old/current/future、unknown/普通文件保留、root/candidate/nested reparse 零删除、candidate/file/byte/depth bounds、删除前 TOCTOU 复核、幂等与部分删除事实。
25. file_edit mutation 响应不得含 diff/正文，1 行、50 行、editMany、大文件响应大小不随目标文件增长；preview diff 必须 bounded，CAS 冲突 details 不含正文。
26. code_search 必须覆盖 stable RG reason code、VCS ignore、通用生成目录、显式 include 优先、扫描统计与 primary/fallback timing。
27. host/WSL runtime 必须覆盖任意盘符、空格/中文 cwd、真实执行/build/cancel、keepalive 数量/关闭边界与 Job trace/timing。
28. Agent/Browser readiness 必须证明不产生 Prompt/Session，Codex/Claude create 幂等需覆盖并发、重启重放、spec 冲突、确定失败释放、in-doubt 按 key 显式对账释放、语义损坏 fail-closed、容量上限，以及 delete intent/Provider not-found 续做回收；Browser launch/open/click/type/snapshot/screenshot 必须带 timing，readiness 并发不得在握手完成前误报 ready。
29. 缓存回归必须覆盖满容量同 key 更新不淘汰其它 key、`[]any` 内嵌 map/slice 的返回值隔离、availability holder 容量上限，以及 Codex 删除 thread 后 loaded 记录回收。
30. Browser/组件生命周期必须覆盖启动时只清理超过宽限期的严格 `brs_` 普通目录、unknown/link/reparse/新目录保留，以及并发 Ensure 不共用下载或解压临时路径、不被 Cleanup 互删。
31. Artifact 生命周期必须覆盖不同 upload 可并行、同 upload 串行、maintenance 与上传不交错、文件删除失败持久重试、有界批次、共享 Blob 引用保护，以及清理失败可观察。
32. Release manifest 缓存必须覆盖串行与并发命中只执行一次 hash/sign、产物或版本变化立即失效、同尺寸同 mtime 原子替换仍失效、缺失/损坏 fail-closed，HTTP `no-store` 不变。
33. Claude 控制索引必须覆盖损坏/超限/非法版本 fail-closed、落盘/容量/目录同步失败不返回虚假成功、同 Session 并发 Send 只有一个请求取得 active 预留、失败请求不回滚成功 turn、失败后内存与重载状态一致，并确认 Fast Spider 不删除 Claude 原生历史。
34. Secret gate 必须覆盖 tracked、untracked nonignored、staged-only、reachable history、dangling object、原始二进制、有界 ZIP、实际导出树与输出脱敏；测试占位仅允许按值精确识别，不得按测试目录或 `*_test.go` 宽泛跳过。默认本地 private markers 只用于当前 worktree/index 与 `--tree`，历史仅在显式 `--markers` 时应用；marker 位于文件名或 ZIP entry 名时 locator/error 也必须脱敏。任何读取、Git 子进程、对象/文件/展开上限异常均失败关闭。
35. 0.4.18 生命周期回归必须覆盖 OAuth revoked/deleted 授权的 30 分钟孤儿回收与 90 天历史边界、Presentation 根目录删除失败后的不可用/重试、Artifact 共享 Blob 与 `.part` 崩溃恢复、Release manifest 等待者取消和内容替换失效、staging quarantine 恢复、Codex generation、starting Job shutdown、Browser 持续孤儿清理及组件语义版本排序。

## Release Gate

`bash scripts/release-gate.sh --full` 继续作为发布前硬门槛，但不再重复
`go test ./...` 已覆盖的历史版本专项。它覆盖：

- git whitespace / worktree + index secret scan
- synthetic scanner redaction self-test / full object database history scan（full）
- module checksum / tidy
- static analysis
- `go test ./...`
- Windows amd64 / Linux amd64 构建
- backup/restore E2E
- Local Bridge E2E
- 发布 HEAD 历史秘密扫描（full）
- 按当前 diff 自动选择的 real WSL / Browser / CC Switch / Claude Code E2E
- 影响 Provider/Local Bridge 时的 multi-provider discovery E2E
- 影响 Codex 链路时的 real Local Bridge → Codex product E2E

如需人工执行所有真实 runtime E2E，设置 `FAST_SPIDER_GATE_ALL_E2E=1`。

测试中不得重新引入旧目录对象或目录白名单来让旧断言通过。
