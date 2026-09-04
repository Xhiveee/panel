package db

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ---------- users ----------

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	CreatedAt    int64  `json:"createdAt"`
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

const userCols = `id, username, password_hash, role, created_at`

// CreateUser inserts a new user.
func (d *DB) CreateUser(username, passwordHash, role string) (*User, error) {
	res, err := d.sql.Exec(
		`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, role, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.UserByID(id)
}

// UserByName finds a user by username.
func (d *DB) UserByName(name string) (*User, error) {
	return scanUser(d.sql.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, name))
}

// UserByID finds a user by id.
func (d *DB) UserByID(id int64) (*User, error) {
	return scanUser(d.sql.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// Users lists all users.
func (d *DB) Users() ([]*User, error) {
	rows, err := d.sql.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword updates a user's password hash.
func (d *DB) SetPassword(userID int64, hash string) error {
	_, err := d.sql.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

// SetRole updates a user's role.
func (d *DB) SetRole(userID int64, role string) error {
	_, err := d.sql.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, userID)
	return err
}

// DeleteUser removes a user.
func (d *DB) DeleteUser(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// UserCount returns the number of users.
func (d *DB) UserCount() (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ---------- user ↔ instances ----------

// UserInstanceIDs lists instance ids assigned to a user.
func (d *DB) UserInstanceIDs(userID int64) ([]int64, error) {
	rows, err := d.sql.Query(`SELECT instance_id FROM user_instances WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetUserInstances replaces the user's instance assignments atomically.
func (d *DB) SetUserInstances(userID int64, ids []int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM user_instances WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_instances (user_id, instance_id) VALUES (?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UserHasInstance reports whether the instance is assigned to the user.
func (d *DB) UserHasInstance(userID, instanceID int64) (bool, error) {
	var n int
	err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM user_instances WHERE user_id = ? AND instance_id = ?`,
		userID, instanceID).Scan(&n)
	return n > 0, err
}

func requireAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
