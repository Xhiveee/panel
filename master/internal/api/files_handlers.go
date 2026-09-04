package api

import (
	"net/http"
	"strings"

	"panel/common/protocol"
)

// filesHandlers proxy file operations to the owning agent. The instance dir is
// sent from the backend so the agent never trusts a client-supplied path.
// Paths are still validated here (defense-in-depth) before any RPC.

// validFilePath rejects absolute paths, NUL bytes and oversized values.
// Lexical ".." is rejected here; the agent enforces symlink containment.
func validFilePath(p string) bool {
	if strings.ContainsRune(p, 0) || len(p) > 1024 {
		return false
	}
	return true
}

func (s *Server) filesList(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	var in struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validFilePath(in.Path) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	var res protocol.FilesListResult
	if err := s.Hub.RequestDecode(ctx, i.NodeID, protocol.OpFilesList,
		protocol.PathParams{InstanceID: i.ID, Dir: i.Dir, Path: in.Path}, &res); err != nil {
		s.mapAgentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) filesRead(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	var in struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validFilePath(in.Path) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	var res protocol.FilesReadResult
	if err := s.Hub.RequestDecode(ctx, i.NodeID, protocol.OpFilesRead,
		protocol.PathParams{InstanceID: i.ID, Dir: i.Dir, Path: in.Path}, &res); err != nil {
		s.mapAgentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) filesWrite(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	var in struct {
		Path       string `json:"path"`
		ContentB64 string `json:"contentB64"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validFilePath(in.Path) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	if len(in.ContentB64) > 12*1024*1024 { // ~8MB decoded + base64 overhead
		writeErr(w, http.StatusBadRequest, "file too large")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	var res any
	if err := s.Hub.RequestDecode(ctx, i.NodeID, protocol.OpFilesWrite,
		protocol.FilesWriteParams{InstanceID: i.ID, Dir: i.Dir, Path: in.Path, ContentB64: in.ContentB64}, &res); err != nil {
		s.mapAgentErr(w, err)
		return
	}
	s.DB.AddAudit(requester(r).Username, "files.write", i.Name, in.Path)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) filesMkdir(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	var in struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validFilePath(in.Path) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	var res any
	if err := s.Hub.RequestDecode(ctx, i.NodeID, protocol.OpFilesMkdir,
		protocol.PathParams{InstanceID: i.ID, Dir: i.Dir, Path: in.Path}, &res); err != nil {
		s.mapAgentErr(w, err)
		return
	}
	s.DB.AddAudit(requester(r).Username, "files.mkdir", i.Name, in.Path)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) filesDelete(w http.ResponseWriter, r *http.Request) {
	i := instanceFrom(r)
	var in struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validFilePath(in.Path) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	var res any
	if err := s.Hub.RequestDecode(ctx, i.NodeID, protocol.OpFilesDelete,
		protocol.PathParams{InstanceID: i.ID, Dir: i.Dir, Path: in.Path}, &res); err != nil {
		s.mapAgentErr(w, err)
		return
	}
	s.DB.AddAudit(requester(r).Username, "files.delete", i.Name, in.Path)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
