# Job 与 Event 模型（0.4.9）

Job 是 Node 上一次异步执行的运行事实。Job 绑定 Machine 和自身 jobId，不再绑定目录授权对象。

## 创建

`shell_run`、`build_control` 以及部分 Git 网络动作可以创建 Job。创建时固定 argv/cwd/runtime/action/timeout/idempotencyKey 等规范化参数。`runtime.kind` 默认为 `host`；Windows 上可选 `wsl` 与发行版，cwd 继续使用 Windows 绝对路径并由 Node 映射。

```text
created → running → completed / failed / canceled / expired
```

## 查询与取消

远程调用使用：

```text
machineId + jobId
```

`job_watch` 支持事件 cursor 和有限 long-poll；`job_cancel` 终止对应进程树。无需再附加目录范围 ID。

## Event

stdout/stderr/state 等事件按 sequence 排序并设置容量上限。输出超限时截断或转 Artifact，不能无限堆内存/磁盘。

Windows 子进程若输出当前控制台/OEM 代码页而不是 UTF-8，Node 在事件层转换为 UTF-8 后再发送；只有无法识别的字节才做替换并产生 warning，避免中文系统错误信息变成乱码。

## Shell 边界

Shell/Build 的 cwd 必须是绝对本机目录，命令以运行 Fast Spider Node 的 OS 用户身份启动。取消、timeout、Hub/Node shutdown 都必须清理进程树。

## 幂等

同一个 idempotencyKey + 同一规范化启动参数只创建一个 Job；相同 key 配不同参数返回冲突。

## Timing

Job 快照携带创建调用的 `requestId/traceId/runtime` 与 `nodeReceivedAt/processStartedAt/finishedAt/queueMs/runMs`。后续 watch/cancel 保留这组创建来源 ID，并通过 `callRequestId/callTraceId` 标识当前查询调用。`queueMs` 是从 Node 收到请求到子进程启动的实测启动阶段耗时（包含 runtime 准备、keepalive 与进程创建），不是额外实现的 FIFO 队列等待。通用响应还补充 `nodeExecutionMs/hubPreDispatchMs/nodeRoundTripMs/hubTotalMs`，不保存高频长期 trace。
