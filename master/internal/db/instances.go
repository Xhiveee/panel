package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Instance is a managed server instance.
type Instance struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	NodeID    int64             `json:"nodeId"`
	Dir       string            `json:"dir"`
	Cmd       []string          `json:"cmd"`
	StopCmd   string            `json:"stopCmd"`
	Env       map[string]string `json:"env"`
	CreatedAt int64             `json:"createdAt"`

	// Joined display fields / runtime info (not stored).
	NodeName string `json:"nodeName,omitempty"`
	NodeSeen int64  `json:"-"`
}

const instCols = `i.id, i.name, i.node_id, i.dir, i.cmd_json, i.stop_cmd, i.env_json, i.created_at`

func scanInstance(row interface{ Scan(...any) error }) (*Instance, error) {
	in := &Instance{}
	var cmdJSON, envJSON string
	err := row.Scan(&in.ID, &in.Name, &in.NodeID, &in.Dir, &cmdJSON, &in.StopCmd, &envJSON, &in.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(cmdJSON), &in.Cmd)
	_ = json.Unmarshal([]byte(envJSON), &in.Env)
	if in.Env == nil {
		in.Env = map[string]string{}
	}
	return in, nil
}

const instJoin = ` FROM instances i JOIN nodes n ON n.id = i.node_id`

func scanInstanceNamed(row interface{ Scan(...any) error }) (*Instance, error) {
	in := &Instance{}
	var cmdJSON, envJSON string
	err := row.Scan(&in.ID, &in.Name, &in.NodeID, &in.Dir, &cmdJSON, &in.StopCmd, &envJSON, &in.CreatedAt, &in.NodeName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(cmdJSON), &in.Cmd)
	_ = json.Unmarshal([]byte(envJSON), &in.Env)
	if in.Env == nil {
		in.Env = map[string]string{}
	}
	return in, nil
}

// CreateInstance validates and inserts an instance.
func (d *DB) CreateInstance(name string, nodeID int64, dir string, cmd []string, stopCmd string, env map[string]string) (*Instance, error) {
	cmdB, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	if env == nil {
		env = map[string]string{}
	}
	envB, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(
		`INSERT INTO instances (name, node_id, dir, cmd_json, stop_cmd, env_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, nodeID, dir, string(cmdB), stopCmd, string(envB), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetInstance(id)
}

// GetInstance fetches one instance (with node name).
func (d *DB) GetInstance(id int64) (*Instance, error) {
	return scanInstanceNamed(d.sql.QueryRow(
		`SELECT `+instCols+`, n.name AS node_name`+instJoin+` WHERE i.id = ?`, id))
}

// ListInstances lists instances; adminID == 0 returns all, otherwise only assigned.
func (d *DB) ListInstances(userID int64, admin bool) ([]*Instance, error) {
	q := `SELECT ` + instCols + `, n.name AS node_name` + instJoin
	var args []any
	if !admin {
		q += ` JOIN user_instances ui ON ui.instance_id = i.id AND ui.user_id = ?`
		args = append(args, userID)
	}
	q += ` ORDER BY i.id`
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		in, err := scanInstanceNamed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// InstancesByNode lists all instances hosted on a node.
func (d *DB) InstancesByNode(nodeID int64) ([]*Instance, error) {
	rows, err := d.sql.Query(
		`SELECT `+instCols+`, n.name AS node_name`+instJoin+` WHERE i.node_id = ? ORDER BY i.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		in, err := scanInstanceNamed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// UpdateInstance saves editable fields.
func (d *DB) UpdateInstance(id int64, name, dir string, cmd []string, stopCmd string, env map[string]string) (*Instance, error) {
	cmdB, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	if env == nil {
		env = map[string]string{}
	}
	envB, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(
		`UPDATE instances SET name = ?, dir = ?, cmd_json = ?, stop_cmd = ?, env_json = ? WHERE id = ?`,
		name, dir, string(cmdB), stopCmd, string(envB), id)
	if err != nil {
		return nil, err
	}
	if err := requireAffected(res); err != nil {
		return nil, err
	}
	return d.GetInstance(id)
}

// DeleteInstance removes an instance.
func (d *DB) DeleteInstance(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}
