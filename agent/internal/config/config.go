package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
)

// Config is the agent configuration, persisted as JSON.
type Config struct {
	MasterURL string `json:"masterUrl"` // ws(s)://master/api/agent/ws
	Token     string `json:"token"`
	DataDir   string `json:"dataDir,omitempty"`
	Name      string `json:"name,omitempty"`
}

// DefaultPath is where Load looks without an explicit -config flag.
const DefaultPath = "agent.json"

// Load resolves configuration from flags, the config file and defaults.
func Load() *Config {
	path := ""
	c := &Config{}
	flag.StringVar(&path, "config", DefaultPath, "config file path")
	flag.StringVar(&c.MasterURL, "master", "", "master url, overrides config")
	flag.StringVar(&c.Token, "token", "", "node token, overrides config")
	flag.StringVar(&c.DataDir, "data", "", "data dir, overrides config")
	flag.StringVar(&c.Name, "name", "", "node label, overrides config")
	flag.Parse()

	if path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			var fc Config
			if err := json.Unmarshal(b, &fc); err != nil {
				slog.Error("parse config", "path", path, "err", err)
				os.Exit(1)
			}
			if c.MasterURL == "" {
				c.MasterURL = fc.MasterURL
			}
			if c.Token == "" {
				c.Token = fc.Token
			}
			if c.DataDir == "" {
				c.DataDir = fc.DataDir
			}
			if c.Name == "" {
				c.Name = fc.Name
			}
		} else if path != DefaultPath {
			slog.Error("read config", "path", path, "err", err)
			os.Exit(1)
		}
	}
	if c.DataDir == "" {
		c.DataDir = "agent-data"
	}
	if c.Name == "" {
		c.Name = "agent"
	}
	if c.MasterURL == "" {
		slog.Error("master url is required (-master or config file)")
		os.Exit(1)
	}
	if c.Token == "" {
		slog.Error("node token is required (-token or config file); create a node in the panel first")
		os.Exit(1)
	}
	return c
}

// Path returns the config file path used by Load.
func Path() string {
	p := flag.Lookup("config")
	if p == nil || p.Value.String() == "" {
		return DefaultPath
	}
	return p.Value.String()
}

// Save persists the config with restrictive permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), b, 0o600)
}
