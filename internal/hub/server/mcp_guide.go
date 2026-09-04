package server

import (
	"fmt"
	"sort"
	"strings"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpGuideVersion = "1.4"

type mcpGuideCategory struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Tools   []string `json:"tools"`
}

// mcpToolSummary is the compact, first-pass description returned by the
// capability overview. Keep this intentionally smaller than the full tool
// input schema so clients can understand the whole FS surface before loading
// one detailed guide.
type mcpToolSummary struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Guide    string `json:"guide"`
}

// mcpCapabilitySummary describes the lower-level Node capability catalog. It
// complements the wire descriptor (capabilityId/version/actions) without
// changing the Node protocol or making the model infer semantics from IDs.
type mcpCapabilitySummary struct {
	CapabilityID string   `json:"capabilityId"`
	Version      string   `json:"version"`
	Actions      []string `json:"actions"`
	Summary      string   `json:"summary"`
	MCPTools     []string `json:"mcpTools,omitempty"`
}

type mcpGuide struct {
	GuideVersion    string                `json:"guideVersion"`
	ServerVersion   string                `json:"serverVersion"`
	View            string                `json:"view"`
	Name            string                `json:"name"`
	Summary         string                `json:"summary"`
	Categories      []mcpGuideCategory    `json:"categories,omitempty"`
	ToolSummaries   []mcpToolSummary      `json:"toolSummaries,omitempty"`
	Capability      *mcpCapabilitySummary `json:"capability,omitempty"`
	GoldenRules     []string              `json:"goldenRules,omitempty"`
	WhenToUse       []string              `json:"whenToUse,omitempty"`
	RequiredInputs  []string              `json:"requiredInputs,omitempty"`
	SafeSequence    []string              `json:"safeSequence,omitempty"`
	Returns         []string              `json:"returns,omitempty"`
	RecommendedNext []string              `json:"recommendedNext,omitempty"`
	CommonErrors    []string              `json:"commonErrors,omitempty"`
	BoundedExamples []map[string]any      `json:"boundedExamples,omitempty"`
}

type mcpToolGuideEntry struct {
	Description     string
	WhenToUse       []string
	RequiredInputs  []string
	SafeSequence    []string
	Returns         []string
	RecommendedNext []string
	CommonErrors    []string
	BoundedExamples []map[string]any
}

type mcpWorkflowGuideEntry struct {
	Summary         string
	RequiredInputs  []string
	SafeSequence    []string
	Returns         []string
	RecommendedNext []string
	CommonErrors    []string
	BoundedExamples []map[string]any
}

type mcpErrorGuideEntry struct {
	Summary         string
	WhenToUse       []string
	SafeSequence    []string
	RecommendedNext []string
}

var mcpToolGuides = map[string]mcpToolGuideEntry{
	"machine_list": {
		Description: "Discover machineId or verify FastSpider_FS connectivity (fsprobe). Stable pages default to 20, max 50; use nextCursor and includeCapabilities=true only when needed.",
		WhenToUse:   []string{"Discover a machineId", "Check whether a Node is online and ready"}, RequiredInputs: []string{"none; optional limit, cursor and includeCapabilities"},
		SafeSequence: []string{"Call machine_list", "Follow nextCursor while hasMore", "Choose an online machine", "Use machine_get only when detailed machine facts are needed"},
		Returns:      []string{"Stable owned-machine page, hasMore/nextCursor and optional negotiated capabilities"}, RecommendedNext: []string{"machine_get", "the selected machine-bound tool"},
		CommonErrors: []string{"INVALID_REQUEST", "INTERNAL"}, BoundedExamples: []map[string]any{{"limit": 20}, {"limit": 20, "cursor": 20}, {"limit": 20, "includeCapabilities": true}},
	},
	"machine_get": {
		Description: "Inspect one known machine after machine_list. Requires machineId; returns detailed status and negotiated capabilities without secrets.",
		WhenToUse:   []string{"Inspect one known machine"}, RequiredInputs: []string{"machineId from machine_list"}, SafeSequence: []string{"machine_list", "machine_get"},
		Returns: []string{"One machine and its negotiated capabilities"}, RecommendedNext: []string{"capability_list with machineId", "a machine-bound tool"}, CommonErrors: []string{"NOT_FOUND"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>"}},
	},
	"audit_log": {
		Description:    "Read recent owner-scoped Hub mutation audits without an online Node. Supports machineId, actionPrefix, result, before and limit filters.",
		WhenToUse:      []string{"Inspect Fast Spider operations", "Confirm whether a mutation was attempted or rejected", "Review recent owner or machine activity"},
		RequiredInputs: []string{"none; current MCP owner is always enforced"},
		SafeSequence:   []string{"Call audit_log with a small limit", "Narrow with machineId/actionPrefix/result when needed", "Use before for older time windows"},
		Returns:        []string{"Owner-scoped audit IDs, actors, actions, results, metadata and timestamps"}, RecommendedNext: []string{"machine_get when a machine needs inspection"}, CommonErrors: []string{"INVALID_REQUEST", "INTERNAL"},
		BoundedExamples: []map[string]any{{"limit": 20}, {"machineId": "<machine-id>", "actionPrefix": "git.repository.", "result": "success", "limit": 50}},
	},
	"operation_log": {
		Description:    "Read bounded events from one owned Node. Supports level/category filters and before paging; omits paths, messages, IPs and extra fields.",
		WhenToUse:      []string{"Check whether a Node was recently used", "Inspect recent capability or local UI activity"},
		RequiredInputs: []string{"machineId", "limit up to 200; before is the returned nextCursor for older entries"},
		SafeSequence:   []string{"Call machine_list", "Call operation_log with a small limit", "Use nextCursor only when hasMore is true"},
		Returns:        []string{"Recent owner-authorized Node operation IDs, timestamps, levels, categories, actions and bounded timing/status facts"}, RecommendedNext: []string{"machine_get", "machine_list"}, CommonErrors: []string{"MACHINE_OFFLINE", "OPERATION_LOG_UNAVAILABLE", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "limit": 20}, {"machineId": "<machine-id>", "category": "capability", "limit": 50}},
	},
	"capability_list": {
		Description:    "FastSpider_FS health, capability catalog and on-demand guides. Optional machineId; view selects overview, catalog, capability, tool, workflow or error.",
		WhenToUse:      []string{"Check MCP connectivity", "Read the Hub or Machine catalog", "Load one detailed guide only when needed"},
		RequiredInputs: []string{"view=capability|tool|workflow|error requires name", "machineId is optional for catalog"},
		SafeSequence:   []string{"Start with overview or the default call", "Read only the needed capability/tool/workflow/error guide", "Do not fetch every guide"},
		Returns:        []string{"capabilities plus an optional bounded guide"}, RecommendedNext: []string{"machine_list", "the guide's recommendedNext"}, CommonErrors: []string{"INVALID_REQUEST", "NOT_FOUND"},
		BoundedExamples: []map[string]any{{"view": "overview"}, {"view": "capability", "name": "shell.exec"}, {"view": "workflow", "name": "connection-check"}},
	},
	"file_read": {
		Description: "Read bounded UTF-8 content or file metadata/SHA. Requires machineId and absolute path; returns fileSha256 for edit/verification CAS.",
		WhenToUse:   []string{"Inspect source or configuration", "Obtain fileSha256 before editing"}, RequiredInputs: []string{"machineId", "absolute path", "at most one selector"},
		SafeSequence: []string{"code_search when location is unknown", "file_read", "retain fileSha256 for CAS"}, Returns: []string{"bounded content/stat, hashes and truncation facts"},
		RecommendedNext: []string{"file_edit preview", "file_read verification"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "NOT_FOUND", "NOT_TEXT"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "path": "<absolute-file>", "headLines": 80}},
	},
	"file_edit": {
		Description: "Preview, create or atomically edit bounded UTF-8 files. Requires machineId, absolute path and CAS inputs; verify with file_read.",
		WhenToUse:   []string{"Preview or apply a precise file change"}, RequiredInputs: []string{"machineId", "absolute path", "expectedFileSha256 for existing files", "expectedAbsent=true for create"},
		SafeSequence: []string{"code_search", "file_read and capture fileSha256", "file_edit preview", "file_edit with expectedFileSha256", "file_read verification"},
		Returns:      []string{"success/change metadata, old/new SHA and bounded preview diff"}, RecommendedNext: []string{"file_read"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "CONFLICT", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "preview", "previewOf": "replace", "path": "<absolute-file>", "oldText": "old", "newText": "new", "expectedFileSha256": "<sha256>"}},
	},
	"code_search": {
		Description: "Locate code below an absolute directory. Requires machineId, query and path; returns bounded matches and search-engine facts.",
		WhenToUse:   []string{"Find relevant files or text before reading/editing"}, RequiredInputs: []string{"machineId", "query", "absolute directory path"}, SafeSequence: []string{"Use narrow include globs when known", "code_search", "file_read exact matches"},
		Returns: []string{"bounded matches/files, scan statistics and fallback reason"}, RecommendedNext: []string{"file_read"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "DEADLINE_EXCEEDED", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "query": "symbol", "path": "<absolute-project>", "include": []string{"internal/**/*.go"}, "limit": 20}},
	},
	"shell_run": {
		Description: "Start a bounded host/WSL process with explicit argv. On Windows invoke powershell.exe in argv; PowerShell is not a separate FS tool. Returns jobId for job_watch.",
		WhenToUse:   []string{"Run a command that is not a dedicated Git or build action"}, RequiredInputs: []string{"machineId", "argv", "absolute cwd", "12-128 character idempotencyKey"},
		SafeSequence: []string{"shell_run", "capture jobId", "job_watch until completed, failed or canceled"}, Returns: []string{"started Job metadata and jobId"}, RecommendedNext: []string{"job_watch", "job_cancel if needed"},
		CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE"}, BoundedExamples: []map[string]any{
			{"machineId": "<machine-id>", "argv": []string{"go", "version"}, "cwd": "<absolute-project>", "idempotencyKey": "<unique-key>"},
			{"machineId": "<machine-id>", "argv": []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Date; tzutil /g"}, "cwd": "C:\\", "idempotencyKey": "<unique-key>"},
		},
	},
	"job_watch": {
		Description: "Read bounded events and terminal state for jobId; continue until completed, failed or canceled.",
		WhenToUse:   []string{"Observe a started Job", "Confirm actual completion"}, RequiredInputs: []string{"machineId", "jobId"}, SafeSequence: []string{"Pass the last nextCursor", "Long-poll up to 15 seconds", "Stop only at a terminal state"},
		Returns: []string{"bounded events, nextCursor, state, exitCode and timing"}, RecommendedNext: []string{"job_watch again", "artifact_get uploadJobLog", "job_cancel"}, CommonErrors: []string{"JOB_NOT_FOUND", "CONNECTION_LOST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "jobId": "<job-id>", "cursor": 0, "waitSeconds": 15}},
	},
	"job_cancel": {
		Description: "Cancel an active Job process tree; confirm terminal state with job_watch.",
		WhenToUse:   []string{"Stop an active shell/build Job"}, RequiredInputs: []string{"machineId", "jobId"}, SafeSequence: []string{"job_cancel", "job_watch until canceled or another terminal state"},
		Returns: []string{"Job state after cancellation request"}, RecommendedNext: []string{"job_watch"}, CommonErrors: []string{"JOB_NOT_FOUND", "CONNECTION_LOST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "jobId": "<job-id>"}},
	},
	"git_control": {
		Description: "Run allowlisted Git reads, writes and network actions. Requires machineId, repositoryPath and action; ambiguous remotes fail closed.",
		WhenToUse:   []string{"Inspect or change a Git repository"}, RequiredInputs: []string{"machineId", "absolute repositoryPath", "action", "idempotencyKey for network actions; remote only when inference is ambiguous or an explicit remote is intended"},
		SafeSequence: []string{"status", "diff", "obtain authorization for writes/network", "add/commit/push", "status"}, Returns: []string{"allowlisted Git action result"}, RecommendedNext: []string{"git_control status", "job_watch when a jobId is returned"},
		CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "CONNECTION_LOST", "INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "repositoryPath": "<absolute-project>", "action": "status"}},
	},
	"build_control": {
		Description: "Start a bounded build/test argv; returns jobId for job_watch. The caller owns and removes any temporary test/build directory or compiled test binary it creates.",
		WhenToUse:   []string{"Run a build, test or lint command"}, RequiredInputs: []string{"machineId", "action=run", "argv", "absolute cwd", "idempotencyKey"}, SafeSequence: []string{"create a uniquely named temporary path only when the command needs one", "build_control", "capture jobId", "job_watch to a terminal state", "remove only the caller-created temporary path on success/failure/cancel"},
		Returns: []string{"started Job envelope"}, RecommendedNext: []string{"job_watch"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "run", "argv": []string{"go", "test", "./..."}, "cwd": "<absolute-project>", "idempotencyKey": "<unique-key>"}},
	},
	"browser_control": {
		Description: "Automate isolated Chromium via readiness/session/page/snapshot refs; screenshots return temporary URL metadata. Each verification owns one session, and close removes its managed session directory.",
		WhenToUse:   []string{"Automate or inspect a real web page"}, RequiredInputs: []string{"machineId", "action", "session/page IDs after launch/open"}, SafeSequence: []string{"readiness", "launch one session for this verification", "register close in a finally/defer path immediately after launch", "page.open", "snapshot", "click/type/batch using refs", "optional screenshot", "close even after failure or cancellation"},
		Returns: []string{"browser readiness, IDs, accessibility snapshots/refs, events, or a 48-hour screenshot URL"}, RecommendedNext: []string{"snapshot", "close"}, CommonErrors: []string{"BROWSER_REF_STALE", "RUNTIME_UNAVAILABLE", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "readiness"}},
	},
	"screenshot_take": {
		Description: "Capture desktop/display/window evidence as temporary URL metadata, not embedded chat images.",
		WhenToUse:   []string{"Capture visual desktop evidence"}, RequiredInputs: []string{"machineId", "action", "displayIndex or windowId when applicable"}, SafeSequence: []string{"list displays/windows when needed", "capture once", "use the returned URL"},
		Returns: []string{"url, fileName, contentType, sizeBytes, expiresAt; maximum lifetime 48 hours"}, RecommendedNext: []string{"use the returned URL directly"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "desktop", "format": "png"}},
	},
	"thinking_team": {
		Description: "Return calling-side role/department/collaboration guidance; use working_context for durable facts.",
		WhenToUse:   []string{"Get structured multi-perspective thinking guidance"}, RequiredInputs: []string{"action and optional role/department/workflow name"}, SafeSequence: []string{"Read only the needed view", "Apply it in the calling model", "Do not treat it as local AI execution"},
		Returns: []string{"roles, departments, workflows or workspace protocol"}, RecommendedNext: []string{"working_context", "continue reasoning in the caller"}, CommonErrors: []string{"INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"action": "overview"}},
	},
	"ai_control": {
		Description:    "Direct Codex/Claude/ChatGPT cloud CHAT session discovery and lifecycle control. For a CHAT task that must return later by callback, use codex_cloud_collaboration action=dispatch instead.",
		WhenToUse:      []string{"Discover AI runtimes and models", "Create, inspect, continue, cancel, archive or otherwise control a session interactively", "Read the internal callback queue only during explicit recovery"},
		RequiredInputs: []string{"machineId and action", "workingDirectory for scoped list/create", "an exact sessionId for get/send/watch/result or lifecycle actions"},
		SafeSequence: []string{
			"Call machine_list when machineId is unknown",
			"Use provider.readiness and models.list before creating a Cloud CHAT",
			"Use an exact user-supplied Codex or ChatGPT sessionId regardless of who created it; do not list or guess old sessions unless the user asks",
			"Direct interactive work uses ai_control session.create/send/get/watch/result",
			"Asynchronous delegation uses codex_cloud_collaboration action=dispatch and then ends the current turn instead of polling",
			"Do not call session.callback.register, session.callback.arm or session.callback.enqueue directly; callback routing, active delivery, acknowledgement and recovery are internal collaboration transport",
			"Keep durable goals and progress in the caller's own compact working_context text when needed",
		},
		Returns:         []string{"bounded provider, model and session facts", "direct lifecycle results", "internal callback queue metadata only for recovery"},
		RecommendedNext: []string{"codex_cloud_collaboration for asynchronous delegation", "session.get/watch/result only for direct interactive sessions"},
		CommonErrors:    []string{"RUNTIME_UNAVAILABLE", "INVALID_REQUEST", "UNSUPPORTED_SESSION_PLUGIN_BINDING", "CALLBACK_ROUTE_MANAGED_ONLY", "AGENT_SESSION_BUSY"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "session.list", "workingDirectory": "<absolute-project>"}, {"machineId": "<machine-id>", "action": "provider.readiness", "providerId": "codex", "backend": "chatgpt_cloud", "mode": "safe"}},
	},
	"codex_cloud_collaboration": {
		Description: "Send one task to a visible ChatGPT Cloud CHAT and return its durable callback to one local Codex session. Controller/coordinator topology stays outside Fast Spider.",
		WhenToUse:   []string{"A local Codex session wants one Cloud CHAT to do work and report back later", "Reuse one exact visible CHAT", "Create one clean visible CHAT for a new task"}, RequiredInputs: []string{"dispatch: machineId, callbackSessionId, workingDirectory, prompt and idempotencyKey", "optional targetSessionId to reuse one exact CHAT", "optional read_only accessMode; otherwise the task may edit and test inside workingDirectory", "Cloud CHAT completion uses the same tool with action=completion.notify"},
		SafeSequence: []string{
			"Call action=dispatch once; Fast Spider validates the callback session, creates or reuses exactly one CHAT, registers its callback and sends the prompt",
			"The default accessMode is write with writeScope=workingDirectory and callbackType=text; override accessMode=read_only when no edits are needed",
			"After dispatch returns callerShouldYield=true and nextAction=end_turn, end the current turn without polling",
			"The CHAT calls action=completion.notify before its final reply; Hub persists it, pushes it into the Node callback queue, and Node wakes callbackSessionId as soon as that Codex session is idle",
			"Provider realtime, startup reconciliation and timed status reads are recovery only; they are not the normal completion channel",
			"The callback always returns to callbackSessionId, regardless of whether the caller calls that session a controller, coordinator or single AI",
		}, Returns: []string{"one asynchronous dispatch receipt", "collaborationId and taskId", "durable callback to callbackSessionId"}, RecommendedNext: []string{"end the current turn after dispatch"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "INVALID_REQUEST", "CONFLICT", "AGENT_SESSION_BUSY", "CALLBACK_BASELINE_UNAVAILABLE"},
		BoundedExamples: []map[string]any{{"action": "dispatch", "params": map[string]any{"machineId": "<machine-id>", "callbackSessionId": "<local-codex-session>", "workingDirectory": "<absolute-project>", "prompt": "Implement and test the requested change.", "idempotencyKey": "cloud-task-unique-001"}}},
	},
	"working_context": {
		Description: "Store one revisioned plain-text project context. The AI chooses how to write goals, progress, blockers and next steps.",
		WhenToUse:   []string{"Remember a compact project goal or progress note across calls", "Share a bounded text handoff between one or more AIs"}, RequiredInputs: []string{"machineId", "absolute projectPath", "action=get|set|clear", "text for set"},
		SafeSequence: []string{"get the current text and revision", "update the text", "set with expectedRevision when avoiding concurrent overwrite"}, Returns: []string{"plain text state, revision and live Git facts"},
		RecommendedNext: []string{"continue the project work"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "CONFLICT", "INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "set", "projectPath": "<absolute-project>", "text": "# Goal\n...\n\n## Progress\n..."}},
	},
	"artifact_get": {
		Description: "Upload/read files or Job logs as bounded native MCP content; publishFile returns URL metadata expiring after 48 hours.",
		WhenToUse:   []string{"Display a local file or Job log", "Retrieve an existing Artifact", "Create a 48-hour temporary attachment URL"}, RequiredInputs: []string{"action", "artifactId for get", "machineId plus absolute path or terminal jobId for upload"},
		SafeSequence: []string{"uploadFile or uploadJobLog for native content", "get later by artifactId", "publishFile only when a temporary external URL is explicitly needed"}, Returns: []string{"Native Artifact content for upload/get, or URL/fileName/contentType/sizeBytes/expiresAt for publishFile"},
		RecommendedNext: []string{"artifact_get get for persistent Artifact re-read", "use the returned publishFile URL directly"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "JOB_NOT_FOUND", "NOT_FOUND"}, BoundedExamples: []map[string]any{{"action": "uploadJobLog", "machineId": "<machine-id>", "jobId": "<terminal-job-id>", "logicalName": "test.log"}, {"action": "publishFile", "machineId": "<machine-id>", "path": "<absolute-file>", "logicalName": "preview.png", "contentType": "image/png"}},
	},
	"result_get": {
		Description:     "Read an owner-scoped Result Pool manifest. MCP credentials are manifest-only; direct pages.read access is required for page content.",
		WhenToUse:       []string{"Inspect a completed result manifest"},
		RequiredInputs:  []string{"action=getManifest and resultId"},
		SafeSequence:    []string{"getManifest first", "use a separately authorized direct pages.read identity for page content"},
		Returns:         []string{"manifest metadata without page Artifact identifiers or正文"},
		RecommendedNext: []string{"getManifest", "direct result_get readPage with pages.read"}, CommonErrors: []string{"NOT_FOUND", "CONFLICT", "PAGES_READ_REQUIRED"},
		BoundedExamples: []map[string]any{{"action": "getManifest", "resultId": "<result-id>"}},
	},
}

var mcpToolSummaryDefinitions = []mcpToolSummary{
	{Name: "machine_list", Category: "connection", Summary: "Discover owned Machines, online state and machine IDs.", Guide: "capability_list(view=tool,name=machine_list)"},
	{Name: "machine_get", Category: "connection", Summary: "Inspect one known Machine and its negotiated capabilities.", Guide: "capability_list(view=tool,name=machine_get)"},
	{Name: "capability_list", Category: "connection", Summary: "Read the compact FS map, low-level catalog or one on-demand guide.", Guide: "capability_list(view=tool,name=capability_list)"},
	{Name: "audit_log", Category: "audit", Summary: "Read recent owner-scoped Hub mutation audit entries without requiring a Node online.", Guide: "capability_list(view=tool,name=audit_log)"},
	{Name: "operation_log", Category: "audit", Summary: "Read recent bounded operation events from one owned Node without exposing local paths or messages.", Guide: "capability_list(view=tool,name=operation_log)"},
	{Name: "code_search", Category: "files", Summary: "Find bounded text or matching files below an absolute directory.", Guide: "capability_list(view=tool,name=code_search)"},
	{Name: "file_read", Category: "files", Summary: "Read bounded UTF-8 content or file metadata and SHA-256.", Guide: "capability_list(view=tool,name=file_read)"},
	{Name: "file_edit", Category: "files", Summary: "Preview or apply precise CAS-protected file changes.", Guide: "capability_list(view=tool,name=file_edit)"},
	{Name: "shell_run", Category: "jobs", Summary: "Start an explicit-argv host/WSL Job; Windows argv can invoke PowerShell or cmd.exe.", Guide: "capability_list(view=tool,name=shell_run)"},
	{Name: "build_control", Category: "jobs", Summary: "Start a bounded build, test or lint Job with explicit argv.", Guide: "capability_list(view=tool,name=build_control)"},
	{Name: "job_watch", Category: "jobs", Summary: "Observe Job events until a terminal state is reached.", Guide: "capability_list(view=tool,name=job_watch)"},
	{Name: "job_cancel", Category: "jobs", Summary: "Cancel one active Job and its process tree.", Guide: "capability_list(view=tool,name=job_cancel)"},
	{Name: "git_control", Category: "git", Summary: "Run allowlisted Git inspection, mutation and network actions.", Guide: "capability_list(view=tool,name=git_control)"},
	{Name: "browser_control", Category: "browser", Summary: "Operate isolated Chromium through accessibility snapshots and refs.", Guide: "capability_list(view=tool,name=browser_control)"},
	{Name: "screenshot_take", Category: "browser", Summary: "Capture one-time desktop, display or window visual evidence.", Guide: "capability_list(view=tool,name=screenshot_take)"},
	{Name: "ai_control", Category: "ai", Summary: "Direct AI session discovery and lifecycle control; it is not the callback-based Cloud CHAT delegation entry.", Guide: "capability_list(view=tool,name=ai_control)"},
	{Name: "codex_cloud_collaboration", Category: "codex-cloud-collaboration", Summary: "Dispatch one CHAT task and return its durable callback to one local session.", Guide: "capability_list(view=tool,name=codex_cloud_collaboration)"},
	{Name: "working_context", Category: "context", Summary: "Persist one bounded plain-text project context.", Guide: "capability_list(view=tool,name=working_context)"},
	{Name: "thinking_team", Category: "guidance", Summary: "Return calling-side role, department and workflow guidance.", Guide: "capability_list(view=tool,name=thinking_team)"},
	{Name: "artifact_get", Category: "artifacts", Summary: "Upload/retrieve native MCP content or create a 48-hour URL-only temporary attachment.", Guide: "capability_list(view=tool,name=artifact_get)"},
	{Name: "result_get", Category: "results", Summary: "Read a Result Pool manifest or one explicitly requested page.", Guide: "capability_list(view=tool,name=result_get)"},
}

var mcpCapabilitySummaryByID = map[string]string{
	"machine.status":     "Report Node OS, runtime health and negotiated capability state.",
	"file.read":          "Read bounded UTF-8 files or return metadata and SHA-256 selectors.",
	"file.write":         "Create or edit files with CAS checks, atomic replacement and preview.",
	"code.search":        "Search bounded source content or matching files with stable scan facts.",
	"shell.exec":         "Run explicit argv on host or WSL; Windows interpreters include powershell.exe, pwsh.exe and cmd.exe.",
	"job.control":        "Watch Job events and cancel process trees with terminal-state accounting.",
	"git.repository":     "Perform fixed, allowlisted Git repository actions.",
	"build.exec":         "Run bounded host or WSL build/test commands as Jobs.",
	"artifact.store":     "Store, retrieve or explicitly publish bounded local files and Job logs.",
	"working.context":    "Maintain one revisioned plain-text project context.",
	"browser.automation": "Control isolated Chromium through readiness, pages, snapshots and refs.",
	"screenshot.capture": "Capture one-time desktop, display or window images.",
	"agent.control":      "Discover and control supported local AI Harnesses, sessions and the Windows Codex Desktop bridge state.",
	"operation.log":      "Read bounded recent Node operation events with local-sensitive fields omitted.",
}

var mcpCapabilityMCPToolsByID = map[string][]string{
	"machine.status":     {"machine_list", "machine_get"},
	"file.read":          {"file_read"},
	"file.write":         {"file_edit"},
	"code.search":        {"code_search"},
	"shell.exec":         {"shell_run"},
	"job.control":        {"job_watch", "job_cancel"},
	"git.repository":     {"git_control"},
	"build.exec":         {"build_control"},
	"artifact.store":     {"artifact_get"},
	"working.context":    {"working_context"},
	"browser.automation": {"browser_control"},
	"screenshot.capture": {"screenshot_take"},
	"agent.control":      {"ai_control"},
	"operation.log":      {"operation_log"},
}

func mcpToolSummaries() []mcpToolSummary {
	out := make([]mcpToolSummary, len(mcpToolSummaryDefinitions))
	copy(out, mcpToolSummaryDefinitions)
	return out
}

func mcpCapabilitySummaries(capabilities []protocolv1.CapabilityDescriptor) []mcpCapabilitySummary {
	if len(capabilities) == 0 {
		return nil
	}
	out := make([]mcpCapabilitySummary, 0, len(capabilities))
	for _, capability := range capabilities {
		summary := mcpCapabilitySummaryByID[capability.CapabilityId]
		if summary == "" {
			summary = "Use the declared actions for this negotiated Node capability; load a tool guide when needed."
		}
		out = append(out, mcpCapabilitySummary{
			CapabilityID: capability.CapabilityId,
			Version:      capability.Version,
			Actions:      append([]string(nil), capability.Actions...),
			Summary:      summary,
			MCPTools:     append([]string(nil), mcpCapabilityMCPToolsByID[capability.CapabilityId]...),
		})
	}
	return out
}

func newMCPCapabilityGuide(serverVersion string, capabilities []protocolv1.CapabilityDescriptor, name string) (*mcpGuide, error) {
	name = strings.TrimSpace(name)
	if len(name) > 128 {
		return nil, fmt.Errorf("INVALID_REQUEST: capability guide selector exceeds its bound")
	}
	if name == "" {
		return nil, fmt.Errorf("INVALID_REQUEST: name is required for a capability guide")
	}
	for _, capability := range capabilities {
		if capability.CapabilityId != name {
			continue
		}
		summaries := mcpCapabilitySummaries([]protocolv1.CapabilityDescriptor{capability})
		if len(summaries) != 1 {
			return nil, fmt.Errorf("INTERNAL: capability summary could not be built")
		}
		summary := summaries[0]
		return &mcpGuide{
			GuideVersion:    mcpGuideVersion,
			ServerVersion:   serverVersion,
			View:            "capability",
			Name:            name,
			Summary:         summary.Summary,
			Capability:      &summary,
			RecommendedNext: append([]string(nil), summary.MCPTools...),
		}, nil
	}
	return nil, fmt.Errorf("NOT_FOUND: unknown Node capability")
}

func aiControlSessionSelectionRules() []string {
	return []string{
		"Use the current Codex or CHAT directly when it already has the required tools and context; delegating to ChatGPT Cloud is optional, not a mandatory coding stage",
		"If the request means create or continue a Cloud CHAT and receive the result later, use codex_cloud_collaboration even when there is only one simple task; bare ai_control quick_chat is for direct interactive lifecycle control and has no durable controller callback",
		"When the user supplies an exact Codex or ChatGPT sessionId, validate and use that exact ID regardless of which earlier Codex or CHAT created it",
		"For a known local Codex ID, call session.get with metadataOnly=true, then session.send only when idle; all local Codex operations use the Node-owned app-server, report AGENT_SESSION_BUSY for an active Turn, and never create a substitute or inject input into an active Turn",
		"To create a local Codex task through Hub, use session.create with providerId=codex, backend=codex_local, an absolute workingDirectory and a stable idempotencyKey",
		"When no sessionId is supplied and the new task is unrelated to current context, create a new appropriately scoped session; do not call session.list, search, or guess an old session",
		"A known ChatGPT conversation can be continued with session.send backend=chatgpt_cloud (or appType=chatgpt); mode=quick_chat returns after acceptance, while complete waits for the turn response",
	}
}

func codexCloudCollaborationGuide() (string, []string, []string, []string) {
	summary := "Dispatch one task to one visible ChatGPT Cloud CHAT and return the durable callback to callbackSessionId. Fast Spider does not distinguish controller, coordinator and single-AI topologies."
	whenToUse := []string{"A local Codex session wants Cloud CHAT assistance and needs the result later", "The user supplies an exact CHAT to continue", "A new task needs one clean visible CHAT"}
	requiredInputs := []string{
		"action=dispatch with machineId, callbackSessionId, workingDirectory, prompt and a stable idempotencyKey",
		"optional targetSessionId to reuse exactly that CHAT",
		"optional accessMode=read_only; otherwise writeScope defaults to workingDirectory and callbackType defaults to text",
	}
	safeSequence := []string{
		"First decide whether the current Codex can complete the task directly; use this collaboration only when CHAT assistance is useful or explicitly requested",
		"Call dispatch once; Fast Spider performs readiness checks, creates its internal callback state, sends the task and arms the route",
		"If targetSessionId is omitted, dispatch creates one backend=chatgpt_cloud visible quick_chat; it never scans or guesses old CHATs",
		"After callerShouldYield=true and nextAction=end_turn, return a short receipt and end the turn without polling",
		"The CHAT calls action=completion.notify with actorSessionId=$self before its final reply",
		"Hub persists the completion, actively pushes it into the Node callback queue, and Node wakes callbackSessionId immediately when idle or after its active turn finishes",
		"Provider realtime, startup reconciliation and timed status reads are recovery only for missed delivery; a future-created CHAT or task is not guaranteed to be covered by an external timer, so polling never replaces the active Hub-to-Node callback",
		"Queues, leases, acknowledgement and archive/release remain internal details",
	}
	return summary, whenToUse, requiredInputs, safeSequence
}

var mcpWorkflowGuides = map[string]mcpWorkflowGuideEntry{
	"connection-check":          {Summary: "Verify real MCP and Machine connectivity without making changes, including ChatGPT per-conversation connector recovery.", SafeSequence: []string{"If ChatGPT does not expose the FastSpider_FS namespace, use filtered connector discovery for the lightweight machine tools first (api_tool.list_resources(paths=[\"FastSpider_FS\"], query=\"fsprobe\") when available)", "Never materialize the full tool schema just to test connectivity; load later tools only for the current action", "Do not ask for login/reauthorization solely because one conversation lost its namespace", "capability_list(view=overview)", "machine_list", "machine_get only when details are needed"}, Returns: []string{"Lightweight recovered connection tools, Hub guide/catalog and current Machine availability"}, RecommendedNext: []string{"Load only the specific tool required by the next action"}, CommonErrors: []string{"MACHINE_OFFLINE"}},
	"cloud-chat-callback":       {Summary: "Dispatch one task to one visible Cloud CHAT and return its durable callback to one local Codex session.", RequiredInputs: []string{"machineId", "callbackSessionId", "absolute workingDirectory", "prompt", "stable idempotencyKey", "optional exact targetSessionId"}, SafeSequence: []string{"Call codex_cloud_collaboration action=dispatch once", "Let Fast Spider create or reuse exactly one CHAT and register the callback route", "End the current turn when callerShouldYield=true; do not poll", "CHAT completion.notify is persisted by Hub and actively pushed through Node to callbackSessionId", "Provider realtime and timed status checks are fallback recovery only", "On callback, continue the local task and update working_context only when a compact project note is useful"}, Returns: []string{"asynchronous dispatch receipt", "durable active callback to callbackSessionId", "restart-safe internal recovery"}, RecommendedNext: []string{"end the current turn after dispatch"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "AGENT_SESSION_BUSY", "CONFLICT", "CALLBACK_BASELINE_UNAVAILABLE", "CALLBACK_DELIVERY_PENDING"}},
	"file-edit":                 {Summary: "Locate, preview, CAS-write and verify a file change.", RequiredInputs: []string{"machineId", "absolute project/file path"}, SafeSequence: []string{"code_search", "file_read", "capture fileSha256", "file_edit preview", "file_edit(expectedFileSha256)", "file_read verification"}, Returns: []string{"Verified file change with before/after SHA"}, RecommendedNext: []string{"git-change when the project is versioned"}, CommonErrors: []string{"CONFLICT", "ABSOLUTE_PATH_REQUIRED"}},
	"shell-job":                 {Summary: "Run a command and observe its real terminal result.", SafeSequence: []string{"shell_run", "capture jobId", "job_watch using cursor until completed/failed/canceled"}, Returns: []string{"Terminal state, exit code and bounded events"}, RecommendedNext: []string{"artifact-display for a full terminal log"}, CommonErrors: []string{"JOB_NOT_FOUND", "DEADLINE_EXCEEDED"}},
	"build-job":                 {Summary: "Run a build/test, observe its real terminal result, and remove caller-created temporary outputs.", SafeSequence: []string{"create a unique temporary directory/file only when needed and retain its exact path", "build_control(action=run)", "capture jobId", "job_watch until completed/failed/canceled", "remove only that temporary path in a finally/defer path"}, Returns: []string{"Terminal build state, exit code and bounded events"}, RecommendedNext: []string{"git-change after a successful validation"}, CommonErrors: []string{"JOB_NOT_FOUND", "RUNTIME_UNAVAILABLE"}},
	"git-change":                {Summary: "Inspect Git facts before and after authorized mutations.", SafeSequence: []string{"git_control(status)", "git_control(diff)", "obtain authorization", "git_control(add/commit/push as authorized)", "git_control(status)"}, Returns: []string{"Reviewable Git diff and final repository state"}, RecommendedNext: []string{"job_watch if an action returns jobId"}, CommonErrors: []string{"CONNECTION_LOST", "INVALID_REQUEST"}},
	"browser":                   {Summary: "Automate one browser verification in one managed session and remove its session directory when finished.", SafeSequence: []string{"browser_control(readiness)", "launch one session", "register close in a finally/defer path", "page.open", "snapshot", "click/type/batch with ref", "screenshot only for visual evidence", "close after success/failure/cancel"}, Returns: []string{"DOM/accessibility evidence and optional 48-hour screenshot URL; close removes the managed session directory"}, RecommendedNext: []string{"Take a fresh snapshot after navigation"}, CommonErrors: []string{"BROWSER_REF_STALE", "RUNTIME_UNAVAILABLE"}},
	"codex-session":             {Summary: "Find and inspect Codex sessions without creating a new Session.", RequiredInputs: []string{"machineId", "absolute workingDirectory"}, SafeSequence: []string{"machine_list", "ai_control(action=session.list, workingDirectory=...)", "session.get", "session.watch for active sessions", "session.result for terminal sessions"}, Returns: []string{"Existing session facts and bounded output"}, RecommendedNext: []string{"session.send only when the user requests new work"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "NOT_FOUND"}},
	"long-task":                 {Summary: "Keep a long task understandable with one compact revisioned text note.", SafeSequence: []string{"working_context get", "edit goals, progress, blockers and next steps as plain text", "working_context set with expectedRevision"}, Returns: []string{"bounded project text and live Git facts"}, RecommendedNext: []string{"continue the next concrete project action"}, CommonErrors: []string{"CONFLICT", "INVALID_REQUEST"}},
	"artifact-display":          {Summary: "Return a local file/Job log through native MCP content, or publish a URL-only temporary attachment.", SafeSequence: []string{"artifact_get(uploadFile or uploadJobLog) for native content", "artifact_get(get) when re-reading", "artifact_get(publishFile) only for an explicitly requested 48-hour URL"}, Returns: []string{"Bounded native image/text/blob, or URL-only temporary attachment metadata"}, RecommendedNext: []string{"No further call when native content or the returned URL is sufficient"}, CommonErrors: []string{"NOT_FOUND", "JOB_NOT_FOUND"}},
	"codex-cloud-collaboration": {Summary: "Use the same single dispatch-and-callback chain for one AI, a controller, or a controller plus coordinator.", RequiredInputs: []string{"machineId", "callbackSessionId", "absolute workingDirectory", "prompt", "stable idempotencyKey", "optional exact targetSessionId"}, SafeSequence: []string{"Call codex_cloud_collaboration action=dispatch once", "Fast Spider sends one task to one visible CHAT and registers callback delivery", "End the current turn when callerShouldYield=true", "Keep multi-AI roles and progress in caller-side text, not in a second FS protocol"}, Returns: []string{"one dispatch receipt", "one durable callback to callbackSessionId"}, RecommendedNext: []string{"end the current turn after dispatch"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "AGENT_SESSION_BUSY", "CONFLICT", "CALLBACK_BASELINE_UNAVAILABLE"}},
}

var mcpErrorGuides = map[string]mcpErrorGuideEntry{
	"CONNECTION_LOST":             {Summary: "The Node connection disappeared before the Hub received a response.", WhenToUse: []string{"A capability call reached the Hub but lost its Node connection"}, SafeSequence: []string{"Re-read machine_list", "Re-read Job/file/Git facts", "Retry only read-only or explicitly safe operations"}, RecommendedNext: []string{"machine_list"}},
	"MACHINE_OFFLINE":             {Summary: "The selected Machine has no active Node connection.", SafeSequence: []string{"Call machine_list", "Wait for/restart the intended Node outside MCP if authorized", "Retry after online/ready"}, RecommendedNext: []string{"machine_list"}},
	"DEADLINE_EXCEEDED":           {Summary: "The bounded request deadline expired.", SafeSequence: []string{"Check machine_list and any returned jobId", "Do not assume a mutation did not happen", "Reconcile state before retry"}, RecommendedNext: []string{"job_watch", "machine_list"}},
	"ABSOLUTE_PATH_REQUIRED":      {Summary: "A filesystem/cwd/repository argument was not an absolute local path.", SafeSequence: []string{"Use the Node platform's absolute path", "Do not infer or concatenate an unverified path", "Retry with the corrected input"}},
	"BROWSER_REF_STALE":           {Summary: "A short-lived browser element ref no longer matches the current page snapshot.", SafeSequence: []string{"Call snapshot again", "Select the new ref", "Retry the single intended interaction"}, RecommendedNext: []string{"browser_control(snapshot)"}},
	"NODE_UPDATING":               {Summary: "The Node is draining new capability work to apply a verified update.", SafeSequence: []string{"Do not cancel existing work", "Wait for Node reconnect", "Call machine_list and retry after ready"}, RecommendedNext: []string{"machine_list"}},
	"RUNTIME_UNAVAILABLE":         {Summary: "The requested host/WSL/browser/AI runtime is unavailable.", SafeSequence: []string{"Read the relevant readiness/status action", "Correct the runtime selection or local installation", "Retry only after readiness changes"}},
	"WSL_CWD_UNMAPPABLE":          {Summary: "The Windows absolute cwd cannot be mapped into the selected WSL distribution.", SafeSequence: []string{"Verify the Windows path exists", "Verify the selected distribution can access that drive/path", "Retry with a mappable cwd"}},
	"JOB_NOT_FOUND":               {Summary: "The supplied jobId is not known on the selected Machine.", SafeSequence: []string{"Confirm machineId and jobId came from the same start call", "Do not blindly restart a possibly completed mutation", "Reconcile target state first"}},
	"INVALID_REQUEST":             {Summary: "The request shape, action or bounded input failed validation.", SafeSequence: []string{"Read only the relevant tool/workflow guide", "Correct required inputs and allowed values", "Retry without adding unrelated parameters"}, RecommendedNext: []string{"capability_list(view=tool,name=<tool>)"}},
	"CALLBACK_ROUTE_MANAGED_ONLY": {Summary: "Callback route creation and delivery are owned by codex_cloud_collaboration.", SafeSequence: []string{"Do not retry ai_control session.callback.register/arm/enqueue", "Create or use the formal collaboration task", "Use session.callback.list/claim/ack only for Node fallback recovery"}, RecommendedNext: []string{"codex_cloud_collaboration"}},
	"CALLBACK_DELIVERY_PENDING":   {Summary: "Hub has durably stored the completion, but the active Hub-to-Node callback has not yet been accepted by Node.", SafeSequence: []string{"Keep the returned notificationId as the durable completion identity", "Retry the identical codex_cloud_collaboration completion.notify request", "Do not create a replacement task or switch to polling as the normal completion path", "If delivery remains unavailable, let startup, realtime-gap or timed recovery reconcile the same notification"}, RecommendedNext: []string{"codex_cloud_collaboration(action=completion.notify)"}},
	"ORPHAN_CALLBACK_ROUTE":       {Summary: "A Node callback owner references a collaboration or task that cannot be loaded.", SafeSequence: []string{"Stop repeated notify attempts", "Inspect the exact callback and CHAT", "Preserve any needed result", "Explicitly unregister the orphan route"}, RecommendedNext: []string{"ai_control(session.callback.list)"}},
	"COLLABORATION_NOT_FOUND":     {Summary: "A completion notification references a collaboration that does not exist.", SafeSequence: []string{"Do not retry indefinitely", "Verify the callback route IDs", "Record the bounded issue and clean up an orphan Node route"}, RecommendedNext: []string{"ai_control(session.callback.list)"}},
	"TASK_NOT_FOUND":              {Summary: "A completion notification references a task missing from its collaboration.", SafeSequence: []string{"Do not retry indefinitely", "Verify taskId and generation", "Record the bounded issue and clean up an orphan Node route"}, RecommendedNext: []string{"codex_cloud_collaboration(action=get)"}},
	"CALLBACK_TEXT_REQUIRED":      {Summary: "A completed text callback has no usable text.", SafeSequence: []string{"Do not claim success", "Return a bounded text result or create a new local_file task for a document"}, RecommendedNext: []string{"codex_cloud_collaboration(action=get)"}},
	"CALLBACK_TEXT_TOO_LARGE":     {Summary: "A text callback exceeds 2000 Unicode characters or 8192 UTF-8 bytes.", SafeSequence: []string{"Do not truncate or upload the result", "Create a new local_file task and write to its registered Node-local path"}, RecommendedNext: []string{"codex_cloud_collaboration(task.add)"}},
	"CALLBACK_TEXT_INVALID":       {Summary: "A text callback is not valid bounded UTF-8 text.", SafeSequence: []string{"Remove NUL or invalid content", "Retry the same notification with valid text"}, RecommendedNext: []string{"codex_cloud_collaboration(action=completion.notify)"}},
}

func mcpToolDefinition(name string, annotations *mcp.ToolAnnotations) *mcp.Tool {
	entry, ok := mcpToolGuides[name]
	if !ok {
		panic("missing MCP tool guide: " + name)
	}
	return &mcp.Tool{Name: name, Description: entry.Description, Annotations: annotations}
}

func mcpRegisteredGuideNames() []string {
	names := make([]string, 0, len(mcpToolGuides))
	for name := range mcpToolGuides {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newMCPGuide(serverVersion, view, name string) (*mcpGuide, error) {
	view = strings.TrimSpace(view)
	name = strings.TrimSpace(name)
	if len(view) > 32 || len(name) > 128 {
		return nil, fmt.Errorf("INVALID_REQUEST: capability guide selector exceeds its bound")
	}
	base := &mcpGuide{GuideVersion: mcpGuideVersion, ServerVersion: serverVersion, View: view, Name: name}
	switch view {
	case "overview":
		base.Summary = "FastSpider_FS exposes one stable 21-tool surface: start with real read-only discovery, then load only the detailed guide needed for the current action."
		base.ToolSummaries = mcpToolSummaries()
		base.Categories = []mcpGuideCategory{
			{Name: "连接与设备", Summary: "Discover Hub/Machine availability.", Tools: []string{"capability_list", "machine_list", "machine_get"}},
			{Name: "审计日志", Summary: "Read owner-scoped Hub mutation history or recent bounded Node operation events.", Tools: []string{"audit_log", "operation_log"}},
			{Name: "文件与代码", Summary: "Search, read and CAS-edit local files.", Tools: []string{"code_search", "file_read", "file_edit"}},
			{Name: "命令、构建与任务", Summary: "Start bounded work and observe terminal state.", Tools: []string{"shell_run", "build_control", "job_watch", "job_cancel"}},
			{Name: "Git", Summary: "Use allowlisted repository actions.", Tools: []string{"git_control"}},
			{Name: "浏览器与桌面", Summary: "Automate Chromium or capture one-time visual evidence.", Tools: []string{"browser_control", "screenshot_take"}},
			{Name: "本机 AI", Summary: "Discover/control Codex and Claude Code.", Tools: []string{"ai_control"}},
			{Name: "Codex 云端协作", Summary: "Dispatch one CHAT task and return the callback to one local session.", Tools: []string{"codex_cloud_collaboration"}},
			{Name: "项目上下文", Summary: "Maintain one revisioned plain-text project note.", Tools: []string{"working_context"}},
			{Name: "多视角协作", Summary: "Return calling-side role/workflow guidance only.", Tools: []string{"thinking_team"}},
			{Name: "文件与日志回显", Summary: "Return bounded native MCP content.", Tools: []string{"artifact_get", "result_get"}},
		}
		base.GoldenRules = []string{
			"When @FastSpider_FS is selected or mentioned, try a real read-only tool before judging availability from UI text.",
			"On ChatGPT, an absent direct namespace is not proof of disconnection: use filtered discovery and machine_list before asking for login.",
			"If machineId is unknown, call machine_list first; connection checks use capability_list plus machine_list.",
			"Use toolSummaries first; load one detailed guide only when needed.",
			"The low-level catalog reports capabilityId/version/actions and summaries.",
			"For an unclear capability ID, load its capability guide and mcpTools mapping.",
			"shell_run is the host/WSL process entry point; on Windows name the shell explicitly in argv.",
			"Use the current Codex or CHAT directly when it can finish the task; Cloud CHAT assistance is opt-in, not mandatory.",
			"Choose the Cloud CHAT entry by delivery: direct interactive reads use ai_control; any create/continue request that should return later by callback uses codex_cloud_collaboration, even for one simple task.",
			"After collaboration task.dispatch succeeds, obey callerShouldYield=true and nextAction=end_turn; do not remain active with session.get/watch/result, tick, status.poll, sleeps, or repeated reasoning.",
			"Use an exact user-supplied session ID regardless of its creator; without an ID, create a clean session for unrelated work instead of listing or guessing an old one.",
			"Codex session history is ai_control(action=session.list), but list only when the user actually asks to find or inspect sessions.",
			"Codex cloud collaboration is separate from ordinary FS operations; Cloud CHAT is an ordinary visible account conversation, not ChatGPT Work.",
			"A started process is not completion: follow every shell/build jobId with job_watch to a terminal state.",
		}
		base.RecommendedNext = []string{"machine_list", "capability_list(view=workflow,name=connection-check)"}
		return base, nil
	case "catalog":
		base.Summary = "Returns the explicit Hub or selected Machine capability catalog; use overview for tool selection guidance."
		base.RecommendedNext = []string{"capability_list(view=overview)", "machine_list"}
		return base, nil
	case "tool":
		entry, ok := mcpToolGuides[name]
		if name == "" {
			return nil, fmt.Errorf("INVALID_REQUEST: name is required for a tool guide")
		}
		if !ok {
			return nil, fmt.Errorf("NOT_FOUND: unknown MCP tool guide")
		}
		base.Summary, base.WhenToUse, base.RequiredInputs, base.SafeSequence = entry.Description, entry.WhenToUse, entry.RequiredInputs, entry.SafeSequence
		base.Returns, base.RecommendedNext, base.CommonErrors, base.BoundedExamples = entry.Returns, entry.RecommendedNext, entry.CommonErrors, entry.BoundedExamples
		if name == "ai_control" {
			base.SafeSequence = append(aiControlSessionSelectionRules(), base.SafeSequence...)
		}
		if name == "codex_cloud_collaboration" {
			base.Summary, base.WhenToUse, base.RequiredInputs, base.SafeSequence = codexCloudCollaborationGuide()
		}
		return base, nil
	case "workflow":
		entry, ok := mcpWorkflowGuides[name]
		if name == "" {
			return nil, fmt.Errorf("INVALID_REQUEST: name is required for a workflow guide")
		}
		if !ok {
			return nil, fmt.Errorf("NOT_FOUND: unknown MCP workflow guide")
		}
		base.Summary, base.RequiredInputs, base.SafeSequence = entry.Summary, entry.RequiredInputs, entry.SafeSequence
		base.Returns, base.RecommendedNext, base.CommonErrors, base.BoundedExamples = entry.Returns, entry.RecommendedNext, entry.CommonErrors, entry.BoundedExamples
		if name == "codex-cloud-collaboration" {
			base.Summary, _, base.RequiredInputs, base.SafeSequence = codexCloudCollaborationGuide()
		}
		return base, nil
	case "error":
		entry, ok := mcpErrorGuides[name]
		if name == "" {
			return nil, fmt.Errorf("INVALID_REQUEST: name is required for an error guide")
		}
		if !ok {
			return nil, fmt.Errorf("NOT_FOUND: unknown MCP error guide")
		}
		base.Summary, base.WhenToUse, base.SafeSequence, base.RecommendedNext = entry.Summary, entry.WhenToUse, entry.SafeSequence, entry.RecommendedNext
		return base, nil
	default:
		return nil, fmt.Errorf("INVALID_REQUEST: unsupported capability guide view")
	}
}
