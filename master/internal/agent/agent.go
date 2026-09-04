// Package agent manages live websocket connections to agent nodes.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"panel/common/protocol"
)

// ErrNodeOffline is returned when no connection exists for a node.
var ErrNodeOffline = errors.New("node is offline")

// Hub is the live connection registry for agent nodes.
type Hub struct {
	mu    sync.RWMutex
	conns map[int64]*Conn
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{conns: map[int64]*Conn{}}
}

var reqSeq atomic.Uint64

// OnEvent / OnConnect / OnDisconnect are app-level callbacks set once at
// startup. They must not block; they are invoked from connection goroutines.
var (
	OnEvent      func(nodeID int64, msg protocol.Message)
	OnConnect    func(nodeID int64)
	OnDisconnect func(nodeID int64)
)

// AgentWS upgrades and registers an agent connection. The caller must have
// authenticated the node token; nodeID identifies the authenticated node.
func (h *Hub) AgentWS(w http.ResponseWriter, r *http.Request, nodeID int64) {
	// No OriginPatterns on purpose: non-browser agents send no Origin and
	// are always accepted; browser cross-origin requests are rejected by
	// default. Do not add "*".
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	c := &Conn{
		nodeID:  nodeID,
		ws:      conn,
		pending: map[string]chan protocol.Message{},
	}
	conn.SetReadLimit(4 << 20) // bound a single agent frame (DoS guard)
	h.add(c)

	slog.Info("agent connected", "node", nodeID, "remote", r.RemoteAddr)
	if OnConnect != nil {
		OnConnect(nodeID)
	}

	c.readLoop(r.Context())

	h.remove(c)
	slog.Info("agent disconnected", "node", nodeID)
	if OnDisconnect != nil {
		OnDisconnect(nodeID)
	}
}

func (h *Hub) add(c *Conn) {
	h.mu.Lock()
	// Supersede a previous (half-dead) connection for the same node.
	if old, ok := h.conns[c.nodeID]; ok {
		_ = old.ws.Close(websocket.StatusNormalClosure, "superseded")
	}
	h.conns[c.nodeID] = c
	h.mu.Unlock()
}

func (h *Hub) remove(c *Conn) {
	h.mu.Lock()
	if cur, ok := h.conns[c.nodeID]; ok && cur == c {
		delete(h.conns, c.nodeID)
	}
	h.mu.Unlock()
}

// Online reports whether the node currently has a live connection.
func (h *Hub) Online(nodeID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[nodeID]
	return ok
}

// OnlineNodes returns online state for all given node ids.
func (h *Hub) OnlineNodes(ids []int64) map[int64]bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = false
	}
	for id := range h.conns {
		if _, want := m[id]; want {
			m[id] = true
		}
	}
	return m
}

// Request sends op to the node and returns the raw result JSON.
func (h *Hub) Request(ctx context.Context, nodeID int64, op string, params any) (json.RawMessage, error) {
	var res protocol.Message
	if err := h.requestMsg(ctx, nodeID, op, params, &res); err != nil {
		return nil, err
	}
	return res.Result, nil
}

// RequestDecode sends op and decodes the result JSON into out (optional).
func (h *Hub) RequestDecode(ctx context.Context, nodeID int64, op string, params any, out any) error {
	var res protocol.Message
	if err := h.requestMsg(ctx, nodeID, op, params, &res); err != nil {
		return err
	}
	if out == nil || len(res.Result) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result, out)
}

// requestMsg performs one RPC round trip.
func (h *Hub) requestMsg(ctx context.Context, nodeID int64, op string, params any, out *protocol.Message) error {
	h.mu.RLock()
	c := h.conns[nodeID]
	h.mu.RUnlock()
	if c == nil {
		return ErrNodeOffline
	}

	id := nextReqID()
	ch := make(chan protocol.Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := protocol.NewRequest(id, op, params)
	if err := c.write(ctx, msg); err != nil {
		return fmt.Errorf("send %s: %w", op, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return ErrNodeOffline
		}
		if !resp.OK {
			if resp.Error != "" {
				return errors.New(resp.Error)
			}
			return errors.New("agent request failed")
		}
		if out != nil {
			*out = resp
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func nextReqID() string {
	return fmt.Sprintf("r%d-%d", time.Now().UnixMilli(), reqSeq.Add(1))
}

// Conn is one live agent connection.
type Conn struct {
	nodeID  int64
	ws      *websocket.Conn
	mu      sync.Mutex
	wmu     sync.Mutex // serializes writes
	pending map[string]chan protocol.Message
}

// write sends one message (writes are serialized).
func (c *Conn) write(ctx context.Context, msg protocol.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.ws.Write(wctx, websocket.MessageText, b)
}

// readLoop pumps incoming messages until the socket closes.
func (c *Conn) readLoop(ctx context.Context) {
	defer func() { _ = c.ws.Close(websocket.StatusNormalClosure, "") }()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		var msg protocol.Message
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Kind {
		case protocol.KindResponse:
			c.mu.Lock()
			ch, ok := c.pending[msg.ID]
			c.mu.Unlock()
			if ok {
				select {
				case ch <- msg:
				default:
				}
			}
		case protocol.KindEvent:
			if OnEvent != nil {
				OnEvent(c.nodeID, msg)
			}
		default:
			// Agents never send bare requests in this design.
		}
	}
}

// StartKeepalive pings every connected agent periodically. Call it once at
// master startup.
func (h *Hub) StartKeepalive(ctx context.Context) {
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.mu.RLock()
				conns := make([]*Conn, 0, len(h.conns))
				for _, c := range h.conns {
					conns = append(conns, c)
				}
				h.mu.RUnlock()
				for _, c := range conns {
					pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					_ = c.ws.Ping(pctx)
					cancel()
				}
			}
		}
	}()
}
