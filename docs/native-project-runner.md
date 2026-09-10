# Node 原生 CHAT 项目运行器

运行器位于 `internal/agent/native_runner.go`，直接运行在 FS Node 的 Go 进程内。没有 Python 守护进程、Hub 消费者队列或常驻本地 AI 机械助手。现有本地 Codex、插件和普通回调通道继续由原来的实现负责。

## 工作划分

用户目标 → 大任务 → 多个并行任务块。`parent` 标识同一大任务，`after` 只表示真实产出依赖，`scope` 表示本块写域。每块由一个 CHAT 完成调查、实现、自测和普通修复。可以有很多任务块，但不把读文件、改函数、跑一次测试拆成单独 CHAT。

云端规划 CHAT 按需分析当前目标、源码、结果和检查证据，返回结构化计划。Node 编译完整任务包、事务应用计划并机械派发，不需要本地 Codex 逐条调用工具。默认复用本块原 CHAT；没有固定三轮换代。上下文损坏或原方法无效时才由计划显式 `rotate`。

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

- `runner.goal`：传入 `projectId` 和新的完整 `goal`，更新目标版本。旧结果不会直接完成新目标；可复用的旧成果由规划器给出当前证据后 `revalidate`，无需机械重复测试。
- `runner.pause`：禁止新业务派发，仍保存已发生的结果并处理运输确认。
- `runner.resume`：恢复当前项目。
- `runner.signal`：传入 `projectId`、`taskId` 和非空 `evidence`，登记外部条件变化并恢复对应分支。

## 确定性的推进与恢复

任务、冻结请求、绑定、结果、轮次和计划保存在 Node 数据目录的 `native-runner/projects.sqlite3`。任务包和结果快照在同一目录。Node 生命周期负责启动与关闭运行器；重新启动时从持久状态恢复，不依赖本地 Codex 保持对话。

派发前请求落账；不确定响应保持同一任务、轮次和幂等键。一个任务块的失败只延期该块，不取消本轮其它独立派发。只有正式终态结果才释放执行占用；恢复观察不作为业务完成。结果先保存，再确认运输；历史轮次的 ACK 义务在续发后仍然保留。

配置检查走 Node 的持久 Job。运行时只查询准确 Job，不启动本地模型来跑命令。检查运行期间临时避免同域 writer 污染证据，独立域照常推进。

规划方案原子应用：错误依赖、循环、重复任务、过时轮次、未通过检查的采纳和空方案会被明确拒绝，原状态不被部分修改。规划自身出错时安排准确修正，不暂停其它块。阻塞分支必须有明确恢复事件或未来时间；相同事实不反复生成同样的规划。

队列空了仍会检查目标差距。只有全部任务块在当前目标下验收，且有整体完成证据，才能标记目标完成。提交、推送、部署和不可逆操作不由规划报告产生授权。

## 验证边界

`go test ./internal/agent -run TestNativeRunner -count=1` 验证调度、计划原子性、回调保存与 ACK、并发和换代；相关 transport 测试验证真实适配器契约。源码测试不代表已安装新 Node，也不代表完成真实 Cloud 端到端验收。部署、新项目激活和旧项目迁移必须各自记录实际证据，不能以创建任务或消息送达代替交付。
