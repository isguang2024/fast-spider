# CHAT private split: dependency audit and public host contract

Status: migration contract for the public `fast-spider` repository. This stage introduces the cross-module seam only; it intentionally does **not** delete the current CHAT or Native Runner implementation.

## Boundary decision

The public repository remains the Node host and generic capability authority. A private module may add specialized Agent routing plus UI/MCP surfaces, but it must import only public packages (`hostapi`, `nodehost`) and must not import any `fast-spider/internal/...` package. The public repository must not import, require, fetch, or name the private CHAT module.

The public host keeps ownership of machine identity, Hub connection, FS/search/shell/build/git/browser capabilities, JobManager/process lifecycle, result publication transport, Local Bridge, local MCP transport, Node UI transport/security guard, updates, operation logs, and the built-in local Codex/Claude providers. An external module can wrap `nodehost.NewDefaultAgent` and intercept only specialized actions/parameters, delegating everything else to the built-in controller.

`hostapi.HostBindings` is intentionally capability-shaped rather than implementation-shaped: `CloudResultPublisher`, `NativeRunnerJobExecutor`, `MachineID`, and `CapabilityCaller`. This prevents a second Job ledger, a second FS engine, or a private copy of Node credentials. `hostapi.UISurface` and `hostapi.MCPSurface` register into the existing local servers. `nodehost` is the supported public composition package for headless Node runtime, default Agent, local UI, and local MCP.

## Keep / move / bridge audit

| Area | Current public implementation | Disposition | Migration rule |
| --- | --- | --- | --- |
| Node identity, Hub transport, device connection | `internal/node`, `cmd/node` | **keep** | Public Node remains lifecycle/credential owner. Private code receives no credential object. |
| FS/read/write/search, shell/build/git, browser/screenshot, working context | `internal/node/*` capability handlers | **keep** | Private code calls through `hostapi.CapabilityCaller` / `nodehost.Runtime.CallCapability`; never imports protocol or Node internals. |
| Job process/idempotency ledger | `internal/node/jobs*` | **keep** | `hostapi.NativeRunnerJobExecutor` is the bridge. One public JobManager remains authoritative. |
| Result publication transport | Node/Hub Result Pool client | **keep + bridge** | `hostapi.CloudResultPublisher` exposes only publication. Authentication/transport stay public. |
| Local Codex / Claude providers and generic routing | `internal/agent/codex_*`, `claude_*`, provider registry/routing | **keep** | `nodehost.NewDefaultAgent` preserves current public behavior; specialized composite delegates non-private routes. |
| ChatGPT Cloud transport, realtime/Sentinel/read budget/progress | `internal/agent/chatgpt_cloud_*` | **move** | Move implementation to private module in a later stage; current public copy remains until parity is proven. |
| CHAT advanced model/config surface | `internal/agent/chatgpt_advanced_config.go`, `internal/nodeui/chatgpt_advanced.go` and related UI | **move** | Private composition owns CHAT catalog/defaults and registers UI/MCP surfaces through public contracts. |
| Native project runner/planner/review/recovery | `internal/agent/native_runner_*` | **move** | Move as one stateful subsystem; do not duplicate its SQLite ledger. Checks continue to execute through the public Job bridge. |
| Session callback code | `internal/agent/session_callback_*` | **split / bridge** | Local-provider-generic callback semantics may remain public; CHAT/runner generation, cloud recovery and transport-specific branches move. Preserve generation/claim/ACK semantics exactly during split. |
| Session create idempotency / visibility | `session_create_store.go`, `session_visibility.go` | **split carefully** | Visibility is generic and stays public. CHAT create records must retain existing spec-hash/replay/in-doubt semantics when private ownership moves. |
| Hub Cloud collaboration orchestration | `internal/hub/core/cloud_collaboration.go`, `cloud_completion.go` and dedicated tests | **move** | Private service/module owns CHAT collaboration policy; public Hub result/Node primitives may remain generic. |
| Hub Cloud completion/recovery notification policy | Hub core/store migrations 015-018 | **move policy, preserve data** | Existing persisted rows are migration input, not disposable cache. Schema/data compatibility is mandatory before removal from public code. |
| Local Node UI server/security | `internal/nodeui/app.go` | **keep + bridge** | Keep one server and its token/origin guard. Private routes register with `hostapi.UISurface`; API routes should use `UISurfaceContext.APIOnly`. |
| Task center / CHAT-specific Node UI | `internal/nodeui/tasks*`, CHAT advanced routes/markup | **move** | Re-register from private module after behavior parity; do not create a second local UI listener. |
| Generic Local MCP transport | `internal/localmcp` | **keep + bridge** | Keep `local_machine` / `local_capability`; private tools register via `hostapi.MCPSurface` and receive the existing capability caller. |
| Hub MCP CHAT collaboration tools/guides | CHAT-specific branches in `internal/hub/server/mcp*` | **move** | Private surface owns CHAT tool schema/presentation; generic MCP remains public. |
| Public docs/tests for generic Node | existing generic docs/tests | **keep** | Continue testing local Codex/Claude, FS/Job, Node lifecycle and security. |
| CHAT/Runner implementation docs/tests | CHAT Cloud / Native Runner-specific docs and tests | **move with implementation** | Keep public compatibility tests for the seam; move implementation tests with their owner. |
| Cross-module seam tests | `nodehost`, `hostapi`, UI/MCP extension tests | **keep** | These are the long-term public compatibility contract. |

## Persisted state that cannot silently change

| State | Current path/table | Required invariant |
| --- | --- | --- |
| CHAT SSE progress/receipts | `<dataDir>/agent/cloud-progress.sqlite3` | Move with CHAT transport. Event sequence, receipt uniqueness, bounded/redacted frames and reconnect recovery remain compatible. |
| CHAT advanced model config | `<dataDir>/chatgpt-advanced-models.json` | Becomes private-owned CHAT configuration. A private default may be layered only here/composition-side, never in public generic defaults. |
| Session callback registrations/events | `<dataDir>/agent/session-callbacks.json` | Generation, source/target session identity, pending delivery, claim and ACK state are durable; no reset on split. |
| Session-create idempotency | `<dataDir>/agent/session-create-idempotency.json` | Preserve request/spec hashes, `in_doubt` recovery, replay and conflict behavior. Never retry an uncertain external create under a new key. |
| Session visibility | `<dataDir>/agent/session-visibility.json` | Generic directory/visibility mapping remains public and compatible. |
| Claude local session index | `<dataDir>/agent/claude-code-sessions.json` | Public/generic and unchanged. |
| Native Runner project/task/evidence ledger | `<dataDir>/native-runner/projects.sqlite3` | Move as one authoritative ledger. Project/task/round, revisions, checks, history and evidence identities must not be regenerated. |
| Native Runner task packet | `<dataDir>/native-runner/<hash>-r<round>.packet.json` | Immutable authorization/execution package for the assigned round; reports referenced inside are evidence only. |
| Native Runner result | `<dataDir>/native-runner/<hash>-r<round>.result` | Result path is bound into the dispatch; submit must refer to the same immutable taskRef/round. |
| Hub collaboration rows | `cloud_collaborations` | Owner + idempotency key uniqueness, request hash, revision and collaboration state are durable. |
| Hub completion/recovery rows | `cloud_completion_notifications` | `(owner, collaboration, task, generation, notification_kind)` uniqueness plus `pending -> claimed -> acked` delivery state is durable. Recovery receipts remain distinct from immutable completion results. |

## Identity, idempotency, ACK and recovery invariants

1. Native Runner identity is `(projectId, taskId, round)` and the transport derives one immutable `taskRef` from that assignment. `runner.context`, `runner.checkpoint` and `runner.submit` must resolve the active binding from that taskRef instead of accepting a caller-invented project/session identity.
2. The Runner dispatch result path and packet path are round-bound. A later round gets different files; an old generation cannot checkpoint or submit the new assignment.
3. CHAT `session.create` requires a safe 12-128 character idempotency key. The persisted spec hash protects semantic equality; `in_doubt` creation is reconciled by the original request message identity before retry.
4. Public Job execution remains Node-owned. The private runner passes a bounded `NativeRunnerJobSpec`; JobManager remains the only process/idempotency ledger and returns a bounded snapshot/evidence reference.
5. Callback delivery remains at-least-once until ACK. Claim identity and generation are validated before ACK; reconnect/recovery observations must not be promoted into a different immutable completion result.
6. CHAT realtime/SSE recovery uses persisted sequence/receipt state. Healthy realtime operation should not be replaced by aggressive status polling; reconnect may request a bounded recovery pass for potentially missed terminal events.
7. Result publication credentials never enter private Runner prompts or callback payloads. Private code receives only the `CloudResultPublisher` operation.

## CHAT model default ownership

Audit result: the literal `gpt-6-astra-wm` does **not** exist in the current public repository. The current public `AgentManager` initializes CHAT create defaults as `configurationMode=auto`, `mode=complete`, with model/thinking empty. For `backend=chatgpt_cloud`, an explicitly configured model is used; `quick_chat` falls back to `auto` when model is empty, while complete mode passes the selected (possibly empty/provider-default) model through the Cloud request path. Node UI may persist a user-selected CHAT default, but there is no public hard-coded `gpt-6-astra-wm` source.

Therefore `gpt-6-astra-wm` must be introduced, if desired, only by the private composition/configuration layer after the split. The public `hostapi`, `nodehost`, `go.mod`, generic Agent defaults and generic UI must not contain that private CHAT default. This keeps the public Node provider-neutral and prevents a private product default from leaking back into the public repository.

## Public composition API added in this stage

- `hostapi.AgentController` / `CapabilityError`: stable cross-module Agent boundary.
- `hostapi.AgentHostBinder` + `HostBindings`: typed Node-owned result publication, Job, machine identity and generic capability hooks.
- `hostapi.NativeRunnerJobExecutor`: reuse public JobManager without importing `internal/node`.
- `hostapi.UISurface`: register specialized routes into the existing Node UI and reuse `APIOnly` security.
- `hostapi.MCPSurface`: register specialized local MCP tools into the existing transport and call generic Node capabilities.
- `nodehost.NewDefaultAgent`: public factory for the generic built-in controller so a private composite can delegate Codex/Claude and other public routes.
- `nodehost.NewRuntime`: public headless Node composition/lifecycle entrypoint with injected Agent.
- `nodehost.NewUI`: public UI composition entrypoint with injected Agent and surfaces.
- `nodehost.RunLocalMCP`: public local MCP composition entrypoint with specialized surfaces.

The existing `cmd/node` assembly path is intentionally unchanged in this stage. With no external Agent/surfaces, public Node behavior remains the existing built-in behavior.

## Later removal gate

Do not delete any current CHAT/Runner public implementation until a private module build proves all of the following against migrated real state: cross-module compilation without `internal` imports; the same taskRef/idempotency/ACK/recovery semantics; local Codex/Claude delegation; FS/Job execution through public host bindings; UI/API guard parity; MCP schema parity; CHAT SSE reconnect/terminal recovery; Native Runner restart/recovery; and migration of every persisted path/table above. Only then can implementation files and their specialized tests/docs be removed from the public repository.
