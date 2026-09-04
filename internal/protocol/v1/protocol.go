package v1

import (
	"encoding/json"
	"fmt"
	"time"
)

//go:generate go run ../../../cmd/contractgen -schema ../../../contracts/v1/control.schema.json -out generated_types.go -package v1

const (
	ProtocolVersion = "1.0"

	MessageServerHello        = "server.hello"
	MessageClientHello        = "client.hello"
	MessageSessionEstablished = "session.established"
	MessageNodeReady          = "node.ready"
	MessageHeartbeat          = "heartbeat"
	MessageHeartbeatAck       = "heartbeat.ack"
	MessageConnectionClose    = "connection.close"
	MessageCapabilityRequest  = "capability.request"
	MessageCapabilityResponse = "capability.response"
)

var BrowserCapability = CapabilityDescriptor{
	CapabilityId: "browser.automation", Version: "1.3",
	Actions: []string{"readiness", "launch", "close", "page.open", "page.navigate", "page.close", "pages.list", "click", "type", "press", "wait", "batch", "snapshot", "screenshot", "events"},
}

var ScreenshotCapability = CapabilityDescriptor{
	CapabilityId: "screenshot.capture", Version: "1.0",
	Actions: []string{"listDisplays", "desktop", "display", "listWindows", "window"},
}

// ResultPoolCapability describes the Hub-managed Result Pool HTTP API. It is
// intentionally not part of NodeCapabilities: nodes publish through the Hub
// result endpoints and do not execute these actions via capability.request.
var ResultPoolCapability = CapabilityDescriptor{
	CapabilityId: "result.pool", Version: "1.0",
	Actions: []string{"create", "attachPage", "commit", "getManifest", "readPage", "lookup", "abort", "fail"},
}

const (
	CloudCallbackTypeLocalFile = "local_file"
	CloudCallbackTypeText      = "text"
	CloudCallbackTypeStatus    = "status"

	CloudCallbackTextMaxRunes      = 2000
	CloudCallbackTextMaxBytes      = 8 << 10
	CloudCallbackClaimMaxTextBytes = 64 << 10
)

var AgentCapability = CapabilityDescriptor{
	CapabilityId: "agent.control", Version: "1.6",
	Actions: []string{"routing.status", "providers.list", "provider.readiness", "models.list", "provider.capabilities", "projects.list", "skills.list", "hooks.list", "permissions.list", "plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read", "mcp.status.list", "session.list", "session.get", "session.create", "session.send", "session.steer", "session.respond", "session.watch", "session.callback.register", "session.callback.arm", "session.callback.enqueue", "session.callback.unregister", "session.callback.list", "session.callback.claim", "session.callback.ack", "session.cancel", "session.result", "session.rename", "session.archive", "session.unarchive", "session.delete", "session.fork", "session.compact", "session.rollback", "session.goal.get", "session.goal.set", "session.goal.clear", "session.settings.update", "session.review"},
}

func ScreenshotCapabilityForOS(goos string) CapabilityDescriptor {
	capability := ScreenshotCapability
	if goos != "windows" {
		capability.Actions = []string{"listDisplays", "desktop", "display"}
	} else {
		capability.Actions = append([]string(nil), ScreenshotCapability.Actions...)
	}
	return capability
}

var NodeCapabilities = []CapabilityDescriptor{
	{CapabilityId: "machine.status", Version: "1.0", Actions: []string{"report"}},
	{CapabilityId: "file.read", Version: "2.0", Actions: []string{"read"}},
	{CapabilityId: "file.write", Version: "2.1", Actions: []string{"edit", "create", "replace", "editMany", "preview"}},
	{CapabilityId: "code.search", Version: "2.1", Actions: []string{"search"}},
	{CapabilityId: "shell.exec", Version: "1.1", Actions: []string{"run"}},
	{CapabilityId: "job.control", Version: "1.1", Actions: []string{"watch", "cancel"}},
	{CapabilityId: "git.repository", Version: "1.0", Actions: []string{"status", "diff", "stagedDiff", "log", "show", "branches", "currentBranch", "worktrees", "add", "commit", "fetch", "pull", "push", "createWorktree", "deleteWorktree"}},
	{CapabilityId: "build.exec", Version: "1.1", Actions: []string{"run"}},
	{CapabilityId: "artifact.store", Version: "1.0", Actions: []string{"uploadFile", "uploadJobLog", "publishFile"}},
	{CapabilityId: "operation.log", Version: "1.0", Actions: []string{"query"}},
	{CapabilityId: "working.context", Version: "2.0", Actions: []string{"get", "set", "clear"}},
	AgentCapability,
}

type messageHeader struct {
	MessageType string `json:"messageType"`
}

func MessageType(raw []byte) (string, error) {
	var header messageHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return "", fmt.Errorf("decode message header: %w", err)
	}
	if header.MessageType == "" {
		return "", fmt.Errorf("messageType is required")
	}
	return header.MessageType, nil
}

func Timestamp(now time.Time) string {
	return now.UTC().Format(time.RFC3339Nano)
}

func ServerChallengePayload(machineID, challenge string) []byte {
	return []byte("fast-spider-server-challenge-v1\n" + machineID + "\n" + challenge)
}

func DeviceChallengePayload(machineID, challenge string) []byte {
	return []byte("fast-spider-device-challenge-v1\n" + machineID + "\n" + challenge)
}

func DeviceTokenPayload(machineID, nonce, timestamp string) []byte {
	return []byte("fast-spider-device-token-v1\n" + machineID + "\n" + nonce + "\n" + timestamp)
}
