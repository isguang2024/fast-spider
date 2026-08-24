# Getting Started

[简体中文](getting-started.zh-CN.md)

## Architecture

Fast Spider consists of:

- Hub: control plane for identity, routing, jobs, audit events and artifacts.
- Node: execution plane running on the user's machine.
- MCP/Web/CLI/Local Bridge: clients sharing the same capability model.

The Hub routes requests; the Node performs local work. A Node is never a
remote desktop and the Hub never mounts the Node file system.

## First run: Project mode

From a Fast Spider source checkout, run:

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```

The command:

1. starts a temporary Hub on `127.0.0.1:8787`;
2. creates a temporary administrator password and first owner account;
3. creates a 30-day Node connection token;
4. prints the Node registration command and the MCP URL;
5. keeps the Hub running until you press Ctrl-C.

Run the printed Node command in a second terminal. `share` deliberately does
not start Node or a Node UI, so the local process owner remains explicit. The
temporary profile is removed on normal exit when `--data-dir` was omitted.

The command needs either a `fast-spider-hub` executable in `PATH`, or the
Fast Spider source tree (or `FAST_SPIDER_SOURCE_ROOT`) so it can use
`go run ./cmd/hub`. If neither is available, it stops with an installation
hint instead of silently starting an unconfigured service.

For a cloud client, replace `--tunnel none` with `--tunnel cloudflare` or
`--tunnel ngrok`. Those commands require the corresponding executable in
`PATH`; see [Free Local Deployment](free-local-deployment.md).

Use this first safe prompt after the Node is online:

```text
Inspect this repository and summarize its structure. Do not make changes.
```

## Manual deployment path

For a persistent profile or a separately managed Hub:

1. Start Hub and set `FAST_SPIDER_ADMIN_PASSWORD`.
2. Open the one-time setup URL from `spiderctl setup-url`.
3. Sign in to the Web Console and create a Connection Token.
4. Start Node with `go run ./cmd/node connect` or the Node UI.
5. Revoke unused Connection Tokens and devices in the Web Console.

The Connection Token registers a Node. It is not an MCP credential. MCP uses
OAuth, while Direct API calls use a separate Direct Access Key.

## Project mode and Machine mode

`--project <directory>` enables Project mode. The Node policy checks local
paths at the capability boundary for file read/write, code search, shell/build
working directories, Git repositories/worktrees, working context, artifacts
and explicit AI input paths. Native desktop/window screenshots are denied.

Project mode is a path-constraint profile, not an operating-system sandbox.
Shell interpreters, Git remotes, browsers and local AI providers can still
have side effects or use credentials available to the OS account. Machine mode
(omit `--project-root` when running Node) preserves the original whole-machine
OS-user boundary for private advanced use.

## Development

Requirements:

- Go 1.26+
- Optional browser runtime dependencies for browser automation

Run tests:

```bash
go test ./...
go vet ./...
```
