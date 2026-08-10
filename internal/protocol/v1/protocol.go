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
	CapabilityId: "browser.automation", Version: "1.0",
	Actions: []string{"launch", "close", "page.open", "page.navigate", "page.close", "pages.list", "click", "type", "press", "wait", "snapshot", "screenshot", "events"},
}

var ScreenshotCapability = CapabilityDescriptor{
	CapabilityId: "screenshot.capture", Version: "1.0",
	Actions: []string{"listDisplays", "desktop", "display", "listWindows", "window"},
}

var AgentCapability = CapabilityDescriptor{
	CapabilityId: "agent.control", Version: "1.0",
	Actions: []string{"providers.list", "models.list", "session.list", "session.get", "session.create", "session.send", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive"},
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
	{CapabilityId: "file.read", Version: "1.0", Actions: []string{"read"}},
	{CapabilityId: "file.write", Version: "1.0", Actions: []string{"edit"}},
	{CapabilityId: "code.search", Version: "1.0", Actions: []string{"search"}},
	{CapabilityId: "shell.exec", Version: "1.0", Actions: []string{"run"}},
	{CapabilityId: "job.control", Version: "1.0", Actions: []string{"watch", "cancel"}},
	{CapabilityId: "git.repository", Version: "1.0", Actions: []string{"status", "diff", "stagedDiff", "log", "show", "branches", "currentBranch", "worktrees", "add", "commit", "fetch", "pull", "push", "createWorktree", "deleteWorktree"}},
	{CapabilityId: "build.exec", Version: "1.0", Actions: []string{"run"}},
	{CapabilityId: "artifact.store", Version: "1.0", Actions: []string{"uploadFile", "uploadJobLog", "publishFile"}},
	{CapabilityId: "working.context", Version: "1.0", Actions: []string{"get", "set", "clear"}},
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
