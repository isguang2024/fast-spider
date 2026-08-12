package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type machineListInput struct{}

type machineGetInput struct {
	MachineID string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
}

type capabilityListInput struct {
	MachineID string `json:"machineId,omitempty" jsonschema:"optional machine ID; omit for the Hub capability catalog"`
}

type fileReadInput struct {
	MachineID          string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Path               string `json:"path" jsonschema:"absolute regular UTF-8 text file path on the Node machine"`
	Offset             *int64 `json:"offset,omitempty" jsonschema:"byte offset, default 0; cannot be combined with line selectors or statOnly"`
	Limit              *int64 `json:"limit,omitempty" jsonschema:"maximum byte chunk, default and maximum 131072; cannot be combined with line selectors or statOnly"`
	LineStart          *int   `json:"lineStart,omitempty" jsonschema:"1-based first line; requires lineCount"`
	LineCount          *int   `json:"lineCount,omitempty" jsonschema:"number of lines from lineStart, 1 through 2000"`
	HeadLines          *int   `json:"headLines,omitempty" jsonschema:"first 1 through 2000 lines; mutually exclusive with other selectors"`
	TailLines          *int   `json:"tailLines,omitempty" jsonschema:"last 1 through 2000 lines; scanned with bounded memory"`
	AroundLine         *int   `json:"aroundLine,omitempty" jsonschema:"1-based center line; requires contextLines"`
	ContextLines       *int   `json:"contextLines,omitempty" jsonschema:"lines before and after aroundLine, 0 through 1000"`
	StatOnly           *bool  `json:"statOnly,omitempty" jsonschema:"return regular-file metadata and original-file SHA-256 without a content chunk"`
	IncludeLineNumbers *bool  `json:"includeLineNumbers,omitempty" jsonschema:"prefix selected lines with their 1-based line numbers; chunkSha256 then hashes the rendered content"`
}

func addOptionalFileReadParam[T any](params map[string]any, key string, value *T) {
	if value != nil {
		params[key] = *value
	}
}

type codeSearchInput struct {
	MachineID     string   `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Query         string   `json:"query" jsonschema:"literal text or regular expression to search"`
	Path          string   `json:"path" jsonschema:"absolute directory path to search on the Node machine"`
	Mode          string   `json:"mode,omitempty" jsonschema:"content (default) returns line matches; files returns paths whose contents match"`
	Regex         bool     `json:"regex,omitempty" jsonschema:"interpret query as a regular expression"`
	IgnoreCase    bool     `json:"ignoreCase,omitempty" jsonschema:"case-insensitive matching"`
	Include       []string `json:"include,omitempty" jsonschema:"up to 32 bounded include globs relative to path"`
	Exclude       []string `json:"exclude,omitempty" jsonschema:"up to 32 bounded exclude globs relative to path"`
	Context       int      `json:"context,omitempty" jsonschema:"before and after context lines, 0 through 10"`
	BeforeContext int      `json:"beforeContext,omitempty" jsonschema:"before-match context lines, 0 through 10"`
	AfterContext  int      `json:"afterContext,omitempty" jsonschema:"after-match context lines, 0 through 10"`
	Limit         int      `json:"limit,omitempty" jsonschema:"maximum matches or files, default 100 and maximum 200"`
}

type fileEditInput struct {
	MachineID          string          `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Action             string          `json:"action,omitempty" jsonschema:"edit (legacy default), create, replace, editMany, or preview"`
	PreviewOf          string          `json:"previewOf,omitempty" jsonschema:"create, replace, or editMany when action is preview"`
	Path               string          `json:"path" jsonschema:"absolute file path on the Node machine"`
	Content            string          `json:"content,omitempty" jsonschema:"bounded UTF-8 file content for create"`
	OldText            string          `json:"oldText,omitempty" jsonschema:"text that must occur exactly once for edit or replace"`
	NewText            string          `json:"newText,omitempty" jsonschema:"replacement text for edit or replace"`
	Edits              []fileEditEntry `json:"edits,omitempty" jsonschema:"bounded exact replacements for editMany; all are planned against one original revision"`
	ExpectedFileSHA256 string          `json:"expectedFileSha256,omitempty" jsonschema:"full file SHA-256 from file_read; required for existing-file actions"`
	ExpectedAbsent     *bool           `json:"expectedAbsent,omitempty" jsonschema:"must be true for create and preview create"`
}

type fileEditEntry struct {
	OldText string `json:"oldText" jsonschema:"text that must occur exactly once in the original file"`
	NewText string `json:"newText" jsonschema:"replacement text; may be empty"`
}

type executionRuntimeInput struct {
	Kind         string `json:"kind" jsonschema:"host or wsl; omitted runtime defaults to host"`
	Distribution string `json:"distribution,omitempty" jsonschema:"optional WSL distribution name; only valid when kind is wsl"`
}

type shellRunInput struct {
	MachineID      string                 `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Argv           []string               `json:"argv" jsonschema:"explicit executable and arguments; no implicit shell interpolation. Windows cmd example: [\"cmd.exe\",\"/d\",\"/s\",\"/c\",\"mkdir\",\"V:\\\\target\"]"`
	Cwd            string                 `json:"cwd" jsonschema:"absolute working directory on the Node machine. On Windows, bare drive V: is accepted as shorthand for drive root V:\\; drive-relative forms such as V:folder remain invalid"`
	Runtime        *executionRuntimeInput `json:"runtime,omitempty" jsonschema:"optional host or WSL execution runtime; cwd remains a Windows absolute path"`
	TimeoutSeconds int64                  `json:"timeoutSeconds,omitempty" jsonschema:"0 uses the default; maximum 1800 seconds"`
	IdempotencyKey string                 `json:"idempotencyKey" jsonschema:"12-128 character key preventing duplicate process starts on retries"`
}

type jobWatchInput struct {
	MachineID   string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	JobID       string `json:"jobId" jsonschema:"opaque job ID returned by shell_run/build_control/git_control"`
	Cursor      int64  `json:"cursor,omitempty" jsonschema:"last consumed event sequence"`
	WaitSeconds int64  `json:"waitSeconds,omitempty" jsonschema:"long-poll wait from 0 to 15 seconds"`
}

type jobCancelInput struct {
	MachineID string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	JobID     string `json:"jobId" jsonschema:"opaque job ID returned by shell_run/build_control/git_control"`
}

type gitControlInput struct {
	MachineID      string   `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	RepositoryPath string   `json:"repositoryPath" jsonschema:"absolute path to the Git repository on the Node machine"`
	Action         string   `json:"action" jsonschema:"one of status,diff,stagedDiff,log,show,branches,currentBranch,worktrees,add,commit,fetch,pull,push,createWorktree,deleteWorktree"`
	Revision       string   `json:"revision,omitempty" jsonschema:"revision for show"`
	Paths          []string `json:"paths,omitempty" jsonschema:"repository-relative paths for add"`
	Message        string   `json:"message,omitempty" jsonschema:"commit message"`
	Remote         string   `json:"remote,omitempty" jsonschema:"configured remote name for network actions"`
	Branch         string   `json:"branch,omitempty" jsonschema:"branch or ref for network/worktree actions"`
	WorktreePath   string   `json:"worktreePath,omitempty" jsonschema:"absolute managed worktree path for create/deleteWorktree; create may omit to use the Fast Spider managed directory"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty" jsonschema:"required for network actions"`
}

type buildControlInput struct {
	MachineID      string                 `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Action         string                 `json:"action" jsonschema:"run"`
	Argv           []string               `json:"argv" jsonschema:"build executable and arguments"`
	Cwd            string                 `json:"cwd" jsonschema:"absolute working directory on the Node machine. On Windows, bare drive V: is accepted as shorthand for drive root V:\\; drive-relative forms such as V:folder remain invalid"`
	Runtime        *executionRuntimeInput `json:"runtime,omitempty" jsonschema:"optional host or WSL execution runtime; cwd remains a Windows absolute path"`
	TimeoutSeconds int64                  `json:"timeoutSeconds,omitempty" jsonschema:"0 uses the default; maximum 1800 seconds"`
	IdempotencyKey string                 `json:"idempotencyKey" jsonschema:"12-128 character idempotency key"`
}

type artifactGetInput struct {
	Action      string `json:"action" jsonschema:"get, uploadFile, uploadJobLog, or publishFile"`
	ArtifactID  string `json:"artifactId,omitempty" jsonschema:"artifact ID for get"`
	MachineID   string `json:"machineId,omitempty" jsonschema:"machine ID for upload/publish actions"`
	Path        string `json:"path,omitempty" jsonschema:"absolute Node file path for uploadFile or publishFile"`
	JobID       string `json:"jobId,omitempty" jsonschema:"terminal job ID for uploadJobLog"`
	LogicalName string `json:"logicalName,omitempty" jsonschema:"display file name"`
	ContentType string `json:"contentType,omitempty" jsonschema:"optional MIME type for uploadFile or publishFile"`
}

type workingContextInput struct {
	MachineID            string           `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Action               string           `json:"action" jsonschema:"get,set,clear,plan.init,plan.get,plan.list,plan.sync,task.update,markdown.list,markdown.read,markdown.append,or progress.watch"`
	ProjectPath          string           `json:"projectPath" jsonschema:"absolute project directory on the Node machine"`
	PlanID               string           `json:"planId,omitempty" jsonschema:"bounded plan identifier; omitted by legacy get/set/clear to use the default plan"`
	ExpectedRevision     string           `json:"expectedRevision,omitempty" jsonschema:"working-context revision required by CAS mutations such as task.update and plan.sync"`
	Goal                 string           `json:"goal,omitempty" jsonschema:"current development goal; required for set and plan.init"`
	Title                string           `json:"title,omitempty" jsonschema:"plan title for plan.init"`
	TargetVersion        string           `json:"targetVersion,omitempty" jsonschema:"plan target version"`
	MarkdownRoot         string           `json:"markdownRoot,omitempty" jsonschema:"project-relative Markdown workspace directory; defaults to docs/progress"`
	InitializeMarkdown   bool             `json:"initializeMarkdown,omitempty" jsonschema:"create missing default docs/progress Markdown files without replacing existing content"`
	BaselineBranch       string           `json:"baselineBranch,omitempty" jsonschema:"optional saved task baseline branch; set auto-fills from current Git when both baseline fields are omitted"`
	BaselineCommit       string           `json:"baselineCommit,omitempty" jsonschema:"optional saved task baseline commit; set auto-fills from current Git when both baseline fields are omitted"`
	Completed            []string         `json:"completed,omitempty" jsonschema:"bounded completed-work summary for set"`
	Constraints          []string         `json:"constraints,omitempty" jsonschema:"bounded active constraints for set; never put secrets here"`
	Pending              []string         `json:"pending,omitempty" jsonschema:"bounded remaining work for set"`
	KeyFiles             []string         `json:"keyFiles,omitempty" jsonschema:"project-relative or in-project absolute key file paths for set"`
	Facts                []string         `json:"facts,omitempty" jsonschema:"bounded project/task facts for set; never chat transcripts or secrets"`
	Tasks                []map[string]any `json:"tasks,omitempty" jsonschema:"plan.init task objects with id,title,status,completion,blockedReason,and evidences; maximum 500"`
	TaskID               string           `json:"taskId,omitempty" jsonschema:"task identifier for task.update"`
	TaskTitle            string           `json:"taskTitle,omitempty" jsonschema:"task title when creating or updating a task"`
	TaskStatus           string           `json:"taskStatus,omitempty" jsonschema:"pending,in_progress,blocked,or done"`
	BlockedReason        string           `json:"blockedReason,omitempty" jsonschema:"bounded blocked reason without secrets or raw upstream errors"`
	Completion           *int             `json:"completion,omitempty" jsonschema:"task completion from 0 through 100"`
	Evidence             map[string]any   `json:"evidence,omitempty" jsonschema:"optional acceptance evidence with summary,kind,and reference; maximum 32 per task"`
	MarkdownPath         string           `json:"markdownPath,omitempty" jsonschema:"project-relative .md path inside the bound workspace"`
	Content              string           `json:"content,omitempty" jsonschema:"bounded UTF-8 content for markdown.append"`
	ManagedBlock         string           `json:"managedBlock,omitempty" jsonschema:"optional managed block name to replace instead of appending"`
	ExpectedFileRevision string           `json:"expectedFileRevision,omitempty" jsonschema:"required file revision for markdown.append CAS"`
	SinceRevision        string           `json:"sinceRevision,omitempty" jsonschema:"last observed plan revision for progress.watch"`
	WaitSeconds          int              `json:"waitSeconds,omitempty" jsonschema:"progress.watch long poll from 0 to 15 seconds"`
}

type browserLocatorInput struct {
	Role   string `json:"role,omitempty" jsonschema:"accessible role locator"`
	Name   string `json:"name,omitempty" jsonschema:"accessible name used with role"`
	Label  string `json:"label,omitempty" jsonschema:"form label locator"`
	Text   string `json:"text,omitempty" jsonschema:"visible text locator"`
	TestID string `json:"testId,omitempty" jsonschema:"test id locator"`
	CSS    string `json:"css,omitempty" jsonschema:"bounded CSS locator; XPath is not supported"`
	Exact  bool   `json:"exact,omitempty" jsonschema:"require exact text/name match"`
}

type browserBatchStepInput struct {
	Action         string               `json:"action" jsonschema:"click,type,press,or wait"`
	Ref            string               `json:"ref,omitempty" jsonschema:"short-lived element ref returned by snapshot; preferred over locator"`
	Locator        *browserLocatorInput `json:"locator,omitempty" jsonschema:"fallback structured locator when a snapshot ref is unavailable"`
	Text           string               `json:"text,omitempty" jsonschema:"text for type"`
	Key            string               `json:"key,omitempty" jsonschema:"key for press"`
	State          string               `json:"state,omitempty" jsonschema:"attached,detached,visible,hidden for wait"`
	TimeoutSeconds int                  `json:"timeoutSeconds,omitempty" jsonschema:"step timeout from 1 to 30 seconds"`
}

type browserControlInput struct {
	MachineID        string                  `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Action           string                  `json:"action" jsonschema:"readiness,launch,close,page.open,page.navigate,page.close,pages.list,click,type,press,wait,batch,snapshot,screenshot,events"`
	BrowserSessionID string                  `json:"browserSessionId,omitempty" jsonschema:"opaque managed browser session ID"`
	PageID           string                  `json:"pageId,omitempty" jsonschema:"opaque managed page ID"`
	Engine           string                  `json:"engine,omitempty" jsonschema:"managed chromium"`
	Headed           bool                    `json:"headed,omitempty" jsonschema:"show the isolated managed browser window instead of headless mode"`
	ViewportWidth    int                     `json:"viewportWidth,omitempty" jsonschema:"viewport width from 320 to 2560"`
	ViewportHeight   int                     `json:"viewportHeight,omitempty" jsonschema:"viewport height from 240 to 1600"`
	URL              string                  `json:"url,omitempty" jsonschema:"absolute HTTP(S) URL without embedded credentials; public, localhost, private-network, WSL, Docker, LAN and development hostnames are allowed without DNS allowlisting"`
	WaitUntil        string                  `json:"waitUntil,omitempty" jsonschema:"load,domcontentloaded,networkidle, or commit"`
	Ref              string                  `json:"ref,omitempty" jsonschema:"short-lived element ref returned by snapshot; preferred for click/type/press/wait and fails fast when stale"`
	Locator          *browserLocatorInput    `json:"locator,omitempty" jsonschema:"fallback structured locator for click/type/press/wait when ref is unavailable"`
	Text             string                  `json:"text,omitempty" jsonschema:"text for type"`
	Key              string                  `json:"key,omitempty" jsonschema:"key for press"`
	State            string                  `json:"state,omitempty" jsonschema:"attached,detached,visible,hidden for wait"`
	Steps            []browserBatchStepInput `json:"steps,omitempty" jsonschema:"1-32 fixed browser actions executed inside the Node in one batch"`
	SnapshotAfter    bool                    `json:"snapshotAfter,omitempty" jsonschema:"for batch, return a fresh accessibility snapshot and refs after all steps succeed"`
	TimeoutSeconds   int                     `json:"timeoutSeconds,omitempty" jsonschema:"action timeout from 1 to 30 seconds"`
	FullPage         bool                    `json:"fullPage,omitempty" jsonschema:"capture the full page when within pixel limits"`
	Format           string                  `json:"format,omitempty" jsonschema:"png or jpeg"`
	Quality          int                     `json:"quality,omitempty" jsonschema:"jpeg quality 20-95"`
	Cursor           int64                   `json:"cursor,omitempty" jsonschema:"last browser event cursor for events"`
}

type screenshotTakeInput struct {
	MachineID    string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Action       string `json:"action" jsonschema:"listDisplays, desktop, display, listWindows, or window"`
	DisplayIndex int    `json:"displayIndex,omitempty" jsonschema:"zero-based active display index for action=display"`
	WindowID     string `json:"windowId,omitempty" jsonschema:"opaque short-lived window ID returned by listWindows for action=window"`
	Format       string `json:"format,omitempty" jsonschema:"png or jpeg; png is the default"`
	Quality      int    `json:"quality,omitempty" jsonschema:"jpeg quality 20-95"`
}

type aiControlInput struct {
	MachineID             string              `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	Action                string              `json:"action" jsonschema:"routing.status,providers.list,provider.readiness,models.list,provider.capabilities,projects.list,skills.list,hooks.list,permissions.list,plugins.list,plugins.installed,plugins.get,plugin.skill.read,mcp.status.list,session.list,session.get,session.create,session.send,session.steer,session.respond,session.watch,session.cancel,session.result,session.rename,session.archive,session.unarchive,session.delete,session.fork,session.compact,session.rollback,session.goal.get,session.goal.set,session.goal.clear,session.settings.update,session.review"`
	ProviderID            string              `json:"providerId,omitempty" jsonschema:"AI harness provider ID; defaults to codex"`
	AppType               string              `json:"appType,omitempty" jsonschema:"routing.status app scope: claude,codex,or claude-desktop; omit to inspect all supported CC Switch routes"`
	SessionID             string              `json:"sessionId,omitempty" jsonschema:"opaque provider session ID; optional thread scope for mcp.status.list"`
	TurnID                string              `json:"turnId,omitempty" jsonschema:"active turn ID for cancel and required expected active turn ID for session.steer"`
	RequestID             string              `json:"requestId,omitempty" jsonschema:"pending Codex server request ID required for session.respond"`
	IdempotencyKey        string              `json:"idempotencyKey,omitempty" jsonschema:"12-128 character key required for session.create; with session.delete and no sessionId, identifies an unresolved create reservation to release"`
	Mode                  string              `json:"mode,omitempty" jsonschema:"provider.readiness mode: passive or safe"`
	Prompt                string              `json:"prompt,omitempty" jsonschema:"text input for session.create/session.send/session.steer; a turn may instead contain skills/images/localImages/mentions"`
	WorkingDirectory      string              `json:"workingDirectory,omitempty" jsonschema:"absolute working directory on the Node machine; required for session.create and used by session.list/send/fork/settings plus skills/plugins discovery when supplied"`
	Model                 string              `json:"model,omitempty" jsonschema:"optional provider model ID"`
	Thinking              string              `json:"thinking,omitempty" jsonschema:"optional provider reasoning effort for session.create/session.send"`
	Cursor                int64               `json:"cursor,omitempty" jsonschema:"last consumed normalized event sequence"`
	WaitSeconds           int64               `json:"waitSeconds,omitempty" jsonschema:"session.watch long-poll from 0 to 15 seconds"`
	Limit                 int                 `json:"limit,omitempty" jsonschema:"session.list maximum, default 50 and maximum 100"`
	Name                  string              `json:"name,omitempty" jsonschema:"new session name for session.rename"`
	ForceReload           bool                `json:"forceReload,omitempty" jsonschema:"skills.list only; bypass the local Codex skill cache"`
	MarketplaceKinds      []string            `json:"marketplaceKinds,omitempty" jsonschema:"plugins.list filter: local,vertical,workspace-directory,shared-with-me,created-by-me-remote"`
	PluginName            string              `json:"pluginName,omitempty" jsonschema:"plugin name required for plugins.get"`
	MarketplacePath       string              `json:"marketplacePath,omitempty" jsonschema:"optional absolute local marketplace path for plugins.get"`
	RemoteMarketplaceName string              `json:"remoteMarketplaceName,omitempty" jsonschema:"remote marketplace name for plugins.get or plugin.skill.read"`
	RemotePluginID        string              `json:"remotePluginId,omitempty" jsonschema:"remote plugin identifier required for plugin.skill.read"`
	SkillName             string              `json:"skillName,omitempty" jsonschema:"skill name required for plugin.skill.read"`
	NumTurns              int                 `json:"numTurns,omitempty" jsonschema:"session.rollback only; number of trailing Codex turns to remove, 1-1000; does not revert working-tree changes"`
	Objective             string              `json:"objective,omitempty" jsonschema:"goal objective for session.goal.set"`
	GoalStatus            string              `json:"goalStatus,omitempty" jsonschema:"session.goal.set status: active,paused,blocked,usageLimited,budgetLimited,complete"`
	TokenBudget           int64               `json:"tokenBudget,omitempty" jsonschema:"optional non-negative Codex goal token budget"`
	Skills                []map[string]string `json:"skills,omitempty" jsonschema:"native Codex skill inputs with name and absolute path for session.create/session.send/session.steer"`
	Images                []string            `json:"images,omitempty" jsonschema:"absolute http(s) image URLs for session.create/session.send/session.steer"`
	LocalImages           []string            `json:"localImages,omitempty" jsonschema:"absolute local image paths for session.create/session.send/session.steer"`
	Mentions              []map[string]string `json:"mentions,omitempty" jsonschema:"native Codex mention inputs with name and absolute path for session.create/session.send/session.steer"`
	ImageDetail           string              `json:"imageDetail,omitempty" jsonschema:"image detail for all image/localImage inputs: auto,low,high,original"`
	OutputSchema          map[string]any      `json:"outputSchema,omitempty" jsonschema:"bounded JSON Schema object constraining the final assistant message for session.create/session.send"`
	Decision              string              `json:"decision,omitempty" jsonschema:"session.respond approval/elicitation decision accept,decline,cancel; or confirm_not_created for session.delete by idempotencyKey after reconciling session.list"`
	Answers               map[string][]string `json:"answers,omitempty" jsonschema:"session.respond answers keyed by Codex request_user_input question ID"`
	ResponseContent       map[string]any      `json:"responseContent,omitempty" jsonschema:"session.respond structured content when accepting an MCP elicitation"`
	PageCursor            string              `json:"pageCursor,omitempty" jsonschema:"opaque pagination cursor for permissions.list or mcp.status.list"`
	MCPDetail             string              `json:"mcpDetail,omitempty" jsonschema:"mcp.status.list detail: full or toolsAndAuthOnly"`
	Effort                string              `json:"effort,omitempty" jsonschema:"session.settings.update reasoning effort: low,medium,high,xhigh"`
	Permissions           string              `json:"permissions,omitempty" jsonschema:"session.settings.update named Codex permission profile ID"`
	Personality           string              `json:"personality,omitempty" jsonschema:"session.create/session.send turn override or session.settings.update personality: none,friendly,pragmatic"`
	ServiceTier           string              `json:"serviceTier,omitempty" jsonschema:"session.create/session.send turn override or session.settings.update service tier"`
	Summary               string              `json:"summary,omitempty" jsonschema:"session.create/session.send turn override or session.settings.update reasoning summary: auto,concise,detailed,none"`
	ReviewType            string              `json:"reviewType,omitempty" jsonschema:"session.review target: uncommittedChanges,baseBranch,commit,custom; defaults to uncommittedChanges"`
	ReviewDelivery        string              `json:"reviewDelivery,omitempty" jsonschema:"session.review delivery: inline or detached; defaults to inline"`
	ReviewBranch          string              `json:"reviewBranch,omitempty" jsonschema:"branch required for reviewType=baseBranch"`
	ReviewSHA             string              `json:"reviewSha,omitempty" jsonschema:"commit SHA required for reviewType=commit"`
	ReviewTitle           string              `json:"reviewTitle,omitempty" jsonschema:"optional title for reviewType=commit"`
	ReviewInstructions    string              `json:"reviewInstructions,omitempty" jsonschema:"instructions required for reviewType=custom"`
}

type genericCapabilityOutput struct {
	Result map[string]any `json:"result"`
}

type mcpMachine struct {
	MachineID            string                            `json:"machineId"`
	DisplayName          string                            `json:"displayName"`
	AdminNote            string                            `json:"adminNote,omitempty"`
	Status               string                            `json:"status"`
	Online               bool                              `json:"online"`
	RuntimeStatus        string                            `json:"runtimeStatus,omitempty"`
	OS                   string                            `json:"os"`
	Arch                 string                            `json:"arch"`
	NodeVersion          string                            `json:"nodeVersion"`
	Generation           int64                             `json:"generation"`
	LastSeenAt           string                            `json:"lastSeenAt,omitempty"`
	RegistrationMode     string                            `json:"registrationMode"`
	ConfigurationScope   string                            `json:"configurationScope"`
	RuntimeCredential    string                            `json:"runtimeCredential"`
	ConnectionTokenSaved bool                              `json:"connectionTokenSaved"`
	Capabilities         []protocolv1.CapabilityDescriptor `json:"capabilities,omitempty"`
}

type machineListOutput struct {
	Machines []mcpMachine `json:"machines"`
}

type machineGetOutput struct {
	Machine mcpMachine `json:"machine"`
}

type capabilityListOutput struct {
	Capabilities []protocolv1.CapabilityDescriptor `json:"capabilities"`
}

type fileReadOutput struct {
	RequestID       string           `json:"requestId,omitempty"`
	TraceID         string           `json:"traceId,omitempty"`
	Path            string           `json:"path"`
	Content         *string          `json:"content,omitempty"`
	Offset          int64            `json:"offset"`
	BytesRead       int64            `json:"bytesRead"`
	SourceBytesRead int64            `json:"sourceBytesRead,omitempty"`
	Size            int64            `json:"size"`
	LineStart       int              `json:"lineStart,omitempty"`
	LineEnd         int              `json:"lineEnd,omitempty"`
	StatOnly        bool             `json:"statOnly,omitempty"`
	Truncated       bool             `json:"truncated"`
	ChunkSHA256     string           `json:"chunkSha256,omitempty"`
	FileSHA256      string           `json:"fileSha256"`
	Encoding        string           `json:"encoding"`
	Timing          capabilityTiming `json:"timing"`
}

type fileEditOutput struct {
	RequestID     string         `json:"requestId,omitempty"`
	TraceID       string         `json:"traceId,omitempty"`
	Success       bool           `json:"success"`
	Changed       bool           `json:"changed"`
	Path          string         `json:"path"`
	Operation     string         `json:"operation"`
	Preview       bool           `json:"preview,omitempty"`
	EditsApplied  int            `json:"editsApplied"`
	OldSHA256     string         `json:"oldSha256,omitempty"`
	NewSHA256     string         `json:"newSha256"`
	BytesChanged  int64          `json:"bytesChanged"`
	LineDelta     int            `json:"lineDelta"`
	Timing        fileEditTiming `json:"timing"`
	Warnings      []string       `json:"warnings,omitempty"`
	Diff          string         `json:"diff,omitempty"`
	DiffTruncated bool           `json:"diffTruncated,omitempty"`
}

type fileEditTiming struct {
	TotalMs          int64 `json:"totalMs"`
	NodeExecutionMs  int64 `json:"nodeExecutionMs"`
	HubPreDispatchMs int64 `json:"hubPreDispatchMs"`
	NodeRoundTripMs  int64 `json:"nodeRoundTripMs"`
	HubTotalMs       int64 `json:"hubTotalMs"`
}

type capabilityTiming struct {
	NodeExecutionMs  int64 `json:"nodeExecutionMs"`
	HubPreDispatchMs int64 `json:"hubPreDispatchMs"`
	NodeRoundTripMs  int64 `json:"nodeRoundTripMs"`
	HubTotalMs       int64 `json:"hubTotalMs"`
}

type mcpJobEvent struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Timestamp string `json:"timestamp"`
}

type jobOutput struct {
	JobID           string        `json:"jobId"`
	RequestID       string        `json:"requestId,omitempty"`
	TraceID         string        `json:"traceId,omitempty"`
	CallRequestID   string        `json:"callRequestId,omitempty"`
	CallTraceID     string        `json:"callTraceId,omitempty"`
	Runtime         string        `json:"runtime"`
	State           string        `json:"state"`
	ExitCode        *int          `json:"exitCode,omitempty"`
	Error           string        `json:"error,omitempty"`
	Events          []mcpJobEvent `json:"events"`
	NextCursor      int64         `json:"nextCursor"`
	TruncatedBefore int64         `json:"truncatedBefore,omitempty"`
	StartedAt       string        `json:"startedAt"`
	FinishedAt      string        `json:"finishedAt,omitempty"`
	Timing          jobTiming     `json:"timing"`
}

type jobTiming struct {
	NodeReceivedAt   string `json:"nodeReceivedAt"`
	ProcessStartedAt string `json:"processStartedAt"`
	FinishedAt       string `json:"finishedAt,omitempty"`
	QueueMs          int64  `json:"queueMs"`
	RunMs            int64  `json:"runMs,omitempty"`
	NodeExecutionMs  int64  `json:"nodeExecutionMs"`
	HubPreDispatchMs int64  `json:"hubPreDispatchMs"`
	NodeRoundTripMs  int64  `json:"nodeRoundTripMs"`
	HubTotalMs       int64  `json:"hubTotalMs"`
}

type codeSearchMatch struct {
	Path   string                  `json:"path"`
	Line   int                     `json:"line"`
	Column int                     `json:"column"`
	Text   string                  `json:"text"`
	Before []codeSearchContextLine `json:"before,omitempty"`
	After  []codeSearchContextLine `json:"after,omitempty"`
}

type codeSearchContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type codeSearchOutput struct {
	RequestID         string            `json:"requestId,omitempty"`
	TraceID           string            `json:"traceId,omitempty"`
	Matches           []codeSearchMatch `json:"matches"`
	Files             []string          `json:"files,omitempty"`
	ScannedFiles      int               `json:"scannedFiles"`
	MatchedFiles      int               `json:"matchedFiles"`
	BytesScanned      int64             `json:"bytesScanned"`
	MatchCount        int               `json:"matchCount"`
	SkippedFiles      int               `json:"skippedFiles,omitempty"`
	SkipReasons       map[string]int    `json:"skipReasons,omitempty"`
	Incomplete        bool              `json:"incomplete,omitempty"`
	Engine            string            `json:"engine"`
	FallbackReason    string            `json:"fallbackReason,omitempty"`
	PrimaryElapsedMs  int64             `json:"primaryElapsedMs"`
	FallbackElapsedMs int64             `json:"fallbackElapsedMs"`
	ElapsedMs         int64             `json:"elapsedMs"`
	Truncated         bool              `json:"truncated"`
	Timing            capabilityTiming  `json:"timing"`
}

func (s *Server) newMCPHandler() http.Handler {
	base := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		tokenInfo := auth.TokenInfoFromContext(r.Context())
		if tokenInfo == nil || tokenInfo.UserID == "" {
			return nil
		}
		return s.mcpServerFor(tokenInfo.UserID)
	}, &mcp.StreamableHTTPOptions{
		JSONResponse:               true,
		Stateless:                  true,
		DisableLocalhostProtection: true, // Hub is intentionally loopback-only behind a TLS reverse proxy; hostGuard enforces the configured public Host allowlist.
	})
	limited := http.MaxBytesHandler(base, maxControlMessageBytes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataURL, err := s.oauthResourceMetadataURL(r)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", err.Error())
			return
		}
		middleware := auth.RequireBearerToken(s.mcpTokenVerifier, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: metadataURL,
			Scopes:              []string{oauthScope},
		})
		middleware(limited).ServeHTTP(w, r)
	})
}

func (s *Server) mcpServerFor(ownerID string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "fast-spider", Title: "Fast Spider", Version: s.service.Version()}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_list",
		Description: "List Fast Spider machines owned by the authenticated owner, including online state, connection-token registration mode, local configuration scope, runtime credential mode and negotiated capabilities. Connection-token secrets are never returned.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ machineListInput) (*mcp.CallToolResult, machineListOutput, error) {
		machines, err := s.service.ListMachines(ctx, ownerID)
		if err != nil {
			return nil, machineListOutput{}, err
		}
		out := machineListOutput{Machines: make([]mcpMachine, 0, len(machines))}
		for _, machine := range machines {
			out.Machines = append(out.Machines, toMCPMachine(machine))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_get",
		Description: "Get one Fast Spider machine by opaque machineId, including its registration/runtime credential model. This never returns connection-token secrets or accepts a local filesystem path.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input machineGetInput) (*mcp.CallToolResult, machineGetOutput, error) {
		machine, err := s.service.GetMachine(ctx, ownerID, input.MachineID)
		if err != nil {
			return nil, machineGetOutput{}, err
		}
		return nil, machineGetOutput{Machine: toMCPMachine(machine)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "capability_list",
		Description: "List the fixed Fast Spider capability catalog, or capabilities currently reported by a specific machine.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input capabilityListInput) (*mcp.CallToolResult, capabilityListOutput, error) {
		if input.MachineID == "" {
			return nil, capabilityListOutput{Capabilities: s.service.CapabilityCatalog()}, nil
		}
		machine, err := s.service.GetMachine(ctx, ownerID, input.MachineID)
		if err != nil {
			return nil, capabilityListOutput{}, err
		}
		return nil, capabilityListOutput{Capabilities: machine.Capabilities}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_read",
		Description: "Read a bounded byte or line selection, or stat and hash a regular UTF-8 text file at an absolute path on the selected Node. Selectors are mutually exclusive.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input fileReadInput) (*mcp.CallToolResult, fileReadOutput, error) {
		params := map[string]any{"path": input.Path}
		addOptionalFileReadParam(params, "offset", input.Offset)
		addOptionalFileReadParam(params, "limit", input.Limit)
		addOptionalFileReadParam(params, "lineStart", input.LineStart)
		addOptionalFileReadParam(params, "lineCount", input.LineCount)
		addOptionalFileReadParam(params, "headLines", input.HeadLines)
		addOptionalFileReadParam(params, "tailLines", input.TailLines)
		addOptionalFileReadParam(params, "aroundLine", input.AroundLine)
		addOptionalFileReadParam(params, "contextLines", input.ContextLines)
		addOptionalFileReadParam(params, "statOnly", input.StatOnly)
		addOptionalFileReadParam(params, "includeLineNumbers", input.IncludeLineNumbers)
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "file.read", "read", params)
		if err != nil {
			return nil, fileReadOutput{}, err
		}
		var out fileReadOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, fileReadOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "code_search",
		Description: "Search content or matching-content file paths below an absolute Node directory using a managed ripgrep component with a safe native fallback.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input codeSearchInput) (*mcp.CallToolResult, codeSearchOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "code.search", "search", map[string]any{
			"query": input.Query, "path": input.Path, "mode": input.Mode, "regex": input.Regex, "ignoreCase": input.IgnoreCase,
			"include": input.Include, "exclude": input.Exclude, "context": input.Context, "beforeContext": input.BeforeContext,
			"afterContext": input.AfterContext, "limit": input.Limit,
		})
		if err != nil {
			return nil, codeSearchOutput{}, err
		}
		adaptRollingCodeSearchResult(result)
		var out codeSearchOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, codeSearchOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_edit",
		Description: "Create, exactly replace, batch-edit, or preview a bounded UTF-8 file change on a Node. Existing-file writes use optimistic concurrency; preview never writes.",
		Annotations: toolAnnotations(false, true, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input fileEditInput) (*mcp.CallToolResult, fileEditOutput, error) {
		action := input.Action
		if action == "" {
			action = "edit"
		}
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "file.write", action, map[string]any{
			"path": input.Path, "previewOf": input.PreviewOf, "content": input.Content,
			"oldText": input.OldText, "newText": input.NewText, "edits": input.Edits,
			"expectedFileSha256": input.ExpectedFileSHA256, "expectedAbsent": input.ExpectedAbsent,
		})
		if err != nil {
			return nil, fileEditOutput{}, err
		}
		adaptRollingFileEditResult(result, action)
		var out fileEditOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, fileEditOutput{}, err
		}
		if action != "preview" {
			// Keep MCP lean during rolling upgrades even when an older Node still sends diff text.
			out.Diff = ""
			out.DiffTruncated = false
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "shell_run",
		Description: "Start a bounded non-interactive process on the selected Node using an explicit argv array and absolute cwd. The process runs as the same OS user as Fast Spider Node.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input shellRunInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "shell.exec", "run", map[string]any{"argv": input.Argv, "cwd": input.Cwd, "runtime": input.Runtime, "timeoutSeconds": input.TimeoutSeconds, "idempotencyKey": input.IdempotencyKey})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_watch",
		Description: "Read bounded stdout/stderr/status events for one Node job after a cursor, optionally long-polling for up to 15 seconds.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobWatchInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "job.control", "watch", map[string]any{"jobId": input.JobID, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_cancel",
		Description: "Cancel one active Node job and terminate its process tree. Repeated cancellation of a terminal job is safe.",
		Annotations: toolAnnotations(false, true, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobCancelInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "job.control", "cancel", map[string]any{"jobId": input.JobID})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "git_control",
		Description: "Run one allowlisted system-Git action in an absolute repository path on the selected Node. It runs with the same OS-user permissions as Fast Spider Node.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input gitControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "git.repository", input.Action, map[string]any{
			"repositoryPath": input.RepositoryPath, "revision": input.Revision, "paths": input.Paths, "message": input.Message, "remote": input.Remote,
			"branch": input.Branch, "worktreePath": input.WorktreePath, "idempotencyKey": input.IdempotencyKey,
		})
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "build_control",
		Description: "Run one bounded build/test command on the selected Node using an explicit argv array and absolute cwd.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input buildControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "build.exec", input.Action, map[string]any{"argv": input.Argv, "cwd": input.Cwd, "runtime": input.Runtime, "timeoutSeconds": input.TimeoutSeconds, "idempotencyKey": input.IdempotencyKey})
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "browser_control",
		Description: "Control one Node-managed isolated Chromium session with accessibility snapshots, short-lived element refs, fallback semantic locators, and bounded batch actions. Prefer snapshot refs over screenshots/selectors for interaction. Public, localhost and private-network targets are allowed without DNS allowlisting; raw CDP/Playwright execution is never exposed.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input browserControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := browserControlParams(input)
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "browser.automation", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return s.presentationToolResult(ctx, ownerID, result), genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "screenshot_take",
		Description: "Capture a one-time desktop, display, or window image on the selected Node. Captured images use the Hub temporary presentation relay, return native MCP image content, and never expose local paths.",
		Annotations: toolAnnotations(false, false, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input screenshotTakeInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := map[string]any{"displayIndex": input.DisplayIndex, "windowId": input.WindowID, "format": input.Format, "quality": input.Quality}
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "screenshot.capture", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return s.presentationToolResult(ctx, ownerID, result), genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ai_control",
		Description: "Discover AI harnesses, CC Switch routing/upstream model facts, and control supported local provider sessions through the Node. Current harnesses are Codex and Claude Code; credentials and raw CC Switch provider settings remain local.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input aiControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := map[string]any{
			"providerId": input.ProviderID, "appType": input.AppType, "sessionId": input.SessionID, "turnId": input.TurnID, "requestId": input.RequestID,
			"idempotencyKey": input.IdempotencyKey, "mode": input.Mode,
			"prompt": input.Prompt, "workingDirectory": input.WorkingDirectory, "model": input.Model,
			"thinking": input.Thinking, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds,
			"limit": input.Limit, "pageCursor": input.PageCursor, "mcpDetail": input.MCPDetail, "name": input.Name, "forceReload": input.ForceReload,
			"marketplaceKinds": input.MarketplaceKinds, "pluginName": input.PluginName, "marketplacePath": input.MarketplacePath,
			"remoteMarketplaceName": input.RemoteMarketplaceName, "remotePluginId": input.RemotePluginID, "skillName": input.SkillName,
			"numTurns": input.NumTurns, "objective": input.Objective, "goalStatus": input.GoalStatus, "tokenBudget": input.TokenBudget,
			"skills": input.Skills, "images": input.Images, "localImages": input.LocalImages, "mentions": input.Mentions, "imageDetail": input.ImageDetail, "outputSchema": input.OutputSchema,
			"decision": input.Decision, "answers": input.Answers, "responseContent": input.ResponseContent,
			"effort": input.Effort, "permissions": input.Permissions, "personality": input.Personality, "serviceTier": input.ServiceTier, "summary": input.Summary,
			"reviewType": input.ReviewType, "reviewDelivery": input.ReviewDelivery, "reviewBranch": input.ReviewBranch,
			"reviewSha": input.ReviewSHA, "reviewTitle": input.ReviewTitle, "reviewInstructions": input.ReviewInstructions,
		}
		if input.Action == "session.create" && (len(input.IdempotencyKey) < 12 || len(input.IdempotencyKey) > 128) {
			return nil, genericCapabilityOutput{}, fmt.Errorf("idempotencyKey is required for session.create and must be 12 to 128 characters")
		}
		if len(input.Skills) > 0 {
			converted := make([]map[string]any, len(input.Skills))
			for i, item := range input.Skills {
				converted[i] = map[string]any{"name": item["name"], "path": item["path"]}
			}
			params["skills"] = converted
		}
		if len(input.Mentions) > 0 {
			converted := make([]map[string]any, len(input.Mentions))
			for i, item := range input.Mentions {
				converted[i] = map[string]any{"name": item["name"], "path": item["path"]}
			}
			params["mentions"] = converted
		}
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "agent.control", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "working_context",
		Description: "Manage project-scoped Working Context plans, tasks, acceptance evidence, and a bounded in-project Markdown task workspace on the selected Node. Legacy get/set/clear use the default plan; reads include live Git facts. Secrets, full prompts, chat transcripts, and raw upstream errors are not accepted.",
		Annotations: toolAnnotations(false, false, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workingContextInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := map[string]any{
			"projectPath": input.ProjectPath, "goal": input.Goal,
			"planId": input.PlanID, "expectedRevision": input.ExpectedRevision, "title": input.Title,
			"targetVersion": input.TargetVersion, "markdownRoot": input.MarkdownRoot, "initializeMarkdown": input.InitializeMarkdown,
			"baselineBranch": input.BaselineBranch, "baselineCommit": input.BaselineCommit,
			"completed": input.Completed, "constraints": input.Constraints, "pending": input.Pending,
			"keyFiles": input.KeyFiles, "facts": input.Facts,
			"tasks": input.Tasks, "taskId": input.TaskID, "taskTitle": input.TaskTitle, "taskStatus": input.TaskStatus,
			"blockedReason": input.BlockedReason, "completion": input.Completion, "evidence": input.Evidence,
			"markdownPath": input.MarkdownPath, "content": input.Content, "managedBlock": input.ManagedBlock,
			"expectedFileRevision": input.ExpectedFileRevision, "sinceRevision": input.SinceRevision, "waitSeconds": input.WaitSeconds,
		}
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "working.context", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "artifact_get",
		Description: "Get Artifact metadata/content, ask a Node to upload a file or Job log into Hub Artifact storage, or publish an absolute-path file through the Hub temporary presentation relay without creating a Hub Artifact record.",
		Annotations: toolAnnotations(false, false, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input artifactGetInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		switch input.Action {
		case "uploadFile", "uploadJobLog":
			params := map[string]any{"path": input.Path, "jobId": input.JobID, "logicalName": input.LogicalName, "contentType": input.ContentType}
			result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "artifact.store", input.Action, params)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			return nil, genericCapabilityOutput{Result: result}, nil
		case "publishFile":
			params := map[string]any{"path": input.Path, "logicalName": input.LogicalName, "contentType": input.ContentType}
			result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "artifact.store", input.Action, params)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			return s.presentationToolResult(ctx, ownerID, result), genericCapabilityOutput{Result: result}, nil
		case "get":
			artifact, err := s.service.GetArtifact(ctx, ownerID, input.ArtifactID)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			raw, err := json.Marshal(artifact)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			result["downloadPath"] = "/api/v1/artifacts/" + artifact.ID + "/content"
			if content, ok, err := readArtifactInline(ctx, s.service, artifact); err != nil {
				return nil, genericCapabilityOutput{}, err
			} else if ok {
				result["content"] = content
				result["encoding"] = "utf-8"
			}
			return nil, genericCapabilityOutput{Result: result}, nil
		default:
			return nil, genericCapabilityOutput{}, fmt.Errorf("unsupported artifact action %q", input.Action)
		}
	})

	return server
}

func adaptRollingCodeSearchResult(result map[string]any) {
	legacyReasons := map[string]string{
		"platform_unsupported": "RG_NOT_FOUND", "component_missing": "RG_NOT_FOUND", "component_invalid": "RG_NOT_FOUND",
		"start_failed": "RG_START_FAILED", "command_failed": "RG_EXIT_ERROR", "output_limit": "RG_OUTPUT_LIMIT", "output_invalid": "RG_OUTPUT_INVALID",
	}
	if reason, _ := result["fallbackReason"].(string); reason != "" {
		if stable := legacyReasons[reason]; stable != "" {
			result["fallbackReason"] = stable
		}
	}
}

func adaptRollingFileEditResult(result map[string]any, requestedAction string) {
	if _, current := result["newSha256"]; current {
		return
	}
	afterSHA, legacy := result["afterSha256"].(string)
	if !legacy || afterSHA == "" {
		return
	}
	operation, _ := result["action"].(string)
	if operation == "" {
		operation = requestedAction
	}
	if operation == "preview" {
		result["preview"] = true
		if previewOf, _ := result["previewOf"].(string); previewOf != "" {
			operation = previewOf
		}
	}
	result["success"] = true
	result["operation"] = operation
	result["oldSha256"] = result["beforeSha256"]
	result["newSha256"] = afterSHA
	if editCount, ok := numberAsInt64(result["editCount"]); ok {
		result["editsApplied"] = editCount
	} else {
		result["editsApplied"] = int64(0)
	}
	result["bytesChanged"] = int64(0)
	if operation == "create" {
		if bytes, ok := numberAsInt64(result["bytes"]); ok {
			result["bytesChanged"] = bytes
		}
	}
	result["lineDelta"] = int64(0)
	result["warnings"] = []string{"rolling_upgrade_metadata_partial"}
	delete(result, "action")
	delete(result, "previewOf")
	delete(result, "beforeSha256")
	delete(result, "afterSha256")
	delete(result, "bytes")
	delete(result, "editCount")
}

const maxMCPPresentationImageBytes int64 = 8 << 20

func (s *Server) presentationToolResult(ctx context.Context, ownerID string, result map[string]any) *mcp.CallToolResult {
	presentationID, _ := result["presentationId"].(string)
	presentationID = strings.TrimSpace(presentationID)
	if presentationID == "" {
		return nil
	}
	record, err := s.presentations.getForOwner(ownerID, presentationID, time.Now().UTC())
	if err != nil {
		return nil
	}
	publicURL := s.presentationPublicURL(record.ID)
	result["fileName"] = record.FileName
	result["contentType"] = record.ContentType
	result["sizeBytes"] = record.SizeBytes
	result["sha256"] = record.SHA256
	result["expiresAt"] = record.ExpiresAt
	if publicURL != "" {
		result["publicUrl"] = publicURL
	}

	content := make([]mcp.Content, 0, 2)
	if imageMIME, ok := presentationImageMIME(record.ContentType); ok {
		if data, readErr := readPresentationImage(ctx, record, maxMCPPresentationImageBytes); readErr == nil && len(data) > 0 {
			if verifyPresentationImageBytes(imageMIME, data) == nil {
				content = append(content, &mcp.ImageContent{Data: data, MIMEType: imageMIME})
			}
		}
	}
	if publicURL != "" {
		size := record.SizeBytes
		content = append(content, &mcp.ResourceLink{
			URI: publicURL, Name: record.FileName, Title: record.FileName,
			Description: "Fast Spider temporary presentation resource", MIMEType: record.ContentType, Size: &size,
		})
	}
	if len(content) == 0 {
		return nil
	}
	return &mcp.CallToolResult{Content: content}
}

func presentationImageMIME(contentType string) (string, bool) {
	mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mimeType {
	case "image/png", "image/jpeg":
		return mimeType, true
	default:
		return "", false
	}
}

func numberAsInt64(value any) (int64, bool) {
	switch current := value.(type) {
	case int64:
		return current, true
	case int:
		return int64(current), true
	case float64:
		return int64(current), current == float64(int64(current))
	case json.Number:
		parsed, err := current.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func toolAnnotations(readOnly, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
}

func browserControlParams(input browserControlInput) map[string]any {
	locatorParams := func(locator *browserLocatorInput) map[string]any {
		if locator == nil {
			return nil
		}
		return map[string]any{
			"role": locator.Role, "name": locator.Name, "label": locator.Label,
			"text": locator.Text, "testId": locator.TestID, "css": locator.CSS, "exact": locator.Exact,
		}
	}

	params := map[string]any{}
	if input.Action != "launch" && input.Action != "readiness" {
		params["browserSessionId"] = input.BrowserSessionID
	}
	if input.PageID != "" {
		params["pageId"] = input.PageID
	}
	if input.TimeoutSeconds > 0 {
		params["timeoutMs"] = input.TimeoutSeconds * 1000
	}
	switch input.Action {
	case "launch":
		params["engine"] = input.Engine
		params["headless"] = !input.Headed
		if input.ViewportWidth > 0 || input.ViewportHeight > 0 {
			width, height := input.ViewportWidth, input.ViewportHeight
			if width == 0 {
				width = 1280
			}
			if height == 0 {
				height = 720
			}
			params["viewport"] = map[string]any{"width": width, "height": height}
		}
	case "page.open", "page.navigate":
		params["url"] = input.URL
		params["waitUntil"] = input.WaitUntil
	case "click", "type", "press", "wait":
		if input.Ref != "" {
			params["ref"] = input.Ref
		}
		if locator := locatorParams(input.Locator); locator != nil {
			params["locator"] = locator
		}
		if input.Action == "type" {
			params["text"] = input.Text
		}
		if input.Action == "press" {
			params["key"] = input.Key
		}
		if input.Action == "wait" {
			params["state"] = input.State
		}
	case "batch":
		steps := make([]map[string]any, 0, len(input.Steps))
		for _, step := range input.Steps {
			value := map[string]any{"action": step.Action}
			if step.Ref != "" {
				value["ref"] = step.Ref
			}
			if locator := locatorParams(step.Locator); locator != nil {
				value["locator"] = locator
			}
			if step.Action == "type" {
				value["text"] = step.Text
			}
			if step.Action == "press" {
				value["key"] = step.Key
			}
			if step.Action == "wait" {
				value["state"] = step.State
			}
			if step.TimeoutSeconds > 0 {
				value["timeoutMs"] = step.TimeoutSeconds * 1000
			}
			steps = append(steps, value)
		}
		params["steps"] = steps
		params["snapshotAfter"] = input.SnapshotAfter
	case "screenshot":
		params["fullPage"] = input.FullPage
		params["format"] = input.Format
		if input.Quality > 0 {
			params["quality"] = input.Quality
		}
	case "events":
		params["cursor"] = input.Cursor
	}
	return params
}

func decodeCapabilityResult(input map[string]any, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}

func toMCPMachine(machine core.MachineView) mcpMachine {
	out := mcpMachine{
		MachineID:            machine.MachineID,
		DisplayName:          machine.DisplayName,
		AdminNote:            machine.AdminNote,
		Status:               machine.Status,
		Online:               machine.Online,
		RuntimeStatus:        machine.RuntimeStatus,
		OS:                   machine.OS,
		Arch:                 machine.Arch,
		NodeVersion:          machine.NodeVersion,
		Generation:           machine.Generation,
		RegistrationMode:     machine.RegistrationMode,
		ConfigurationScope:   machine.ConfigurationScope,
		RuntimeCredential:    machine.RuntimeCredential,
		ConnectionTokenSaved: machine.ConnectionTokenSaved,
		Capabilities:         machine.Capabilities,
	}
	if machine.LastSeenAt != nil {
		out.LastSeenAt = protocolv1.Timestamp(*machine.LastSeenAt)
	}
	return out
}
