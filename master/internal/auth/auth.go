// Package auth implements sessions (JWT), passwords and RBAC middleware.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// CookieName is the session cookie for browser sessions.
const CookieName = "panel_token"

// Roles.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

type ctxKey struct{}

// User is the authenticated principal attached to requests.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// IsAdmin reports whether the user has the admin role.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// WithUser stores u in ctx.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// FromContext returns the authenticated user or nil.
func FromContext(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// HashPassword hashes a password with bcrypt.
func HashPassword(password string) (string, error) {
	if len(password) < 6 {
		return "", errors.New("password must be at least 6 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword verifies a password against a bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type claims struct {
	Username string `json:"name"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// IssueToken creates a signed HS256 JWT.
func IssueToken(secret string, userID int64, username, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	c := claims{
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "panel-master",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
}

// ParseToken verifies a JWT and returns its principal.
func ParseToken(secret, token string) (*User, error) {
	t, err := jwt.ParseWithClaims(token, &claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := t.Claims.(*claims)
	if !ok || !t.Valid {
		return nil, errors.New("invalid token")
	}
	var id int64
	_, _ = fmt.Sscanf(c.Subject, "%d", &id)
	return &User{ID: id, Username: c.Username, Role: c.Role}, nil
}

// Middleware authenticates via Authorization header or session cookie.
func Middleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token == "" {
				writeErr(w, http.StatusUnauthorized, "authentication required")
				return
			}
			u, err := ParseToken(secret, token)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid or expired session")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
		})
	}
}

// RequireAdmin rejects non-admin users.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := FromContext(r.Context())
		if !u.IsAdmin() {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	c, err := r.Cookie(CookieName)
	if err == nil {
		return c.Value
	}
	return ""
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		b = []byte(`{"error":"internal error"}`)
	}
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n"))
}
