# 14 部署与运维

## 1. 当前阶段

本文定义未来部署方式和运维标准。Phase 0 不创建服务器资源、不安装服务、不签发证书、不部署 Hub/Node，也不产生付费行为。

## 2. 生产推荐拓扑

Fast Spider 只有一个 Hub 应用进程；生产推荐在德国公网 Linux 服务器上使用已有/标准 TLS 反向代理：

```text
Internet :443
→ Caddy/Nginx/现有反向代理（TLS）
→ 127.0.0.1:8787 fast-spider-hub
→ SQLite + Artifact local disk
```

Node 仅主动访问：

```text
https://fast-spider.example.com
wss://fast-spider.example.com/node/connect
```

对外只开放 TCP 443。Hub 管理端口不监听公网；Node 不开放任何入站端口。反向代理不是 Fast Spider 微服务，只负责 TLS、Host 路由和基础连接限制。

如果环境没有反向代理，后续可以让 Hub 直接终止 TLS，但正式发布只维护一条默认生产文档，避免多个半成品启动方式并存。

## 3. 唯一部署方式

### Hub

生产唯一推荐：**签名发布包中的单一 Hub 二进制 + systemd service**。

- 不以 `go run`、源码脚本、临时 shell 或多个守护工具作为生产方式。
- Docker 可在后续提供，但不是 MVP 主路径，也不能改变数据/备份语义。
- 升级通过签名版本包和统一 `fast-spider-hub upgrade`/运维流程，不手工覆盖未知文件。

### Windows Node

生产唯一推荐：**签名的每用户安装包**，安装后台 Node 与托盘管理界面。

- 默认以当前普通用户启动。
- 可选开机启动，安装界面明确展示。
- 不默认安装 SYSTEM 服务。
- 开发模式使用同一二进制和独立数据目录，不能与正式实例共用状态。

### Linux Node

生产推荐：**签名发布包 + user-level systemd unit**。服务器场景可显式安装 system-level unit，但必须配置非 root 用户。

## 4. Hub 目录

建议：

```text
/etc/fast-spider/
├─ config.yaml
└─ secrets/                 # root/服务用户严格权限

/var/lib/fast-spider/
├─ fast-spider.db
├─ artifacts/
├─ uploads/
├─ backups/
└─ updates/

/var/log/fast-spider/       # 若不只使用 journald
/run/fast-spider/
```

权限：

- 运行用户专用，如 `fast-spider`。
- `/etc/fast-spider/secrets` 和数据库/Artifact 仅服务用户可读。
- 反向代理不能读取数据库或 Node 设备材料。
- 数据目录不得放在不支持可靠文件锁的网络共享。

## 5. Node 目录

### Windows 每用户

```text
%LOCALAPPDATA%\FastSpider\
├─ app/
├─ config.json
├─ state.db
├─ credentials/
├─ logs/
├─ jobs/
├─ recovery-bin/
├─ browser-profiles/
├─ updates/
└─ run/
```

### Linux 每用户

```text
~/.config/fast-spider/config.yaml
~/.local/share/fast-spider/
~/.cache/fast-spider/
~/.local/state/fast-spider/logs/
```

Node 私钥优先进入 OS Secret Store；状态目录中的凭据文件只是受保护引用或加密封装。

## 6. Hub 配置

配置分为非秘密设置和秘密引用。

示例概念：

```yaml
server:
  listen: 127.0.0.1:8787
  public_url: https://fast-spider.example.com
  trusted_proxy_cidrs:
    - 127.0.0.1/32

storage:
  database_path: /var/lib/fast-spider/fast-spider.db
  artifact_dir: /var/lib/fast-spider/artifacts
  upload_dir: /var/lib/fast-spider/uploads

limits:
  max_node_connections: 50
  max_artifact_bytes: 104857600
  max_control_message_bytes: 1048576

retention:
  job_days: 90
  event_days: 14
  artifact_days: 30
  audit_days: 30

security:
  secret_key_ref: file:/etc/fast-spider/secrets/app-key
  update_trust_root: /etc/fast-spider/update-root.pub
```

规则：

- 配置字段有版本和启动校验。
- 未知字段默认警告或拒绝，不能静默拼错。
- Secret 不放命令行参数、普通环境变量 dump 或 Git。
- `public_url` 与允许 Host/Redirect URI 精确匹配。
- 反向代理 Header 仅信任配置 CIDR，防伪造客户端 IP/Proto。

## 7. Node 配置

- Hub URL 与信任指纹。
- 连接/心跳/重连策略。
- Workspace Registry 引用。
- Local Bridge 开关和传输。
- 每类 Job 并发和资源上限。
- 更新渠道。
- UI 可见性、通知和本机确认策略。

Hub 不能远程覆盖 Node 的强制安全底线，如允许任意系统路径、自动提权或关闭本机高风险确认。

## 8. systemd 服务

概念要求：

- `User=fast-spider`，不使用 root。
- `WorkingDirectory=/var/lib/fast-spider`。
- 仅一个 `ExecStart` 指向版本化稳定路径。
- `Restart=on-failure`，有退避，避免崩溃循环刷日志。
- 文件句柄、进程、内存和写目录限制。
- 合理的 `NoNewPrivileges`、`PrivateTmp`、`ProtectSystem` 等沙箱设置，经功能测试后启用。
- 数据库和 Artifact 路径显式可写，其他系统路径只读。
- `ExecReload` 仅用于明确支持的安全配置重载；秘密/监听变化建议重启。

服务单元由发布包管理，升级不手工产生多个重复 unit。

## 9. 反向代理要求

- 只代理明确 Host。
- `/node/connect` 支持 WebSocket upgrade 和长连接，不设置过短 idle timeout。
- MCP/API/Artifact/Node WSS 分路由限额。
- Request body 在边缘和 Hub 两层限制。
- 不缓存认证响应、MCP、Event 或 Artifact 私有内容。
- Access log 脱敏 query、Authorization、Cookie。
- TLS 1.2+，优先 1.3；自动续期有监控。
- 保留真实客户端 IP 只通过可信 proxy Header。

心跳是单 Node 连接级别，不为每个 Workspace/工具/路由建立健康检查，避免探活风暴。

## 10. 防火墙

Hub 主机：

- 入站：443；运维 SSH 按现有安全策略限制。
- 8787 仅 loopback。
- 数据库无网络端口。
- 出站：证书/更新/外部身份 Provider 的必要地址，按部署策略。

Node：

- 不要求入站规则。
- 出站仅需 DNS、HTTPS/WSS 443 访问 Hub，以及用户明确运行的构建/浏览器业务网络。
- Local Bridge Named Pipe/UDS 不占网络端口。

## 11. 首次初始化

未来正式流程：

1. 安装签名 Hub 发布包。
2. 创建专用用户、目录和权限。
3. 生成/导入 Hub 应用密钥与更新信任根。
4. 写入最小配置并执行 `config validate`。
5. 执行数据库初始化/migration dry-run。
6. 启动 Hub，确认 `/livez`、`/readyz`。
7. 通过一次性本地 bootstrap 流程创建首个 Owner；Token 立即销毁。
8. 配置 OAuth/MCP Client。
9. 创建 enrollment token，在 Node 本机完成配对。
10. 在 Node 本机授权 Workspace。

不能在日志中输出完整 bootstrap/enrollment token。

## 12. 启停与快速重启

标准命令由 service manager 负责：

```text
systemctl status fast-spider-hub
systemctl restart fast-spider-hub
journalctl -u fast-spider-hub
```

Node 使用托盘 UI、系统服务管理或正式 CLI。

快速重启要求：

- Hub 退出前停止接受新 Job，关闭/通知 Node 连接并完成短事务。
- 重启后运行 migration 检查、打开数据库、恢复连接注册和 Job 对账。
- Node 自动指数退避重连并带抖动。
- 不要求手工启动多个 agent-service、隧道或脚本才能恢复核心链路。

## 13. 健康检查

### Hub

- `/livez`：进程事件循环可响应。
- `/readyz`：DB、migration、必需目录、密钥和监听可用。
- 管理员详情：WAL 大小、磁盘水位、连接数、Job、清理滞后、备份状态。

健康检查不遍历所有 Node 或为每个路由主动请求 Node。

### Node

本机 UI/CLI 显示：

- 进程、Hub 连接、凭据状态。
- Workspace 可用性。
- 当前 Job/资源组容量。
- Local Bridge、Provider、浏览器 runtime。
- 更新和磁盘容量。

Node 可在 heartbeat 中上报摘要，但不得高频上报大明细。

## 14. 备份

### 备份集合

- SQLite 一致性备份。
- Hub 非秘密配置。
- 加密的必要密钥备份（与数据分离管理）。
- Artifact 目录及 manifest/hash。
- 发布版本和 migration 版本信息。

### 一致性

- 使用 SQLite backup API 或受控短暂停写/checkpoint 后快照。
- 不能只复制 `.db` 而忽略活动 WAL。
- Artifact 先生成 manifest；数据库备份记录对应 Artifact 快照点。
- 上传临时目录不必长期备份，但恢复后要能安全清理。

### 策略

建议：每日增量/快照、每周完整、至少一份异机加密副本。实际 RPO/RTO 在编码前开放问题中确认。

### 验证

- 每次备份记录大小、hash、数据库 integrity check 和版本。
- 定期在隔离目录执行恢复演练。
- “备份成功”必须包含可读取验证，不只看命令退出码。

## 15. 恢复

1. 停止 Hub 并保存损坏现场。
2. 验证目标版本、migration 和更新签名。
3. 恢复数据库、配置、密钥和 Artifact manifest。
4. 运行只读 integrity/consistency check。
5. 启动到管理只读模式，核对机器、Workspace、Job 和 Artifact。
6. 恢复正常服务；Node 重连并对账。
7. 对恢复时间点之后状态不确定的写 Job 标记 lost，不自动重跑。

恢复到旧版本时必须检查防回滚策略和数据库兼容，不能用旧二进制直接打开新 Schema。

## 16. SQLite 运维

- 开启 WAL、foreign_keys、busy_timeout。
- 监控 DB、WAL、SHM 大小和 checkpoint 延迟。
- 定期但不过频 checkpoint；不每请求强制 checkpoint。
- Migration 前自动生成一致性备份。
- 定期 `quick_check`，完整 `integrity_check` 在维护窗口。
- 不定期 VACUUM 大库；按实际碎片和维护窗口执行。
- 磁盘逼近阈值时先停止 Artifact/新 Job，再进行清理，不等写满崩溃。

## 17. 日志与保留

默认结构化日志到 stdout/journald；可选文件输出必须轮换。

- 普通日志默认 7–14 天。
- 安全审计独立保留；当前 MVP 默认 30 天，并由 Hub 定时清理，后续配置化时仍需设置硬上限。
- stdout/stderr 和大诊断转 Artifact。
- 日志按大小/时间限制，压缩旧文件，删除有上限。
- 禁止无限 debug；临时 debug 有自动过期。
- 清理任务属于 Hub/Node 内置调度，不依赖用户手工 cron 散落多个脚本。

详细见 [15-observability.md](15-observability.md)。

## 18. Artifact 与磁盘容量

容量水位建议：

- <70%：正常。
- 70–85%：告警并加快已过期清理。
- 85–95%：拒绝大 Artifact/浏览器下载，限制新执行。
- ≥95%：进入保护模式，只保留管理、取消、清理、备份相关操作。

删除顺序：失败临时上传 → 已过期普通 Artifact → 可重建日志 → 按策略过期数据。不能为了腾空间删除未过期安全审计或数据库备份而不告警。

## 19. 定时任务

内置 Operation Scheduler 任务：

- enrollment/auth/lease 过期清理。
- Job Event、Artifact 和审计保留。
- 临时上传、浏览器 Profile、下载、Node recovery-bin 清理。
- SQLite checkpoint/健康检查。
- 备份与恢复验证状态检查。
- 凭据/证书/更新过期提醒。
- 孤儿进程/文件/Blob 对账。

每个任务：唯一名称、互斥、超时、批次、cursor、退避、摘要。失败不每秒重试，不刷海量堆栈。

## 20. 升级

- 只升级到签名、平台匹配、hash 正确的包。
- Hub：备份 → migration dry-run → drain → 安装 → health gate → 完成/回滚。
- Node：下载 → 验证 → 等待安全窗口 → 原子切换 → 重启 → 健康确认 → 删除旧版本。
- Hub/Node 协议支持明确兼容窗口，滚动升级期间协商共同版本。
- 跨 major 或不可逆 migration 需要停机升级文档。
- 不长期维护双写或两个业务实现。

详见 [16-update-and-recovery.md](16-update-and-recovery.md)。

## 21. 容量基线

MVP 验证目标：

- 50 台已注册、10 台同时在线 Node。
- 每 Node 2 个 exec、1 个 write、4 个 read Job 默认上限。
- Hub 空闲连接低 CPU/内存。
- 断网重连带退避和抖动，不产生连接风暴。
- 事件批量写入，不因持续日志造成 SQLite 高频单行事务。
- 清理后磁盘可回收，无长期孤儿 Artifact/日志。

这不是承诺上限；正式值由 Phase 8 压测确定。

## 22. 故障处理手册

### Node 全部离线

检查公网 TLS/DNS、反向代理 WSS、Hub ready、凭据/时间、连接限流。不要逐 Workspace 探活或盲目重启所有组件。

### Hub 502/503

区分反向代理无法连接 Hub、Hub not-ready、请求限流和 Node offline。使用 requestId/traceId 与服务状态，不只看客户端错误文本。

### DB busy/写入慢

检查长事务、WAL、磁盘、Event 批次和清理任务。禁止通过无限提高 timeout 掩盖事务内网络/文件 I/O。

### 磁盘满

进入保护模式，停止新 Artifact/Job，执行受控清理，验证 Artifact Blob 引用和备份；不直接 `rm -rf` 数据目录。

### 取消失败

查看 Node Job/进程树和 `CANCEL_INCOMPLETE`；本机终止/隔离后记录结果。UI 不得显示 canceled 直到确认。

### 更新失败

自动回滚到 previous；保留错误摘要和包 hash，停止重复自动尝试，等待修复版本。

## 23. 运维验收

- 新服务器按单一路径完成 Hub 安装、启动、备份和恢复。
- 生产无源码脚本/安装包/手工进程三套并行入口。
- 公网只暴露 443；Hub 内部端口 loopback；Node 无入站端口。
- 日志、Event、Artifact、上传、恢复区都能自动清理且有硬上限。
- 重启 Hub 后 Node 自动重连、Job 对账，不重复执行写操作。
- 磁盘满、DB 只读、证书过期和更新失败有明确降级/恢复路径。
