package storage

import (
	"strings"
	"time"
)

func (s *Storage) EnsureTrialSignupTable() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	queries := []string{
		`CREATE TABLE IF NOT EXISTS trial_signup_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL DEFAULT '',
			ip_address TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			succeeded INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_signup_attempts_ip_created_at
			ON trial_signup_attempts (ip_address, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_signup_attempts_ip_success_created_at
			ON trial_signup_attempts (ip_address, succeeded, created_at)`,
	}
	for _, query := range queries {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) CountRecentTrialSignupAttemptsByIP(ip string, since time.Time) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	ip = strings.TrimSpace(ip)
	if ip == "" || since.IsZero() {
		return 0, nil
	}

	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM trial_signup_attempts WHERE ip_address = ? AND created_at >= ?`,
		ip,
		sqliteTimeValue(since),
	).Scan(&count)
	return count, err
}

func (s *Storage) CountRecentSuccessfulTrialSignupsByIP(ip string, since time.Time) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	ip = strings.TrimSpace(ip)
	if ip == "" || since.IsZero() {
		return 0, nil
	}

	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM trial_signup_attempts WHERE ip_address = ? AND succeeded = 1 AND created_at >= ?`,
		ip,
		sqliteTimeValue(since),
	).Scan(&count)
	return count, err
}

func (s *Storage) RecordTrialSignupAttempt(email, ip, userAgent string, succeeded bool, reason string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO trial_signup_attempts (email, ip_address, user_agent, succeeded, reason)
		 VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(strings.ToLower(email)),
		strings.TrimSpace(ip),
		strings.TrimSpace(userAgent),
		boolToInt(succeeded),
		strings.TrimSpace(reason),
	)
	return err
}

func sqliteTimeValue(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}
