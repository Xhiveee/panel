package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "test-secret"

func TestIssueParseToken(t *testing.T) {
	token, err := IssueToken(testSecret, 42, "alice", RoleAdmin, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	u, err := ParseToken(testSecret, token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if u.ID != 42 || u.Username != "alice" || u.Role != RoleAdmin {
		t.Fatalf("got id=%d name=%q role=%q", u.ID, u.Username, u.Role)
	}
	if !u.IsAdmin() {
		t.Fatal("expected admin user")
	}
}

func TestParseTokenExpired(t *testing.T) {
	token, err := IssueToken(testSecret, 1, "bob", RoleUser, -time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if _, err := ParseToken(testSecret, token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseTokenWrongSecret(t *testing.T) {
	token, _ := IssueToken("a", 1, "bob", RoleUser, time.Hour)
	if _, err := ParseToken("b", token); err == nil {
		t.Fatal("expected error with wrong secret")
	}
}

func TestParseTokenGarbage(t *testing.T) {
	if _, err := ParseToken(testSecret, "not-a-jwt"); err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestMiddleware(t *testing.T) {
	token, _ := IssueToken(testSecret, 7, "u", RoleUser, time.Hour)

	var gotName string
	h := Middleware(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = FromContext(r.Context()).Username
	}))

	// header auth
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotName != "u" {
		t.Fatalf("header auth failed: %q", gotName)
	}

	// cookie auth
	gotName = ""
	req = httptest.NewRequest("GET", "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotName != "u" {
		t.Fatalf("cookie auth failed: %q", gotName)
	}

	// no auth
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("expected JSON error, got %s", rec.Body.String())
	}
}

func TestRequireAdmin(t *testing.T) {
	adminTok, _ := IssueToken(testSecret, 1, "a", RoleAdmin, time.Hour)
	userTok, _ := IssueToken(testSecret, 2, "u", RoleUser, time.Hour)

	// RequireAdmin reads the principal from the context, so it must be
	// composed after the authentication middleware in a real route chain.
	protected := Middleware(testSecret)(RequireAdmin(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
	)))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin rejected: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+userTok)
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("user allowed into admin route: %d", rec.Code)
	}
}

func TestWithUserFromContext(t *testing.T) {
	u := &User{ID: 9, Username: "carol", Role: RoleAdmin}
	ctx := WithUser(context.Background(), u)
	if FromContext(ctx) != u {
		t.Fatal("FromContext did not return the stored user")
	}
	if FromContext(context.Background()) != nil {
		t.Fatal("expected nil user for empty context")
	}
	if (&User{Role: RoleUser}).IsAdmin() {
		t.Fatal("user role must not be admin")
	}
	if (*User)(nil).IsAdmin() {
		t.Fatal("nil user must not be admin")
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !CheckPassword(hash, "hunter22") {
		t.Fatal("CheckPassword should match")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("CheckPassword should not match wrong password")
	}
	if _, err := HashPassword("abc"); err == nil {
		t.Fatal("short password should be rejected")
	}
}
