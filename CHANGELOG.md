# Changelog

This file records notable public changes to Fast Spider. The project follows
semantic versioning for public releases.

## Unreleased

- Add public CI, governance, support and maintainer workflow documentation.
- Document the current early-stage public project status and contribution path.

## 0.4.32 - 2026-09-02

- Separate ChatGPT Cloud quick/complete return modes from preset/advanced model
  configuration, load Advanced models from a user-editable Node-local file, and
  refresh reasoning choices from the live ChatGPT model catalog.
- Preserve the initial ChatGPT Cloud model and reasoning effort when continuing a
  session unless the caller explicitly overrides either selection.
- Clarify deployment-specific Hub URL configuration and public-history hygiene for
  self-hosted installations.

## 0.4.31 - 2026-09-02

- Unified ChatGPT Cloud creation behind one `session.create` entry with Quick
  chat and complete modes.
- Added model/thinking creation presets sourced from the live ChatGPT model
  catalog and forwarded selected reasoning as `thinking_effort`.
- Included mode, model and thinking selections in idempotent creation matching.

## 0.4.30 - 2026-09-02

- Added Quick chat creation for ChatGPT Cloud sessions, returning as soon as
  the conversation is created while completion continues in the background.
- Preserved idempotent recovery, session watch/result behavior and the existing
  complete creation mode.

## 0.4.28 - 2026-08-29

- Added a configurable Codex Desktop session ownership mode for shared and
  Fast Spider-managed workflows.
- Added caller cleanup contracts for browser sessions, jobs and temporary
  artifacts.
- Hardened uncertain AI session creation and recovery behavior.
- Expanded cross-platform Node release validation and lifecycle checks.

## 0.4.27 - 2026-08-27

- Shortened Windows Local Bridge endpoints and improved bridge stability.
- Improved MCP and Codex Desktop integration behavior.
- Added the safe `share --project` first-run workflow.

## 0.4.25 - 2026-08-24

- Repaired cloud session routing and remote handling.
- Added public-source hygiene, contribution, security and release-export
  documentation.

Earlier development history is available through Git tags and commit history.
