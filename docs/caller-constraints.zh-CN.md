# Fast Spider 调用方约束规格

本文规定通过 MCP、Direct API 或其它适配层调用 Fast Spider（FS）时必须遵守的生命周期与清理契约。工具 Schema 说明“参数能否被接受”，本文说明调用方如何完整结束一次操作；两者共同构成调用契约。

## 1. 通用规则

1. 调用方只清理自己在本次操作中创建、且已记录精确绝对路径的临时资源。不得使用项目根目录、用户目录、通配符、未解析变量或搜索结果作为递归删除目标。
2. 清理动作必须放在 `finally`、`defer` 或等价的必经结束路径中；成功、失败、超时和取消都执行同一清理流程。
3. 启动型工具返回 `jobId` 只表示已经受理。`shell_run`、`build_control` 后必须以 `job_watch` 观察到 `completed`、`failed` 或 `canceled`，才能判定操作结束并清理该 Job 使用的临时资源。
4. 调用失败但是否已创建资源不确定时，不得盲目重放或扩大删除范围；先用返回的 ID、目标状态或只读工具对账。
5. 用户明确要求保留的输出、项目源码、构建缓存、测试报告、Artifact、下载文件和未知目录不属于临时清理对象。

## 2. 浏览器测试

一次独立的浏览器测试或验收必须只拥有一个 `browserSessionId`：

1. `browser_control(readiness)`。
2. `browser_control(launch)`，记录返回的 `browserSessionId`。
3. 取得 ID 后立即注册结束清理：无论后续页面操作是否成功，都调用 `browser_control(close)`。
4. 在同一 Session 内执行 `page.open`、`snapshot`、交互和必要的 `screenshot`。
5. 测试结束后调用 `close`。Node 关闭 Context/Browser/Sidecar 会话并立即删除该 ID 对应的受管 Session 目录。

调用方不得为了复用登录态把一个测试完成的 Session 留给下一次测试。若调用方崩溃或遗漏 `close`，Node 的空闲关闭和孤儿回收只是异常兜底，不替代显式关闭。截图上传后的 Hub 临时附件有独立生命周期，不因本地 Session 目录删除而失效。

## 3. 测试与编译临时文件

调用方执行测试、lint 或编译时，应优先使用工具链自身不会在项目树留下文件的命令。确需显式生成临时内容时，必须先建立唯一的专用临时目录，或确定一个唯一的临时文件路径，并记录所有权。

必须在 Job 进入终态后删除的典型内容包括：

- 为一次测试显式生成的测试可执行文件，例如 `go test -c -o <临时路径>` 的输出；
- 仅为测试编译创建的 bundle、转译输出、覆盖率中间文件或临时配置；
- 调用方创建的临时下载、解压目录、浏览器测试数据目录和一次性 fixture 副本。

不得自动删除：

- 仓库原有或用户创建的文件，即使名称看起来像临时文件；
- 项目约定的正式构建产物、测试报告或用户要求交付的文件；
- Go、Node、浏览器等工具链拥有的共享缓存；
- 任何无法证明由本次调用创建的目录或文件。

推荐顺序为：记录精确临时路径 → 启动 `build_control` → 保存 `jobId` → `job_watch` 至终态 → 收集所需日志/结果 → 按精确路径清理。清理失败必须作为未完成事项明确返回，不得把存在残留的操作报告为完整结束。

## 4. 文件、Git 与 AI 会话

- 文件修改遵循 `code_search` → `file_read` 取得 SHA → `file_edit(preview)` → CAS 写入 → `file_read` 验证；调用方不得把回滚临时副本留在项目树。
- Git 写入和网络操作必须有当前任务授权；不得用 `reset`、`checkout`、`clean` 或无恢复方案的 `stash` 清理测试残留。
- AI Session 的归档、删除和历史保留使用 `ai_control` 的会话契约；不得把原生 Codex/Claude 历史当作临时文件删除。
- Cloud CHAT 任务只在 `dispatch.prompt` 写目标，FS 自动附带 `task_result_submit` 和绑定回调的 `taskRef`，调用方不重复粘贴协议。
- 正常回调直接调用通知给出的 `completion.claim`：文本读取 `text`，文件读取 `deliverablePath`；随后原样调用返回的 `acknowledge`。FS 内部核验文件元数据并确认对应 Node 通知，无需另查队列、历史会话或手工拼接哈希。
- `recoveryOnly=true` 只代表恢复观察，不是任务结果；缺少正式提交时才读取 `capability_list(view=workflow,name=cloud-callback-recovery)`。对用户简短汇报业务结果，不展开队列 ID 和内部协议。

## 5. 结束条件

一次 FS 调用链只有同时满足以下条件才算完整结束：

- 所有启动的 Job 已到达可观察的终态；
- 本次浏览器测试的 Session 已显式 `close`；
- 调用方创建的临时测试/编译资源已按精确路径删除，或清理失败已明确报告；
- 用户产物、项目文件和未知资源未被误删；
- 返回结果只声明实际取得的静态、测试、浏览器或部署证据层级。
