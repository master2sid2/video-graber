package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---------- invites ----------

// Invite is a registration code created by an administrator.
type Invite struct {
	ID        int64      `json:"id"`
	Code      string     `json:"code"`
	Note      string     `json:"note"`
	CreatedBy int64      `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
}

// Usable reports whether the invite can still be redeemed.
func (i *Invite) Usable() bool {
	if i.ExpiresAt != nil && time.Now().After(*i.ExpiresAt) {
		return false
	}
	return i.MaxUses <= 0 || i.Uses < i.MaxUses
}

const inviteCols = "id, code, note, COALESCE(created_by, 0), created_at, expires_at, max_uses, uses"

func scanInvite(row interface{ Scan(...any) error }) (*Invite, error) {
	var i Invite
	var created int64
	var exp sql.NullInt64
	if err := row.Scan(&i.ID, &i.Code, &i.Note, &i.CreatedBy, &created, &exp, &i.MaxUses, &i.Uses); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	i.CreatedAt = time.Unix(created, 0)
	i.ExpiresAt = unixPtr(exp)
	return &i, nil
}

// CreateInvite stores a new invite. ttl <= 0 means it never expires,
// maxUses <= 0 means unlimited.
func (s *Store) CreateInvite(ctx context.Context, code, note string, by int64, ttl time.Duration, maxUses int) (*Invite, error) {
	var exp any
	if ttl > 0 {
		exp = time.Now().Add(ttl).Unix()
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO invites(code, note, created_by, created_at, expires_at, max_uses) VALUES(?, ?, ?, ?, ?, ?)",
		code, note, by, now(), exp, maxUses)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return scanInvite(s.db.QueryRowContext(ctx, "SELECT "+inviteCols+" FROM invites WHERE id = ?", id))
}

// InviteByCode loads an invite.
func (s *Store) InviteByCode(ctx context.Context, code string) (*Invite, error) {
	return scanInvite(s.db.QueryRowContext(ctx, "SELECT "+inviteCols+" FROM invites WHERE code = ?", code))
}

// ListInvites returns all invites, newest first.
func (s *Store) ListInvites(ctx context.Context) ([]*Invite, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+inviteCols+" FROM invites ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteInvite removes an invite.
func (s *Store) DeleteInvite(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM invites WHERE id = ?", id)
	return affected(res, err)
}

// RegisterWithInvite atomically redeems an invite and creates a user.
func (s *Store) RegisterWithInvite(ctx context.Context, code, username, passwordHash string) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inv, err := scanInvite(tx.QueryRowContext(ctx, "SELECT "+inviteCols+" FROM invites WHERE code = ?", code))
	if err != nil {
		return nil, err
	}
	if !inv.Usable() {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE invites SET uses = uses + 1 WHERE id = ?", inv.ID); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO users(username, password_hash, is_admin, created_at) VALUES(?, ?, 0, ?)", username, passwordHash, now())
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.UserByID(ctx, id)
}

// ---------- categories ----------

// Category groups downloaded materials of a user.
type Category struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"created_at"`
	Count     int       `json:"count"`
}

// ListCategories returns the user's categories with item counts.
func (s *Store) ListCategories(ctx context.Context, userID int64) ([]*Category, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.name, c.color, c.created_at, (SELECT COUNT(*) FROM history h WHERE h.category_id = c.id)
		 FROM categories c WHERE c.user_id = ? ORDER BY c.name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Category{}
	for rows.Next() {
		var c Category
		var created int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Color, &created, &c.Count); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// CreateCategory adds a category for the user.
func (s *Store) CreateCategory(ctx context.Context, userID int64, name, color string) (*Category, error) {
	t := now()
	res, err := s.db.ExecContext(ctx, "INSERT INTO categories(user_id, name, color, created_at) VALUES(?, ?, ?, ?)",
		userID, name, color, t)
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Category{ID: id, Name: name, Color: color, CreatedAt: time.Unix(t, 0)}, nil
}

// UpdateCategory renames/recolors a category owned by the user.
func (s *Store) UpdateCategory(ctx context.Context, userID, id int64, name, color string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE categories SET name = ?, color = ? WHERE id = ? AND user_id = ?",
		name, color, id, userID)
	return affected(res, err)
}

// DeleteCategory removes a category; its history items become uncategorized.
func (s *Store) DeleteCategory(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM categories WHERE id = ? AND user_id = ?", id, userID)
	return affected(res, err)
}

// CategoryOwned reports whether the category belongs to the user.
func (s *Store) CategoryOwned(ctx context.Context, userID, id int64) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories WHERE id = ? AND user_id = ?", id, userID).Scan(&n)
	return n > 0
}

// ---------- history ----------

// HistoryItem is one recorded download.
type HistoryItem struct {
	ID         int64      `json:"id"`
	CategoryID *int64     `json:"category_id"`
	PageURL    string     `json:"page_url"`
	Title      string     `json:"title"`
	Subtitle   string     `json:"subtitle"`
	Thumbnail  string     `json:"thumbnail"`
	Extractor  string     `json:"extractor"`
	Options    string     `json:"options"`
	Status     string     `json:"status"`
	Error      string     `json:"error"`
	Size       int64      `json:"size"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// AddHistory records a new download and returns its id.
func (s *Store) AddHistory(ctx context.Context, userID int64, h *HistoryItem) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO history(user_id, category_id, page_url, title, subtitle, thumbnail, extractor, options, status, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, h.CategoryID, h.PageURL, h.Title, h.Subtitle, h.Thumbnail, h.Extractor, h.Options, h.Status, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishHistory stores the final state of a download.
func (s *Store) FinishHistory(ctx context.Context, id int64, status, errText string, size int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE history SET status = ?, error = ?, size = ?, finished_at = ? WHERE id = ?",
		status, errText, size, now(), id)
	return err
}

// HistoryQuery filters ListHistory.
type HistoryQuery struct {
	CategoryID    int64 // 0 = any, -1 = uncategorized
	Search        string
	Limit, Offset int
}

// ListHistory returns history items and the total number of matches.
func (s *Store) ListHistory(ctx context.Context, userID int64, q HistoryQuery) ([]*HistoryItem, int, error) {
	where := []string{"user_id = ?"}
	args := []any{userID}
	switch {
	case q.CategoryID > 0:
		where = append(where, "category_id = ?")
		args = append(args, q.CategoryID)
	case q.CategoryID < 0:
		where = append(where, "category_id IS NULL")
	}
	if q.Search != "" {
		where = append(where, "(title LIKE ? ESCAPE '\\' OR subtitle LIKE ? ESCAPE '\\' OR page_url LIKE ? ESCAPE '\\')")
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Search) + "%"
		args = append(args, like, like, like)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM history WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, category_id, page_url, title, subtitle, thumbnail, extractor, options, status, error, size, created_at, finished_at
		 FROM history WHERE `+cond+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		var cat, fin sql.NullInt64
		var created int64
		if err := rows.Scan(&h.ID, &cat, &h.PageURL, &h.Title, &h.Subtitle, &h.Thumbnail, &h.Extractor, &h.Options,
			&h.Status, &h.Error, &h.Size, &created, &fin); err != nil {
			return nil, 0, err
		}
		if cat.Valid {
			h.CategoryID = new(cat.Int64)
		}
		h.CreatedAt = time.Unix(created, 0)
		h.FinishedAt = unixPtr(fin)
		out = append(out, &h)
	}
	return out, total, rows.Err()
}

// SetHistoryCategory moves a history item to a category (nil = none).
func (s *Store) SetHistoryCategory(ctx context.Context, userID, id int64, categoryID *int64) error {
	res, err := s.db.ExecContext(ctx, "UPDATE history SET category_id = ? WHERE id = ? AND user_id = ?", categoryID, id, userID)
	return affected(res, err)
}

// DeleteHistory removes a history item.
func (s *Store) DeleteHistory(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM history WHERE id = ? AND user_id = ?", id, userID)
	return affected(res, err)
}

// ClearHistory removes all history of a user.
func (s *Store) ClearHistory(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM history WHERE user_id = ?", userID)
	return err
}
