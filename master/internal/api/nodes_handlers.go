package api

import (
	"errors"
	"net/http"
	"strings"

	"panel/master/internal/db"
)

// ---------- nodes (admin) ----------

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.DB.Nodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	ids := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	online := s.Hub.OnlineNodes(ids)
	type nodeView struct {
		*db.Node
		Online bool `json:"online"`
	}
	out := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeView{Node: n, Online: online[n.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// createNode registers a node and returns the one-time agent token.
func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 64 {
		writeErr(w, http.StatusBadRequest, "name must be 1-64 characters")
		return
	}
	n, token, err := s.DB.CreateNode(name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not create node (name taken?)")
		return
	}
	s.DB.AddAudit(requester(r).Username, "node.create", name, "")
	// The token is returned exactly once and stored hashed in the DB.
	// The plaintext is sent as a bare string so the UI can copy it directly.
	writeJSON(w, http.StatusCreated, map[string]any{"node": n, "token": token.Token})
}

// deleteNode removes a node and cascades its instances.
func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, _ := s.DB.NodeByID(id)
	if err := s.DB.DeleteNode(id); err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	name := ""
	if n != nil {
		name = n.Name
	}
	s.DB.AddAudit(requester(r).Username, "node.delete", name, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// agentWS authenticates an agent by its node token and hands the connection
// to the hub. This endpoint lives outside user auth on purpose.
func (s *Server) agentWS(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if len(token) > 7 && (strings.HasPrefix(token, "Bearer ") || strings.HasPrefix(token, "bearer ")) {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "node token required")
		return
	}
	node, err := s.DB.NodeByToken(token)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "invalid node token")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Hub.AgentWS(w, r, node.ID)
}
