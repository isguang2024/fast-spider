# Public Release Guide

## Repository policy

Fast Spider separates private development records from public source code.

Public repositories should not contain:

- production deployment records;
- machine identifiers;
- private environment details;
- tokens, credentials, backups, logs, or generated artifacts;
- internal task tracking documents.

## Release flow

The recommended flow is:

1. Keep the development repository private.
2. Run the public release hygiene check.
3. Run the release gate.
4. Export a public snapshot using `scripts/public-export.sh`.
5. Create a new public repository root commit.
6. Run tests again on the exported repository.

The public repository is a source distribution, not a mirror of the private development history.

## Required checks

Run:

```bash
bash scripts/public-release-check.sh
bash scripts/release-gate.sh
bash scripts/public-export.sh \
  --output /absolute/path/fast-spider-public \
  --require-license
```

When all local runtimes are available, also run:

```bash
bash scripts/release-gate.sh --full
```

The exported repository should have exactly one root commit named `Initial public source snapshot`.

## Security checklist

Before publishing:

- select and include a LICENSE;
- review third-party licenses;
- scan source and Git history for secrets;
- verify deployment examples only use placeholder values;
- confirm `docs/progress`, `.local`, `.learnings`, runtime data, backups, logs and generated artifacts are not tracked;
- confirm README and docs do not contain production-only hosts, private paths, machine names or real backup evidence.

## Public repository setup

After the export passes:

```bash
cd /absolute/path/fast-spider-public
git remote add origin <new-public-repository-url>
git push -u origin main
```

Do not add a remote that points back to the private development repository.

## Support

Security issues should follow `SECURITY.md`.
Contribution rules are described in `CONTRIBUTING.md`.
