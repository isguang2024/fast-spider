# Configuration Reference

Fast Spider uses command-line flags for most runtime configuration. Environment variables are used only where they avoid exposing sensitive values in process listings or where a local runtime needs a user-specific override.

## Hub flags

| Flag | Default | Description |
|---|---:|---|
| `--listen` | `127.0.0.1:8787` | Hub HTTP listen address. Production deployments should keep this on loopback behind a TLS reverse proxy. |
| `--data-dir` | `./data` | Hub database, secrets and runtime state directory. |
| `--release-dir` | `<data-dir>-releases` | Signed Node and component release directory. |
| `--allowed-hosts` | `localhost,127.0.0.1` | Comma-separated Host allowlist. Use the public Hub hostname in production. |
| `--public-base-url` | empty | Public Hub base URL used for MCP OAuth discovery. |
| `--oauth-redirect-hosts` | `chatgpt.com,localhost,127.0.0.1,::1` | Comma-separated OAuth redirect host allowlist. |
| `--admin-password` | empty | One-time administrator password. Prefer `FAST_SPIDER_ADMIN_PASSWORD`. |
| `--version` | false | Print version and exit. |

## Deployment URL policy

The public Hub URL is deployment configuration, not a compiled-in project
default. Set `--public-base-url` to the exact HTTPS origin that external
clients can reach. The value may include a path prefix such as
`https://hub.example/fast-spider`; OAuth metadata, redirects and generated
resource URLs will use that prefix.

`--allowed-hosts` accepts hostnames only (no scheme or path). Include the
public hostname and keep loopback entries when local health checks or Node
connections are needed:

```bash
go run ./cmd/hub \
  --listen 127.0.0.1:8787 \
  --data-dir ./data \
  --allowed-hosts localhost,127.0.0.1,hub.example \
  --public-base-url https://hub.example
```

For a reverse-proxy subpath, keep the prefix in `--public-base-url` and route
the public `/fast-spider/...` requests to the Hub's root routes. The proxy
must also terminate TLS and preserve the documented forwarded-IP headers.
When the hostname or prefix changes, restart Hub with the new value, update
Node connection settings and reconnect external MCP/OAuth clients.

## Environment variables

| Variable | Required | Description |
|---|---:|---|
| `FAST_SPIDER_ADMIN_PASSWORD` | first Hub startup / admin rotation | One-time administrator password. Prefer this over `--admin-password` so secrets are not visible in process listings. |
| `FAST_SPIDER_ALLOWED_HOSTS` | service launcher | Host allowlist passed to `--allowed-hosts` by the systemd template or another launcher. |
| `FAST_SPIDER_PUBLIC_BASE_URL` | service launcher | Canonical external URL passed to `--public-base-url`; keep it outside the public source tree. |
| `FAST_SPIDER_OAUTH_REDIRECT_HOSTS` | service launcher | OAuth redirect host allowlist passed to `--oauth-redirect-hosts`. |
| `FAST_SPIDER_CODEX_EXECUTABLE` | no | Absolute path override for the Codex executable used by Node. |
| `FAST_SPIDER_CODEX_APP_SERVER_SOCKET` | no | Optional explicit socket path for an externally owned Codex app-server proxy. |
| `FAST_SPIDER_CODEX_DESKTOP_BRIDGE` | no | Compatibility fallback for headless Node processes without Node UI local configuration. Set `0`/`false` to disable the local Codex Desktop owner/control IPC bridge; set `1`/`true` to require it explicitly on Windows. The Node UI's Codex session-mode setting takes precedence. It does not replace Fast Spider's app-server and does not yet provide native Desktop live-history streaming. |
| `FAST_SPIDER_WSL_DISTRIBUTION` | tests only | Selects the WSL distribution for local full-gate tests. |
| `FAST_SPIDER_WSL_TEST_CWD` | tests only | Selects a Windows path used by WSL runtime tests. |
| `FAST_SPIDER_HUB_BIN` | `share` only | Optional absolute or PATH-resolvable Hub executable used when `share` cannot use the source tree. |
| `FAST_SPIDER_SOURCE_ROOT` | `share` only | Fast Spider source tree used for `go run ./cmd/hub` and the printed Node command. |

See `.env.example` for a safe local template.

## Node project profile

`cmd/node connect` and `cmd/node run` accept:

| Flag | Default | Description |
|---|---:|---|
| `--project-root` | empty | Resolved project directory for Project mode. Omit it to preserve Machine mode. |

Project mode checks explicit file, search, shell/build cwd, Git, context,
artifact and AI input paths at the Node capability boundary. Native desktop,
display and window screenshots are disabled. It is a path-constraint profile,
not a complete OS sandbox.

## `spiderctl share`

| Flag | Default | Description |
|---|---:|---|
| `--project` | `.` | Project directory bound to the printed Node command. |
| `--tunnel` | `none` | `none`, `cloudflare` or `ngrok`. Tunnel binaries must already be in `PATH`. |
| `--listen` | `127.0.0.1:8787` | Loopback Hub address. Share rejects non-loopback addresses. |
| `--data-dir` | temporary | Hub profile directory. Keep it outside the project root. |
| `--hub-bin` | auto | Optional `fast-spider-hub` executable. |
| `--node-bin` | auto | Optional Node executable name/path for the printed command. |
| `--source-root` | auto | Fast Spider source tree fallback when Hub/Node binaries are unavailable. |

`share` creates a temporary owner account and Node Connection Token through the
existing Web Console flow. The printed token is for Node registration only;
MCP still uses OAuth and Direct API still uses a Direct Access Key.

## Production notes

- Keep Hub on loopback and terminate TLS at a reverse proxy.
- Configure reverse proxy forwarded headers carefully; never append user-controlled forwarding headers to rate-limit inputs.
- Store Hub data and backups as sensitive material.
- Do not commit local data directories, logs, release artifacts, backups or generated binaries.
