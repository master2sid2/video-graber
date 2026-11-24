package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is an account in individual mode.
type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	IsAdmin     bool       `json:"is_admin"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`

	PasswordHash string `json:"-"`
}

const userCols = "id, username, password_hash, is_admin, disabled, created_at, last_login_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.Disabled, &created, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	u.LastLoginAt = unixPtr(last)
	return &u, nil
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CreateUser inserts a new account.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, admin bool) (*User, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO users(username, password_hash, is_admin, created_at) VALUES(?, ?, ?, ?)",
		username, passwordHash, admin, now())
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(ctx, id)
}

// UserByID loads an account by id.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
}

// UserByName loads an account by username (case insensitive).
func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", name))
}

// ListUsers returns all accounts ordered by id.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY id")
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

// UpdateUser updates mutable account flags.
func (s *Store) UpdateUser(ctx context.Context, id int64, admin, disabled bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE users SET is_admin = ?, disabled = ? WHERE id = ?", admin, disabled, id)
	return affected(res, err)
}

// SetPassword replaces the password hash of an account.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", hash, id)
	return affected(res, err)
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", now(), id)
	return err
}

// DeleteUser removes an account with all its data.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	return affected(res, err)
}

// CountAdmins returns the number of enabled administrators.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_admin = 1 AND disabled = 0").Scan(&n)
	return n, err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- sessions ----------

// Session is a login session; only the token hash is stored.
type Session struct {
	TokenHash string
	UserID    int64
	ExpiresAt time.Time
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, ttl time.Duration, ip, ua string) error {
	t := now()
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions(token_hash, user_id, created_at, expires_at, ip, user_agent) VALUES(?, ?, ?, ?, ?, ?)",
		tokenHash, userID, t, t+int64(ttl.Seconds()), ip, ua)
	return err
}

// SessionUser returns the user owning a valid session.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		"SELECT u.id, u.username, u.password_hash, u.is_admin, u.disabled, u.created_at, u.last_login_at "+
			"FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.expires_at > ?",
		tokenHash, now()))
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// DeleteUserSessions removes all sessions of a user, except keepHash.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keepHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?", userID, keepHash)
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now())
	return err
}

// ---------- user settings ----------

// UserPrefs returns the stored preferences JSON of a user ("{}" by default).
func (s *Store) UserPrefs(ctx context.Context, userID int64) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT prefs FROM user_settings WHERE user_id = ?", userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "{}", nil
	}
	return v, err
}

// SetUserPrefs stores preferences JSON.
func (s *Store) SetUserPrefs(ctx context.Context, userID int64, prefs string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO user_settings(user_id, prefs, updated_at) VALUES(?, ?, ?) "+
			"ON CONFLICT(user_id) DO UPDATE SET prefs = excluded.prefs, updated_at = excluded.updated_at",
		userID, prefs, now())
	return err
}

// UserCookies returns the user's cookies.txt contents.
func (s *Store) UserCookies(ctx context.Context, userID int64) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT cookies FROM user_settings WHERE user_id = ?", userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetUserCookies stores the user's cookies.txt contents ("" clears).
func (s *Store) SetUserCookies(ctx context.Context, userID int64, cookies string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO user_settings(user_id, cookies, updated_at) VALUES(?, ?, ?) "+
			"ON CONFLICT(user_id) DO UPDATE SET cookies = excluded.cookies, updated_at = excluded.updated_at",
		userID, cookies, now())
	return err
}
