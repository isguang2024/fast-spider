package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestDesktopCallbackLargeStateFrame(t *testing.T) {
	payload := []byte(`{"type":"broadcast","method":"thread-stream-state-changed","version":11,"params":{"change":{"conversationState":{"id":"long-lived-target","threadRuntimeStatus":{"type":"idle"},"history":"` + strings.Repeat("x", 10<<20) + `"}}}}`)
	var wire bytes.Buffer
	if err := binary.Write(&wire, binary.LittleEndian, uint32(len(payload))); err != nil {
		t.Fatal(err)
	}
	wire.Write(payload)
	// A second frame proves the larger payload is consumed at its exact boundary.
	next := []byte(`{"type":"response","requestId":"next","resultType":"success"}`)
	if err := binary.Write(&wire, binary.LittleEndian, uint32(len(next))); err != nil {
		t.Fatal(err)
	}
	wire.Write(next)
	message, err := readCodexDesktopIPCFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	state := mapValueMap(mapValueMap(message.Params, "change"), "conversationState")
	if mapString(state, "id") != "long-lived-target" || mapString(mapValueMap(state, "threadRuntimeStatus"), "type") != "idle" {
		t.Fatal("lost runtime state")
	}
	message, err = readCodexDesktopIPCFrame(&wire)
	if err != nil || message.RequestID != "next" {
		t.Fatalf("next frame: %v, %v", message, err)
	}
}

func TestDesktopCallbackInvalidFrameClassified(t *testing.T) {
	for _, size := range []uint32{0, codexDesktopIPCReceiveFrameLimit + 1} {
		var wire bytes.Buffer
		if err := binary.Write(&wire, binary.LittleEndian, size); err != nil {
			t.Fatal(err)
		}
		_, err := readCodexDesktopIPCFrame(&wire)
		if err == nil || classifyExecutionError(fmt.Errorf("read Desktop state: %w", err)) != ErrorConfigInvalid {
			t.Fatalf("size=%d err=%v", size, err)
		}
	}
	if classifyExecutionError(fmt.Errorf("wake: %w", errCodexDesktopOwnerUnavailable)) != ErrorRuntimeUnavailable {
		t.Fatal("owner failure is unclassified")
	}
}
