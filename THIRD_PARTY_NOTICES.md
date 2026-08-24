# Third Party Notices

Fast Spider uses third-party open source dependencies. This file is a public release notice entry point, not a generated SBOM.

Before publishing binary releases, maintainers should generate and review a dependency license report for:

- Go modules in `go.mod` and `go.sum`;
- browser sidecar dependencies in `sidecar/browser/package.json`;
- packaged browser/runtime components;
- externally distributed helper binaries such as managed search tools.

## Direct Go dependencies

Current direct Go dependencies are declared in `go.mod` and include:

- `github.com/coder/websocket`
- `github.com/google/jsonschema-go`
- `github.com/kbinani/screenshot`
- `github.com/lxn/win`
- `github.com/modelcontextprotocol/go-sdk`
- `github.com/refraction-networking/utls`
- `golang.org/x/net`
- `golang.org/x/sys`
- `modernc.org/sqlite`

## Browser sidecar dependencies

Current browser sidecar dependencies are declared in `sidecar/browser/package.json` and include:

- `playwright`

## Distribution rule

When distributing binaries or packaged components, include required upstream license and notice files for bundled dependencies and runtime components.
