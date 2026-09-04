# Contributing to Fast Spider

Thank you for helping improve Fast Spider.

This project controls local machine capabilities, so changes should be precise, reviewable and security-conscious.

## Before opening a pull request

1. Create a focused branch.
2. Keep unrelated formatting or refactors out of the change.
3. Update documentation when behavior, configuration or public contracts change.
4. Run the relevant checks.
5. Remove credentials, private paths, machine identifiers and production details from examples, logs and screenshots.

## Required checks

For most changes:

```bash
go test ./... -count=1
go vet ./...
git diff --check
```

For release-sensitive or security-sensitive changes:

```bash
bash scripts/public-release-check.sh
bash scripts/release-gate.sh
```

For release-critical changes, run the diff-aware extended gate instead of the
core gate:

```bash
bash scripts/release-gate.sh --full
```

## Pull request content

A pull request should include:

- what changed;
- why it changed;
- how it was validated;
- compatibility or migration notes;
- security implications if the change touches identity, tokens, file access, shell execution, browser automation, AI harnesses, artifacts or release flows.

## Security-sensitive changes

Use extra care for changes touching:

- authentication and authorization;
- connection tokens and Direct Access Keys;
- machine routing and capability execution;
- file system, shell, Git, browser and screenshot operations;
- artifact upload/download paths;
- AI provider discovery, routing and session control;
- update, backup and public export logic.

Do not discuss suspected vulnerabilities in a public issue before following `SECURITY.md`.

## Documentation policy

Public documentation should use placeholder values and localhost examples.

Do not add:

- real production hosts or IP addresses;
- private backup paths or backup archive hashes;
- machine identifiers;
- private environment values;
- internal acceptance logs;
- credentials, tokens, cookies or private keys.
