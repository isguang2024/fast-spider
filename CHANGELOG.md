# Changelog

This file records notable public changes to Fast Spider. The project follows
semantic versioning for public releases.

## 0.4.60 - 2026-09-05

- Require Codex Desktop IPC confirmation before marking Cloud CHAT callback
  nudges as delivered to a local Codex task. Desktop-owned callback targets no
  longer fall back to app-server delivery when the Desktop owner is temporarily
  unavailable; the durable callback queue stays pending for retry instead.
- Record the callback nudge delivery proof (`executionMode`, `owner`, and
  `turnId`) in the local callback registry for diagnosis and recovery.

## 0.4.55 - 2026-09-04

- Keep typed Cloud CHAT callbacks usable before a ChatGPT connector schema
  refresh by routing `completion.notify|claim|ack` through the existing
  `codex_cloud_collaboration` tool; the dedicated completion tool remains an
  equivalent shortcut.
- Validate collaboration controller and dispatcher IDs from the local Codex
  Desktop registry before asking app-server for metadata, avoiding an observed
  request timeout while preserving Provider fallback for unregistered IDs.

## 0.4.54 - 2026-09-04

- Share one account-level ChatGPT Celsius WebSocket across managed CHATs, use
  realtime completion as the primary path, and synchronize CHAT subscriptions
  on that same live socket. Local callback delivery is event/deadline driven
  instead of a fixed queue scan; Provider recovery runs on the 30-minute fallback
  only after startup, a realtime connection gap, or while disconnected. Early
  status polls are served from persisted state with the next due time and no
  Node/provider call.
- Make reused CHAT dispatch restart-safe with a stable provider message ID:
  retries reconcile or resume the same turn before arming the callback, and
  status recovery cannot mistake the previous completed turn for the new task.
- Add durable typed collaboration callbacks: `local_file` references a result
  written directly through FS to a registered Node-local path without uploading
  it, `text` is limited to 2000 Unicode characters/8192 UTF-8 bytes, and `status`
  carries no payload. Batch claims remain capped at 64 records and now also cap
  total inline text at 64 KiB.

## 0.4.53 - 2026-09-04

- Make Cloud CHAT assistance optional and session-ID driven: reuse only an exact
  user-supplied CHAT regardless of its creator, otherwise create a clean
  `quick_chat` without guessing history; support quick follow-up sends, fast
  local Codex metadata checks, and precise callback release for reused CHATs.

## 0.4.40 - 2026-09-04

- Initialize the Working Context Markdown workspace when the recovery issue log
  is missing, then use file-revision CAS to append the bounded issue report.

## 0.4.39 - 2026-09-03

- Make the existing ChatGPT Cloud CHAT the primary completion callback for
  `codex_cloud_collaboration` through FastSpider_FS `event.ingest` →
  `event.ack`; retain Node callback delivery and polling as recovery fallback,
  and explicitly forbid creating a new Cloud Worker/CHAT for completion.
- Expose `ai_control session.result` manifest selectors and a task-scoped
  `actorSessionId=$self` binding for Cloud CHAT callbacks.
- Persist callback results as a batch-claim queue with five-minute claim leases;
  keep local queue checks at roughly 30 seconds, provider status recovery at
  roughly 10 minutes, and send only lightweight delayed nudges to idle dispatchers.
- Add the stalled Cloud CHAT recovery message `请继续` after a provider status
  check, suppress duplicates until new progress, leave replacement to an
  explicit controller decision, and direct bounded recovery questions to
  `docs/progress/04-open-issues.md` through Working Context CAS.
- Limit release builds to the Linux Hub/spiderctl server and the Windows amd64
  Node client, and scan only the candidate HEAD history during the full gate.
- Add public CI, governance, support and maintainer workflow documentation.
- Document the current early-stage public project status and contribution path.

## 0.4.38 - 2026-09-03

- Retry callback delivery to idle Desktop dispatchers every five seconds while
  retaining the low-frequency subscription recovery loop.
- Retry the post-registration terminal backfill briefly to cover delayed Cloud
  completion visibility.

## 0.4.37 - 2026-09-03

- Reconcile Cloud turns that complete before callback registration and retry
  failed callback result publication during status-poll recovery.

## 0.4.36 - 2026-09-03

- Add a same-machine stdio MCP entry that invokes the current-user Local Bridge
  directly without Hub routing or a machine ID.
- Recognize Desktop-registered projectless Codex tasks for callback delivery and
  continuation while preserving real provider failures and rejecting unknown IDs.
- Automatically close callback-driven Cloud collaborations after acknowledgement,
  with result-manifest polling as an idempotent recovery path when delivery is lost.

## 0.4.35 - 2026-09-03

- Add the Hub MCP-only `codex_cloud_collaboration` control plane for persistent,
  bounded coordination of ordinary visible ChatGPT Cloud CHAT conversations;
  require two existing local Codex sessions plus local ChatGPT readiness, use
  callback-delivered local artifacts or Result Pool metadata, and archive by
  default without falling back to another AI provider.
- Add a durable Cloud Result Pool with bounded, paginated result pages and
  idempotent generation-isolated storage for coordinator follow-up reads.
- Include only a `result_id` and bounded manifest metadata in Cloud completion
  callbacks, while preserving concurrent callback delivery and recovery.
- Derive Cloud session terminal status from provider async/terminal facts instead
  of treating any observed assistant message as completed.
- Keep MCP result access manifest-only for controller-facing flows; allow Direct
  API `pages.read` only within the owning result scope and machine isolation.
- Persist result metadata and callback recovery state without copying Cloud
  conversation bodies into coordinator envelopes.

## 0.4.34 - 2026-09-02

- Add durable ChatGPT Cloud completion callbacks to a dedicated local Codex
  coordinator, with register, unregister and reconciliation actions.
- Persist callback registrations and pending batches across reconnects and Node
  restarts, with generation fencing, bounded event deduplication and at-least-once
  delivery.
- Queue concurrent Worker completions while the coordinator is active, retry when
  its Turn ends, and retain a low-frequency heartbeat only for recovery.
- Advance `agent.control` to 1.3 and document the callback-first collaboration
  contract across Node, Hub, MCP and Direct API surfaces.

## 0.4.33 - 2026-09-02

- Add Node-local defaults for ChatGPT Cloud creation mode, model and reasoning
  effort, configurable from the local client without restarting the Node.
- Preserve explicit per-request selections, including Auto, and keep follow-up
  messages inheriting the original conversation model and reasoning effort.
- Populate default model and reasoning choices from the live ChatGPT catalog
  together with the user-maintained Advanced model list.

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
