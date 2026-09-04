package db

import (
	"database/sql"
	"errors"
	"time"
)

// Node is a registered agent node.
type Node struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"-"`
	CreatedAt int64  `json:"createdAt"`
	LastSeen  int64  `json:"lastSeen"`
}

// Token is returned once when a node is created.
type Token struct {
	Token string `json:"token"`
	Hash  string `json:"-"`
}

const nodeCols = `id, name, token_hash, created_at, last_seen`

func scanNode(row interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	err := row.Scan(&n.ID, &n.Name, &n.TokenHash, &n.CreatedAt, &n.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// CreateNode registers a node and returns its one-time token.
func (d *DB) CreateNode(name string) (*Node, *Token, error) {
	tok, hash, err := GenToken()
	if err != nil {
		return nil, nil, err
	}
	res, err := d.sql.Exec(
		`INSERT INTO nodes (name, token_hash, created_at, last_seen) VALUES (?, ?, ?, 0)`,
		name, hash, time.Now().Unix())
	if err != nil {
		return nil, nil, err
	}
	id, _ := res.LastInsertId()
	n, err := scanNode(d.sql.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
	if err != nil {
		return nil, nil, err
	}
	return n, &Token{Token: tok, Hash: hash}, nil
}

// NodeByToken resolves a node by its raw bearer token.
func (d *DB) NodeByToken(token string) (*Node, error) {
	return scanNode(d.sql.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE token_hash = ?`, hashToken(token)))
}

// NodeByID fetches a node.
func (d *DB) NodeByID(id int64) (*Node, error) {
	return scanNode(d.sql.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
}

// Nodes lists all nodes.
func (d *DB) Nodes() ([]*Node, error) {
	rows, err := d.sql.Query(`SELECT ` + nodeCols + ` FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TouchNode updates last_seen.
func (d *DB) TouchNode(id int64, ts int64) error {
	_, err := d.sql.Exec(`UPDATE nodes SET last_seen = ? WHERE id = ?`, ts, id)
	return err
}

// DeleteNode removes a node (instances cascade).
func (d *DB) DeleteNode(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}
