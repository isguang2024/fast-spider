# Fast Spider

Fast Spider is a self-hosted, cross-platform remote development and automation platform.

It connects one or more user-owned machines to a Hub over outbound HTTPS/WSS. The Hub provides identity, routing, jobs, audit logs, artifacts and API surfaces. Each Node performs the actual work on the local machine under the operating-system user that started it.

Fast Spider is designed for structured automation, not generic remote desktop access.

## What it provides

- Multi-node registration, discovery and revocation.
- File read, precise file edit, code search and diff operations.
- Shell, build, test and log-stream jobs with cancellation.
- Git status, diff, commit and controlled remote operations.
- Artifact transfer and temporary presentation files.
- Browser automation and screenshots through an isolated browser runtime.
- Provider-neutral AI control for local AI harnesses such as Codex and Claude Code.
- MCP, Web Console, CLI and Local Bridge surfaces sharing the same capability model.

## What it does not provide

Fast Spider is not a hidden remote desktop, privilege escalation tool or generic tunnel.

It does not provide:

- arbitrary TCP forwarding;
- continuous desktop video streaming;
- automatic privilege escalation;
- unrestricted raw provider credential access;
- a second file-system permission model that overrides the operating system.

Node actions run with the permissions of the current OS user. Operators should treat a connected Node as a powerful local automation agent.

## Architecture

```text
+-------------------+        HTTPS/WSS 443        +----------------------+
|                   |  ------------------------->  |                      |
|  Fast Spider Node |                              |   Fast Spider Hub    |
|                   |  <-------------------------  |                      |
+-------------------+                              +----------------------+
        |                                                       |
        | local execution                                      | API / MCP / Web
        v                                                       v
+-------------------+                              +----------------------+
| Files / Shell /   |                              | Users / Machines /   |
| Git / Browser / AI|                              | Jobs / Audit / Relay  |
+-------------------+                              +----------------------+
```

The Hub never directly mounts a Node file system. All execution happens on the Node and is routed through explicit capabilities.

## Quick start

Requirements:

- Go 1.26+
- Git
- Optional Node.js / Playwright dependencies for browser automation

Start a local Hub:

```bash
FAST_SPIDER_ADMIN_PASSWORD='<replace-with-a-strong-password>' \
  go run ./cmd/hub --data-dir ./data
```

Initialize the Hub URL and owner bootstrap:

```bash
go run ./cmd/spiderctl setup-url \
  --public-url http://127.0.0.1:8787 \
  --allow-insecure \
  --bootstrap-token-file ./data/bootstrap-token
```

Start the local Node UI:

```bash
go run ./cmd/node ui
```

Or connect a headless Node after creating a connection token in the Hub:

```bash
go run ./cmd/node connect \
  --hub http://127.0.0.1:8787 \
  --allow-insecure \
  --token '<connection-token>' \
  --name dev-node
```

For more details, see [Getting Started](docs/getting-started.md), [Configuration Reference](docs/configuration.md) and [Deployment and Operations](docs/14-deployment-and-operations.md).

## MCP tools

The current public surface contains the following top-level MCP tools:

```text
machine_list
machine_get
capability_list
audit_log
operation_log
file_read
file_edit
code_search
shell_run
job_watch
job_cancel
git_control
build_control
artifact_get
browser_control
screenshot_take
thinking_team
ai_control
working_context
```

Tool inputs use explicit absolute paths for machine-local operations. Examples:

- `file_read`, `file_edit`, `code_search`: absolute `path`
- `shell_run`, `build_control`: absolute `cwd`
- `git_control`: absolute `repositoryPath`
- `ai_control.session.create`: absolute `workingDirectory`

## Documentation

Start here:

- [Documentation Index](docs/README.md)
- [Getting Started](docs/getting-started.md)
- [Configuration Reference](docs/configuration.md)
- [Product Vision](docs/00-product-vision.md)
- [Requirements and Scope](docs/01-requirements-and-scope.md)
- [System Architecture](docs/02-system-architecture.md)
- [Hub Design](docs/03-hub-design.md)
- [Node Design](docs/04-node-design.md)
- [Node Capabilities](docs/05-node-capabilities.md)
- [Public API and MCP](docs/10-public-api-and-mcp.md)
- [Deployment and Operations](docs/14-deployment-and-operations.md)
- [Security Threat Model](docs/09-security-threat-model.md)
- [Test Strategy](docs/17-test-strategy.md)
- [Public Release Guide](docs/public-release.md)

## Development

Run the standard checks:

```bash
go test ./... -count=1
go vet ./...
git diff --check
```

Run the public release hygiene check:

```bash
bash scripts/public-release-check.sh
```

Run the release gate:

```bash
bash scripts/release-gate.sh
```

Run the extended release gate where the required local runtimes are available:

```bash
bash scripts/release-gate.sh --full
```

## Public source release

Do not publish private development history as the public repository history.

Use the public export flow to create a clean source snapshot with a new root commit:

```bash
bash scripts/public-export.sh \
  --output /absolute/path/fast-spider-public \
  --require-license
```

See [Public Release Guide](docs/public-release.md) for the full policy.

## Security

Read [SECURITY.md](SECURITY.md) before reporting a vulnerability or sharing logs.

Never publish:

- connection tokens;
- Direct Access Keys;
- private keys;
- environment files;
- production backup paths or backup archives;
- logs containing machine identifiers, credentials or local private paths.

## Contributing

Contributions should follow [CONTRIBUTING.md](CONTRIBUTING.md) and the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Fast Spider is released under the Apache License 2.0. See [LICENSE](LICENSE).
