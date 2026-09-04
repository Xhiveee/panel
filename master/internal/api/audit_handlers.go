package api

import (
	"net/http"
	"strconv"

	"panel/master/internal/db"
)

// listAudit returns the newest audit records (admin).
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	if !requester(r).IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin only")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := s.DB.Audit(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rows == nil {
		rows = []*db.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": rows})
}
