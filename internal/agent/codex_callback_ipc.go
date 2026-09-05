package agent

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const codexDesktopIPCHostID = "local"
const codexDesktopIPCFrameLimit = 8 << 20

// Desktop state broadcasts include the conversation history. Long-lived
// callback targets can exceed the much smaller outbound request limit.
const codexDesktopIPCReceiveFrameLimit = 64 << 20

var errCodexDesktopIPCProtocol = errors.New("Codex Desktop IPC protocol error")

type codexDesktopIPCMessage struct {
	Type              string                  `json:"type"`
	RequestID         string                  `json:"requestId,omitempty"`
	SourceClientID    string                  `json:"sourceClientId,omitempty"`
	TargetClientID    string                  `json:"targetClientId,omitempty"`
	Version           int                     `json:"version,omitempty"`
	Method            string                  `json:"method,omitempty"`
	Params            map[string]any          `json:"params,omitempty"`
	TimeoutMS         int                     `json:"timeoutMs,omitempty"`
	Request           *codexDesktopIPCMessage `json:"request,omitempty"`
	Response          map[string]any          `json:"response,omitempty"`
	ResultType        string                  `json:"resultType,omitempty"`
	HandledByClientID string                  `json:"handledByClientId,omitempty"`
	Result            map[string]any          `json:"result,omitempty"`
	Error             string                  `json:"error,omitempty"`
}

func mapValueMap(values map[string]any, key string) map[string]any {
	value, _ := values[key].(map[string]any)
	return value
}

func readCodexDesktopIPCFrame(reader io.Reader) (codexDesktopIPCMessage, error) {
	var sizeBytes [4]byte
	if _, err := io.ReadFull(reader, sizeBytes[:]); err != nil {
		return codexDesktopIPCMessage{}, err
	}
	size := binary.LittleEndian.Uint32(sizeBytes[:])
	if size == 0 || size > codexDesktopIPCReceiveFrameLimit {
		return codexDesktopIPCMessage{}, fmt.Errorf("%w: invalid frame size: %d (receive limit %d)", errCodexDesktopIPCProtocol, size, codexDesktopIPCReceiveFrameLimit)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return codexDesktopIPCMessage{}, err
	}
	var message codexDesktopIPCMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return codexDesktopIPCMessage{}, fmt.Errorf("%w: decode frame: %v", errCodexDesktopIPCProtocol, err)
	}
	return message, nil
}

func writeCodexDesktopIPCFrame(writer io.Writer, mu *sync.Mutex, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > codexDesktopIPCFrameLimit {
		return fmt.Errorf("invalid Codex Desktop IPC frame size: %d", len(payload))
	}
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	mu.Lock()
	defer mu.Unlock()
	for len(frame) > 0 {
		written, writeErr := writer.Write(frame)
		if written > 0 {
			frame = frame[written:]
		}
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
