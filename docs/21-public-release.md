# 21 公开发布与干净 Git 历史

## 1. 原则

Fast Spider 的私有开发仓库可以保留完整迭代历史，但公开仓库不得直接 push、mirror 或 rewrite 后继续使用该历史。真正公开时只从一个已经通过 release gate 的干净源码快照生成新的 Git 仓库；公开仓库从一个新的 root commit 开始。

这样可以确保早期已经删除的私有 Provider、生产域名、Machine 标识、临时运维记录和其它历史实现不会因为 Git object/history 被带到公开远端。

`.learnings/` 与 `.local/` 属于内部开发记录/本机状态，通过 `export-ignore` 排除，不进入公共源码快照。

## 2. 发布前检查

当前 release gate 会检查 tracked public-source 文件中的常见密钥/Token、Machine ID 和本机仓库绝对路径。私有部署还可以在本机创建：

```text
.local/public-private-markers.txt
```

每行一个不允许进入公开源码的私有标识。该文件被 Git 忽略，只参与本机 release gate 与 public export 扫描，不会进入公开仓库。

真正公开前还必须确认：

- `main` 工作区干净且完整 release gate 通过；
- 源码默认值不绑定任何人的生产 Hub；
- `LICENSE` 或 `LICENSE.txt` 已由维护者明确选择；
- README、部署文档和示例只使用 `example`/localhost 等公开安全值；
- 没有把 `.git`、`.local`、`.learnings`、运行数据库、密钥、日志、Artifact 或生产备份复制进公开目录。

## 3. 生成公开仓库

从私有 `main` 的干净状态执行：

```bash
bash scripts/public-export.sh --output /absolute/path/fast-spider-public
```

脚本会：

1. 要求源仓库工作区干净；
2. 使用 `git archive` 导出一个已提交 revision，不读取未提交文件；
3. 应用 `export-ignore`，排除内部开发记录；
4. 再次执行秘密、Machine ID、本机路径与本地私有 marker 扫描；
5. 默认执行 `go mod verify` 与 `go test ./...`；
6. 在输出目录初始化全新 Git 仓库；
7. 创建唯一一个 `Initial public source snapshot` root commit；
8. 验证公开仓库只有一个 commit 且工作区干净。

脚本默认只警告 LICENSE 缺失，便于内部验证导出链路。真正准备公开时必须使用：

```bash
bash scripts/public-export.sh \
  --output /absolute/path/fast-spider-public \
  --require-license
```

不要给 public export 目录添加指向私有仓库的 remote，也不要把私有仓库 `.git` 复制过去。完成最终人工复核后，再在公开目录中添加新的公共 remote 并首次 push。

## 4. 事实源

公开 Git 历史只证明“公开版本从哪个公共快照开始”，不承担保存私有开发过程的职责。私有 `main`、Working Context、Codex Thread 和本机运维记录继续各自保存其真实用途；项目当前代码事实仍以 Git + 文件内容为最终事实源。
