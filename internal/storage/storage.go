package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

// BroadcastRecord represents a single broadcast session
type BroadcastRecord struct {
	ID        int64     `json:"id"`
	Date      time.Time `json:"date"`
	Account   string    `json:"account"`
	Total     int       `json:"total"`
	Sent      int       `json:"sent"`
	Failed    int       `json:"failed"`
	Message   string    `json:"message"`
	Duration  string    `json:"duration"`
	Type      string    `json:"type"` // "broadcast" or "personalisasi"
	CreatedAt time.Time `json:"created_at"`
}

// ContactGroup represents a saved contact group
type ContactGroup struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Numbers string `json:"numbers"`
}

// MessageTemplate represents a saved message template
type MessageTemplate struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
	Type    string `json:"type"` // "broadcast" or "personalisasi"
}

// AppPreference represents a key-value setting
type AppPreference struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ChatHistoryRecord struct {
	ID          int64     `json:"id"`
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name"`
	ChatJID     string    `json:"chat_jid"`
	Phone       string    `json:"phone"`
	Name        string    `json:"name"`
	LastMessage string    `json:"last_message"`
	LastSeen    time.Time `json:"last_seen"`
	ChatType    string    `json:"chat_type"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type BroadcastSchedule struct {
	ID           int64     `json:"id"`
	ScheduleType string    `json:"schedule_type"`
	Name         string    `json:"name"`
	AccountID    string    `json:"account_id"`
	AccountName  string    `json:"account_name"`
	Numbers      string    `json:"numbers"`
	CSVData      string    `json:"csv_data"`
	Message      string    `json:"message"`
	UseSpintax   bool      `json:"use_spintax"`
	ImageB64     string    `json:"image_b64"`
	ImageMime    string    `json:"image_mime"`
	ImagesJSON   string    `json:"images_json"`
	DelaySeconds int       `json:"delay_seconds"`
	RandomDelay  bool      `json:"random_delay"`
	DelayMin     int       `json:"delay_min"`
	DelayMax     int       `json:"delay_max"`
	BurstEvery   int       `json:"burst_every"`
	BurstPause   int       `json:"burst_pause"`
	RunAt        time.Time `json:"run_at"`
	Status       string    `json:"status"`
	Total        int       `json:"total"`
	Sent         int       `json:"sent"`
	Failed       int       `json:"failed"`
	LastError    string    `json:"last_error"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type MetaWABAAccount struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	BusinessID         string    `json:"business_id"`
	WABAID             string    `json:"waba_id"`
	PhoneNumberID      string    `json:"phone_number_id"`
	DisplayPhoneNumber string    `json:"display_phone_number"`
	Status             string    `json:"status"`
	OnboardingStatus   string    `json:"onboarding_status"`
	AccessToken        string    `json:"-"`
	TokenType          string    `json:"-"`
	TokenExpiresAt     time.Time `json:"token_expires_at"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Storage handles all database operations
type Storage struct {
	db   *sql.DB
	lock sync.Mutex
}

// New creates a new Storage instance
func New(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Storage{db: db}
	s.initSchema()
	return s, nil
}

func (s *Storage) initSchema() {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS broadcast_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			date DATETIME NOT NULL,
			account TEXT NOT NULL DEFAULT '',
			total INTEGER NOT NULL DEFAULT 0,
			sent INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			message TEXT NOT NULL DEFAULT '',
			duration TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'broadcast',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS contact_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			numbers TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS message_templates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'broadcast'
		)`,
		`CREATE TABLE IF NOT EXISTS preferences (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS chat_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL DEFAULT '',
			account_name TEXT NOT NULL DEFAULT '',
			chat_jid TEXT NOT NULL DEFAULT '',
			phone TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			last_message TEXT NOT NULL DEFAULT '',
			last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			chat_type TEXT NOT NULL DEFAULT 'direct',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(account_id, chat_jid)
		)`,
		`CREATE TABLE IF NOT EXISTS broadcast_schedules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_type TEXT NOT NULL DEFAULT 'broadcast',
			name TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL DEFAULT '',
			account_name TEXT NOT NULL DEFAULT '',
			numbers TEXT NOT NULL DEFAULT '',
			csv_data TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			use_spintax INTEGER NOT NULL DEFAULT 0,
			image_b64 TEXT NOT NULL DEFAULT '',
			image_mime TEXT NOT NULL DEFAULT '',
			images_json TEXT NOT NULL DEFAULT '',
			delay_seconds INTEGER NOT NULL DEFAULT 0,
			random_delay INTEGER NOT NULL DEFAULT 0,
			delay_min INTEGER NOT NULL DEFAULT 0,
			delay_max INTEGER NOT NULL DEFAULT 0,
			burst_every INTEGER NOT NULL DEFAULT 0,
			burst_pause INTEGER NOT NULL DEFAULT 0,
			run_at DATETIME NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			total INTEGER NOT NULL DEFAULT 0,
			sent INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS meta_waba_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			business_id TEXT NOT NULL DEFAULT '',
			waba_id TEXT NOT NULL DEFAULT '',
			phone_number_id TEXT NOT NULL DEFAULT '',
			display_phone_number TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'draft',
			onboarding_status TEXT NOT NULL DEFAULT 'not_started',
			access_token TEXT NOT NULL DEFAULT '',
			token_type TEXT NOT NULL DEFAULT '',
			token_expires_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(waba_id, phone_number_id)
		)`,
	}

	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			logrus.Errorf("Failed to create table: %v", err)
		}
	}
	_, _ = s.db.Exec(`ALTER TABLE broadcast_schedules ADD COLUMN schedule_type TEXT NOT NULL DEFAULT 'broadcast'`)
	_, _ = s.db.Exec(`ALTER TABLE broadcast_schedules ADD COLUMN csv_data TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE broadcast_schedules ADD COLUMN images_json TEXT NOT NULL DEFAULT ''`)
}

// === Chat History ===

func (s *Storage) UpsertChatHistory(rec ChatHistoryRecord) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO chat_history (account_id, account_name, chat_jid, phone, name, last_message, last_seen, chat_type, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(account_id, chat_jid) DO UPDATE SET
			account_name = excluded.account_name,
			phone = excluded.phone,
			name = CASE
				WHEN excluded.name <> '' THEN excluded.name
				ELSE chat_history.name
			END,
			last_message = CASE
				WHEN excluded.last_message <> '' THEN excluded.last_message
				ELSE chat_history.last_message
			END,
			last_seen = CASE
				WHEN excluded.last_seen > chat_history.last_seen THEN excluded.last_seen
				ELSE chat_history.last_seen
			END,
			chat_type = excluded.chat_type,
			updated_at = CURRENT_TIMESTAMP`,
		rec.AccountID, rec.AccountName, rec.ChatJID, rec.Phone, rec.Name, rec.LastMessage, rec.LastSeen, rec.ChatType,
	)
	return err
}

func (s *Storage) GetChatHistory(accountID string, limit int) ([]ChatHistoryRecord, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if limit <= 0 {
		limit = 500
	}

	var (
		rows *sql.Rows
		err  error
	)
	if accountID == "" || accountID == "all" {
		rows, err = s.db.Query(
			`SELECT id, account_id, account_name, chat_jid, phone, name, last_message, last_seen, chat_type, created_at, updated_at
			 FROM chat_history
			 ORDER BY last_seen DESC, updated_at DESC
			 LIMIT ?`, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, account_id, account_name, chat_jid, phone, name, last_message, last_seen, chat_type, created_at, updated_at
			 FROM chat_history
			 WHERE account_id = ?
			 ORDER BY last_seen DESC, updated_at DESC
			 LIMIT ?`, accountID, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ChatHistoryRecord
	for rows.Next() {
		var rec ChatHistoryRecord
		if err := rows.Scan(
			&rec.ID,
			&rec.AccountID,
			&rec.AccountName,
			&rec.ChatJID,
			&rec.Phone,
			&rec.Name,
			&rec.LastMessage,
			&rec.LastSeen,
			&rec.ChatType,
			&rec.CreatedAt,
			&rec.UpdatedAt,
		); err != nil {
			continue
		}
		result = append(result, rec)
	}
	return result, nil
}

func (s *Storage) ClearChatHistory(accountID string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if accountID == "" || accountID == "all" {
		_, err := s.db.Exec(`DELETE FROM chat_history`)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM chat_history WHERE account_id = ?`, accountID)
	return err
}

// === Broadcast History ===

func (s *Storage) SaveBroadcast(rec *BroadcastRecord) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO broadcast_history (date, account, total, sent, failed, message, duration, type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Date, rec.Account, rec.Total, rec.Sent, rec.Failed, rec.Message, rec.Duration, rec.Type,
	)
	return err
}

func (s *Storage) GetBroadcastHistory(accountFilter string) ([]BroadcastRecord, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	var rows *sql.Rows
	var err error
	if accountFilter == "" || accountFilter == "all" {
		rows, err = s.db.Query(`SELECT id, date, account, total, sent, failed, message, duration, type, created_at FROM broadcast_history ORDER BY id DESC LIMIT 100`)
	} else {
		rows, err = s.db.Query(`SELECT id, date, account, total, sent, failed, message, duration, type, created_at FROM broadcast_history WHERE account = ? ORDER BY id DESC LIMIT 100`, accountFilter)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []BroadcastRecord
	for rows.Next() {
		var r BroadcastRecord
		if err := rows.Scan(&r.ID, &r.Date, &r.Account, &r.Total, &r.Sent, &r.Failed, &r.Message, &r.Duration, &r.Type, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

func (s *Storage) ClearBroadcastHistory() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(`DELETE FROM broadcast_history`)
	return err
}

// === Broadcast Schedules ===

func (s *Storage) SaveBroadcastSchedule(rec *BroadcastSchedule) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if rec.Status == "" {
		rec.Status = "pending"
	}
	if rec.ID > 0 {
		_, err := s.db.Exec(
			`UPDATE broadcast_schedules
			 SET schedule_type = ?, name = ?, account_id = ?, account_name = ?, numbers = ?, csv_data = ?, message = ?, use_spintax = ?, image_b64 = ?, image_mime = ?, images_json = ?,
			     delay_seconds = ?, random_delay = ?, delay_min = ?, delay_max = ?, burst_every = ?, burst_pause = ?, run_at = ?, status = ?,
			     total = ?, sent = ?, failed = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			rec.ScheduleType, rec.Name, rec.AccountID, rec.AccountName, rec.Numbers, rec.CSVData, rec.Message, boolToInt(rec.UseSpintax), rec.ImageB64, rec.ImageMime, rec.ImagesJSON,
			rec.DelaySeconds, boolToInt(rec.RandomDelay), rec.DelayMin, rec.DelayMax, rec.BurstEvery, rec.BurstPause, rec.RunAt, rec.Status,
			rec.Total, rec.Sent, rec.Failed, rec.LastError, rec.ID,
		)
		return err
	}

	res, err := s.db.Exec(
		`INSERT INTO broadcast_schedules (
			schedule_type, name, account_id, account_name, numbers, csv_data, message, use_spintax, image_b64, image_mime, images_json,
			delay_seconds, random_delay, delay_min, delay_max, burst_every, burst_pause, run_at, status,
			total, sent, failed, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ScheduleType, rec.Name, rec.AccountID, rec.AccountName, rec.Numbers, rec.CSVData, rec.Message, boolToInt(rec.UseSpintax), rec.ImageB64, rec.ImageMime, rec.ImagesJSON,
		rec.DelaySeconds, boolToInt(rec.RandomDelay), rec.DelayMin, rec.DelayMax, rec.BurstEvery, rec.BurstPause, rec.RunAt, rec.Status,
		rec.Total, rec.Sent, rec.Failed, rec.LastError,
	)
	if err != nil {
		return err
	}
	rec.ID, _ = res.LastInsertId()
	return nil
}

func (s *Storage) GetBroadcastSchedules(status, scheduleType string) ([]BroadcastSchedule, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	where := []string{}
	args := []interface{}{}
	if status != "" && status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if scheduleType != "" && scheduleType != "all" {
		where = append(where, "schedule_type = ?")
		args = append(args, scheduleType)
	}
	query := `SELECT id, schedule_type, name, account_id, account_name, numbers, csv_data, message, use_spintax, image_b64, image_mime, images_json,
		        delay_seconds, random_delay, delay_min, delay_max, burst_every, burst_pause, run_at, status,
		        total, sent, failed, last_error, created_at, updated_at
		 FROM broadcast_schedules`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY run_at DESC, id DESC LIMIT 200"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BroadcastSchedule
	for rows.Next() {
		var rec BroadcastSchedule
		var useSpintax, randomDelay int
		if err := rows.Scan(
			&rec.ID, &rec.ScheduleType, &rec.Name, &rec.AccountID, &rec.AccountName, &rec.Numbers, &rec.CSVData, &rec.Message, &useSpintax, &rec.ImageB64, &rec.ImageMime, &rec.ImagesJSON,
			&rec.DelaySeconds, &randomDelay, &rec.DelayMin, &rec.DelayMax, &rec.BurstEvery, &rec.BurstPause, &rec.RunAt, &rec.Status,
			&rec.Total, &rec.Sent, &rec.Failed, &rec.LastError, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			continue
		}
		rec.UseSpintax = useSpintax == 1
		rec.RandomDelay = randomDelay == 1
		result = append(result, rec)
	}
	return result, nil
}

func (s *Storage) GetDueBroadcastSchedules(now time.Time, limit int) ([]BroadcastSchedule, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(
		`SELECT id, schedule_type, name, account_id, account_name, numbers, csv_data, message, use_spintax, image_b64, image_mime, images_json,
		        delay_seconds, random_delay, delay_min, delay_max, burst_every, burst_pause, run_at, status,
		        total, sent, failed, last_error, created_at, updated_at
		 FROM broadcast_schedules
		 WHERE status = 'pending' AND run_at <= ?
		 ORDER BY run_at ASC, id ASC
		 LIMIT ?`, now, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BroadcastSchedule
	for rows.Next() {
		var rec BroadcastSchedule
		var useSpintax, randomDelay int
		if err := rows.Scan(
			&rec.ID, &rec.ScheduleType, &rec.Name, &rec.AccountID, &rec.AccountName, &rec.Numbers, &rec.CSVData, &rec.Message, &useSpintax, &rec.ImageB64, &rec.ImageMime, &rec.ImagesJSON,
			&rec.DelaySeconds, &randomDelay, &rec.DelayMin, &rec.DelayMax, &rec.BurstEvery, &rec.BurstPause, &rec.RunAt, &rec.Status,
			&rec.Total, &rec.Sent, &rec.Failed, &rec.LastError, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			continue
		}
		rec.UseSpintax = useSpintax == 1
		rec.RandomDelay = randomDelay == 1
		result = append(result, rec)
	}
	return result, nil
}

func (s *Storage) ClaimBroadcastSchedule(id int64) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	res, err := s.db.Exec(
		`UPDATE broadcast_schedules
		 SET status = 'running', last_error = '', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'pending'`, id,
	)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func (s *Storage) UpdateBroadcastScheduleStatus(id int64, status string, sent, failed int, lastError string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(
		`UPDATE broadcast_schedules
		 SET status = ?, sent = ?, failed = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		status, sent, failed, lastError, id,
	)
	return err
}

func (s *Storage) DeleteBroadcastSchedule(id int64) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(`DELETE FROM broadcast_schedules WHERE id = ?`, id)
	return err
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// === Contact Groups ===

func (s *Storage) GetContactGroups() ([]ContactGroup, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	rows, err := s.db.Query(`SELECT id, name, numbers FROM contact_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []ContactGroup
	for rows.Next() {
		var g ContactGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Numbers); err != nil {
			continue
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func (s *Storage) SaveContactGroup(name, numbers string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO contact_groups (name, numbers) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET numbers = excluded.numbers`,
		name, numbers,
	)
	return err
}

func (s *Storage) DeleteContactGroup(name string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(`DELETE FROM contact_groups WHERE name = ?`, name)
	return err
}

// === Message Templates ===

func (s *Storage) GetTemplates(templateType string) ([]MessageTemplate, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	rows, err := s.db.Query(`SELECT id, name, message, type FROM message_templates WHERE type = ? ORDER BY name`, templateType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []MessageTemplate
	for rows.Next() {
		var t MessageTemplate
		if err := rows.Scan(&t.ID, &t.Name, &t.Message, &t.Type); err != nil {
			continue
		}
		templates = append(templates, t)
	}
	return templates, nil
}

func (s *Storage) SaveTemplate(name, message, templateType string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	// Delete existing with same name and type, then insert
	s.db.Exec(`DELETE FROM message_templates WHERE name = ? AND type = ?`, name, templateType)
	_, err := s.db.Exec(
		`INSERT INTO message_templates (name, message, type) VALUES (?, ?, ?)`,
		name, message, templateType,
	)
	return err
}

func (s *Storage) DeleteTemplate(name, templateType string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(`DELETE FROM message_templates WHERE name = ? AND type = ?`, name, templateType)
	return err
}

// === Preferences ===

func (s *Storage) GetPref(key string) string {
	s.lock.Lock()
	defer s.lock.Unlock()
	var value string
	s.db.QueryRow(`SELECT value FROM preferences WHERE key = ?`, key).Scan(&value)
	return value
}

func (s *Storage) SetPref(key, value string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO preferences (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (s *Storage) GetPrefJSON(key string, target interface{}) error {
	val := s.GetPref(key)
	if val == "" {
		return nil
	}
	return json.Unmarshal([]byte(val), target)
}

func (s *Storage) SetPrefJSON(key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.SetPref(key, string(data))
}

// === Meta WABA Accounts ===

func (s *Storage) ListMetaWABAAccounts() ([]MetaWABAAccount, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	rows, err := s.db.Query(`SELECT id, name, business_id, waba_id, phone_number_id, display_phone_number, status, onboarding_status, access_token, token_type, token_expires_at, created_at, updated_at FROM meta_waba_accounts ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []MetaWABAAccount
	for rows.Next() {
		var item MetaWABAAccount
		var tokenExpiresAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.BusinessID,
			&item.WABAID,
			&item.PhoneNumberID,
			&item.DisplayPhoneNumber,
			&item.Status,
			&item.OnboardingStatus,
			&item.AccessToken,
			&item.TokenType,
			&tokenExpiresAt,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			continue
		}
		if tokenExpiresAt.Valid {
			item.TokenExpiresAt = tokenExpiresAt.Time
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Storage) SaveMetaWABAAccount(item *MetaWABAAccount) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if item == nil {
		return fmt.Errorf("meta account is nil")
	}
	if strings.TrimSpace(item.WABAID) == "" && strings.TrimSpace(item.PhoneNumberID) == "" && item.ID == 0 {
		return fmt.Errorf("waba_id atau phone_number_id wajib diisi")
	}

	if item.ID > 0 {
		_, err := s.db.Exec(
			`UPDATE meta_waba_accounts
			 SET name = ?, business_id = ?, waba_id = ?, phone_number_id = ?, display_phone_number = ?, status = ?, onboarding_status = ?, access_token = ?, token_type = ?, token_expires_at = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			item.Name, item.BusinessID, item.WABAID, item.PhoneNumberID, item.DisplayPhoneNumber, item.Status, item.OnboardingStatus, item.AccessToken, item.TokenType, nullableTime(item.TokenExpiresAt), item.ID,
		)
		return err
	}

	res, err := s.db.Exec(
		`INSERT INTO meta_waba_accounts (name, business_id, waba_id, phone_number_id, display_phone_number, status, onboarding_status, access_token, token_type, token_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(waba_id, phone_number_id) DO UPDATE SET
			name = excluded.name,
			business_id = excluded.business_id,
			display_phone_number = excluded.display_phone_number,
			status = excluded.status,
			onboarding_status = excluded.onboarding_status,
			access_token = excluded.access_token,
			token_type = excluded.token_type,
			token_expires_at = excluded.token_expires_at,
			updated_at = CURRENT_TIMESTAMP`,
		item.Name, item.BusinessID, item.WABAID, item.PhoneNumberID, item.DisplayPhoneNumber, item.Status, item.OnboardingStatus, item.AccessToken, item.TokenType, nullableTime(item.TokenExpiresAt),
	)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	return nil
}

func nullableTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

// Close closes the database
func (s *Storage) Close() error {
	return s.db.Close()
}
