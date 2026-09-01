# Governance

Fast Spider is currently maintained under a lightweight maintainer-led model.
The goal is to keep decisions reviewable while the contributor community is
still developing.

## Maintainer

The current core maintainer is [isguang2024](https://github.com/isguang2024),
the repository owner with write and release access.

## Decision process

- Bug fixes, documentation and narrowly scoped improvements are reviewed in a
  pull request.
- Changes to public capabilities, authentication, local execution, data
  formats or security boundaries require corresponding documentation and tests.
- Large product or protocol changes should begin with an issue or ADR so the
  problem, alternatives and compatibility impact are visible before code is
  merged.
- The maintainer makes the final decision when consensus is not reached, and
  records the reasoning in the issue, pull request or ADR.

## Releases

Releases are cut from `main` after the public hygiene and release gates pass.
Release notes summarize user-visible behavior, compatibility and security
impact. The latest released version is the supported public line.

## Adding maintainers

Maintainer access is earned through sustained, security-conscious
contributions and reliable participation in review, issue triage and release
work. New maintainers are recorded in this file through a pull request.

## Conduct and security

Community participation follows [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
Potential vulnerabilities must follow [SECURITY.md](SECURITY.md) and must not
be disclosed in a public issue before a fix or mitigation is available.
