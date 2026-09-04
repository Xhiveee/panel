package api

import (
	"net/http"
	"strconv"
	"strings"

	"panel/master/internal/auth"
	"panel/master/internal/db"
)

// ---------- users (admin) ----------

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.Users()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	username := strings.TrimSpace(body.Username)
	if len(username) < 2 || len(username) > 64 {
		writeErr(w, http.StatusBadRequest, "username must be 2-64 characters")
		return
	}
	if body.Role != "admin" && body.Role != "user" {
		body.Role = "user"
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.DB.CreateUser(username, hash, body.Role)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not create user (name taken?)")
		return
	}
	s.DB.AddAudit(requester(r).Username, "user.create", username, "role="+body.Role)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Role != "admin" && body.Role != "user" {
		writeErr(w, http.StatusBadRequest, "role must be admin or user")
		return
	}
	if id == requester(r).ID {
		writeErr(w, http.StatusBadRequest, "cannot change your own role")
		return
	}
	if err := s.DB.SetRole(id, body.Role); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	s.DB.AddAudit(requester(r).Username, "user.role", strconv.FormatInt(id, 10), "role="+body.Role)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if id == requester(r).ID {
		writeErr(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	u, _ := s.DB.UserByID(id)
	if err := s.DB.DeleteUser(id); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	name := ""
	if u != nil {
		name = u.Username
	}
	s.DB.AddAudit(requester(r).Username, "user.delete", name, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminSetPassword resets a user's password (admin, no old password).
func (s *Server) adminSetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		NewPassword string `json:"newPassword"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.SetPassword(id, hash); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	s.DB.AddAudit(requester(r).Username, "user.password", strconv.FormatInt(id, 10), "reset by admin")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// me returns the authenticated user's profile, wrapped so the frontend can
// use a single shape for all auth responses.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := requester(r)
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role,
	}})
}

// changePassword updates a password. Admins may reset any user; otherwise the
// user must confirm their current password.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	self := requester(r)
	admin := self.IsAdmin()
	if !admin && id != self.ID {
		writeErr(w, http.StatusForbidden, "cannot change another user's password")
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !admin {
		u, err := s.DB.UserByID(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		if !auth.CheckPassword(u.PasswordHash, body.CurrentPassword) {
			writeErr(w, http.StatusForbidden, "current password is incorrect")
			return
		}
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.SetPassword(id, hash); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	s.DB.AddAudit(self.Username, "user.password", strconv.FormatInt(id, 10), "changed")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// listUserInstances returns instances assigned to a user (admin or self).
func (s *Server) listUserInstances(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	self := requester(r)
	if !self.IsAdmin() && id != self.ID {
		writeErr(w, http.StatusForbidden, "cannot view another user's instances")
		return
	}
	list, err := s.DB.ListInstances(id, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if list == nil {
		list = []*db.Instance{}
	}
	writeJSON(w, http.StatusOK, list)
}

// setUserInstances replaces a user's instance assignments.
func (s *Server) setUserInstances(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, err := s.DB.UserByID(id); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if err := s.DB.SetUserInstances(id, body.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.DB.AddAudit(requester(r).Username, "user.assign", strconv.FormatInt(id, 10),
		strconv.Itoa(len(body.IDs))+" instances")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
