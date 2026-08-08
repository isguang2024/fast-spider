package registry

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type Connection struct {
	MachineID    string
	ConnectionID string
	Generation   int64
	ConnectedAt  time.Time
	LastSeenAt   time.Time
	Status       string
	Capabilities []protocolv1.CapabilityDescriptor

	conn    *websocket.Conn
	writeMu sync.Mutex
}

type Snapshot struct {
	MachineID    string
	ConnectionID string
	Generation   int64
	ConnectedAt  time.Time
	LastSeenAt   time.Time
	Status       string
	Capabilities []protocolv1.CapabilityDescriptor
}

type Registry struct {
	mu    sync.RWMutex
	conns map[string]*Connection
}

func New() *Registry {
	return &Registry{conns: make(map[string]*Connection)}
}

func (r *Registry) Register(conn *Connection) (replaced *Connection, accepted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.conns[conn.MachineID]; current != nil {
		if current.Generation >= conn.Generation {
			return nil, false
		}
		replaced = current
	}
	r.conns[conn.MachineID] = conn
	return replaced, true
}

func (r *Registry) Remove(machineID string, generation int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.conns[machineID]; current != nil && current.Generation == generation {
		delete(r.conns, machineID)
	}
}

func (r *Registry) Touch(machineID string, generation int64, status string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.conns[machineID]
	if current == nil || current.Generation != generation {
		return false
	}
	current.LastSeenAt = now
	if status != "" {
		current.Status = status
	}
	return true
}

func (r *Registry) SetCapabilities(machineID string, generation int64, capabilities []protocolv1.CapabilityDescriptor) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.conns[machineID]
	if current == nil || current.Generation != generation {
		return false
	}
	current.Capabilities = cloneCapabilities(capabilities)
	return true
}

func (r *Registry) Get(machineID string) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	current := r.conns[machineID]
	if current == nil {
		return Snapshot{}, false
	}
	return snapshot(current), true
}

func (r *Registry) List() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, 0, len(r.conns))
	for _, current := range r.conns {
		out = append(out, snapshot(current))
	}
	return out
}

func (r *Registry) CloseMachine(ctx context.Context, machineID, code, reason string) bool {
	r.mu.RLock()
	current := r.conns[machineID]
	r.mu.RUnlock()
	if current == nil {
		return false
	}
	_ = current.WriteJSON(ctx, protocolv1.ConnectionClose{
		MessageType: protocolv1.MessageConnectionClose,
		Code:        code,
		Reason:      reason,
		Timestamp:   protocolv1.Timestamp(time.Now()),
	})
	_ = current.Close(websocket.StatusPolicyViolation, reason)
	return true
}

func (r *Registry) CloseStale(ctx context.Context, before time.Time) int {
	r.mu.RLock()
	var stale []*Connection
	for _, current := range r.conns {
		if current.LastSeenAt.Before(before) {
			stale = append(stale, current)
		}
	}
	r.mu.RUnlock()
	for _, current := range stale {
		_ = current.WriteJSON(ctx, protocolv1.ConnectionClose{
			MessageType: protocolv1.MessageConnectionClose,
			Code:        "HEARTBEAT_TIMEOUT",
			Reason:      "heartbeat timeout",
			Timestamp:   protocolv1.Timestamp(time.Now()),
		})
		_ = current.Close(websocket.StatusPolicyViolation, "heartbeat timeout")
	}
	return len(stale)
}

func (c *Connection) ReadJSON(ctx context.Context, value any) error {
	return wsjson.Read(ctx, c.conn, value)
}

func (c *Connection) WriteJSON(ctx context.Context, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.conn, value)
}

func (c *Connection) Close(status websocket.StatusCode, reason string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Close(status, reason)
}

func NewConnection(machineID, connectionID string, generation int64, now time.Time, conn *websocket.Conn) *Connection {
	return &Connection{
		MachineID:    machineID,
		ConnectionID: connectionID,
		Generation:   generation,
		ConnectedAt:  now,
		LastSeenAt:   now,
		Status:       "ready",
		conn:         conn,
	}
}

func snapshot(current *Connection) Snapshot {
	return Snapshot{
		MachineID:    current.MachineID,
		ConnectionID: current.ConnectionID,
		Generation:   current.Generation,
		ConnectedAt:  current.ConnectedAt,
		LastSeenAt:   current.LastSeenAt,
		Status:       current.Status,
		Capabilities: cloneCapabilities(current.Capabilities),
	}
}

func cloneCapabilities(in []protocolv1.CapabilityDescriptor) []protocolv1.CapabilityDescriptor {
	out := make([]protocolv1.CapabilityDescriptor, len(in))
	for i, item := range in {
		out[i] = item
		out[i].Actions = append([]string(nil), item.Actions...)
	}
	return out
}
