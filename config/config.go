package config

import (
	"os"
	"strconv"
	"strings"
)

var (
	// App settings
	AppPort    = "3000"
	AppHost    = "0.0.0.0"
	AppDebug   = false
	AppVersion = "1.0.0"

	// Trial
	TrialActiveDays              = 3
	TrialMaxDevices              = 1
	TrialSignupWindowMinutes     = 60
	TrialSignupMaxAttemptsPerIP  = 5
	TrialSignupSuccessWindowDays = 30
	TrialSignupMaxSuccessPerIP   = 1
	TrialSignupMinFormSeconds    = 2
	TrialBlockedEmailDomains     = ""
	TrialOTPEnabled              = true
	TrialOTPTTLMinutes           = 5
	TrialOTPMaxVerifyAttempts    = 5

	// Database
	DBURI = "file:storages/whatsapp.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"

	// WhatsApp
	WhatsappAutoReplyMessage    = ""
	WhatsappAccountValidation   = true
	WhatsappLogLevel            = "ERROR"
	WhatsappAIEndpoint          = "https://integrate.api.nvidia.com/v1/chat/completions"
	WhatsappAIModel             = "openai/gpt-oss-120b"
	WhatsappAIMaxTokens         = 512
	WhatsappAIRequestTimeoutSec = 30

	// Paths
	PathQrCode    = "storages/qrcode"
	PathSendItems = "storages/senditems"
	PathStorages  = "storages"
	PathMedia     = "storages/media"

	// Broadcast defaults
	BroadcastDelaySeconds  = 3
	BroadcastDelayMin      = 2
	BroadcastDelayMax      = 5
	BroadcastRandomDelay   = false
	BroadcastBurstEvery    = 0
	BroadcastBurstPauseSec = 0
)

func init() {
	AppPort = firstNonEmpty(strings.TrimSpace(os.Getenv("APP_PORT")), strings.TrimSpace(os.Getenv("PORT")), AppPort)
	AppHost = firstNonEmpty(strings.TrimSpace(os.Getenv("APP_HOST")), AppHost)
	DBURI = firstNonEmpty(strings.TrimSpace(os.Getenv("DB_URI")), DBURI)
	AppDebug = envBool("APP_DEBUG", AppDebug)
	TrialActiveDays = envInt("TRIAL_ACTIVE_DAYS", TrialActiveDays)
	TrialMaxDevices = envInt("TRIAL_MAX_DEVICES", TrialMaxDevices)
	TrialSignupWindowMinutes = envInt("TRIAL_SIGNUP_WINDOW_MINUTES", TrialSignupWindowMinutes)
	TrialSignupMaxAttemptsPerIP = envInt("TRIAL_SIGNUP_MAX_ATTEMPTS_PER_IP", TrialSignupMaxAttemptsPerIP)
	TrialSignupSuccessWindowDays = envInt("TRIAL_SIGNUP_SUCCESS_WINDOW_DAYS", TrialSignupSuccessWindowDays)
	TrialSignupMaxSuccessPerIP = envInt("TRIAL_SIGNUP_MAX_SUCCESS_PER_IP", TrialSignupMaxSuccessPerIP)
	TrialSignupMinFormSeconds = envInt("TRIAL_SIGNUP_MIN_FORM_SECONDS", TrialSignupMinFormSeconds)
	TrialBlockedEmailDomains = envString("TRIAL_BLOCKED_EMAIL_DOMAINS", TrialBlockedEmailDomains)
	TrialOTPEnabled = envBool("TRIAL_OTP_ENABLED", TrialOTPEnabled)
	TrialOTPTTLMinutes = envInt("TRIAL_OTP_TTL_MINUTES", TrialOTPTTLMinutes)
	TrialOTPMaxVerifyAttempts = envInt("TRIAL_OTP_MAX_VERIFY_ATTEMPTS", TrialOTPMaxVerifyAttempts)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
