# 身份与权限（0.3.x）

## 身份链

| 对象 | 身份 | 凭据 |
|---|---|---|
| Owner Web | ownerId | Web Session Cookie + CSRF |
| MCP Client | OAuth clientId | OAuth Access/Refresh Token |
| Machine | machineId | Device Key / 设备证明 |
| 首次 Node 登记 | Owner 归属 | Connection Token |

Connection Token 只用于首次登记，不保存到 Node。MCP OAuth 不参与 Node 登记。

## 本地权限

0.3.x 不再实现 Fast Spider 自己的目录授权层。授权一个 MCP Client 访问当前 Owner 后，它可以针对该 Owner 的在线 Machine 发起 Node 能力请求；Node 最终能做什么由**运行 Node 的操作系统用户权限**决定。

这意味着：

- 文件访问遵循 NTFS/POSIX 权限。
- Shell/Git/Build 以同一 OS 用户启动。
- 绝对路径直接用于定位本机资源。
- 不存在逐目录 read/write/shell/git/build 开关。
- 不存在目录授权 ID 或目录白名单。

这个模型是明确的个人自用设计，不适用于把同一 Machine 暴露给不可信租户。

## 吊销

- 撤销 Connection Token：只阻止新机器登记，不影响已经登记机器。
- 撤销 Machine：使该机器后续设备认证失效并断开在线连接。
- 撤销 OAuth Authorization：该 MCP Client 的授权和相关 Token 失效。
- 删除已撤销记录：从日常后台列表移除软删除记录。

## 敏感信息

普通日志、MCP 输出和后台列表不得返回 Connection Token、Device Key 私钥、OAuth 明文 Token、密码或 Session Cookie。
