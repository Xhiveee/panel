package api

import (
	"net/http"
)

// nodeMetrics returns the CPU/RAM history plus current totals for a node.
func (s *Server) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.DB.NodeByID(id); err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	memTotal, memUsed, cpu, uptime := s.Store.NodeSummary(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"node":     metricsJSON(s.Store.NodeHistory(id)),
		"memTotal": memTotal,
		"memUsed":  memUsed,
		"cpu":      cpu,
		"uptime":   uptime,
	})
}
