# 缓存与生命周期维护（0.4.18）

本文是 Fast Spider 缓存、临时资源、控制索引和运维清理的统一事实源。目标不是“尽可能多删”，而是在长期运行下保持有界、可恢复、可观察，并把用户数据排除在自动清理之外。

## 1. 数据分级

| 级别 | 内容 | 默认策略 | 删除边界 |
|---|---|---|---|
| L0 派生内存缓存 | readiness、模型、路由、发布 manifest | TTL + 容量上限；命中返回隔离副本 | 可自动淘汰，不影响事实源 |
| L1 受管临时数据 | Artifact `.part`、Browser Session、下载 ZIP、update staging | 严格根目录/命名 + 宽限期 + 有界批次 | 不跟随 symlink/reparse；活动项和未知项保留 |
| L2 控制索引 | Claude Session 控制索引、Codex create 幂等索引 | 原子持久化、容量门禁、失败显式返回 | 不自动删除 active、`in_doubt` 或原生 AI 历史 |
| L3 权威或用户数据 | Hub SQLite、密钥、完整 Artifact、备份、项目 Markdown | 明确保留策略、备份与人工操作 | 不由通用缓存清理器删除 |

统一规则：扫描和返回结果必须有上限；删除型 CLI 默认预览；失败项保留重试依据；后台任务记录稳定阶段和计数，不记录正文、Token、密钥或不必要的绝对路径。

## 2. 当前生命周期矩阵

| 资源 | 生命周期 | 触发方式 | 说明 |
|---|---|---|---|
| Agent CLI/模型 TTL cache | 45 秒 / 20 秒，固定 key 上限 | 请求时自动 | 同 key 刷新不挤掉其它有效 key；嵌套结果按值隔离 |
| Routing cache | 默认 1.5 秒，最多 8 key | 请求时自动 | Route 变化可显式 invalidate |
| Browser availability | 成功 30 秒、失败 5 秒；最多 128 个 holder，空闲 1 分钟可淘汰 | diagnostics/readiness | 只缓存探测结果，不共享 Browser Session |
| Codex loaded thread 集合 | app-server 进程期 | load/delete/进程退出 | 删除成功立即淘汰对应记录 |
| Browser Session 目录 | 活动期；空闲 10 分钟关闭 | Node 启动对账 + 每分钟周期维护 | 每轮扫描最多 256 项、删除最多 32 项；只回收严格 `brs_` 随机 ID、超过 1 小时的普通目录；大 backlog 会在后续轮次继续清理 |
| Job 日志 | 运行期 + 本地保留窗口 | Node 周期维护 | 受 Job 数量、日志大小和保留时间共同约束 |
| Managed Component ZIP/旧版本 | 安装完成后 | 组件中心显式安装/更新 | 安装按组件串行、临时目录唯一；已验证版本按语义版本选择最新，未知文件保留 |
| Node update staging | Ready/apply 完成或显式 staging prune | Node 启动；`spiderctl staging-prune` | future、失败现场、`.previous` 和未知目录保留 |
| Artifact upload | 30 分钟 | Hub 每 30 秒维护 | 同 upload 串行，不同 upload 可并发；慢磁盘删除不持有全局上传锁；完成后遗留 `.part` 由持久删除队列兜底 |
| 完整 Artifact | 30 天 | Hub 每 30 秒维护 | 每批最多 128 条；内容寻址 Blob 仅在无引用时删除，失败进入持久队列并指数退避重试 |
| 截图 / `publishFile` 临时附件 | 最长 48 小时 | Hub 每分钟生命周期维护 | MCP/Direct 仅返回 URL 元数据；到期自动删除；不进入 Hub 数据库或备份；Hub 重启可提前清理 |
| Legacy presentation（旧 Node） | 20 分钟 | Hub 生命周期维护 | 未携带 resource kind 的滚动升级兼容路径 |
| Release manifest | 发布文件未变化期间，最多 64 项 | manifest 请求 | stamp 纳入 version 内容和平台文件 identity（Windows file index/change time，Linux dev/inode/ctime）；同尺寸、同 mtime 原子替换也会失效；并发 miss 只做一次 hash/sign，等待者可独立取消，生成中变化 fail-closed |
| Release backup | 运维保留数量 | `backup-prune` | 默认只输出计划，`--apply` 才删除；同进程创建与清理按目录串行，Windows/Linux 删除前用已冻结的文件身份逐项复核；其它平台存在候选时 fail-closed |
| Release staging | 已完成版本上界 | `staging-prune` | 默认只输出计划；apply 先同盘原子隔离再复核和删除，失败恢复原名，崩溃 quarantine 保留待人工对账 |

## 3. 安全运维流程

### Release backup

普通 `fast-spider-<timestamp>.zip` 不进入自动轮换。正式升级使用 `pre-<semver>-<commit>.zip`，并严格按以下顺序执行：

1. 升级前创建标准备份。
2. 执行 `backup-verify`。
3. 完成升级与健康检查。
4. 执行 `backup-prune --dir <absolute-backup-dir> --keep 3` 查看 `planned`。
5. 人工确认后追加 `--apply`；保留 JSON 结果并对非零退出告警。

### Release staging

`--through` 必须取“已经发布、验收且确认不再被回滚或复制流程引用”的最高版本，不能直接复制文档中的历史版本常量。先运行 plan-only，再以同一绝对 root、layout 和版本追加 `--apply`。

## 4. 协作资料室定时清理

AI 公司协作资料室支持清理命令，但自身不带定时器：

```bash
# 默认 dry-run：只列出超过保留期的已关闭资料室
python <team-workspace-skill>/scripts/workspace.py cleanup --older-than-days 30

# 仅在确认目标、保留期和报告后执行；删除不可恢复
python <team-workspace-skill>/scripts/workspace.py cleanup --older-than-days 30 --yes
```

它只处理已经关闭且超过阈值的资料室；打开中的资料室不会进入候选。Codex 支持按计划运行自动化任务，适合周期执行 dry-run 并把候选报告带回同一任务；本地自动化在电脑保持唤醒且 Codex 正在运行时最可靠，详见 [Codex Automations](https://openai.com/academy/codex-automations/)。

推荐策略是每周运行一次 dry-run，只有出现候选才通知维护者。不要在没有明确授权时把 `--yes` 放入无人值守任务。本轮没有创建定时任务，也没有删除任何资料室；如需全自动应用，必须先明确保留天数、执行时间、报告保存位置和失败告警方式。

## 5. 下一维护周期

| 方向 | 进入条件 | 预期退出门禁 |
|---|---|---|
| 生命周期可观测性 | 清理失败、磁盘水位或索引容量接近门限 | 最近成功/失败、候选/删除/保留计数可诊断，信息保持脱敏 |
| Session create 对账 | 幂等索引接近容量或出现 `in_doubt` | 提供显式 reconcile；窗口内重放不重复创建，`in_doubt` 不按时间静默删除 |
| Working Context 归档 | 长期计划数量影响 list 或磁盘 | CAS 确认、跨项目隔离、默认保留 Markdown，list 明确返回 total/truncated |
| 跨进程组件安装锁 | 同一 data-dir 允许多进程管理组件 | 文件锁 + 唯一临时路径 + 崩溃恢复测试，不误删已发布版本 |
| 跨进程发布运维锁 | 同一备份目录由多个 `spiderctl` 或外部发布器同时管理 | 协作式文件锁 + 稳定句柄身份；Windows/Linux 同元数据原子替换与真实文件占用测试通过 |
| 清理基准与背压 | Artifact/日志/会话规模达到压测阈值 | 固定批次、SQL 次数不随候选线性增长，在线请求延迟无明显回归 |

不进入通用清理器的内容：Hub 数据库、密钥、用户项目文件、Claude/Codex 原生历史、未知目录、活动 Browser/Artifact、future update staging、`in_doubt` 幂等记录。
