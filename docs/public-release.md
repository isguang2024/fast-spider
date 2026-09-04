# Public Release Guide

## Repository policy

Fast Spider separates private development records from public source code.

Public repositories should not contain:

- production deployment records;
- machine identifiers;
- private environment details;
- tokens, credentials, backups, logs, or generated artifacts;
- internal task tracking documents.

## If the repository is already public

A clean working tree or a clean current commit does not prove that a public
repository's history is clean. If an earlier commit contained deployment
records, machine identifiers, private paths, generated bundles or credentials,
deleting the file later does not remove it from Git objects available to the
public.

Before treating an existing public remote as safe, run the history scan from a
full clone:

```bash
go run ./cmd/secretscan --history
```

For deployment-specific names that are not suitable for this repository, put
one marker per line in the ignored `.local/public-private-markers.txt` and run:

```bash
go run ./cmd/secretscan --history \
  --markers .local/public-private-markers.txt
```

A hit means the existing history must not be advertised as a clean public
history.

Use the export flow below to create a new one-commit public source snapshot.
The export script never rewrites or pushes the source repository; replacing an
already-public default branch is a separate, coordinated repository migration.

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
bash scripts/release-gate.sh --full
bash scripts/public-export.sh \
  --output /absolute/path/fast-spider-public \
  --require-license
```

For a source-only update that does not need real-runtime qualification, replace
the extended gate above with the core gate:

```bash
bash scripts/release-gate.sh
```

The exported repository should have exactly one root commit named `Initial public source snapshot`.

## Security checklist

Before publishing:

- select and include a LICENSE;
- review third-party licenses;
- scan source and Git history for secrets;
- verify deployment examples only use placeholder values;
- verify deployment-specific Hub URLs stay in service environment/configuration,
  not in source, examples or public release notes;
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
