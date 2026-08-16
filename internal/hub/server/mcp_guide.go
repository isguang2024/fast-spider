package server

import (
	"fmt"
	"sort"
	"strings"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpGuideVersion = "1.1"

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
		Description: "Use first when machineId is unknown or to verify FastSpider_FS connectivity. Unique connector discovery marker: fsprobe. Requires no inputs; returns owned machines with online/runtime state and negotiated capabilities; usually continue with machine_get or a machine-bound tool.",
		WhenToUse:   []string{"Discover a machineId", "Check whether a Node is online and ready"}, RequiredInputs: []string{"none"},
		SafeSequence: []string{"Call machine_list", "Choose an online machine", "Use machine_get only when detailed machine facts are needed"},
		Returns:      []string{"Owned machines, online/runtime status, version and negotiated capabilities"}, RecommendedNext: []string{"machine_get", "the selected machine-bound tool"},
		CommonErrors: []string{"INTERNAL"}, BoundedExamples: []map[string]any{{}},
	},
	"machine_get": {
		Description: "Use after machine_list when one machine needs detailed status. Requires machineId; returns that machine without secrets; usually continue with capability_list or the intended machine-bound tool.",
		WhenToUse:   []string{"Inspect one known machine"}, RequiredInputs: []string{"machineId from machine_list"}, SafeSequence: []string{"machine_list", "machine_get"},
		Returns: []string{"One machine and its negotiated capabilities"}, RecommendedNext: []string{"capability_list with machineId", "a machine-bound tool"}, CommonErrors: []string{"NOT_FOUND"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>"}},
	},
	"capability_list": {
		Description:    "FastSpider_FS health check, two-layer capability catalog and on-demand guide entry. machineId is optional; view selects overview, catalog, one low-level capability, one tool, one workflow or one error guide; usually continue with machine_list or the recommended tool.",
		WhenToUse:      []string{"Check MCP connectivity", "Read the Hub or Machine catalog", "Load one detailed guide only when needed"},
		RequiredInputs: []string{"view=capability|tool|workflow|error requires name", "machineId is optional for catalog"},
		SafeSequence:   []string{"Start with overview or the default call", "Read only the needed capability/tool/workflow/error guide", "Do not fetch every guide"},
		Returns:        []string{"capabilities plus an optional bounded guide"}, RecommendedNext: []string{"machine_list", "the guide's recommendedNext"}, CommonErrors: []string{"INVALID_REQUEST", "NOT_FOUND"},
		BoundedExamples: []map[string]any{{"view": "overview"}, {"view": "capability", "name": "shell.exec"}, {"view": "workflow", "name": "connection-check"}},
	},
	"file_read": {
		Description: "Use to read a bounded UTF-8 file selection or obtain file metadata/SHA. Requires machineId and absolute path; returns content or stat plus fileSha256; usually continue with file_edit preview or verification.",
		WhenToUse:   []string{"Inspect source or configuration", "Obtain fileSha256 before editing"}, RequiredInputs: []string{"machineId", "absolute path", "at most one selector"},
		SafeSequence: []string{"code_search when location is unknown", "file_read", "retain fileSha256 for CAS"}, Returns: []string{"bounded content/stat, hashes and truncation facts"},
		RecommendedNext: []string{"file_edit preview", "file_read verification"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "NOT_FOUND", "NOT_TEXT"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "path": "<absolute-file>", "headLines": 80}},
	},
	"file_edit": {
		Description: "Use to preview, create or atomically edit a bounded UTF-8 file. Requires machineId, absolute path and CAS inputs; returns fixed metadata (preview alone includes bounded diff); usually verify with file_read.",
		WhenToUse:   []string{"Preview or apply a precise file change"}, RequiredInputs: []string{"machineId", "absolute path", "expectedFileSha256 for existing files", "expectedAbsent=true for create"},
		SafeSequence: []string{"code_search", "file_read and capture fileSha256", "file_edit preview", "file_edit with expectedFileSha256", "file_read verification"},
		Returns:      []string{"success/change metadata, old/new SHA and bounded preview diff"}, RecommendedNext: []string{"file_read"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "CONFLICT", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "preview", "previewOf": "replace", "path": "<absolute-file>", "oldText": "old", "newText": "new", "expectedFileSha256": "<sha256>"}},
	},
	"code_search": {
		Description: "Use to locate code or matching files below an absolute directory. Requires machineId, query and absolute path; returns bounded matches and engine facts; usually continue with file_read.",
		WhenToUse:   []string{"Find relevant files or text before reading/editing"}, RequiredInputs: []string{"machineId", "query", "absolute directory path"}, SafeSequence: []string{"Use narrow include globs when known", "code_search", "file_read exact matches"},
		Returns: []string{"bounded matches/files, scan statistics and fallback reason"}, RecommendedNext: []string{"file_read"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "DEADLINE_EXCEEDED", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "query": "symbol", "path": "<absolute-project>", "include": []string{"internal/**/*.go"}, "limit": 20}},
	},
	"shell_run": {
		Description: "Use to start a bounded non-interactive host/WSL process with explicit argv. On Windows, argv may explicitly invoke powershell.exe, pwsh.exe or cmd.exe; PowerShell is not a separate FS tool. Requires machineId, absolute cwd and idempotencyKey; returns a jobId, not completion; always continue with job_watch.",
		WhenToUse:   []string{"Run a command that is not a dedicated Git or build action"}, RequiredInputs: []string{"machineId", "argv", "absolute cwd", "12-128 character idempotencyKey"},
		SafeSequence: []string{"shell_run", "capture jobId", "job_watch until completed, failed or canceled"}, Returns: []string{"started Job metadata and jobId"}, RecommendedNext: []string{"job_watch", "job_cancel if needed"},
		CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE"}, BoundedExamples: []map[string]any{
			{"machineId": "<machine-id>", "argv": []string{"go", "version"}, "cwd": "<absolute-project>", "idempotencyKey": "<unique-key>"},
			{"machineId": "<machine-id>", "argv": []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Date; tzutil /g"}, "cwd": "C:\\", "idempotencyKey": "<unique-key>"},
		},
	},
	"job_watch": {
		Description: "Use after shell_run, build_control or asynchronous Git work to read bounded events and terminal state. Requires machineId and jobId; returns events/state/cursor; continue until completed, failed or canceled.",
		WhenToUse:   []string{"Observe a started Job", "Confirm actual completion"}, RequiredInputs: []string{"machineId", "jobId"}, SafeSequence: []string{"Pass the last nextCursor", "Long-poll up to 15 seconds", "Stop only at a terminal state"},
		Returns: []string{"bounded events, nextCursor, state, exitCode and timing"}, RecommendedNext: []string{"job_watch again", "artifact_get uploadJobLog", "job_cancel"}, CommonErrors: []string{"JOB_NOT_FOUND", "CONNECTION_LOST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "jobId": "<job-id>", "cursor": 0, "waitSeconds": 15}},
	},
	"job_cancel": {
		Description: "Use to cancel one active Job and its process tree. Requires machineId and jobId; returns current Job state; usually confirm the terminal state with job_watch.",
		WhenToUse:   []string{"Stop an active shell/build Job"}, RequiredInputs: []string{"machineId", "jobId"}, SafeSequence: []string{"job_cancel", "job_watch until canceled or another terminal state"},
		Returns: []string{"Job state after cancellation request"}, RecommendedNext: []string{"job_watch"}, CommonErrors: []string{"JOB_NOT_FOUND", "CONNECTION_LOST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "jobId": "<job-id>"}},
	},
	"git_control": {
		Description: "Use for allowlisted Git reads and authorized writes/network actions. Requires machineId, absolute repositoryPath and action; returns Git facts or a Job/result; normally status/diff before authorized add/commit/push, then status.",
		WhenToUse:   []string{"Inspect or change a Git repository"}, RequiredInputs: []string{"machineId", "absolute repositoryPath", "action", "idempotencyKey for network actions"},
		SafeSequence: []string{"status", "diff", "obtain authorization for writes/network", "add/commit/push", "status"}, Returns: []string{"allowlisted Git action result"}, RecommendedNext: []string{"git_control status", "job_watch when a jobId is returned"},
		CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "CONNECTION_LOST", "INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "repositoryPath": "<absolute-project>", "action": "status"}},
	},
	"build_control": {
		Description: "Use for a bounded build or test command with explicit argv. Requires machineId, absolute cwd and idempotencyKey; returns a jobId, not completion; always continue with job_watch.",
		WhenToUse:   []string{"Run a build, test or lint command"}, RequiredInputs: []string{"machineId", "action=run", "argv", "absolute cwd", "idempotencyKey"}, SafeSequence: []string{"build_control", "capture jobId", "job_watch to a terminal state"},
		Returns: []string{"started Job envelope"}, RecommendedNext: []string{"job_watch"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "run", "argv": []string{"go", "test", "./..."}, "cwd": "<absolute-project>", "idempotencyKey": "<unique-key>"}},
	},
	"browser_control": {
		Description: "Use for isolated Chromium automation. Requires machineId and action-specific IDs; returns readiness/session/page/snapshot refs or native screenshots; normally readiness → launch → page.open → snapshot/ref actions → close.",
		WhenToUse:   []string{"Automate or inspect a real web page"}, RequiredInputs: []string{"machineId", "action", "session/page IDs after launch/open"}, SafeSequence: []string{"readiness", "launch", "page.open", "snapshot", "click/type/batch using refs", "optional screenshot", "close"},
		Returns: []string{"browser readiness, IDs, accessibility snapshots/refs, events or images"}, RecommendedNext: []string{"snapshot", "close"}, CommonErrors: []string{"BROWSER_REF_STALE", "RUNTIME_UNAVAILABLE", "INVALID_REQUEST"},
		BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "readiness"}},
	},
	"screenshot_take": {
		Description: "Use for one-time desktop/display/window visual evidence, not browser element targeting. Requires machineId and action; returns bounded native MCP image content; usually inspect the image or fetch a related artifact.",
		WhenToUse:   []string{"Capture visual desktop evidence"}, RequiredInputs: []string{"machineId", "action", "displayIndex or windowId when applicable"}, SafeSequence: []string{"list displays/windows when needed", "capture once", "inspect returned native image"},
		Returns: []string{"native PNG/JPEG content and bounded metadata"}, RecommendedNext: []string{"artifact_get only when persistence is required"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "desktop", "format": "png"}},
	},
	"thinking_team": {
		Description: "Use when the calling model needs role, department or collaboration guidance. Requires an action defined by this tool; returns calling-side guidance only; continue in the current model or use working_context for durable task facts.",
		WhenToUse:   []string{"Get structured multi-perspective thinking guidance"}, RequiredInputs: []string{"action and optional role/department/workflow name"}, SafeSequence: []string{"Read only the needed view", "Apply it in the calling model", "Do not treat it as local AI execution"},
		Returns: []string{"roles, departments, workflows or workspace protocol"}, RecommendedNext: []string{"working_context", "continue reasoning in the caller"}, CommonErrors: []string{"INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"action": "overview"}},
	},
	"ai_control": {
		Description: "Use to discover/control local Codex or Claude Code and read CC Switch route facts. Requires machineId and action; returns provider/session facts; Codex history starts with action=session.list, then session.get/watch/result.",
		WhenToUse:   []string{"Discover AI runtimes", "List or control Codex/Claude Code sessions"}, RequiredInputs: []string{"machineId", "action", "providerId when not codex", "workingDirectory for scoped list/create"},
		SafeSequence: []string{"machine_list", "session.list with workingDirectory", "session.get", "session.watch while active", "session.result at terminal state"}, Returns: []string{"bounded provider, route or session results"},
		RecommendedNext: []string{"session.get", "session.watch", "session.result"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "INVALID_REQUEST", "DEADLINE_EXCEEDED"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "session.list", "workingDirectory": "<absolute-project>"}},
	},
	"working_context": {
		Description: "Use for durable project Plan/Task state, acceptance evidence and bounded Markdown workspace facts. Requires machineId, absolute projectPath and action; returns revisioned state; mutations use expectedRevision/fileRevision CAS.",
		WhenToUse:   []string{"Track a long task across calls", "Store bounded project facts and acceptance evidence"}, RequiredInputs: []string{"machineId", "absolute projectPath", "action", "planId for non-default plans"},
		SafeSequence: []string{"plan.init or plan.get", "task.update with expectedRevision", "record acceptance evidence", "plan.sync"}, Returns: []string{"revisioned plan/task/Markdown state and live Git facts"},
		RecommendedNext: []string{"task.update", "plan.sync", "progress.watch"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "CONFLICT", "INVALID_REQUEST"}, BoundedExamples: []map[string]any{{"machineId": "<machine-id>", "action": "plan.get", "projectPath": "<absolute-project>", "planId": "release-task"}},
	},
	"artifact_get": {
		Description: "Use to upload/read local files or terminal Job logs as bounded native MCP content. Requires action-specific artifactId or machineId/path/jobId; returns metadata plus native image/text/blob when safe; publishFile is only for explicit temporary sharing.",
		WhenToUse:   []string{"Display a local file or Job log", "Retrieve an existing Artifact"}, RequiredInputs: []string{"action", "artifactId for get", "machineId plus absolute path or terminal jobId for upload"},
		SafeSequence: []string{"uploadFile or uploadJobLog", "inspect native content", "get later by artifactId", "publishFile only when a temporary URL is explicitly needed"}, Returns: []string{"Artifact metadata and bounded native MCP content"},
		RecommendedNext: []string{"artifact_get get"}, CommonErrors: []string{"ABSOLUTE_PATH_REQUIRED", "JOB_NOT_FOUND", "NOT_FOUND"}, BoundedExamples: []map[string]any{{"action": "uploadJobLog", "machineId": "<machine-id>", "jobId": "<terminal-job-id>", "logicalName": "test.log"}},
	},
}

var mcpToolSummaryDefinitions = []mcpToolSummary{
	{Name: "machine_list", Category: "connection", Summary: "Discover owned Machines, online state and machine IDs.", Guide: "capability_list(view=tool,name=machine_list)"},
	{Name: "machine_get", Category: "connection", Summary: "Inspect one known Machine and its negotiated capabilities.", Guide: "capability_list(view=tool,name=machine_get)"},
	{Name: "capability_list", Category: "connection", Summary: "Read the compact FS map, low-level catalog or one on-demand guide.", Guide: "capability_list(view=tool,name=capability_list)"},
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
	{Name: "ai_control", Category: "ai", Summary: "Discover and control local Codex or Claude Code sessions.", Guide: "capability_list(view=tool,name=ai_control)"},
	{Name: "working_context", Category: "context", Summary: "Persist bounded Plan, Task, evidence and Markdown project state.", Guide: "capability_list(view=tool,name=working_context)"},
	{Name: "thinking_team", Category: "guidance", Summary: "Return calling-side role, department and workflow guidance.", Guide: "capability_list(view=tool,name=thinking_team)"},
	{Name: "artifact_get", Category: "artifacts", Summary: "Upload or retrieve bounded native MCP files and Job logs.", Guide: "capability_list(view=tool,name=artifact_get)"},
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
	"working.context":    "Maintain revisioned Plan, Task, evidence and Markdown project context.",
	"browser.automation": "Control isolated Chromium through readiness, pages, snapshots and refs.",
	"screenshot.capture": "Capture one-time desktop, display or window images.",
	"agent.control":      "Discover and control supported local AI Harnesses and sessions.",
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

var mcpWorkflowGuides = map[string]mcpWorkflowGuideEntry{
	"connection-check": {Summary: "Verify real MCP and Machine connectivity without making changes, including ChatGPT per-conversation connector recovery.", SafeSequence: []string{"If ChatGPT does not expose the FastSpider_FS namespace, use filtered connector discovery for the lightweight machine tools first (api_tool.list_resources(paths=[\"FastSpider_FS\"], query=\"fsprobe\") when available)", "Never materialize the full 17-tool schema just to test connectivity; load later tools only for the current action", "Do not ask for login/reauthorization solely because one conversation lost its namespace", "capability_list(view=overview)", "machine_list", "machine_get only when details are needed"}, Returns: []string{"Lightweight recovered connection tools, Hub guide/catalog and current Machine availability"}, RecommendedNext: []string{"Load only the specific tool required by the next action"}, CommonErrors: []string{"MACHINE_OFFLINE"}},
	"file-edit":        {Summary: "Locate, preview, CAS-write and verify a file change.", RequiredInputs: []string{"machineId", "absolute project/file path"}, SafeSequence: []string{"code_search", "file_read", "capture fileSha256", "file_edit preview", "file_edit(expectedFileSha256)", "file_read verification"}, Returns: []string{"Verified file change with before/after SHA"}, RecommendedNext: []string{"git-change when the project is versioned"}, CommonErrors: []string{"CONFLICT", "ABSOLUTE_PATH_REQUIRED"}},
	"shell-job":        {Summary: "Run a command and observe its real terminal result.", SafeSequence: []string{"shell_run", "capture jobId", "job_watch using cursor until completed/failed/canceled"}, Returns: []string{"Terminal state, exit code and bounded events"}, RecommendedNext: []string{"artifact-display for a full terminal log"}, CommonErrors: []string{"JOB_NOT_FOUND", "DEADLINE_EXCEEDED"}},
	"build-job":        {Summary: "Run a build/test and observe its real terminal result.", SafeSequence: []string{"build_control(action=run)", "capture jobId", "job_watch until completed/failed/canceled"}, Returns: []string{"Terminal build state, exit code and bounded events"}, RecommendedNext: []string{"git-change after a successful validation"}, CommonErrors: []string{"JOB_NOT_FOUND", "RUNTIME_UNAVAILABLE"}},
	"git-change":       {Summary: "Inspect Git facts before and after authorized mutations.", SafeSequence: []string{"git_control(status)", "git_control(diff)", "obtain authorization", "git_control(add/commit/push as authorized)", "git_control(status)"}, Returns: []string{"Reviewable Git diff and final repository state"}, RecommendedNext: []string{"job_watch if an action returns jobId"}, CommonErrors: []string{"CONNECTION_LOST", "INVALID_REQUEST"}},
	"browser":          {Summary: "Automate a browser through accessibility snapshots and short-lived refs.", SafeSequence: []string{"browser_control(readiness)", "launch", "page.open", "snapshot", "click/type/batch with ref", "screenshot only for visual evidence", "close"}, Returns: []string{"DOM/accessibility evidence and optional native image"}, RecommendedNext: []string{"Take a fresh snapshot after navigation"}, CommonErrors: []string{"BROWSER_REF_STALE", "RUNTIME_UNAVAILABLE"}},
	"codex-session":    {Summary: "Find and inspect Codex sessions without creating a new Session.", RequiredInputs: []string{"machineId", "absolute workingDirectory"}, SafeSequence: []string{"machine_list", "ai_control(action=session.list, workingDirectory=...)", "session.get", "session.watch for active sessions", "session.result for terminal sessions"}, Returns: []string{"Existing session facts and bounded output"}, RecommendedNext: []string{"session.send only when the user requests new work"}, CommonErrors: []string{"RUNTIME_UNAVAILABLE", "NOT_FOUND"}},
	"long-task":        {Summary: "Keep a long task recoverable with revisioned Plan/Task evidence.", SafeSequence: []string{"working_context(plan.init or plan.get)", "task.update", "attach acceptance evidence", "plan.sync"}, Returns: []string{"Durable bounded task state and Git facts"}, RecommendedNext: []string{"progress.watch or the next task.update"}, CommonErrors: []string{"CONFLICT", "INVALID_REQUEST"}},
	"artifact-display": {Summary: "Return a local file or Job log through native MCP content.", SafeSequence: []string{"artifact_get(uploadFile or uploadJobLog)", "inspect returned native content", "artifact_get(get) when re-reading", "publishFile only for explicitly requested temporary sharing"}, Returns: []string{"Bounded native image, UTF-8 text or blob"}, RecommendedNext: []string{"No further call when native content is sufficient"}, CommonErrors: []string{"NOT_FOUND", "JOB_NOT_FOUND"}},
}

var mcpErrorGuides = map[string]mcpErrorGuideEntry{
	"CONNECTION_LOST":        {Summary: "The Node connection disappeared before the Hub received a response.", WhenToUse: []string{"A capability call reached the Hub but lost its Node connection"}, SafeSequence: []string{"Re-read machine_list", "Re-read Job/file/Git facts", "Retry only read-only or explicitly safe operations"}, RecommendedNext: []string{"machine_list"}},
	"MACHINE_OFFLINE":        {Summary: "The selected Machine has no active Node connection.", SafeSequence: []string{"Call machine_list", "Wait for/restart the intended Node outside MCP if authorized", "Retry after online/ready"}, RecommendedNext: []string{"machine_list"}},
	"DEADLINE_EXCEEDED":      {Summary: "The bounded request deadline expired.", SafeSequence: []string{"Check machine_list and any returned jobId", "Do not assume a mutation did not happen", "Reconcile state before retry"}, RecommendedNext: []string{"job_watch", "machine_list"}},
	"ABSOLUTE_PATH_REQUIRED": {Summary: "A filesystem/cwd/repository argument was not an absolute local path.", SafeSequence: []string{"Use the Node platform's absolute path", "Do not infer or concatenate an unverified path", "Retry with the corrected input"}},
	"BROWSER_REF_STALE":      {Summary: "A short-lived browser element ref no longer matches the current page snapshot.", SafeSequence: []string{"Call snapshot again", "Select the new ref", "Retry the single intended interaction"}, RecommendedNext: []string{"browser_control(snapshot)"}},
	"NODE_UPDATING":          {Summary: "The Node is draining new capability work to apply a verified update.", SafeSequence: []string{"Do not cancel existing work", "Wait for Node reconnect", "Call machine_list and retry after ready"}, RecommendedNext: []string{"machine_list"}},
	"RUNTIME_UNAVAILABLE":    {Summary: "The requested host/WSL/browser/AI runtime is unavailable.", SafeSequence: []string{"Read the relevant readiness/status action", "Correct the runtime selection or local installation", "Retry only after readiness changes"}},
	"WSL_CWD_UNMAPPABLE":     {Summary: "The Windows absolute cwd cannot be mapped into the selected WSL distribution.", SafeSequence: []string{"Verify the Windows path exists", "Verify the selected distribution can access that drive/path", "Retry with a mappable cwd"}},
	"JOB_NOT_FOUND":          {Summary: "The supplied jobId is not known on the selected Machine.", SafeSequence: []string{"Confirm machineId and jobId came from the same start call", "Do not blindly restart a possibly completed mutation", "Reconcile target state first"}},
	"INVALID_REQUEST":        {Summary: "The request shape, action or bounded input failed validation.", SafeSequence: []string{"Read only the relevant tool/workflow guide", "Correct required inputs and allowed values", "Retry without adding unrelated parameters"}, RecommendedNext: []string{"capability_list(view=tool,name=<tool>)"}},
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
		base.Summary = "FastSpider_FS exposes one stable 17-tool surface: start with real read-only discovery, then load only the detailed guide needed for the current action."
		base.ToolSummaries = mcpToolSummaries()
		base.Categories = []mcpGuideCategory{
			{Name: "连接与设备", Summary: "Discover Hub/Machine availability.", Tools: []string{"capability_list", "machine_list", "machine_get"}},
			{Name: "文件与代码", Summary: "Search, read and CAS-edit local files.", Tools: []string{"code_search", "file_read", "file_edit"}},
			{Name: "命令、构建与任务", Summary: "Start bounded work and observe terminal state.", Tools: []string{"shell_run", "build_control", "job_watch", "job_cancel"}},
			{Name: "Git", Summary: "Use allowlisted repository actions.", Tools: []string{"git_control"}},
			{Name: "浏览器与桌面", Summary: "Automate Chromium or capture one-time visual evidence.", Tools: []string{"browser_control", "screenshot_take"}},
			{Name: "本机 AI", Summary: "Discover/control Codex and Claude Code.", Tools: []string{"ai_control"}},
			{Name: "项目上下文", Summary: "Maintain revisioned Plan/Task evidence.", Tools: []string{"working_context"}},
			{Name: "多视角协作", Summary: "Return calling-side role/workflow guidance only.", Tools: []string{"thinking_team"}},
			{Name: "文件与日志回显", Summary: "Return bounded native MCP content.", Tools: []string{"artifact_get"}},
		}
		base.GoldenRules = []string{
			"When @FastSpider_FS is selected or mentioned, try a real read-only tool before judging availability from UI text.",
			"On ChatGPT, an absent direct namespace is not proof of disconnection: first use filtered connector discovery for only the lightweight machine tools, then machine_list; never load all 17 schemas just to test connectivity and do not ask for login/reauthorization unless filtered discovery plus a real connection check fail.",
			"If machineId is unknown, call machine_list first; connection checks use capability_list plus machine_list.",
			"Use the compact toolSummaries to choose a tool, then call capability_list(view=tool,name=...) only when its detail is needed; never load every detailed guide.",
			"The low-level catalog reports capabilityId/version/actions plus capabilitySummaries; do not infer behavior from an opaque ID alone.",
			"If a low-level capability ID is unclear, call capability_list(view=capability,name=<capabilityId>) and follow its mcpTools mapping.",
			"shell_run is the single host/WSL process entry point; on Windows put powershell.exe, pwsh.exe or cmd.exe explicitly in argv rather than looking for a separate PowerShell tool.",
			"Codex session history is ai_control(action=session.list), not a separate top-level tool.",
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
