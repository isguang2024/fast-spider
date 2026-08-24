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

## Environment variables

| Variable | Required | Description |
|---|---:|---|
| `FAST_SPIDER_ADMIN_PASSWORD` | first Hub startup / admin rotation | One-time administrator password. Prefer this over `--admin-password` so secrets are not visible in process listings. |
| `FAST_SPIDER_CODEX_EXECUTABLE` | no | Absolute path override for the Codex executable used by Node. |
| `FAST_SPIDER_CODEX_APP_SERVER_SOCKET` | no | Optional explicit socket path for an externally owned Codex app-server proxy. |
| `FAST_SPIDER_WSL_DISTRIBUTION` | tests only | Selects the WSL distribution for local full-gate tests. |
| `FAST_SPIDER_WSL_TEST_CWD` | tests only | Selects a Windows path used by WSL runtime tests. |

See `.env.example` for a safe local template.

## Production notes

- Keep Hub on loopback and terminate TLS at a reverse proxy.
- Configure reverse proxy forwarded headers carefully; never append user-controlled forwarding headers to rate-limit inputs.
- Store Hub data and backups as sensitive material.
- Do not commit local data directories, logs, release artifacts, backups or generated binaries.
