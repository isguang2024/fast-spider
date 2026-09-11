# Node 原生 CHAT 项目运行器

运行器位于 `internal/agent/native_runner.go`，直接运行在 FS Node 的 Go 进程内。没有 Python 守护进程、Hub 消费者队列或常驻本地 AI 机械助手。现有本地 Codex、插件和普通回调通道继续由原来的实现负责。

## 工作划分

用户目标 → 大任务 → 多个并行任务块。`parent` 标识同一大任务，`after` 只表示真实产出依赖，`scope` 表示本块写域。每块由一个 CHAT 完成调查、实现、自测和普通修复。可以有很多任务块，但不把读文件、改函数、跑一次测试拆成单独 CHAT。

云端规划 CHAT 按需分析当前目标、源码、结果和检查证据，返回结构化计划。Node 编译完整任务包、事务应用计划并机械派发，不需要本地 Codex 逐条调用工具。默认复用本块原 CHAT；规划可显式 `rotate`，运行器也可在确认旧执行终止后进行上下文接力。同一个任务块可以由多代 CHAT 接力，各块仍然并发。

原有 CHAT 创建及普通续发实现保持不变。运行器的续发适配器显式使用当前已配置的创建模型和推理强度，避免失败首轮的 `auto` 选择粘在恢复轮次；不会硬编码模型或启用未配置的服务等级。正式后台沿用加载现有配置的 `ui --background` 入口。任务包区分底层能力名与 MCP 工具名，并给出 `file_read`、`file_edit`、`ai_control` 的按需工具发现说明。

普通派发、回调收件和检查不唤醒本地 Codex。只有需要用户补充事实或作出决定，或目标完成时，运行器向绑定的主控发送有去重标识的简报；主控忙碌时保留待送通知，其它任务块先继续派发。

## 本地入口

通过 `FastSpider_Local.local_capability` 调用 `capability="agent.control"`，动作名为 `runner.*`。这些动作需要包含该实现的 Node；旧版 Node 的能力目录不会列出它们。创建新项目不迁移任何已有 mission 或在途回调。

初始化参数示例（路径和主控 ID 必须替换成实际值）：

```json
{
  "capability": "agent.control",
  "action": "runner.init",
  "params": {
    "projectId": "product-development",
    "root": "V:/repos/GitHub/my-product",
    "controllerSessionId": "实际本机主控ID",
    "goal": "完整用户目标、范围、完成标准和授权边界",
    "concurrency": 6,
    "checks": {
      "backend": {"argv": ["go", "test", "./internal/refunds/..."], "cwd": "backend", "timeoutSeconds": 600}
    }
  }
}
```

初始化后 Node 自动触发云端任务块规划。并发数量是项目上限，不代表账号有同样的可用容量，也不会为了填满槽位拆碎任务。配置检查必须是当前项目需要的实际检查；没有命中检查的工作由云端规划依据源码和报告验收，不因为形式完整重复跑整套测试。

需要明确指定某个任务块时使用 `runner.add`：

```json
{
  "projectId": "product-development",
  "task": {
    "key": "refund-processing",
    "parent": "退款功能",
    "title": "退款申请和处理接口",
    "objective": "实现本块接口、状态变化、自测并修复范围内问题",
    "acceptance": "申请与处理路径及相关异常用例通过",
    "scope": "backend/internal/refunds",
    "context": ["backend/internal/refunds/service.go"],
    "after": [],
    "checks": ["backend"]
  }
}
```

`runner.status` 参数为 `{"projectId":"..."}`，返回按父任务分组的数量、任务块简报、恢复条件和待 ACK 数；增加 `taskId` 只读取一个块的详细绑定和历史。

### 按需上下文和证据（0.4.100）

任务包保留当前目标、写域与执行约束；历史规划只给计数和最近结果索引，已验收业务块只给简短索引。起始任务索引最多 24 条，优先当前活动和未完成块，其余通过 `runner.context` 分页读取；`blockCounts` 和 `omittedBusinessBlocks` 明确说明完整数量与省略量，不把省略当成不存在。完整历史仍在原 SQLite，不再在每次规划中重复复制，也不另生成整份历史 sidecar。

CHAT 通过 FS 的 `ai_control` 调用 `runner.context`，使用派发提示中已分配的 `taskRef`：

```json
{"action":"runner.context","responseContent":{"taskRef":"已分配的绑定","section":"tasks","limit":10,"offset":0}}
```

- `section=tasks`：分页业务任务索引，不返回报告正文。
- `section=task`：同任务区的当前任务契约；传 `taskId`，可用 `fields` 选择 `objective`、`acceptance`、`scope`、`checks`、`result` 等字段。
- `section=history`：指定任务的历史尝试，摘要分页，不返回旧 prompt。
- `section=evidence`：按 `taskId` 列出结果证据；可选 `round` 读取旧轮次。证据按 project/task/round/event 分类存入 `runner_evidence`，含状态、来源路径、SHA-256、体积与正文。
- 只有 `section=evidence` 明确传 `evidenceId` 才返回正文页；`offset` 按 Unicode 字符计数，正文 `limit` 默认 4096、最大 16384。列表默认 10 条、最大 50 条。跟随返回的分页位置读取，勿默认遍历全部历史。

云端查询必须经过当前任务绑定校验，仅可读所属任务区，不能提供任意文件路径或 SQL。本机可复用 CLI：`fast-spider-node.exe local-call -capability agent.control -action runner.context -params-json '{"projectId":"准确任务区","section":"tasks","limit":10}'`。管理动作继续保持本机边界；无需为读取数据库启动 CMD Job 或新的 AI 会话。

无业务状态进展且只返回问题时，Node 先安排一次云端自查，任务包明确要求核对已有交接、归档位置和替代证据，不重复已接受的业务验收。自查后仍缺少真实外部事实，则保留规划依据并只通知一次；`runner.signal/change` 或相关任务状态变化才再次触发。任务中心分别显示业务完成、实际活动与回调待确认；缺失的迟到取消报告保存为明确恢复证据，不冒充业务成功。

- `runner.goal`：传入 `projectId` 和新的完整 `goal`，更新目标版本。旧结果不会直接完成新目标；可复用的旧成果由规划器给出当前证据后 `revalidate`，无需机械重复测试。
- `runner.pause`：禁止新业务派发，仍保存已发生的结果并处理运输确认。
- `runner.resume`：恢复当前项目。
- `runner.signal`：传入 `projectId`、`taskId` 和非空 `evidence`，登记外部条件变化并恢复对应分支。

## 确定性的推进与恢复

### 等待条件与可提前执行的阶段（0.4.101）

`after` 表示当前块开始所需的真实硬依赖。规划可通过已有 `revise` 修改未派发块的 `after/context/objective/acceptance`：有独立价值的准备工作可以先执行，依赖真实上游产物的最终完成/集成块保留硬依赖与最终验收。运行器不会自行删除依赖，也不会把部分完成当作整体完成；依赖仍经过轮次、规划依据及 DAG 校验。项目 revision 变化后，规划需 `keep/revise` 所有应继续执行的排队块。

`defer` 动作可提供 `waitFor: {"taskIds":["同区任务ID或key"],"paths":["项目内文件路径"]}`，或使用已有的未来 `resumeAt`。Node 只用本地任务状态、终态结果、文件 size/mtime 和真实占用写域建立观察指纹，不解析理由文本、不将 Git modified 推断为活跃 writer。条件变化时请求云端重评；没有变化时首次在 5 分钟后复核一次，此后等新事实而不重复规划。已知重叠 writer 仍在写入期间，文件变化不反复触发规划；writer 退出后再核对文件。没有结构化条件的旧任务以同区业务状态和实际占用作为有限复核基线。

等待复核只更新规划依据，保留任务延期状态、原依赖和在途 writer。暂停区不生成新等待复核或规划，但既有在途任务的结果、检查和恢复继续收尾。`waitReview` 记录观察指纹、下一次复核时间及简短事实，可由任务详情/`runner.context` 查询。

恢复探测遇到有效 Retry-After 时按该期限再次核查；缺少期限的限流或连续网络暂态失败按 30、60、120 秒退避，成功后清零失败次数。身份、协议等确定性错误保留明确错误，不进行快速重试。所有续推保持原幂等键和代际绑定，不凭未知状态换 writer。

已分配任务的 SSE 订阅保持到 provider EOF、取消或正式回调退休，不再因固定 30 分钟流期限主动断开。认证和响应头建立仍有 45 秒期限，临时查看者仍有空闲生命周期。连接正常时，恢复探针在 30 分钟无进展窗口内使用本地观察；超过窗口、连接不可用或明确人工核查时才读取完整会话。保持连接不等于宣告业务成功，建立/重连和必要的详情读取仍分别受 HTTP 限流约束。

任务、冻结请求、绑定、结果、轮次和计划保存在 Node 数据目录的 `native-runner/projects.sqlite3`。任务包和结果快照在同一目录。Node 生命周期负责启动与关闭运行器；重新启动时从持久状态恢复，不依赖本地 Codex 保持对话。

派发前请求落账；不确定响应保持同一任务、轮次和幂等键。一个任务块的失败只延期该块，不取消本轮其它独立派发。正式终态结果才进入业务验收，恢复观察不作为业务完成；会话接力须确认旧执行终止并保留原任务写域与旧绑定。结果先保存，再确认运输；历史轮次的 ACK 义务在续发后仍然保留。

配置检查走 Node 的持久 Job。运行时只查询准确 Job，不启动本地模型来跑命令。检查运行期间临时避免同域 writer 污染证据，独立域照常推进。

规划方案原子应用：错误依赖、循环、重复任务、过时轮次、未通过检查的采纳和空方案会被明确拒绝，原状态不被部分修改。规划自身出错时安排准确修正，不暂停其它块。阻塞分支必须有明确恢复事件或未来时间；相同事实不反复生成同样的规划。

队列空了仍会检查目标差距。只有全部任务块在当前目标下验收，且有整体完成证据，才能标记目标完成。提交、推送、部署和不可逆操作不由规划报告产生授权。

## 验证边界

### 无进展核查与接力（0.4.95）

每个执行块持久化 `recovery` 和简洁 `checkpoint`。默认 15 分钟到期核查对应 Cloud 会话，即使实时连接正常也核查；探针和续推在调度锁外执行，最多同时 8 个，单次受超时约束。状态未知或取消结果不确定时保留原 writer 绑定。连续 30 分钟活动指纹没有变化且没有等待中的登记 Job 时，先取消并重新核对旧执行终态，之后才续推。

对于 provider 不能判断运行状态、但活动指纹持续静止的会话，可以沿既有同会话接口去重续推，不释放写域、不创建另一 writer。用户明确确认卡住时，通过本地 `runner.signal` 传入准确 taskId 和新 evidence，即安排一次 fresh probe 及原 CHAT 续推。它不能授权未知状态下换代。派发、观察、启动/查询检查和通知也通过有界后台操作执行，未完成结果按 30 秒缓存，避免占住全局调度锁或形成唤醒查询循环。

续推意图先落账，重启或发送不确定时继续使用相同幂等键。云端已结束但未提交任务结果时，同 CHAT 接续并携带已有检查点及 Job 结果；两次续推仍无检查点进展、明确上下文耗尽或 worker 请求 `context_handover` 时，终态核验后换代。实际观测的对话字节量可用于提前接力，它不是模型 token 容量；没有数据时不能伪造剩余上下文。旧代的请求、回调绑定和终态证明保留，迟到结果只用于旧代收件/确认，不完成新代任务。

云端通过 `ai_control(action="runner.checkpoint", responseContent={"taskRef":"原绑定","summary":"已完成内容","nextStep":"下一步","stage":"implementation","evidence":["准确文件或检查引用"],"waitingJobs":[]})` 保存非终态进展。相同检查点不刷新有效进展时间；旧代不能覆盖新代检查点。摘要和下一步各最多 4096 字节，最多 24 个证据引用及 8 个等待 Job。完整报告仍使用 `runner.submit`。

长 FS Job 启动一次后，worker 登记准确 `waitingJobs` 并结束本轮。Node 每 30 秒读取这些本地持久 Job；作业未结束时不读取 Cloud、不催促 CHAT，作业终态后核查原 CHAT 并携带状态和日志引用接续。未知 Job 身份保留等待状态。任务包保留必要历史引用，移除递归旧提示词；新 CHAT 读取已有文件与检查点，避免重复已完成工作。

本机 UI 左侧“任务中心”进入 `/tasks`，按主控选择任务区并展开任务树，查看核查、等待作业、续推、接力和最近错误。页面从独立 SQLite 只读快照获取状态，不占调度锁；浏览器可见时刷新，关闭页面后不提供浏览器通知。Go 恢复机制仍随 Node 后台进程运行，不依赖页面或本地 Codex 回复。

`go test ./internal/agent -run TestNativeRunner -count=1` 验证调度、计划原子性、回调保存与 ACK、并发和换代；相关 transport 测试验证真实适配器契约。源码测试不代表已安装新 Node，也不代表完成真实 Cloud 端到端验收。部署、新项目激活和旧项目迁移必须各自记录实际证据，不能以创建任务或消息送达代替交付。
