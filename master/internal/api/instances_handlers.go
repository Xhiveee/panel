package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"panel/common/protocol"
	"panel/master/internal/agent"
	"panel/master/internal/db"
	"panel/master/internal/metrics"
)

// instanceView is the API representation of an instance with live state.
type instanceView struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	NodeID     int64             `json:"nodeId"`
	NodeName   string            `json:"nodeName"`
	Dir        string            `json:"dir"`
	Cmd        []string          `json:"cmd"`
	StopCmd    string            `json:"stopCmd"`
	Env        map[string]string `json:"env"`
	CreatedAt  int64             `json:"createdAt"`
	Running    bool              `json:"running"`
	NodeOnline bool              `json:"nodeOnline"`
	PID        int               `json:"pid"`
}

// buildInstanceView merges a db row with live cached state.
func (s *Server) buildInstanceView(i *db.Instance) *instanceView {
	v := &instanceView{
		ID: i.ID, Name: i.Name, NodeID: i.NodeID, NodeName: i.NodeName,
		Dir: i.Dir, StopCmd: i.StopCmd, CreatedAt: i.CreatedAt,
		Cmd: i.Cmd, Env: i.Env,
		NodeOnline: s.Hub.Online(i.NodeID),
	}
	if v.Cmd == nil {
		v.Cmd = []string{}
	}
	if v.Env == nil {
		v.Env = map[string]string{}
	}
	if st, ok := s.InstanceStatus(i.ID); ok {
		v.Running = st.Running
		v.PID = st.PID
	}
	return v
}

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	u := requester(r)
	rows, err := s.DB.ListInstances(u.ID, u.IsAdmin())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list instances")
		return
	}
	out := make([]*instanceView, 0, len(rows))
	for _, i := range rows {
		out = append(out, s.buildInstanceView(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request) {
	if !requester(r).IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin required")
		return
	}
	var in struct {
		Name    string            `json:"name"`
		NodeID  int64             `json:"nodeId"`
		Dir     string            `json:"dir"`
		Cmd     []string          `json:"cmd"`
		StopCmd string            `json:"stopCmd"`
		Env     map[string]string `json:"env"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Dir = strings.TrimSpace(in.Dir)
	if err := validateInstanceFields(in.Name, in.Dir, in.Cmd, in.Env, in.StopCmd); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.NodeID <= 0 {
		writeErr(w, http.StatusBadRequest, "valid nodeId is required")
		return
	}
	if _, err := s.DB.NodeByID(in.NodeID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "node does not exist")
			return
		}
		writeErr(w, http.StatusInternalServerError, "failed to check node")
		return
	}
	if in.Env == nil {
		in.Env = map[string]string{}
	}
	inst, err := s.DB.CreateInstance(in.Name, in.NodeID, in.Dir, in.Cmd,
		strings.TrimSpace(in.StopCmd), in.Env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create instance")
		return
	}
	s.DB.AddAudit(requester(r).Username, "instance.create", in.Name, in.Dir)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       inst.ID,
		"instance": s.buildInstanceView(inst),
	})
}

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"instance": s.buildInstanceView(i)})
}

func (s *Server) patchInstance(w http.ResponseWriter, r *http.Request) {
	if !requester(r).IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin required")
		return
	}
	i := instanceFrom(r)
	var in struct {
		Name    *string            `json:"name"`
		Dir     *string            `json:"dir"`
		Cmd     *[]string          `json:"cmd"`
		StopCmd *string            `json:"stopCmd"`
		Env     *map[string]string `json:"env"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name, dir, stopCmd := i.Name, i.Dir, i.StopCmd
	cmdArr := i.Cmd
	envMap := i.Env
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Dir != nil {
		dir = strings.TrimSpace(*in.Dir)
	}
	if in.StopCmd != nil {
		stopCmd = strings.TrimSpace(*in.StopCmd)
	}
	if in.Cmd != nil {
		cmdArr = *in.Cmd
	}
	if in.Env != nil {
		envMap = *in.Env
	}
	if name == "" || dir == "" || len(cmdArr) == 0 {
		writeErr(w, http.StatusBadRequest, "name, dir and cmd must not be empty")
		return
	}
	if err := validateInstanceFields(name, dir, cmdArr, envMap, stopCmd); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ni, err := s.DB.UpdateInstance(i.ID, name, dir, cmdArr, stopCmd, envMap)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to update instance")
		return
	}
	s.DB.AddAudit(requester(r).Username, "instance.update", name, dir)
	writeJSON(w, http.StatusOK, map[string]any{"instance": s.buildInstanceView(ni)})
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	if !requester(r).IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin required")
		return
	}
	i := instanceFrom(r)
	// Best-effort stop; ignore errors (node may be offline already).
	ctx, cancel := rpcCtx(r)
	defer cancel()
	_, _ = s.Hub.Request(ctx, i.NodeID, protocol.OpInstanceStop,
		protocol.InstanceRef{InstanceID: i.ID})
	if err := s.DB.DeleteInstance(i.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to delete instance")
		return
	}
	s.dropInstanceState(i.ID)
	s.DB.AddAudit(requester(r).Username, "instance.delete", i.Name, i.Dir)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// runPower executes start/stop/restart and caches the fresh status.
// params carries the payload for the op: a full spec for start/restart
// (the agent needs the command) or just a ref for stop.
func (s *Server) runPower(w http.ResponseWriter, r *http.Request, i *db.Instance, action, op string, params any) {
	ctx, cancel := rpcCtx(r)
	defer cancel()
	var res protocol.StatusResult
	if err := s.Hub.RequestDecode(ctx, i.NodeID, op, params, &res); err != nil {
		s.mapAgentErr(w, err)
		return
	}
	st := protocol.StatusEvent{
		InstanceID: i.ID, Running: res.Running, PID: res.PID, StartedAt: res.StartedAt,
	}
	s.cacheStatus(i.ID, st)
	s.DB.AddAudit(requester(r).Username, "instance."+action, i.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{"status": st})
}

func (s *Server) startInstance(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	s.runPower(w, r, i, "start", protocol.OpInstanceStart, instanceSpec(i))
}

func (s *Server) stopInstance(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	s.runPower(w, r, i, "stop", protocol.OpInstanceStop,
		protocol.InstanceRef{InstanceID: i.ID})
}

func (s *Server) restartInstance(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	s.runPower(w, r, i, "restart", protocol.OpInstanceRestart, instanceSpec(i))
}

func (s *Server) instanceStatus(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	online := s.Hub.Online(i.NodeID)
	if online {
		ctx, cancel := rpcCtx(r)
		defer cancel()
		var res protocol.StatusResult
		if err := s.Hub.RequestDecode(ctx, i.NodeID, protocol.OpInstanceStatus,
			protocol.InstanceRef{InstanceID: i.ID}, &res); err == nil {
			st := protocol.StatusEvent{
				InstanceID: i.ID, Running: res.Running, PID: res.PID, StartedAt: res.StartedAt,
			}
			s.cacheStatus(i.ID, st)
			writeJSON(w, http.StatusOK, map[string]any{"status": st, "nodeOnline": true})
			return
		}
	}
	st, ok := s.InstanceStatus(i.ID)
	if !ok {
		st = protocol.StatusEvent{InstanceID: i.ID, Running: false}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "nodeOnline": online})
}

// instanceMetrics returns recent history for the instance and its node.
func (s *Server) instanceMetrics(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	inst := s.Store.InstanceHistory(i.ID)
	node := s.Store.NodeHistory(i.NodeID)
	writeJSON(w, http.StatusOK, map[string]any{
		"instance": metricsJSON(inst),
		"node":     metricsJSON(node),
	})
}

type metricPoint struct {
	T int64   `json:"t"`
	A float64 `json:"a"`
	B float64 `json:"b"`
}

func metricsJSON(pts []metrics.Point) []metricPoint {
	out := make([]metricPoint, 0, len(pts))
	for _, p := range pts {
		out = append(out, metricPoint{T: p.T, A: p.A, B: p.B})
	}
	return out
}

// instanceSpec builds the agent start spec from the stored instance.
func instanceSpec(i *db.Instance) protocol.InstanceSpec {
	env := make([]string, 0, len(i.Env))
	for k, v := range i.Env {
		if !validEnvKey(k) {
			continue // defense-in-depth: skip keys that bypassed validation
		}
		env = append(env, k+"="+v)
	}
	return protocol.InstanceSpec{
		InstanceID: i.ID,
		Dir:        i.Dir,
		Cmd:        i.Cmd,
		StopCmd:    i.StopCmd,
		Env:        env,
	}
}

// validateInstanceFields enforces defense-in-depth limits on stored configs.
// The agent re-validates, but the master must reject dangerous values first.
func validateInstanceFields(name, dir string, cmd []string, env map[string]string, stopCmd string) error {
	if name == "" || dir == "" {
		return errors.New("name and dir are required")
	}
	if len(name) > 64 {
		return errors.New("name must be at most 64 characters")
	}
	if strings.ContainsRune(name, 0) || strings.ContainsRune(dir, 0) {
		return errors.New("name/dir must not contain NUL")
	}
	if len(dir) > 1024 {
		return errors.New("dir is too long")
	}
	if len(cmd) == 0 || len(cmd) > 32 {
		return errors.New("cmd must have 1-32 arguments")
	}
	for _, c := range cmd {
		if strings.TrimSpace(c) == "" {
			return errors.New("cmd must not contain empty arguments")
		}
		if strings.ContainsRune(c, 0) || len(c) > 4096 {
			return errors.New("invalid cmd argument")
		}
	}
	if len(stopCmd) > 8192 || strings.ContainsRune(stopCmd, 0) {
		return errors.New("stopCmd is too long")
	}
	if len(env) > 64 {
		return errors.New("too many env entries")
	}
	for k, v := range env {
		if !validEnvKey(k) {
			return errors.New("invalid env key")
		}
		if strings.ContainsRune(v, 0) || len(v) > 8192 {
			return errors.New("invalid env value")
		}
	}
	return nil
}

// validEnvKey allows NAME-style keys only (no '=', NUL, newlines).
func validEnvKey(k string) bool {
	if k == "" || len(k) > 256 {
		return false
	}
	for _, r := range k {
		if r == 0 || r == '\n' || r == '\r' || r == '=' {
			return false
		}
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '.') {
			return false
		}
	}
	return true
}

// mapAgentErr translates hub errors to HTTP responses.
func (s *Server) mapAgentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNodeOffline):
		writeErr(w, http.StatusServiceUnavailable, "node is offline")
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "agent did not respond in time")
	default:
		// Do not leak agent internals (agent-controlled strings) to browsers.
		writeErr(w, http.StatusBadGateway, "agent request failed")
	}
}

// rpcCtx derives a bounded context for agent RPC calls.
func rpcCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), protocol.RequestTimeout)
}
