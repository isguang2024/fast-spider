# Fast Spider Documentation

This directory contains the public design, operations and release documentation for Fast Spider.

## Start here

- [Getting Started](getting-started.md) — first deployment and development workflow.
- [Configuration Reference](configuration.md) — command flags, environment variables and production notes.
- [Product Vision](00-product-vision.md) — product positioning and boundaries.
- [Requirements and Scope](01-requirements-and-scope.md) — supported and intentionally unsupported capabilities.
- [System Architecture](02-system-architecture.md) — Hub, Node and client surfaces.

## Core design

- [Hub Design](03-hub-design.md)
- [Node Design](04-node-design.md)
- [Node Capabilities](05-node-capabilities.md)
- [Wire Protocol](06-wire-protocol.md)
- [Job and Event Model](07-job-and-event-model.md)
- [Identity and Permissions](08-identity-and-permissions.md)
- [Data Model](13-data-model.md)

## API and integrations

- [Public API and MCP](10-public-api-and-mcp.md)
- [Local Bridge and AI Control](11-local-bridge-and-ai-control.md)
- [Browser and Screenshot](12-browser-and-screenshot.md)
- [Thinking Team](22-thinking-team.md)

## Security and operations

- [Security Threat Model](09-security-threat-model.md)
- [Deployment and Operations](14-deployment-and-operations.md)
- [Observability](15-observability.md)
- [Update and Recovery](16-update-and-recovery.md)
- [Cache and Lifecycle](23-cache-and-lifecycle.md)

## Testing and release

- [Test Strategy](17-test-strategy.md)
- [Open Source Evaluation](18-open-source-evaluation.md)
- [Roadmap](19-roadmap.md)
- [Open Questions](20-open-questions.md)
- [Public Release Guide](public-release.md)

## Public documentation policy

Public documentation should use placeholder paths, localhost URLs and example values.

Do not add:

- production-only hostnames or IP addresses;
- machine identifiers;
- real backup paths or backup archive hashes;
- credentials, tokens or private environment values;
- internal acceptance logs or operator-only incident notes.
