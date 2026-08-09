# Job 与 Event 模型（0.3.x）

Job 是 Node 上一次异步执行的运行事实。Job 绑定 Machine 和自身 jobId，不再绑定目录授权对象。

## 创建

`shell_run`、`build_control` 以及部分 Git 网络动作可以创建 Job。创建时固定 argv/cwd/action/timeout/idempotencyKey 等规范化参数。

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

## Shell 边界

Shell/Build 的 cwd 必须是绝对本机目录，命令以运行 Fast Spider Node 的 OS 用户身份启动。取消、timeout、Hub/Node shutdown 都必须清理进程树。

## 幂等

同一个 idempotencyKey + 同一规范化启动参数只创建一个 Job；相同 key 配不同参数返回冲突。
