package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// A status label is not progress. Expose only the current node's control fields
// and an opaque content fingerprint; never the transcript or a guessed job state.
func chatgptCloudActivity(detail map[string]any) map[string]any {
	current := chatgptCloudCurrentNodeID(detail)
	mapping, _ := detail["mapping"].(map[string]any)
	node, _ := mapping[current].(map[string]any)
	message, _ := node["message"].(map[string]any)
	out := map[string]any{"available": message != nil, "toolExecutionKnown": false}
	if message == nil {
		return out
	}
	out["currentNode"] = current
	out["role"] = chatgptCloudMessageRole(message)
	for _, key := range []string{"status", "channel", "recipient", "end_turn", "create_time", "update_time"} {
		if value, ok := message[key]; ok {
			out[key] = value
		}
	}
	// Exclude conversation update times, title and volatile view metadata: simply
	// opening a CHAT must not make a stuck execution look productive.
	raw, err := json.Marshal([]any{current, message["status"], message["channel"], message["recipient"], message["end_turn"], message["content"]})
	if err == nil {
		digest := sha256.Sum256(raw)
		out["fingerprint"] = hex.EncodeToString(digest[:])
	}
	return out
}
