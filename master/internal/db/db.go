// Package db persists panel state in SQLite (modernc, pure Go).
//
// Models and queries live in split files: users.go, nodes.go,
// instances.go, audit.go, token.go.
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB handle.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database and applies migrations.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection avoids SQLITE_BUSY contention entirely; the panel
	// workload is light.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	if err := migrate(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{sql: d}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

func migrate(d *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'user',
	created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS instances (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	dir        TEXT NOT NULL,
	cmd_json   TEXT NOT NULL,
	stop_cmd   TEXT NOT NULL DEFAULT '',
	env_json   TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_instances (
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	instance_id INTEGER NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, instance_id)
);

CREATE TABLE IF NOT EXISTS audit_log (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       INTEGER NOT NULL,
	username TEXT NOT NULL DEFAULT '',
	action   TEXT NOT NULL,
	target   TEXT NOT NULL DEFAULT '',
	detail   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_instances_node ON instances(node_id);
`
	_, err := d.Exec(schema)
	return err
}
