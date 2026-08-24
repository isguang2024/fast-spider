# Security Policy

Fast Spider can execute powerful local machine capabilities. Security reports are handled with priority and should avoid public disclosure until a fix or mitigation is available.

## Reporting vulnerabilities

Please report security issues privately to the maintainers instead of opening a public issue.

Include:

- affected version or commit;
- affected component: Hub, Node, MCP, Direct API, Local Bridge, browser, AI control, update, backup or artifact handling;
- reproduction steps using placeholder values;
- impact assessment;
- any known workaround or mitigation.

Do not include real credentials, access tokens, private keys, cookies, machine identifiers, private paths, production hostnames or production data in reports.

## What to report

Security-sensitive areas include:

- authentication, authorization and session handling;
- connection token and Direct Access Key handling;
- machine routing and capability boundaries;
- file read/edit, shell, build, Git and browser execution;
- artifact and temporary file relay;
- screenshot and presentation output;
- AI provider discovery, routing and session control;
- update, backup, recovery and public export logic.

## Public issue guidance

Do not open a public issue for an unpatched vulnerability.

Public issues are appropriate for:

- general hardening suggestions without exploit details;
- documentation improvements;
- already-fixed security follow-ups;
- dependency update discussions without active exploit details.

## Supported versions

The latest released version receives security fixes when practical.

Older private development snapshots and unreleased branches are not treated as supported public releases.

## Disclosure

Maintainers may publish a short advisory after a fix is available. Advisories should avoid leaking working exploit details unless disclosure is necessary for users to assess risk or mitigate safely.
