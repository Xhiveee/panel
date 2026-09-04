// Package api implements the master HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"panel/common/protocol"
	"panel/master/internal/agent"
	"panel/master/internal/auth"
	"panel/master/internal/console"
	"panel/master/internal/db"
	"panel/master/internal/metrics"
)

// Server bundles API dependencies.
type Server struct {
	DB       *db.DB
	Hub      *agent.Hub
	Store    *metrics.Store
	Consoles *console.Hub
	Secret   string
	TokenTTL time.Duration

	mu       sync.Mutex
	statuses map[int64]protocol.StatusEvent

	lastTouch sync.Map // nodeID -> unix
}

// NewServer wires the API server.
func NewServer(database *db.DB, hub *agent.Hub, store *metrics.Store, consoles *console.Hub, secret string, ttl time.Duration) *Server {
	return &Server{
		DB: database, Hub: hub, Store: store, Consoles: consoles,
		Secret: secret, TokenTTL: ttl,
		statuses: map[int64]protocol.StatusEvent{},
	}
}

// Routes builds the /api subtree. consoleWS serves the browser console.
func (s *Server) Routes(consoleWS http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(limitBody(20 << 20)) // room for base64 file payloads

	// Public endpoints (their own authentication).
	r.Post("/auth/login", s.login)
	r.Post("/auth/logout", s.logout)
	r.Get("/agent/ws", s.agentWS) // node token auth inside handler

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(s.Secret))

		r.Get("/auth/me", s.me)

		// Users.
		r.With(auth.RequireAdmin).Get("/users", s.listUsers)
		r.With(auth.RequireAdmin).Post("/users", s.createUser)
		r.With(auth.RequireAdmin).Patch("/users/{id}", s.patchUser)
		r.With(auth.RequireAdmin).Delete("/users/{id}", s.deleteUser)
		r.Get("/users/{id}/instances", s.listUserInstances) // admin or self
		r.With(auth.RequireAdmin).Put("/users/{id}/instances", s.setUserInstances)
		r.Put("/users/{id}/password", s.changePassword) // admin or self

		// Nodes.
		r.With(auth.RequireAdmin).Get("/nodes", s.listNodes)
		r.With(auth.RequireAdmin).Post("/nodes", s.createNode)
		r.With(auth.RequireAdmin).Delete("/nodes/{id}", s.deleteNode)

		// Instances.
		r.Get("/instances", s.listInstances)
		r.With(auth.RequireAdmin).Post("/instances", s.createInstance)
		r.With(s.instanceAccess).Get("/instances/{id}", s.getInstance)
		r.With(auth.RequireAdmin, s.instanceAccess).Patch("/instances/{id}", s.patchInstance)
		r.With(auth.RequireAdmin, s.instanceAccess).Delete("/instances/{id}", s.deleteInstance)
		r.With(s.instanceAccess).Post("/instances/{id}/start", s.startInstance)
		r.With(s.instanceAccess).Post("/instances/{id}/stop", s.stopInstance)
		r.With(s.instanceAccess).Post("/instances/{id}/restart", s.restartInstance)
		r.With(s.instanceAccess).Get("/instances/{id}/status", s.instanceStatus)
		r.With(s.instanceAccess).Get("/instances/{id}/metrics", s.instanceMetrics)

		// Files (proxied to the owning agent).
		r.With(s.instanceAccess).Post("/instances/{id}/files/list", s.filesList)
		r.With(s.instanceAccess).Post("/instances/{id}/files/read", s.filesRead)
		r.With(s.instanceAccess).Post("/instances/{id}/files/write", s.filesWrite)
		r.With(s.instanceAccess).Post("/instances/{id}/files/mkdir", s.filesMkdir)
		r.With(s.instanceAccess).Post("/instances/{id}/files/delete", s.filesDelete)

		// Metrics. Node metrics are admin-only; instance metrics are
		// permission-scoped by instanceAccess.
		r.With(auth.RequireAdmin).Get("/metrics/node/{id}", s.nodeMetrics)

		// Audit (admin).
		r.With(auth.RequireAdmin).Get("/audit", s.listAudit)
	})

	// Browser console websocket. The handler re-checks auth + permissions.
	r.With(auth.Middleware(s.Secret)).Get("/ws/instance/{id}", http.HandlerFunc(consoleWS.ServeHTTP))
	return r
}

// HandleAgentEvent processes asynchronous events coming from agents.
func (s *Server) HandleAgentEvent(nodeID int64, msg protocol.Message) {
	switch msg.Event {
	case protocol.EventConsole:
		var ev protocol.ConsoleEvent
		if json.Unmarshal(msg.Data, &ev) == nil && ev.Data != "" {
			s.Consoles.Broadcast(ev.InstanceID, ev.Data)
		}

	case protocol.EventInstanceStatus:
		var ev protocol.StatusEvent
		if json.Unmarshal(msg.Data, &ev) == nil {
			s.mu.Lock()
			s.statuses[ev.InstanceID] = ev
			s.mu.Unlock()
		}

	case protocol.EventNodeMetrics:
		var ev protocol.NodeMetricsEvent
		if json.Unmarshal(msg.Data, &ev) == nil {
			now := time.Now()
			s.Store.PushNode(nodeID, ev.CPU, float64(ev.MemUsed), now)
			for _, im := range ev.Instances {
				s.Store.PushInstance(im.InstanceID, im.CPU, float64(im.RSS), now)
			}
			s.Store.SetNodeSummary(nodeID, ev.MemTotal, ev.MemUsed, ev.CPU, ev.Uptime)
			s.touchNode(nodeID)
		}
	}
}

// touchNode persists node liveness at most once per 15s per node.
func (s *Server) touchNode(nodeID int64) {
	now := time.Now().Unix()
	key := strconv.FormatInt(nodeID, 10)
	if v, ok := s.lastTouch.Load(key); ok {
		if last, _ := v.(int64); now-last < 15 {
			return
		}
	}
	s.lastTouch.Store(key, now)
	_ = s.DB.TouchNode(nodeID, now)
}

// ---------- shared handler helpers ----------

var errForbidden = errors.New("forbidden")

func requester(r *http.Request) *auth.User {
	if u := auth.FromContext(r.Context()); u != nil {
		return u
	}
	return &auth.User{}
}

type instCtxKey struct{}

// instanceAccess loads the URL instance and enforces visibility.
func (s *Server) instanceAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := requester(r)
		id, err := pathID(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		inst, err := s.DB.GetInstance(id)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "instance not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !u.IsAdmin() {
			ok, err := s.DB.UserHasInstance(u.ID, id)
			if err != nil || !ok {
				writeErr(w, http.StatusForbidden, "no access to this instance")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), instCtxKey{}, inst)))
	})
}

// instanceFrom returns the instance placed by the access middleware.
func instanceFrom(r *http.Request) *db.Instance {
	if i, ok := r.Context().Value(instCtxKey{}).(*db.Instance); ok && i != nil {
		return i
	}
	return &db.Instance{}
}

// InstanceStatus returns the last known cached status.
func (s *Server) InstanceStatus(id int64) (protocol.StatusEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.statuses[id]
	return st, ok
}

func (s *Server) cacheStatus(id int64, st protocol.StatusEvent) {
	s.mu.Lock()
	s.statuses[id] = st
	s.mu.Unlock()
}

// dropInstanceState removes cached state for a deleted instance.
func (s *Server) dropInstanceState(id int64) {
	s.mu.Lock()
	delete(s.statuses, id)
	s.mu.Unlock()
	s.Store.DropInstance(id)
	s.Consoles.DropBacklog(id)
}

// SyncNodeState queries statuses for all instances of a node after reconnect.
func (s *Server) SyncNodeState(nodeID int64) {
	rows, err := s.DB.InstancesByNode(nodeID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.RequestTimeout)
	defer cancel()
	for _, i := range rows {
		var res protocol.StatusResult
		if err := s.Hub.RequestDecode(ctx, nodeID, protocol.OpInstanceStatus,
			protocol.InstanceRef{InstanceID: i.ID}, &res); err == nil {
			s.cacheStatus(i.ID, protocol.StatusEvent{
				InstanceID: i.ID, Running: res.Running, PID: res.PID, StartedAt: res.StartedAt,
			})
		}
	}
}

// MarkNodeOffline clears live caches when a node disconnects.
func (s *Server) MarkNodeOffline(nodeID int64) {
	rows, err := s.DB.InstancesByNode(nodeID)
	if err != nil {
		return
	}
	s.mu.Lock()
	for _, i := range rows {
		delete(s.statuses, i.ID)
	}
	s.mu.Unlock()
}

// ConsoleAttach asks the agent to start streaming instance output.
func (s *Server) ConsoleAttach(instanceID int64) {
	i, err := s.DB.GetInstance(instanceID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.RequestTimeout)
	defer cancel()
	_, _ = s.Hub.Request(ctx, i.NodeID, protocol.OpConsoleAttach,
		protocol.InstanceRef{InstanceID: instanceID})
}

// ConsoleDetach tells the agent to stop streaming instance output.
func (s *Server) ConsoleDetach(instanceID int64) {
	i, err := s.DB.GetInstance(instanceID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.RequestTimeout)
	defer cancel()
	_, _ = s.Hub.Request(ctx, i.NodeID, protocol.OpConsoleDetach,
		protocol.InstanceRef{InstanceID: instanceID})
}

// ---------- plumbing ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func limitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}
