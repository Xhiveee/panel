package db

import "time"

// AuditEntry is one audit trail record.
type AuditEntry struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	Username string `json:"username"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
}

// AddAudit records an audited action.
func (d *DB) AddAudit(username, action, target, detail string) {
	_, _ = d.sql.Exec(
		`INSERT INTO audit_log (ts, username, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), username, action, target, detail)
}

// Audit lists recent audit entries (newest first).
func (d *DB) Audit(limit int) ([]*AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.sql.Query(
		`SELECT id, ts, username, action, target, detail FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.TS, &e.Username, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
