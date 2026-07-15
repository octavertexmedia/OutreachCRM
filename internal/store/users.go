package store

import (
	"database/sql"
	"fmt"

	"github.com/manishkumar/outreachcrm/internal/models"
	"golang.org/x/crypto/bcrypt"
)

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) BootstrapAdmin(email, password string) error {
	n, err := s.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	wsID, err := s.EnsureWorkspace("Default")
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO users(email, password_hash, role, active, created_at, workspace_id) VALUES(?,?,?,?,?,?)`,
		email, string(hash), models.RoleAdmin, 1, fmtTime(now()), wsID)
	return err
}

func (s *Store) CreateUser(email, password, role string) (int64, error) {
	return s.CreateUserInWorkspace(email, password, role, 1)
}

func (s *Store) CreateUserInWorkspace(email, password, role string, workspaceID int64) (int64, error) {
	if role == "" {
		role = models.RoleSender
	}
	if workspaceID == 0 {
		workspaceID = 1
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO users(email, password_hash, role, active, created_at, workspace_id) VALUES(?,?,?,?,?,?)`,
		email, string(hash), role, 1, fmtTime(now()), workspaceID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanUser(row scannable) (models.User, error) {
	var u models.User
	var active, totpEn int
	var created string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &active, &created,
		&u.TOTPSecretEnc, &totpEn, &u.WorkspaceID)
	u.Active = active == 1
	u.TOTPEnabled = totpEn == 1
	u.CreatedAt = parseTime(created)
	if u.WorkspaceID == 0 {
		u.WorkspaceID = 1
	}
	return u, err
}

func (s *Store) Authenticate(email, password string) (models.User, error) {
	row := s.db.QueryRow(`SELECT id, email, password_hash, role, active, created_at,
		COALESCE(totp_secret_enc,''), COALESCE(totp_enabled,0), COALESCE(workspace_id,1)
		FROM users WHERE email=?`, email)
	u, err := scanUser(row)
	if err != nil {
		return u, err
	}
	if !u.Active {
		return u, fmt.Errorf("account disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return u, sql.ErrNoRows
	}
	return u, nil
}

func (s *Store) GetUser(id int64) (models.User, error) {
	row := s.db.QueryRow(`SELECT id, email, password_hash, role, active, created_at,
		COALESCE(totp_secret_enc,''), COALESCE(totp_enabled,0), COALESCE(workspace_id,1)
		FROM users WHERE id=?`, id)
	return scanUser(row)
}

func (s *Store) ListUsers() ([]models.User, error) {
	rows, err := s.db.Query(`SELECT id, email, password_hash, role, active, created_at,
		COALESCE(totp_secret_enc,''), COALESCE(totp_enabled,0), COALESCE(workspace_id,1) FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		u.PasswordHash = ""
		u.TOTPSecretEnc = ""
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) SetUserActive(id int64, active bool) error {
	v := 0
	if active {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE users SET active=? WHERE id=?`, v, id)
	return err
}

func (s *Store) SaveOAuthState(state string, userID int64, provider string, expires string) error {
	_, err := s.db.Exec(`INSERT INTO oauth_states(state, user_id, provider, expires_at) VALUES(?,?,?,?)`,
		state, userID, provider, expires)
	return err
}
