package api

import (
	"net/http"
	"strings"
	"time"

	"panel/master/internal/auth"
	"panel/master/internal/db"
)

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// login issues a session JWT for valid credentials.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	username := strings.TrimSpace(body.Username)
	u, err := s.loginUser(username, body.Password)
	if err != nil {
		s.DB.AddAudit(username, "auth.login_failed", "", "")
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := auth.IssueToken(s.Secret, u.ID, u.Username, u.Role, s.TokenTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.setSessionCookie(w, token, int(s.TokenTTL/time.Second))
	s.DB.AddAudit(u.Username, "auth.login", "", "")
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": u})
}

// loginUser checks credentials and returns the user.
func (s *Server) loginUser(username, password string) (*db.User, error) {
	if username == "" || password == "" {
		return nil, errForbidden
	}
	u, err := s.DB.UserByName(username)
	if err != nil {
		// Burn comparable time to blunt user enumeration.
		auth.CheckPassword("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0C1Q0HhxKYc8BLK2m0A0DjS1W0C", password)
		return nil, errForbidden
	}
	if !auth.CheckPassword(u.PasswordHash, password) {
		return nil, errForbidden
	}
	return u, nil
}

// logout clears the session cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.setSessionCookie(w, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
