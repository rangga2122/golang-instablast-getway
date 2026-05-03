package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type TrialOTPSession struct {
	SessionID     string    `json:"session_id"`
	Email         string    `json:"email"`
	PlainPassword string    `json:"-"`
	Phone         string    `json:"phone"`
	OTPCode       string    `json:"-"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	IPAddress     string    `json:"ip_address"`
	UserAgent     string    `json:"user_agent"`
	ExpiresAt     time.Time `json:"expires_at"`
	VerifiedAt    time.Time `json:"verified_at"`
	UsedAt        time.Time `json:"used_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Storage) EnsureTrialOTPSessionTable() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	queries := []string{
		`CREATE TABLE IF NOT EXISTS trial_otp_sessions (
			session_id TEXT PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			plain_password TEXT NOT NULL DEFAULT '',
			phone TEXT NOT NULL DEFAULT '',
			otp_code TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			attempts INTEGER NOT NULL DEFAULT 0,
			ip_address TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			expires_at DATETIME NOT NULL,
			verified_at DATETIME,
			used_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_otp_phone_status ON trial_otp_sessions (phone, status)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_otp_email_status ON trial_otp_sessions (email, status)`,
	}
	for _, query := range queries {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) InvalidatePendingTrialOTPSessions(email, phone string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`UPDATE trial_otp_sessions
		 SET status = 'replaced', plain_password = '', otp_code = '', updated_at = CURRENT_TIMESTAMP
		 WHERE status = 'pending' AND (email = ? OR phone = ?)`,
		normalizeTrialOTPSessionEmail(email),
		normalizeTrialOTPSessionPhone(phone),
	)
	return err
}

func (s *Storage) CreateTrialOTPSession(session TrialOTPSession) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if strings.TrimSpace(session.SessionID) == "" {
		return fmt.Errorf("session id wajib diisi")
	}
	if session.ExpiresAt.IsZero() {
		return fmt.Errorf("expires_at wajib diisi")
	}

	_, err := s.db.Exec(
		`INSERT INTO trial_otp_sessions (
			session_id, email, plain_password, phone, otp_code, status, attempts, ip_address, user_agent, expires_at, verified_at, used_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		strings.TrimSpace(session.SessionID),
		normalizeTrialOTPSessionEmail(session.Email),
		strings.TrimSpace(session.PlainPassword),
		normalizeTrialOTPSessionPhone(session.Phone),
		strings.TrimSpace(session.OTPCode),
		firstNonEmptyTrialOTPStatus(session.Status, "pending"),
		session.Attempts,
		strings.TrimSpace(session.IPAddress),
		strings.TrimSpace(session.UserAgent),
		sqliteTimeValue(session.ExpiresAt),
		nullableTime(session.VerifiedAt),
		nullableTime(session.UsedAt),
	)
	return err
}

func (s *Storage) GetTrialOTPSession(sessionID string) (TrialOTPSession, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	row := s.db.QueryRow(
		`SELECT session_id, email, plain_password, phone, otp_code, status, attempts, ip_address, user_agent, expires_at, verified_at, used_at, created_at, updated_at
		 FROM trial_otp_sessions
		 WHERE session_id = ?
		 LIMIT 1`,
		strings.TrimSpace(sessionID),
	)
	return scanTrialOTPSession(row)
}

func (s *Storage) IncrementTrialOTPSessionAttempts(sessionID string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`UPDATE trial_otp_sessions
		 SET attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
		 WHERE session_id = ?`,
		strings.TrimSpace(sessionID),
	)
	return err
}

func (s *Storage) MarkTrialOTPSessionVerified(sessionID string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`UPDATE trial_otp_sessions
		 SET status = 'verified', verified_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE session_id = ?`,
		strings.TrimSpace(sessionID),
	)
	return err
}

func (s *Storage) MarkTrialOTPSessionUsed(sessionID string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`UPDATE trial_otp_sessions
		 SET status = 'used', plain_password = '', otp_code = '', used_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE session_id = ?`,
		strings.TrimSpace(sessionID),
	)
	return err
}

func (s *Storage) MarkTrialOTPSessionExpired(sessionID string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`UPDATE trial_otp_sessions
		 SET status = 'expired', plain_password = '', otp_code = '', updated_at = CURRENT_TIMESTAMP
		 WHERE session_id = ?`,
		strings.TrimSpace(sessionID),
	)
	return err
}

func (s *Storage) MarkTrialOTPSessionFailed(sessionID string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`UPDATE trial_otp_sessions
		 SET status = 'failed', plain_password = '', otp_code = '', updated_at = CURRENT_TIMESTAMP
		 WHERE session_id = ?`,
		strings.TrimSpace(sessionID),
	)
	return err
}

func (s *Storage) CountUsedTrialOTPSessionsByPhone(phone string) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM trial_otp_sessions WHERE phone = ? AND status = 'used'`,
		normalizeTrialOTPSessionPhone(phone),
	).Scan(&count)
	return count, err
}

func scanTrialOTPSession(row scanner) (TrialOTPSession, error) {
	var (
		item          TrialOTPSession
		expiresAtRaw  string
		verifiedAtRaw sql.NullString
		usedAtRaw     sql.NullString
		createdAtRaw  sql.NullString
		updatedAtRaw  sql.NullString
	)
	err := row.Scan(
		&item.SessionID,
		&item.Email,
		&item.PlainPassword,
		&item.Phone,
		&item.OTPCode,
		&item.Status,
		&item.Attempts,
		&item.IPAddress,
		&item.UserAgent,
		&expiresAtRaw,
		&verifiedAtRaw,
		&usedAtRaw,
		&createdAtRaw,
		&updatedAtRaw,
	)
	if err != nil {
		return TrialOTPSession{}, err
	}
	item.Email = normalizeTrialOTPSessionEmail(item.Email)
	item.Phone = normalizeTrialOTPSessionPhone(item.Phone)
	item.ExpiresAt = parseSQLiteTime(expiresAtRaw)
	item.VerifiedAt = parseSQLiteTime(verifiedAtRaw.String)
	item.UsedAt = parseSQLiteTime(usedAtRaw.String)
	item.CreatedAt = parseSQLiteTime(createdAtRaw.String)
	item.UpdatedAt = parseSQLiteTime(updatedAtRaw.String)
	return item, nil
}

func normalizeTrialOTPSessionEmail(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}

func normalizeTrialOTPSessionPhone(phone string) string {
	return strings.TrimSpace(phone)
}

func firstNonEmptyTrialOTPStatus(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "pending"
}
