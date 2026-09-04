// Package master is the master panel binary.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"panel/master/internal/agent"
	"panel/master/internal/api"
	"panel/master/internal/auth"
	"panel/master/internal/config"
	"panel/master/internal/console"
	"panel/master/internal/db"
	"panel/master/internal/metrics"
	"panel/master/internal/ws"
	"panel/master/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	cfg, err := config.Parse()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := bootstrapAdmin(database, cfg.CreateAdmin); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	hub := agent.NewHub()
	store := metrics.NewStore()
	consoles := console.NewHub()

	apiSrv := api.NewServer(database, hub, store, consoles, string(cfg.JWTSecret), cfg.TokenTTL)

	// Hub wiring: events feed the API caches; liveness touches the node row.
	// The callbacks are package-level hooks on the agent package.
	agent.OnEvent = apiSrv.HandleAgentEvent
	agent.OnConnect = func(nodeID int64) {
		_ = database.TouchNode(nodeID, time.Now().Unix())
		go apiSrv.SyncNodeState(nodeID)
	}
	agent.OnDisconnect = func(nodeID int64) {
		_ = database.TouchNode(nodeID, time.Now().Unix())
		apiSrv.MarkNodeOffline(nodeID)
	}

	// Console hub attaches/detaches the agent console stream.
	consoles.Attach = func(instanceID int64) { apiSrv.ConsoleAttach(instanceID) }
	consoles.Detach = func(instanceID int64) { apiSrv.ConsoleDetach(instanceID) }

	consoleWS := &ws.Handler{Hub: apiSrv.Hub, DB: database, Cons: consoles}
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	hub.StartKeepalive(hubCtx)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Mount("/api", apiSrv.Routes(http.HandlerFunc(consoleWS.ConsoleWS)))

	ui, err := web.Handler()
	if err != nil {
		slog.Error("web ui", "err", err)
		os.Exit(1)
	}
	r.Handle("/*", ui)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           securityHeaders(r),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("master listening", "addr", cfg.Addr, "data", cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shCtx)
}

// securityHeaders adds baseline hardening headers. TLS/HSTS itself must be
// terminated on a reverse proxy for public installs (see README).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; base-uri 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// bootstrapAdmin creates the initial admin account if requested via flag or
// when the panel has no users at all. When -create-admin names an existing
// user (e.g. reinstall over a kept data dir), the password is reset to the
// provided value so the installer never silently ignores the entered password.
func bootstrapAdmin(database *db.DB, createAdmin string) error {
	if createAdmin != "" {
		user, pass, ok := strings.Cut(createAdmin, ":")
		if !ok || user == "" || pass == "" {
			return errors.New("-create-admin must be username:password")
		}
		if err := createAdminUser(database, user, pass, "admin"); err != nil {
			if isUniqueViolation(err) {
				u, uerr := database.UserByName(user)
				if uerr != nil {
					return err
				}
				hash, herr := auth.HashPassword(pass)
				if herr != nil {
					return herr
				}
				if serr := database.SetPassword(u.ID, hash); serr != nil {
					return serr
				}
				slog.Info("reset password for existing user", "username", user)
				return nil
			}
			return err
		}
		slog.Info("created admin user", "username", user)
		return nil
	}
	n, err := database.UserCount()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pass, err := randomPassword()
	if err != nil {
		return err
	}
	if err := createAdminUser(database, "admin", pass, "admin"); err != nil {
		return err
	}
	printBootstrap("admin", pass)
	return nil
}

func createAdminUser(database *db.DB, username, password, role string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = database.CreateUser(username, hash, role)
	return err
}

// isUniqueViolation reports SQLite UNIQUE constraint failures.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// randomPassword generates a printable one-time bootstrap password.
func randomPassword() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
