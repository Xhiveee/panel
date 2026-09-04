// Package ws serves browser-facing WebSocket endpoints (instance console).
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"panel/common/protocol"
	"panel/master/internal/agent"
	"panel/master/internal/auth"
	"panel/master/internal/console"
	"panel/master/internal/db"
)

// Handler carries dependencies for browser WS endpoints.
type Handler struct {
	Hub  *agent.Hub
	DB   *db.DB
	Cons *console.Hub
}

// browserFrame is the JSON frame exchanged with the browser console.
type browserFrame struct {
	Type string `json:"type"` // "output" | "input"
	Data string `json:"data,omitempty"`
}

// ConsoleWS streams a live instance console to an authorized browser.
// Auth and per-instance permissions are checked server-side.
func (h *Handler) ConsoleWS(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad instance id", http.StatusBadRequest)
		return
	}
	inst, err := h.DB.GetInstance(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !u.IsAdmin() {
		ok, err := h.DB.UserHasInstance(u.ID, inst.ID)
		if err != nil || !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if !h.Hub.Online(inst.NodeID) {
		http.Error(w, "node offline", http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Subscribe first so nothing between attach and subscribe is lost.
	// The console hub owns the agent attach/detach lifecycle: the first
	// subscriber attaches, the last one detaches.
	events, backlog, unsub := h.Cons.Subscribe(inst.ID)
	defer unsub()

	writeFrame := func(f browserFrame) bool {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		b, merr := json.Marshal(f)
		if merr != nil {
			cancel()
			return false
		}
		if werr := conn.Write(wctx, websocket.MessageText, b); werr != nil {
			cancel()
			return false
		}
		return true
	}

	// Reader: browser input -> agent stdin.
	go func() {
		defer cancel()
		for {
			_, data, rerr := conn.Read(ctx)
			if rerr != nil {
				return
			}
			var f browserFrame
			if err := json.Unmarshal(data, &f); err != nil || f.Type != "input" || f.Data == "" {
				continue
			}
			ictx, icancel := context.WithTimeout(ctx, protocol.RequestTimeout)
			rerr = h.Hub.RequestDecode(ictx, inst.NodeID, protocol.OpConsoleInput,
				protocol.ConsoleInputParams{InstanceID: inst.ID, Data: f.Data}, nil)
			icancel()
			if rerr != nil {
				return
			}
		}
	}()

	// Backlog first, then the live stream.
	if backlog != "" && !writeFrame(browserFrame{Type: "output", Data: backlog}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case data, ok := <-events:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			if !writeFrame(browserFrame{Type: "output", Data: data}) {
				return
			}
		}
	}
}
