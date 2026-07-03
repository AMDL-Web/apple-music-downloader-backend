package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"amdl/internal/domain"
	"amdl/internal/storage"
)

// IsConflict reports whether err is a SQLite unique-constraint violation,
// e.g. a duplicate username, alias, or email.
func IsConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

const userSelect = `SELECT id,username,role,avatar_url,enabled,created_at,updated_at FROM users`

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(row userScanner) (domain.User, error) {
	var user domain.User
	var role, created, updated string
	var enabled int
	err := row.Scan(&user.ID, &user.Username, &role, &user.AvatarURL, &enabled, &created, &updated)
	user.Role = domain.Role(role)
	user.Enabled = enabled != 0
	user.CreatedAt = parseTime(created)
	user.UpdatedAt = parseTime(updated)
	return user, err
}

func (s *Store) loadIdentities(ctx context.Context, user *domain.User) error {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,value FROM user_identities WHERE user_id=? ORDER BY id`, user.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	user.Aliases = []string{}
	user.Emails = []string{}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return err
		}
		switch kind {
		case "alias":
			user.Aliases = append(user.Aliases, value)
		case "email":
			user.Emails = append(user.Emails, value)
		}
	}
	return rows.Err()
}

// CreateUser inserts user with its alias/email identities in one transaction.
// A missing ID is generated; timestamps are set when zero.
func (s *Store) CreateUser(ctx context.Context, user domain.User) (domain.User, error) {
	if user.ID == "" {
		user.ID = storage.NewID("user")
	}
	if user.Role == "" {
		user.Role = domain.RoleUser
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now()
	}
	user.UpdatedAt = user.CreatedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return user, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,role,avatar_url,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		user.ID, user.Username, string(user.Role), user.AvatarURL, boolToInt(user.Enabled), formatTime(user.CreatedAt), formatTime(user.UpdatedAt)); err != nil {
		return user, err
	}
	if err := insertIdentities(ctx, tx, user.ID, user.Aliases, user.Emails); err != nil {
		return user, err
	}
	return user, tx.Commit()
}

// UpdateUser rewrites the mutable user fields and replaces all identities.
func (s *Store) UpdateUser(ctx context.Context, user domain.User) error {
	user.UpdatedAt = now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET role=?, avatar_url=?, enabled=?, updated_at=? WHERE id=?`,
		string(user.Role), user.AvatarURL, boolToInt(user.Enabled), formatTime(user.UpdatedAt), user.ID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_identities WHERE user_id=?`, user.ID); err != nil {
		return err
	}
	if err := insertIdentities(ctx, tx, user.ID, user.Aliases, user.Emails); err != nil {
		return err
	}
	return tx.Commit()
}

func insertIdentities(ctx context.Context, tx *sql.Tx, userID string, aliases, emails []string) error {
	for _, alias := range aliases {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_identities(user_id,kind,value) VALUES(?,?,?)`, userID, "alias", alias); err != nil {
			return err
		}
	}
	for _, email := range emails {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_identities(user_id,kind,value) VALUES(?,?,?)`, userID, "email", email); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (domain.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE id=?`, id))
	if err != nil {
		return user, err
	}
	return user, s.loadIdentities(ctx, &user)
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (domain.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE username=? COLLATE NOCASE`, username))
	if err != nil {
		return user, err
	}
	return user, s.loadIdentities(ctx, &user)
}

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+` ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.loadIdentities(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// CountEnabledAdmins returns the number of users who can currently reach
// admin-only endpoints (role admin and enabled). Used to prevent removing the
// last administrator, which would lock the system out of user management.
func (s *Store) CountEnabledAdmins(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role=? AND enabled=1`, string(domain.RoleAdmin)).Scan(&count)
	return count, err
}

// ResolveIdentity maps trusted proxy headers to a user, mirroring reference A's
// normalize_username: X-User against username and alias identities first, then
// X-Email against email identities. All matches are case-insensitive.
func (s *Store) ResolveIdentity(ctx context.Context, xUser, xEmail string) (domain.User, error) {
	if xUser != "" {
		user, err := s.GetUserByUsername(ctx, xUser)
		if err == nil {
			return user, nil
		}
		if err != sql.ErrNoRows {
			return domain.User{}, err
		}
		user, err = s.getUserByIdentity(ctx, "alias", xUser)
		if err == nil {
			return user, nil
		}
		if err != sql.ErrNoRows {
			return domain.User{}, err
		}
	}
	if xEmail != "" {
		return s.getUserByIdentity(ctx, "email", xEmail)
	}
	return domain.User{}, sql.ErrNoRows
}

func (s *Store) getUserByIdentity(ctx context.Context, kind, value string) (domain.User, error) {
	row := s.db.QueryRowContext(ctx, userSelect+` WHERE id=(SELECT user_id FROM user_identities WHERE kind=? AND value=? COLLATE NOCASE)`, kind, value)
	user, err := scanUser(row)
	if err != nil {
		return user, err
	}
	return user, s.loadIdentities(ctx, &user)
}

// EnsureBootstrapAdmin creates the bootstrap admin when the users table is
// empty. On later starts it returns the existing user with that username, or a
// zero user when the table is non-empty and the username is absent.
func (s *Store) EnsureBootstrapAdmin(ctx context.Context, username, email string) (domain.User, error) {
	count, err := s.CountUsers(ctx)
	if err != nil {
		return domain.User{}, err
	}
	if count > 0 {
		user, err := s.GetUserByUsername(ctx, username)
		if err == sql.ErrNoRows {
			return domain.User{}, nil
		}
		return user, err
	}
	if !domain.ValidUsername(username) {
		return domain.User{}, fmt.Errorf("bootstrap admin username %q must match ^[a-z0-9_-]{1,32}$", username)
	}
	user := domain.User{Username: username, Role: domain.RoleAdmin, Enabled: true}
	if email != "" {
		user.Emails = []string{email}
	}
	return s.CreateUser(ctx, user)
}

// AssignJobsWithoutUser attributes legacy jobs that predate multi-user support
// to the given user.
func (s *Store) AssignJobsWithoutUser(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET user_id=? WHERE user_id=''`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
