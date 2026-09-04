# Maintainer Workflows

Fast Spider uses repeatable maintenance workflows for issue triage, pull
request review, releases and security-sensitive changes. Automation and AI
assist the maintainer, but do not replace maintainer review or repository
permissions.

## Issue triage

1. Classify the report as a defect, feature request, security concern or usage
   question.
2. Remove or ask the reporter to remove credentials and private environment
   data before investigation continues in public.
3. Record the affected version, operating system, component and reproduction
   boundary.
4. Keep externally blocked behavior distinct from source, test, deployment and
   production evidence.

## Pull request review

Every pull request should explain what changed, why it changed, how it was
validated and whether public contracts or security boundaries are affected.
The maintainer reviews generated or AI-assisted changes as ordinary code and
remains responsible for the final decision.

Codex may assist with:

- locating the contract and implementation responsible for an issue;
- reviewing focused diffs for correctness and security regressions;
- running repository checks and summarizing failures;
- drafting release notes from verified changes;
- maintaining documentation alongside public behavior.

Codex is not permitted to invent adoption data, approve its own changes,
publish credentials, bypass required checks or claim deployment and production
evidence that was not actually collected.

## Required checks

The public CI runs:

```bash
bash scripts/public-release-check.sh
bash scripts/release-gate.sh
```

Security-sensitive or release-critical changes also use the extended local
gate. It automatically selects only the affected real-runtime E2E groups:

```bash
bash scripts/release-gate.sh --full
```

## Release workflow

1. Confirm the intended version and user-visible change set.
2. Run the public hygiene check and core release gate.
3. Run the full gate; use `FAST_SPIDER_GATE_ALL_E2E=1` only when every local
   runtime must be requalified regardless of the changed paths.
4. Update the changelog and release notes with verified facts only.
5. Tag the reviewed `main` commit and publish matching artifacts.
6. Keep rollback material separate from the public repository.

## Codex for open-source maintenance

Additional Codex capacity would be used for day-to-day triage, review,
cross-platform regression analysis, documentation and release workflows. API
credits would be limited to maintainer automation around public issues, pull
requests and releases. Human review, least-privilege credentials and the
project's public release gates remain authoritative.
