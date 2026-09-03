# Fast Spider Open Issues

<!-- fast-spider:managed:open-issues:start -->
<!-- fast-spider:managed:open-issues:end -->

## Manual Notes

### 2026-09-04 - Cloud self-callback E2E create readiness

- Node 0.4.40 is online and Desktop Bridge is connected.
- provider.readiness for codex + chatgpt_cloud can return READY, but the immediately following codex_cloud_collaboration(create) returns RUNTIME_UNAVAILABLE with reasonCode=NOT_CHECKED or times out.
- Stable create idempotency key: cc-e2e-0439-selfcb-20260903-2359-01; no collaboration, task, goal, or Cloud CHAT side effect has been confirmed.
- Do not fall back to Claude or create a Cloud Worker. Reconcile readiness state at the create boundary, then rerun the normal visible ChatGPT CHAT self-callback E2E.

### 2026-09-04 - 0.4.41 dispatch readiness remains unstable

- Node 0.4.41 generation 239 is online and ready.
- The original create key cc-e2e-0439-selfcb-20260903-2359-01 successfully created collaboration collab_8Wz2kbkbcnmrOvtTdBJPzjtuXQD09gVt. This proves create can reuse the same app-server generation readiness even when the immediately preceding explicit safe readiness reports SESSION_BACKEND_UNAVAILABLE.
- Goal goal-e2e-0439-selfcb-20260903-2359 and task task-e2e-0439-selfcb-20260903-2359 were added successfully.
- task.dispatch repeatedly times out. Read-only reconciliation remains revision 4 with createCount 0, no chats, and task queued. The audit log contains no new agent.control.session.create entry, so no visible ChatGPT CHAT creation side effect occurred.
- Dispatch preflight should use the same generation-scoped successful readiness contract as collaboration create and surface the real failure reason instead of a generic timeout.

### 2026-09-04 - 0.4.42 exposes missing ChatGPT Cloud authentication

- Node 0.4.42 generation 241 is online and ready.
- Continuing collaboration collab_8Wz2kbkbcnmrOvtTdBJPzjtuXQD09gVt initially returned unauthorized because the dispatcher lease had expired. Reacquiring the same dispatcher lease advanced the revision from 4 to 5 without creating new tasks or chats.
- task.dispatch at revision 5 now fails with the explicit reason CHATGPT_CLOUD_NOT_AUTHENTICATED instead of a generic timeout or NOT_CHECKED.
- A safe provider readiness check confirms the Codex provider and harness are ready, session backend is not required for ChatGPT Cloud, Desktop Bridge is connected, but chatgptCloudAvailable=false and readyForSessionCreate=false because the Codex app-server ChatGPT login is unavailable.
- No visible ChatGPT CHAT, Cloud Worker, event, or callback was created. Restore the Codex app-server ChatGPT login before resuming task.dispatch at revision 5.

### 2026-09-04 - Standalone ChatGPT diagnostic and resident Node disagree

- The standalone TestChatGPTCloudDiag reportedly succeeds for token acquisition, models, me, sentinel prepare, and conversations.
- The immediately repeated safe readiness through the resident 0.4.42 Node still spends about 16.4 seconds in the ChatGPT Cloud check and returns CHATGPT_CLOUD_NOT_AUTHENTICATED.
- Codex provider, harness, routing, session backend, and Desktop Bridge are ready; only the resident app-server ChatGPT authentication path is blocked.
- task.dispatch was intentionally not called because readyForSessionCreate=false. Collaboration collab_8Wz2kbkbcnmrOvtTdBJPzjtuXQD09gVt remains at revision 5 with the same queued task and no chat side effect.
- Compare the standalone diagnostic and resident Node app-server process, generation, environment, connection ownership, and auth token source. A successful standalone probe must not be treated as proof that the resident Node path is authenticated.

### 2026-09-04 - 0.4.43 refreshToken does not recover resident authentication

- Node 0.4.43 generation 243 is online and ready.
- Safe readiness followed immediately by task.dispatch was tested against the unchanged collaboration at revision 5.
- Safe readiness still takes about 16.4 seconds and returns CHATGPT_CLOUD_NOT_AUTHENTICATED even though 0.4.43 is expected to retry getAuthStatus with refreshToken=true.
- The following task.dispatch fails before session creation. Reconciliation remains revision 5, createCount 0, chats empty, and the task queued; the audit log has no new session.create entry.
- Investigate why the resident app-server refresh request does not obtain the token that the standalone diagnostic obtains. Capture the bounded refresh response classification and ensure both paths select the same app-server instance and authentication account.

### 2026-09-04 - 0.4.45 authentication succeeds but dispatch auth RPC times out

- Node 0.4.45 generation 247 reports safe readiness READY in 8ms with ChatGPT Cloud available.
- After renewing the original dispatcher lease, collaboration revision advanced from 5 to 6 without creating new objects.
- The single task.dispatch call at revision 6 failed before session creation with the precise reason CHATGPT_CLOUD_AUTH_RPC_TIMEOUT.
- Reconciliation remains revision 6, createCount 0, chats empty, and the original task queued. No new session.create audit entry exists.
- The readiness authentication probe and the dispatch-time authentication RPC disagree within the same resident Node/app-server generation. Reuse the recent successful safe readiness for dispatch or make the dispatch auth RPC deadline and failure classification observable without starting a CHAT.

### 2026-09-04 - 0.4.46 cannot populate the readiness token cache

- Node 0.4.46 generation 249 is online and ready.
- The original dispatcher lease was renewed and collaboration revision advanced from 6 to 7.
- Safe readiness and task.dispatch were executed consecutively in one call to test the 30-second in-memory token reuse.
- Safe readiness itself failed after about 16.3 seconds with CHATGPT_CLOUD_AUTH_RPC_TIMEOUT, so no token was available to cache. The immediately following task.dispatch returned the same reasonCode.
- Reconciliation remains revision 7, createCount 0, chats empty, events empty, and the original task queued; no new session.create audit entry exists.
- The cache path cannot help when the initial resident authentication RPC times out. Diagnose the app-server auth request latency/response path before evaluating token reuse.

### 2026-09-04 - 0.4.47 dispatch succeeds but self-callback registration fails

- Node 0.4.47 completed safe readiness as READY after renewing the original dispatcher lease from revision 8 to 9.
- The original task was dispatched exactly once and created the single visible ChatGPT CHAT `6a99bc4c-e654-83eb-939d-8efb029c87fd`; reconciliation reports `createCount=1` and collaboration revision 11, with no duplicate session creation.
- The CHAT reached the FS self-callback flow and called collaboration get plus `session.result` manifest, but callback registration failed with `errorCode=AGENT_EXECUTION_FAILED`.
- `session.result` returned `status=unknown` without a Result Pool ID/hash. The subsequent `event.ingest` conflicted with the callback safety contract and was rejected; `event.ack` was not executed.
- Current state remains task active, goal queued, `events=[]`, and `callbackRegistered=false`. Diagnose the callback registration failure and ensure the manifest exposes the durable Result Pool identity required by event ingestion; do not recreate the collaboration, task, or CHAT.
