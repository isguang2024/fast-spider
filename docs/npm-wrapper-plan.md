# npm Wrapper Plan

This repository does not publish an npm package yet. The current status is
**plan only**; `npm install -g fast-spider` must not be presented as an already
available release.

## Proposed package

- Package name: `fast-spider`
- CLI: `fast-spider`
- First command: `fast-spider share --project .`
- Distribution: platform-specific Hub and Node binaries downloaded from a
  versioned release manifest, with SHA-256 verification before execution.

## MVP wrapper behavior

1. Detect Windows, macOS and Linux plus the CPU architecture.
2. Check whether a matching cached Hub/Node release exists.
3. If no binary is cached, print the exact Go source fallback or a release URL;
   do not run an unverified download.
4. Forward all arguments and exit with the child process exit code.
5. Keep cache, temporary profiles and downloaded artifacts outside the selected
   project root.
6. Provide `--help`, `--version`, a non-zero error for unsupported platforms,
   and a clear offline error when the release cannot be reached.

## Release and security work before publishing

- Reserve the package name and configure provenance/publishing credentials.
- Add a lockfile and platform matrix for wrapper smoke tests.
- Verify release signatures and checksums before extraction.
- Test spaces, Unicode paths, Windows PowerShell quoting and Ctrl-C cleanup.
- Test `none`, `cloudflare` and `ngrok` without requiring a real public tunnel
  by injecting local stubs.
- Document how to pin a version and how to remove the local cache.
- Run the public export and full release checks from the exported tree.

Until those steps are complete, use the source command:

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```
