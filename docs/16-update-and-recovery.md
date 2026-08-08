# 16 备份、恢复与升级

## 1. 当前目标

Fast Spider 当前是单 Owner、个人自托管项目。Phase 7 的目标不是建设企业级更新平台，而是保证三件实际事情简单可靠：

1. Hub 数据能备份、校验和恢复。
2. Hub/Node/CLI 能明确查看当前版本。
3. 升级失败时可以用“上一版二进制 + 升级前备份”恢复，而不是依赖复杂 Recovery Mode。

当前不实现自动更新服务器、Release Key/Root Key 状态机、Windows 托盘更新器、后台下载器、自动 Schema 回滚或多版本常驻进程。

## 2. 当前唯一数据边界

Hub 的持久数据都位于 `--data-dir`：

```text
<data-dir>/
├─ hub.db
├─ hub.db-wal / hub.db-shm      # SQLite 运行时可能存在；备份保留 WAL，跳过可重建 SHM
├─ secrets/
│  └─ hub-ed25519.key
├─ bootstrap-token              # 仅首次初始化窗口可能存在
└─ artifacts/
   ├─ blobs/
   └─ uploads/
```

备份包因此不需要理解每张业务表，也不维护第二套“导出模型”。它直接保存这个数据边界中的普通文件，并为每个文件记录 SHA-256。

**备份包包含 Hub 私钥，应按敏感数据保存。**

## 3. 备份命令

```bash
spiderctl backup \
  --data-dir /var/lib/fast-spider \
  --out /srv/backups/fast-spider-20260808.zip
```

如果省略 `--out`，默认在当前目录生成带 UTC 时间的文件名。

备份流程：

1. 扫描 Hub data-dir，只接受普通文件；symlink、socket、device 等特殊文件直接拒绝。
2. 按相对路径写入 ZIP；`hub.db-shm` 属于 SQLite 可重建共享内存索引，明确跳过，`hub.db-wal` 存在时正常备份。
3. 每个文件记录 size、mode 和 SHA-256。
4. 打包完成前再次扫描并重新计算源文件 SHA-256。
5. 文件集合或任一内容发生变化时，备份失败并删除临时包。
6. 校验通过后才原子发布最终备份文件。

这使空闲 Hub 可以直接备份；如果备份期间 SQLite/WAL/Artifact 有写入，不会静默生成“看起来成功”的混合时间点备份。**重要数据升级前仍推荐先停止 Hub 再备份**，这样最直观。

备份文件必须位于 Hub data-dir 之外，避免把备份自身再次打包。

## 4. 校验命令

```bash
spiderctl backup-verify --file /srv/backups/fast-spider-20260808.zip
```

校验内容：

- backup format/version 可识别；
- manifest 结构合法且只包含一个 JSON 对象；
- ZIP/manifest 路径必须是原生规范路径，不能用 `../`、反斜杠、Windows 非法字符、保留设备名（如 `CON`/`NUL`）、尾随点/空格做跨平台歧义；大小写折叠后冲突的两条路径（如 `Foo`/`foo`）也会拒绝。Linux 上允许但无法跨 Windows 无损恢复的这类文件名会明确报错，不会静默改名；
- 不允许 symlink 或重复 ZIP entry；
- `hub.db` 必须存在；
- 每个文件 size 与 SHA-256 必须匹配；
- manifest 外不能夹带额外 data entry；
- 当前安全上限为 10,000 个数据文件、8 GiB 解压后总数据、8 MiB manifest；Create 与 Verify 使用同一组上限，避免生成“自己都无法验证”的备份。这些限制主要用于提前拒绝异常/恶意 ZIP，并非业务配额。

校验成功只说明备份包自身完整，不等于替代真实恢复演练。

## 5. 恢复命令

恢复前先停止旧 Hub：

```bash
spiderctl restore \
  --file /srv/backups/fast-spider-20260808.zip \
  --data-dir /var/lib/fast-spider-restored
```

恢复规则：

- 目标目录必须不存在或为空；不提供 `--force` 覆盖现有数据。
- 先完整验证备份，再解压到同父目录的临时目录。
- 每个解压文件再次核对 size/SHA-256。
- 全部成功后才把临时目录切换为目标 data-dir。
- 失败或收到 Ctrl+C/SIGTERM 时通过 context 尽快停止复制并清理临时恢复目录，不碰原 data-dir。
- 备份输出父目录和恢复目标父目录会解析到真实路径，避免 symlink/junction 把实际写入位置悄悄改到别处。

这种方式刻意避免“恢复到一半覆盖了正在用的数据”。需要替换正式数据时，先把旧目录改名保留，再将已验证的恢复目录切换到正式路径。

## 6. 版本检查

当前版本查询：

```bash
fast-spider-hub --version
fast-spider-node version
spiderctl version
```

版本只是运维事实，不触发下载或安装动作。

## 7. 当前升级流程

Phase 7 使用简单、可观察的手工升级：

```text
构建/取得新二进制
→ 查看新旧版本
→ 备份并 backup-verify
→ 停止当前进程/服务
→ 替换对应二进制
→ 启动
→ 检查 /livez、/readyz 与 Node 重连
→ 运行关键 smoke/E2E
```

Hub 启动时仍由现有内置 migration runner 检查数据库版本与 migration checksum。不单独运行第二个 migration 服务。

如果新版本启动失败：

1. 停止新版本。
2. 若数据库没有发生不兼容 migration，直接切回上一版二进制。
3. 若数据库已发生不可逆变化，恢复升级前备份到新的 data-dir，再用上一版启动。
4. 保留失败现场和日志用于排查。

当前不自动判断“可逆 migration”，因此重要升级前备份是明确门槛。

## 8. 启动方式

生产环境每个组件只保留一个正式进程：

- Hub：一个 `fast-spider-hub` 进程；Linux 推荐由一个 systemd unit 管理。
- Node：一台机器一个当前用户 `fast-spider-node run` 进程。
- Local Bridge 与 Codex stdio 都由 Node 自己管理，不单独启动 helper/daemon。

源码 `go run` 仅用于开发，不作为生产安装方式。

## 9. 卸载

当前不需要专门卸载器。

卸载顺序：

1. 停止 Hub/Node 进程或对应 service/autostart 项。
2. 删除二进制。
3. 若不再需要数据，再人工删除 Hub data-dir / Node data-dir。
4. 删除前建议先运行一次备份并校验。

数据目录和程序目录分离，因此“卸载程序”默认不应该自动删除用户数据。

## 10. 当前明确不做

以下能力只有真实使用需求出现时再单独设计：

- 自动更新检查/下载/安装；
- 签名 release manifest 和密钥轮换体系；
- Windows Authenticode 发布流水线；
- 托盘更新 UI；
- minimumSafeVersion / emergency revoke；
- 多版本 `current/previous` 目录状态机；
- 自动数据库降级；
- Recovery Mode 常驻恢复器；
- 后台定时备份服务。

如果以后需要自动更新，必须复用本章已经验证的备份/校验边界，而不是绕过它再造一套数据恢复机制。

## 11. Phase 7 验收

至少验证：

- backup → backup-verify → restore 完整闭环；
- 篡改备份内容会被拒绝；
- 非空恢复目录会被拒绝且原内容不变；
- 备份输出不能位于 data-dir 内；
- Hub/Node/CLI 版本可读取；
- 恢复后的 `hub.db`、secrets、Artifact 内容与源数据一致；
- Windows/Linux 均能构建；
- `go test ./...` 与现有 Phase 1–6 E2E 不回归。
