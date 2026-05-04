package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/azkazamdigital/wa-gateway/config"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	"github.com/sirupsen/logrus"
)

const (
	trialOTPSystemUserID               = "__trial_otp_system__"
	trialOTPVerifierAccountID          = "trial-otp-verifier"
	trialOTPMessageTemplatePrefKey     = "trial_otp_message_template"
	trialOTPSuccessTemplatePrefKey     = "trial_otp_success_template"
	trialOTPTTLMinutesPrefKey          = "trial_otp_ttl_minutes"
	defaultTrialOTPVerifierAccountName = "Verifier OTP Trial"
)

type trialOTPAdminConfig struct {
	Enabled         bool   `json:"enabled"`
	TTLMinutes      int    `json:"ttl_minutes"`
	MessageTemplate string `json:"message_template"`
	SuccessTemplate string `json:"success_template"`
}

func initTrialOTPVerifierManager() {
	if Store == nil {
		return
	}
	dbPath := filepath.ToSlash(filepath.Join(config.PathStorages, "trial-otp-verifier.db"))
	dbURI := "file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"
	mgr, err := whatsapp.InitManagerForUser(
		context.Background(),
		trialOTPSystemUserID,
		dbURI,
		Store,
		nil,
	)
	if err != nil {
		logrus.WithError(err).Warn("failed to initialize trial otp verifier manager")
		return
	}
	mgr.AutoConnectAll()
}

func loadTrialOTPAdminConfig() trialOTPAdminConfig {
	cfg := trialOTPAdminConfig{
		Enabled:         true,
		TTLMinutes:      config.TrialOTPTTLMinutes,
		MessageTemplate: defaultTrialOTPMessageTemplate(),
		SuccessTemplate: defaultTrialOTPSuccessTemplate(),
	}
	if Store == nil {
		return cfg
	}

	if ttl := parseStoredInt(Store.GetPref(trialOTPTTLMinutesPrefKey), cfg.TTLMinutes); ttl > 0 {
		cfg.TTLMinutes = ttl
	}
	if value := strings.TrimSpace(Store.GetPref(trialOTPMessageTemplatePrefKey)); value != "" {
		cfg.MessageTemplate = value
	}
	if value := strings.TrimSpace(Store.GetPref(trialOTPSuccessTemplatePrefKey)); value != "" {
		cfg.SuccessTemplate = value
	}
	if cfg.TTLMinutes <= 0 {
		cfg.TTLMinutes = 5
	}
	if strings.TrimSpace(cfg.MessageTemplate) == "" {
		cfg.MessageTemplate = defaultTrialOTPMessageTemplate()
	}
	if strings.TrimSpace(cfg.SuccessTemplate) == "" {
		cfg.SuccessTemplate = defaultTrialOTPSuccessTemplate()
	}
	return cfg
}

func saveTrialOTPAdminConfig(cfg trialOTPAdminConfig) error {
	if Store == nil {
		return fmt.Errorf("storage belum siap")
	}
	cfg.Enabled = true
	if cfg.TTLMinutes <= 0 {
		cfg.TTLMinutes = 5
	}
	cfg.MessageTemplate = strings.TrimSpace(cfg.MessageTemplate)
	cfg.SuccessTemplate = strings.TrimSpace(cfg.SuccessTemplate)
	if cfg.MessageTemplate == "" {
		cfg.MessageTemplate = defaultTrialOTPMessageTemplate()
	}
	if cfg.SuccessTemplate == "" {
		cfg.SuccessTemplate = defaultTrialOTPSuccessTemplate()
	}
	if err := Store.SetPref(trialOTPTTLMinutesPrefKey, fmt.Sprintf("%d", cfg.TTLMinutes)); err != nil {
		return err
	}
	if err := Store.SetPref(trialOTPMessageTemplatePrefKey, cfg.MessageTemplate); err != nil {
		return err
	}
	if err := Store.SetPref(trialOTPSuccessTemplatePrefKey, cfg.SuccessTemplate); err != nil {
		return err
	}
	return nil
}

func defaultTrialOTPMessageTemplate() string {
	return "Kode OTP trial InstaBlast Anda: {{code}}\nBerlaku {{minutes}} menit.\n\nEmail: {{email}}\nNomor WA: {{phone}}\n\nJangan berikan kode ini ke siapa pun."
}

func defaultTrialOTPSuccessTemplate() string {
	return "Verifikasi trial berhasil.\n\nSilakan login ke InstaBlast dengan:\nEmail: {{email}}\nPassword: {{password}}\nLogin: {{login_url}}\n\nMasa trial: {{trial_days}} hari."
}

func parseStoredInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return fallback
	}
	return value
}

func ensureTrialOTPVerifierAccount() (whatsapp.AccountInfo, error) {
	if err := ensureTrialOTPVerifierReady(); err != nil {
		return whatsapp.AccountInfo{}, err
	}
	account, err := whatsapp.GetAccountForUser(trialOTPSystemUserID, trialOTPVerifierAccountID)
	if err == nil {
		return account, nil
	}
	account, err = whatsapp.CreateAccountForUserWithID(trialOTPSystemUserID, trialOTPVerifierAccountID, defaultTrialOTPVerifierAccountName)
	if err != nil {
		return whatsapp.AccountInfo{}, err
	}
	_ = whatsapp.SetActiveAccountForUser(trialOTPSystemUserID, account.ID)
	return account, nil
}

func ensureTrialOTPVerifierReady() error {
	if whatsapp.GetManagerForUser(trialOTPSystemUserID) == nil {
		initTrialOTPVerifierManager()
	}
	if whatsapp.GetManagerForUser(trialOTPSystemUserID) == nil {
		return fmt.Errorf("manager verifier OTP belum siap")
	}
	return nil
}

func renderTrialOTPTemplate(template string, variables map[string]string) string {
	rendered := strings.TrimSpace(template)
	for key, value := range variables {
		rendered = strings.ReplaceAll(rendered, "{{"+strings.TrimSpace(key)+"}}", strings.TrimSpace(value))
	}
	return rendered
}

func newTrialOTPSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func newTrialOTPCode() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value := int(buf[0])<<24 | int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if value < 0 {
		value = -value
	}
	return fmt.Sprintf("%06d", value%1000000), nil
}

func trialOTPExpiry(ttlMinutes int) time.Time {
	if ttlMinutes <= 0 {
		ttlMinutes = 5
	}
	return time.Now().Add(time.Duration(ttlMinutes) * time.Minute)
}
