// Package config loads master configuration from flags.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the master runtime configuration.
type Config struct {
	Addr     string
	DataDir  string
	TokenTTL time.Duration

	JWTSecret       []byte
	CreateAdmin     string // username:password bootstrap value (prefer -create-admin-file)
	CreateAdminFile string // path to a file containing username:password
}

// Parse reads flags and loads/creates the JWT secret.
func Parse() (*Config, error) {
	cfg := &Config{}
	flag.StringVar(&cfg.Addr, "addr", ":8080", "listen address")
	flag.StringVar(&cfg.DataDir, "data", "panel-data", "data directory")
	flag.DurationVar(&cfg.TokenTTL, "token-ttl", 24*time.Hour, "session token TTL")
	flag.StringVar(&cfg.CreateAdmin, "create-admin", "", "create admin on start as username:password (visible in ps; prefer -create-admin-file)")
	flag.StringVar(&cfg.CreateAdminFile, "create-admin-file", "", "path to a file containing username:password (0600)")
	flag.Parse()

	if cfg.CreateAdmin != "" {
		slog.Warn("-create-admin exposes the password in process args; use -create-admin-file")
	}
	if cfg.CreateAdminFile != "" {
		b, err := os.ReadFile(cfg.CreateAdminFile)
		if err != nil {
			return nil, fmt.Errorf("read create-admin file: %w", err)
		}
		cfg.CreateAdmin = string(b)
		// Trim a single trailing newline from echo > file usage.
		cfg.CreateAdmin = strings.TrimSuffix(cfg.CreateAdmin, "\n")
		cfg.CreateAdmin = strings.TrimSuffix(cfg.CreateAdmin, "\r")
	}

	if cfg.DataDir == "" {
		return nil, errors.New("-data must not be empty")
	}
	if cfg.TokenTTL <= 0 {
		return nil, errors.New("-token-ttl must be positive")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	secret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir, "jwt.secret"))
	if err != nil {
		return nil, err
	}
	cfg.JWTSecret = secret
	return cfg, nil
}

// DBPath returns the SQLite file path inside the data dir.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "panel.db")
}

// loadOrCreateSecret reads or generates the JWT signing secret.
func loadOrCreateSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	secret := []byte(hex.EncodeToString(raw))
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("write jwt secret: %w", err)
	}
	slog.Info("generated new JWT secret", "path", path)
	return secret, nil
}
