# Getting Started

## Architecture

Fast Spider consists of:

- Hub: control plane for identity, routing, jobs and artifacts.
- Node: execution plane running on the user's machine.
- MCP/Web/CLI: clients sharing the same capability model.

## First deployment

1. Start Hub.
2. Configure the administrator account.
3. Create a Node connection token.
4. Start a Node client.
5. Register the Node from the local client interface.

## Security model

Node actions execute with the operating system permissions of the user running Node.

Fast Spider does not provide privilege escalation, arbitrary TCP tunneling, or a hidden remote desktop mode.

## Development

Requirements:

- Go 1.26+
- Optional browser runtime dependencies for browser automation

Run tests:

```bash
go test ./...
go vet ./...
```
