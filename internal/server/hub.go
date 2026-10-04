package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

var (
	ErrAgentOffline = errors.New("agent is offline")
	ErrAgentBusy    = errors.New("agent connection is congested")
)

// Hub tracks the live WebSocket tunnel of every connected agent and provides
// request/response calls over it. The agent always dials in, so this is how the
// dashboard "reaches" a machine that is behind NAT.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*agentConn
}

func NewHub() *Hub { return &Hub{conns: map[string]*agentConn{}} }

type agentConn struct {
	agentID string
	ws      *websocket.Conn
	send    chan proto.Envelope
	done    chan struct{}
	once    sync.Once

	mu      sync.Mutex
	pending map[string]chan proto.Envelope
}

func newAgentConn(agentID string, ws *websocket.Conn) *agentConn {
	return &agentConn{
		agentID: agentID,
		ws:      ws,
		send:    make(chan proto.Envelope, 32),
		done:    make(chan struct{}),
		pending: map[string]chan proto.Envelope{},
	}
}

func (c *agentConn) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close(code, reason)
	})
}

// register installs c as the agent's connection, evicting any previous one
// (e.g. a half-dead TCP session the server hasn't noticed yet).
func (h *Hub) register(c *agentConn) {
	h.mu.Lock()
	old := h.conns[c.agentID]
	h.conns[c.agentID] = c
	h.mu.Unlock()
	if old != nil {
		old.close(websocket.StatusPolicyViolation, "replaced by newer connection")
	}
}

func (h *Hub) unregister(c *agentConn) {
	h.mu.Lock()
	if h.conns[c.agentID] == c {
		delete(h.conns, c.agentID)
	}
	h.mu.Unlock()
	c.close(websocket.StatusNormalClosure, "bye")
}

func (h *Hub) get(agentID string) *agentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[agentID]
}

// Disconnect drops an agent's tunnel (used after key rotation or deletion).
func (h *Hub) Disconnect(agentID string) {
	if c := h.get(agentID); c != nil {
		c.close(websocket.StatusPolicyViolation, "credentials revoked")
	}
}

func (h *Hub) Online(agentID string) bool { return h.get(agentID) != nil }

// Notify tells an agent its config changed. Best effort: an offline agent
// picks the change up from the revision it sends in its next hello.
func (h *Hub) Notify(agentID string) {
	_ = h.Send(agentID, proto.MsgConfigChanged, nil)
}

// Send fires a message without waiting for a reply.
func (h *Hub) Send(agentID, typ string, payload any) error {
	c := h.get(agentID)
	if c == nil {
		return ErrAgentOffline
	}
	env, err := newEnvelope("", typ, payload)
	if err != nil {
		return err
	}
	select {
	case c.send <- env:
		return nil
	default:
		return ErrAgentBusy
	}
}

// Call sends a request and waits for the agent's matching MsgResult.
func (h *Hub) Call(ctx context.Context, agentID, typ string, payload any, timeout time.Duration) (json.RawMessage, error) {
	c := h.get(agentID)
	if c == nil {
		return nil, ErrAgentOffline
	}
	id := newID()
	env, err := newEnvelope(id, typ, payload)
	if err != nil {
		return nil, err
	}
	ch := make(chan proto.Envelope, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	select {
	case c.send <- env:
	default:
		return nil, ErrAgentBusy
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case res := <-ch:
		if res.Error != "" {
			return nil, &AgentError{Msg: res.Error}
		}
		return res.Payload, nil
	case <-c.done:
		return nil, ErrAgentOffline
	case <-ctx.Done():
		return nil, fmt.Errorf("agent did not answer in time: %w", ctx.Err())
	}
}

// AgentError is an error the agent itself reported (e.g. "permission denied").
type AgentError struct{ Msg string }

func (e *AgentError) Error() string { return e.Msg }

func (c *agentConn) deliver(env proto.Envelope) {
	c.mu.Lock()
	ch := c.pending[env.ID]
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- env:
		default:
		}
	}
}

func newEnvelope(id, typ string, payload any) (proto.Envelope, error) {
	env := proto.Envelope{ID: id, Type: typ}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return env, err
		}
		env.Payload = b
	}
	return env, nil
}
