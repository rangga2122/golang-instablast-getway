package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type AppUser struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"is_admin"`
	IsTrial      bool      `json:"is_trial"`
	CanUseAI     bool      `json:"can_use_ai"`
	MaxDevices   int       `json:"max_devices"`
	ExpiresAt    time.Time `json:"expires_at"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateUserInput struct {
	Email      string
	Password   string
	IsAdmin    bool
	IsTrial    bool
	CanUseAI   bool
	MaxDevices int
	ExpiresAt  time.Time
}

type UpdateUserInput struct {
	Email      string
	Password   string
	IsAdmin    bool
	IsTrial    bool
	CanUseAI   bool
	MaxDevices int
	ExpiresAt  time.Time
	IsActive   bool
}

func (s *Storage) EnsureUserTable() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS app_users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		is_trial INTEGER NOT NULL DEFAULT 0,
		can_use_ai INTEGER NOT NULL DEFAULT 0,
		max_devices INTEGER NOT NULL DEFAULT 1,
		expires_at DATETIME NOT NULL,
		is_active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err == nil {
		_, _ = s.db.Exec(`ALTER TABLE app_users ADD COLUMN is_trial INTEGER NOT NULL DEFAULT 0`)
		_, _ = s.db.Exec(`UPDATE app_users SET max_devices = 1 WHERE is_trial = 1 AND max_devices > 1`)
	}
	return err
}

func (s *Storage) SeedAdminUser(email, password string) error {
	if err := s.EnsureUserTable(); err != nil {
		return err
	}
	existing, err := s.GetUserByEmail(email)
	if err == nil && existing.ID != "" {
		return nil
	}
	_, err = s.CreateUser(CreateUserInput{
		Email:      email,
		Password:   password,
		IsAdmin:    true,
		IsTrial:    false,
		CanUseAI:   true,
		MaxDevices: 100,
		ExpiresAt:  time.Date(2099, 12, 31, 23, 59, 59, 0, time.Local),
	})
	return err
}

func (s *Storage) CreateUser(input CreateUserInput) (AppUser, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	email := strings.TrimSpace(strings.ToLower(input.Email))
	if email == "" {
		return AppUser{}, fmt.Errorf("email wajib diisi")
	}
	if len(strings.TrimSpace(input.Password)) < 4 {
		return AppUser{}, fmt.Errorf("password minimal 4 karakter")
	}
	if input.MaxDevices <= 0 {
		input.MaxDevices = 1
	}
	if input.IsTrial {
		input.MaxDevices = 1
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = time.Now().Add(30 * 24 * time.Hour)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return AppUser{}, err
	}

	id := fmt.Sprintf("usr-%d", time.Now().UnixNano())
	_, err = s.db.Exec(
		`INSERT INTO app_users (id, email, password_hash, is_admin, is_trial, can_use_ai, max_devices, expires_at, is_active, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP)`,
		id,
		email,
		string(hash),
		boolToInt(input.IsAdmin),
		boolToInt(input.IsTrial),
		boolToInt(input.CanUseAI),
		input.MaxDevices,
		input.ExpiresAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return AppUser{}, fmt.Errorf("email sudah terdaftar")
		}
		return AppUser{}, err
	}
	return s.getUserByFieldLocked("id", id)
}

func (s *Storage) GetUserByEmail(email string) (AppUser, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.getUserByFieldLocked("email", strings.TrimSpace(strings.ToLower(email)))
}

func (s *Storage) GetUserByID(id string) (AppUser, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.getUserByFieldLocked("id", strings.TrimSpace(id))
}

func (s *Storage) ListUsers() ([]AppUser, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	rows, err := s.db.Query(`SELECT id, email, password_hash, is_admin, is_trial, can_use_ai, max_devices, expires_at, is_active, created_at, updated_at
		FROM app_users
		ORDER BY created_at DESC, email ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []AppUser
	for rows.Next() {
		user, err := scanAppUser(rows)
		if err != nil {
			continue
		}
		users = append(users, user)
	}
	return users, nil
}

func (s *Storage) DeleteUserByID(id string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(`DELETE FROM app_users WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func (s *Storage) UpdateUserByID(id string, input UpdateUserInput) (AppUser, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	id = strings.TrimSpace(id)
	if id == "" {
		return AppUser{}, fmt.Errorf("id user tidak valid")
	}

	current, err := s.getUserByFieldLocked("id", id)
	if err != nil {
		return AppUser{}, err
	}

	email := strings.TrimSpace(strings.ToLower(input.Email))
	if email == "" {
		email = current.Email
	}
	if email == "" {
		return AppUser{}, fmt.Errorf("email wajib diisi")
	}
	if email != current.Email {
		existing, err := s.getUserByFieldLocked("email", email)
		if err == nil && existing.ID != "" && existing.ID != current.ID {
			return AppUser{}, fmt.Errorf("email sudah terdaftar")
		}
		if err != nil && err != sql.ErrNoRows {
			return AppUser{}, err
		}
	}

	if input.MaxDevices <= 0 {
		input.MaxDevices = 1
	}
	if input.IsTrial {
		input.MaxDevices = 1
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = current.ExpiresAt
	}

	passwordHash := current.PasswordHash
	if strings.TrimSpace(input.Password) != "" {
		if len(strings.TrimSpace(input.Password)) < 4 {
			return AppUser{}, fmt.Errorf("password minimal 4 karakter")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return AppUser{}, err
		}
		passwordHash = string(hash)
	}

	_, err = s.db.Exec(
		`UPDATE app_users
		 SET email = ?, password_hash = ?, is_admin = ?, is_trial = ?, can_use_ai = ?, max_devices = ?, expires_at = ?, is_active = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		email,
		passwordHash,
		boolToInt(input.IsAdmin),
		boolToInt(input.IsTrial),
		boolToInt(input.CanUseAI),
		input.MaxDevices,
		input.ExpiresAt,
		boolToInt(input.IsActive),
		id,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return AppUser{}, fmt.Errorf("email sudah terdaftar")
		}
		return AppUser{}, err
	}
	return s.getUserByFieldLocked("id", id)
}

func (s *Storage) AuthenticateUser(email, password string) (AppUser, error) {
	user, err := s.GetUserByEmail(email)
	if err != nil {
		return AppUser{}, fmt.Errorf("email atau password salah")
	}
	if !user.IsActive {
		return AppUser{}, fmt.Errorf("akun nonaktif")
	}
	if !user.ExpiresAt.IsZero() && time.Now().After(user.ExpiresAt) {
		return AppUser{}, fmt.Errorf("masa aktif akun sudah habis")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return AppUser{}, fmt.Errorf("email atau password salah")
	}
	return user, nil
}

func (s *Storage) getUserByFieldLocked(field, value string) (AppUser, error) {
	row := s.db.QueryRow(
		fmt.Sprintf(`SELECT id, email, password_hash, is_admin, is_trial, can_use_ai, max_devices, expires_at, is_active, created_at, updated_at FROM app_users WHERE %s = ? LIMIT 1`, field),
		value,
	)
	return scanAppUser(row)
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanAppUser(row scanner) (AppUser, error) {
	var (
		user                       AppUser
		isAdmin, isTrial, canUseAI int
		isActive                   int
	)
	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&isAdmin,
		&isTrial,
		&canUseAI,
		&user.MaxDevices,
		&user.ExpiresAt,
		&isActive,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return AppUser{}, err
		}
		return AppUser{}, err
	}
	user.IsAdmin = isAdmin == 1
	user.IsTrial = isTrial == 1
	user.CanUseAI = canUseAI == 1
	user.IsActive = isActive == 1
	return user, nil
}
