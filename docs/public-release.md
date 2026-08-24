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
2. Run the release gate.
3. Export a public snapshot using `scripts/public-export.sh`.
4. Create a new public repository root commit.
5. Run tests again on the exported repository.

The public repository is a source distribution, not a mirror of the private development history.

## Security

Before publishing:

- select and include a LICENSE;
- review third-party licenses;
- scan source and Git history for secrets;
- verify deployment examples only use placeholder values.

## Support

Security issues should follow `SECURITY.md`.
Contribution rules are described in `CONTRIBUTING.md`.
