package registry

import (
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestRegisterGenerationAndSnapshotIsolation(t *testing.T) {
	r := New()
	now := time.Now()
	first := &Connection{MachineID: "mach_test", ConnectionID: "conn_1", Generation: 1, ConnectedAt: now, LastSeenAt: now, Status: "ready"}
	if replaced, accepted := r.Register(first); replaced != nil || !accepted {
		t.Fatalf("first register=(%v, %v)", replaced, accepted)
	}
	if _, accepted := r.Register(&Connection{MachineID: first.MachineID, Generation: 1}); accepted {
		t.Fatal("registry accepted a duplicate generation")
	}
	if _, accepted := r.Register(&Connection{MachineID: first.MachineID, Generation: 0}); accepted {
		t.Fatal("registry accepted an older generation")
	}
	second := &Connection{MachineID: first.MachineID, ConnectionID: "conn_2", Generation: 2, ConnectedAt: now, LastSeenAt: now, Status: "ready"}
	if replaced, accepted := r.Register(second); replaced != first || !accepted {
		t.Fatalf("replacement register=(%v, %v)", replaced, accepted)
	}

	caps := []protocolv1.CapabilityDescriptor{{CapabilityId: "machine.status", Actions: []string{"report"}}}
	if !r.SetCapabilities(second.MachineID, second.Generation, caps) {
		t.Fatal("SetCapabilities rejected current generation")
	}
	snapshot, ok := r.Get(second.MachineID)
	if !ok {
		t.Fatal("Get did not find registered connection")
	}
	snapshot.Capabilities[0].Actions[0] = "mutated"
	current, _ := r.Get(second.MachineID)
	if current.Capabilities[0].Actions[0] != "report" {
		t.Fatal("registry snapshot exposed mutable capability state")
	}
	if r.Touch(second.MachineID, 1, "stale", now.Add(time.Minute)) {
		t.Fatal("Touch accepted an older generation")
	}
	r.Remove(second.MachineID, 1)
	if _, ok := r.Get(second.MachineID); !ok {
		t.Fatal("Remove removed the current connection using an older generation")
	}
	r.Remove(second.MachineID, 2)
	if _, ok := r.Get(second.MachineID); ok {
		t.Fatal("Remove left the current connection registered")
	}
}
