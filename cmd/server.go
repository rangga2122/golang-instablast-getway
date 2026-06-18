package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/config"
	"github.com/azkazamdigital/wa-gateway/internal/ai"
	"github.com/azkazamdigital/wa-gateway/internal/auth"
	"github.com/azkazamdigital/wa-gateway/internal/broadcast"
	"github.com/azkazamdigital/wa-gateway/internal/storage"
	tenantpkg "github.com/azkazamdigital/wa-gateway/internal/tenant"
	"github.com/azkazamdigital/wa-gateway/internal/warming"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/websocket/v2"
	"github.com/sirupsen/logrus"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

var wsClients = make(map[*websocket.Conn]string)

const (
	metaAppIDPrefKey       = "meta_app_id"
	metaAppSecretPrefKey   = "meta_app_secret"
	metaConfigIDPrefKey    = "meta_config_id"
	metaRedirectURIPrefKey = "meta_redirect_uri"
	metaVerifyTokenPrefKey = "meta_verify_token"
)

type metaSignupState struct {
	UserID    string
	ExpiresAt time.Time
}

var (
	metaSignupStatesMu sync.Mutex
	metaSignupStates   = make(map[string]metaSignupState)
)

type imagePayload struct {
	Data string `json:"data"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

type broadcastAIHelperRequest struct {
	Mode      string   `json:"mode"`
	Message   string   `json:"message"`
	Variables []string `json:"variables"`
}

type nvidiaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type nvidiaChatRequest struct {
	Model       string              `json:"model"`
	Messages    []nvidiaChatMessage `json:"messages"`
	Temperature float64             `json:"temperature"`
	TopP        float64             `json:"top_p"`
	MaxTokens   int                 `json:"max_tokens"`
	Stream      bool                `json:"stream"`
}

type nvidiaChatResponse struct {
	Choices []struct {
		Message nvidiaChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type nvidiaChatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func decodeImagePayloads(items []imagePayload, legacyB64, legacyMime string) ([]broadcast.MediaItem, []imagePayload, error) {
	if len(items) == 0 && strings.TrimSpace(legacyB64) != "" {
		items = []imagePayload{{Data: strings.TrimSpace(legacyB64), Mime: strings.TrimSpace(legacyMime)}}
	}

	decoded := make([]broadcast.MediaItem, 0, len(items))
	sanitized := make([]imagePayload, 0, len(items))
	for _, item := range items {
		raw := strings.TrimSpace(item.Data)
		if raw == "" {
			continue
		}
		imgData, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid media data")
		}
		mimeType := strings.TrimSpace(item.Mime)
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		name := strings.TrimSpace(item.Name)
		decoded = append(decoded, broadcast.MediaItem{
			Data: imgData,
			Mime: mimeType,
			Name: name,
		})
		sanitized = append(sanitized, imagePayload{
			Data: raw,
			Mime: mimeType,
			Name: name,
		})
	}
	return decoded, sanitized, nil
}

func broadcastWSLog(msg, level string) {
	data := fmt.Sprintf(`{"type":"log","message":"%s","level":"%s","time":"%s"}`,
		strings.ReplaceAll(msg, `"`, `\"`),
		level,
		time.Now().Format("15:04:05"),
	)
	for conn := range wsClients {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
			conn.Close()
			delete(wsClients, conn)
		}
	}
}

func broadcastWSProgress() {
	for conn, ownerID := range wsClients {
		p := broadcast.GetEngineForUser(ownerID).GetProgress()
		data := fmt.Sprintf(`{"type":"progress","status":"%s","total":%d,"sent":%d,"failed":%d,"current":%d,"current_num":"%s"}`,
			p.Status, p.Total, p.Sent, p.Failed, p.Current, p.CurrentNum,
		)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
			conn.Close()
			delete(wsClients, conn)
		}
	}
}

func trialSignupClientIP(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}
	ip := strings.TrimSpace(c.IP())
	if ip != "" {
		return ip
	}
	forwarded := strings.TrimSpace(c.Get("X-Forwarded-For"))
	if forwarded != "" {
		if idx := strings.Index(forwarded, ","); idx >= 0 {
			forwarded = forwarded[:idx]
		}
		return strings.TrimSpace(forwarded)
	}
	return strings.TrimSpace(c.Get("X-Real-IP"))
}

func recordTrialSignupAttempt(email, ip, userAgent string, succeeded bool, reason string) {
	if Store == nil {
		return
	}
	if err := Store.RecordTrialSignupAttempt(email, ip, userAgent, succeeded, reason); err != nil {
		logrus.WithError(err).Warn("failed to record trial signup attempt")
	}
}

func maskPhoneNumber(phone string) string {
	phone = normalizePhone(phone)
	if len(phone) <= 4 {
		return phone
	}
	if len(phone) <= 8 {
		return phone[:2] + strings.Repeat("*", len(phone)-4) + phone[len(phone)-2:]
	}
	return phone[:4] + strings.Repeat("*", len(phone)-7) + phone[len(phone)-3:]
}

func runServer(cmd_ *cobra.Command, args []string) {
	initApp()

	app := fiber.New(fiber.Config{BodyLimit: 50 * 1024 * 1024})
	if config.AppDebug {
		app.Use(logger.New())
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept",
	}))

	app.Use("/assets", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		return c.Next()
	})

	app.Use("/assets", filesystem.New(filesystem.Config{
		Root:       http.FS(EmbedViews),
		PathPrefix: "views/assets",
		Browse:     false,
	}))

	app.Use(func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/assets") || path == "/" || path == "/panduan" || path == "/docs" || path == "/icon.ico" || path == "/login" || path == "/health" || path == "/health/whatsapp" || path == "/privacy-policy" || path == "/terms-of-service" || path == "/data-deletion" || path == "/data-deletion-status" || path == "/api/auth/login" || path == "/api/auth/register-trial" || path == "/api/auth/trial/request-otp" || path == "/api/auth/trial/verify-otp" || path == "/api/meta/signup/callback" || path == "/api/meta/webhook" || path == "/api/meta/data-deletion" {
			return c.Next()
		}
		if AuthService == nil {
			return c.Status(500).SendString("Auth service not initialized")
		}
		token := c.Cookies(auth.SessionCookieName)
		user, session, err := AuthService.GetUserByToken(token)
		if err != nil {
			if basicUser, ok := authenticateRequestUser(c); ok {
				c.Locals("current_user", basicUser)
				return c.Next()
			}
			if strings.HasPrefix(path, "/api/") || isCompatAPIPath(path) {
				return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
			}
			return c.Redirect("/login")
		}
		c.Locals("current_user", user)
		c.Locals("current_session", session)
		return c.Next()
	})

	app.Get("/", func(c *fiber.Ctx) error {
		data, err := EmbedViews.ReadFile("views/landing.html")
		if err != nil {
			return c.Status(500).SendString("Failed to load landing page")
		}
		c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		c.Set("Content-Type", "text/html")
		return c.Send(data)
	})
	app.Get("/docs", func(c *fiber.Ctx) error {
		return c.Redirect("/panduan", fiber.StatusMovedPermanently)
	})
	app.Get("/panduan", func(c *fiber.Ctx) error {
		data, err := EmbedViews.ReadFile("views/panduan.html")
		if err != nil {
			return c.Status(500).SendString("Failed to load guide page")
		}
		c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		c.Set("Content-Type", "text/html")
		return c.Send(data)
	})

	app.Get("/app", func(c *fiber.Ctx) error {
		data, err := EmbedViews.ReadFile("views/index.html")
		if err != nil {
			return c.Status(500).SendString("Failed to load UI")
		}
		c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		c.Set("Content-Type", "text/html")
		return c.Send(data)
	})
	app.Get("/login", func(c *fiber.Ctx) error {
		if AuthService != nil {
			if _, _, err := AuthService.GetUserByToken(c.Cookies(auth.SessionCookieName)); err == nil {
				return c.Redirect("/app")
			}
		}
		data, err := EmbedViews.ReadFile("views/login.html")
		if err != nil {
			return c.Status(500).SendString("Failed to load login UI")
		}
		c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		c.Set("Content-Type", "text/html")
		return c.Send(data)
	})
	app.Get("/icon.ico", func(c *fiber.Ctx) error {
		data, err := os.ReadFile("icon.ico")
		if err != nil {
			return c.Status(404).SendString("Not Found")
		}
		c.Set("Content-Type", "image/x-icon")
		return c.Send(data)
	})
	app.Get("/privacy-policy", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(legalPageHTML(legalPageContent{
			Title:       "Privacy Policy",
			Heading:     "Kebijakan Privasi InstaBlast Pro",
			Description: "Halaman ini menjelaskan bagaimana InstaBlast Pro mengelola data akun, pesan, dan integrasi pihak ketiga seperti Meta dan WhatsApp Business.",
			Sections: []legalSection{
				{
					Title: "Data yang Kami Gunakan",
					Body: []string{
						"Kami dapat memproses data akun, data perangkat WhatsApp, konfigurasi integrasi Meta, dan data operasional yang dibutuhkan agar layanan InstaBlast Pro berjalan sebagaimana mestinya.",
						"Data hanya digunakan untuk autentikasi, pengiriman pesan, pengelolaan perangkat, analitik operasional dasar, dan penyelesaian masalah teknis.",
					},
				},
				{
					Title: "Integrasi Pihak Ketiga",
					Body: []string{
						"Jika Anda menghubungkan akun Meta atau WhatsApp Business, kami menerima data yang dibutuhkan untuk menyelesaikan onboarding, menyimpan identitas aset bisnis, dan memproses webhook resmi dari Meta.",
						"Kami tidak menjual data pengguna kepada pihak ketiga.",
					},
				},
				{
					Title: "Kontrol dan Permintaan",
					Body: []string{
						"Anda dapat meminta pembaruan atau penghapusan data dengan menghubungi kontak resmi yang tersedia pada layanan ini.",
						"Untuk permintaan penghapusan data terkait aplikasi Meta, silakan lihat instruksi pada halaman Data Deletion.",
					},
				},
			},
		}))
	})
	app.Get("/terms-of-service", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(legalPageHTML(legalPageContent{
			Title:       "Terms of Service",
			Heading:     "Syarat dan Ketentuan InstaBlast Pro",
			Description: "Dengan menggunakan InstaBlast Pro, Anda setuju untuk menggunakan platform ini secara sah, bertanggung jawab, dan sesuai aturan Meta, WhatsApp, serta hukum yang berlaku.",
			Sections: []legalSection{
				{
					Title: "Penggunaan yang Diizinkan",
					Body: []string{
						"Pengguna wajib menggunakan platform untuk komunikasi bisnis yang sah dan tidak melanggar kebijakan anti-spam, privasi, atau ketentuan resmi WhatsApp Business Platform.",
						"Pengguna bertanggung jawab atas data, nomor telepon, dan akun pihak ketiga yang mereka hubungkan ke sistem.",
					},
				},
				{
					Title: "Kepatuhan Platform",
					Body: []string{
						"Fitur integrasi Meta dan WhatsApp Business tunduk pada kebijakan Meta yang dapat berubah sewaktu-waktu.",
						"Kami berhak menolak atau menonaktifkan penggunaan yang berisiko, melanggar hukum, atau mengancam stabilitas layanan.",
					},
				},
				{
					Title: "Batasan Tanggung Jawab",
					Body: []string{
						"Kami berupaya menjaga layanan tetap tersedia, namun tidak menjamin layanan selalu bebas gangguan, pembatasan pihak ketiga, atau perubahan kebijakan platform eksternal.",
					},
				},
			},
		}))
	})
	app.Get("/data-deletion", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(legalPageHTML(legalPageContent{
			Title:       "Data Deletion",
			Heading:     "Instruksi Penghapusan Data InstaBlast Pro",
			Description: "Jika Anda ingin meminta penghapusan data yang terkait dengan akun, integrasi Meta, atau penggunaan aplikasi ini, ikuti langkah berikut.",
			Sections: []legalSection{
				{
					Title: "Cara Menghapus Koneksi Meta",
					Body: []string{
						"Buka pengaturan akun Facebook atau Meta Anda, lalu hapus aplikasi InstaBlast Pro dari daftar aplikasi yang terhubung jika Anda tidak ingin aplikasi ini lagi mengakses akun Anda.",
						"Anda juga dapat memutus koneksi akun WhatsApp Business atau integrasi lain dari panel aplikasi jika fitur tersebut tersedia.",
					},
				},
				{
					Title: "Permintaan Penghapusan Data",
					Body: []string{
						"Untuk meminta penghapusan data dari sistem kami, kirimkan permintaan melalui email resmi operasional yang Anda gunakan pada layanan ini dengan menyebutkan identitas akun dan detail permintaan penghapusan.",
						"Permintaan akan diverifikasi terlebih dahulu untuk memastikan keamanan akun dan mencegah penghapusan tidak sah.",
					},
				},
				{
					Title: "Waktu Pemrosesan",
					Body: []string{
						"Setelah identitas diverifikasi, kami akan memproses permintaan penghapusan data dalam waktu yang wajar sesuai kebutuhan operasional dan kewajiban hukum yang berlaku.",
					},
				},
			},
		}))
	})
	app.Get("/data-deletion-status", func(c *fiber.Ctx) error {
		code := strings.TrimSpace(c.Query("code"))
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(legalPageHTML(legalPageContent{
			Title:       "Data Deletion Status",
			Heading:     "Status Permintaan Penghapusan Data",
			Description: "Halaman ini dipakai sebagai status callback penghapusan data untuk integrasi Meta.",
			Sections: []legalSection{
				{
					Title: "Status",
					Body: []string{
						firstNonEmpty("Permintaan penghapusan data telah diterima dengan kode konfirmasi "+code+".", "Permintaan penghapusan data telah diterima."),
						"Jika diperlukan tindak lanjut tambahan, tim operasional akan melakukan verifikasi sesuai data yang tersedia.",
					},
				},
			},
		}))
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		stats := whatsapp.GetConnectionStats()
		return c.JSON(fiber.Map{
			"status":                "ok",
			"service":               "wa-gateway",
			"connected_any_account": stats.ConnectedAccounts > 0,
			"whatsapp":              stats,
			"active_account_id":     "",
		})
	})

	app.Get("/health/whatsapp", func(c *fiber.Ctx) error {
		stats := whatsapp.GetConnectionStats()
		if stats.ConnectedAccounts > 0 {
			status := "ok"
			if stats.ProblemAccounts > 0 {
				status = "degraded"
			}
			return c.JSON(fiber.Map{"status": status, "connected": true, "whatsapp": stats})
		}
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"status": "not_connected", "connected": false, "whatsapp": stats})
	})

	api := app.Group("/api")
	registerCompatAPI(app)
	api.Post("/auth/login", func(c *fiber.Ctx) error {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		user, session, err := AuthService.Login(body.Email, body.Password)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		c.Cookie(&fiber.Cookie{
			Name:     auth.SessionCookieName,
			Value:    session.Token,
			HTTPOnly: true,
			SameSite: "Lax",
			Path:     "/",
			Expires:  session.ExpiresAt,
		})
		return c.JSON(fiber.Map{
			"user": fiber.Map{
				"id":          user.ID,
				"email":       user.Email,
				"is_admin":    user.IsAdmin,
				"is_trial":    user.IsTrial,
				"can_use_ai":  user.CanUseAI,
				"max_devices": effectiveUserMaxDevices(user),
				"expires_at":  user.ExpiresAt,
			},
		})
	})
	api.Post("/auth/trial/request-otp", func(c *fiber.Ctx) error {
		var body struct {
			Email           string `json:"email"`
			Password        string `json:"password"`
			ConfirmPassword string `json:"confirm_password"`
			Phone           string `json:"phone"`
			Website         string `json:"website"`
			FormStartedAt   int64  `json:"form_started_at"`
		}
		clientIP := trialSignupClientIP(c)
		userAgent := strings.TrimSpace(c.Get("User-Agent"))
		if err := c.BodyParser(&body); err != nil {
			recordTrialSignupAttempt("", clientIP, userAgent, false, "invalid_body")
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}

		otpCfg := loadTrialOTPAdminConfig()

		email := strings.TrimSpace(strings.ToLower(body.Email))
		password := body.Password
		confirmPassword := body.ConfirmPassword
		phone := normalizePhone(body.Phone)

		if email == "" {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "missing_email")
			return c.Status(400).JSON(fiber.Map{"error": "Email wajib diisi"})
		}
		if _, err := mail.ParseAddress(email); err != nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "invalid_email")
			return c.Status(400).JSON(fiber.Map{"error": "Format email tidak valid"})
		}
		if len(strings.TrimSpace(password)) < 6 {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "password_too_short")
			return c.Status(400).JSON(fiber.Map{"error": "Password minimal 6 karakter"})
		}
		if password != confirmPassword {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "password_mismatch")
			return c.Status(400).JSON(fiber.Map{"error": "Konfirmasi password tidak cocok"})
		}
		if phone == "" || len(phone) < 10 {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "invalid_phone")
			return c.Status(400).JSON(fiber.Map{"error": "Nomor WhatsApp wajib valid"})
		}
		if config.TrialActiveDays <= 0 {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "invalid_trial_config")
			return c.Status(500).JSON(fiber.Map{"error": "Konfigurasi trial tidak valid di server"})
		}
		if _, err := Store.GetUserByEmail(email); err == nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "email_exists")
			return c.Status(400).JSON(fiber.Map{"error": "Email sudah terdaftar"})
		}
		if usedByPhone, err := Store.CountUsedTrialOTPSessionsByPhone(phone); err == nil && usedByPhone > 0 {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "phone_already_used")
			return c.Status(429).JSON(fiber.Map{"error": "Nomor WhatsApp ini sudah pernah dipakai untuk trial. Silakan hubungi admin jika butuh akses."})
		}

		account, err := ensureTrialOTPVerifierAccount()
		if err != nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_verifier_unavailable")
			return c.Status(503).JSON(fiber.Map{"error": "Akun WhatsApp verifikasi OTP belum siap. Silakan hubungi admin."})
		}
		if !whatsapp.IsClientConnectedForAccountForUser(trialOTPSystemUserID, account.ID) {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_verifier_offline")
			return c.Status(503).JSON(fiber.Map{"error": "Akun WhatsApp verifikasi OTP belum online. Silakan hubungi admin."})
		}

		results, err := whatsapp.IsOnWhatsAppForUserAccount(trialOTPSystemUserID, account.ID, []string{"+" + phone})
		if err != nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_phone_validation_failed")
			return c.Status(502).JSON(fiber.Map{"error": "Gagal memvalidasi nomor WhatsApp. Coba lagi sebentar."})
		}
		if len(results) == 0 || !results[0].IsIn {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "phone_not_on_whatsapp")
			return c.Status(400).JSON(fiber.Map{"error": "Nomor WhatsApp tidak valid atau belum terdaftar di WhatsApp"})
		}

		if err := Store.InvalidatePendingTrialOTPSessions(email, phone); err != nil {
			logrus.WithError(err).Warn("failed to invalidate pending otp sessions")
		}

		sessionID, err := newTrialOTPSessionID()
		if err != nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_session_failed")
			return c.Status(500).JSON(fiber.Map{"error": "Gagal membuat sesi OTP"})
		}
		otpCode, err := newTrialOTPCode()
		if err != nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_code_failed")
			return c.Status(500).JSON(fiber.Map{"error": "Gagal membuat kode OTP"})
		}

		expiresAt := trialOTPExpiry(otpCfg.TTLMinutes)
		if err := Store.CreateTrialOTPSession(storage.TrialOTPSession{
			SessionID:     sessionID,
			Email:         email,
			PlainPassword: password,
			Phone:         phone,
			OTPCode:       otpCode,
			Status:        "pending",
			Attempts:      0,
			IPAddress:     clientIP,
			UserAgent:     userAgent,
			ExpiresAt:     expiresAt,
		}); err != nil {
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_session_store_failed")
			return c.Status(500).JSON(fiber.Map{"error": "Gagal menyimpan sesi OTP"})
		}

		otpMessage := renderTrialOTPTemplate(otpCfg.MessageTemplate, map[string]string{
			"code":      otpCode,
			"minutes":   fmt.Sprintf("%d", otpCfg.TTLMinutes),
			"email":     email,
			"phone":     phone,
			"login_url": strings.TrimRight(c.BaseURL(), "/") + "/login",
		})
		if strings.TrimSpace(otpMessage) == "" {
			otpMessage = renderTrialOTPTemplate(defaultTrialOTPMessageTemplate(), map[string]string{
				"code":      otpCode,
				"minutes":   fmt.Sprintf("%d", otpCfg.TTLMinutes),
				"email":     email,
				"phone":     phone,
				"login_url": strings.TrimRight(c.BaseURL(), "/") + "/login",
			})
		}
		if err := whatsapp.SendTextForUserAccount(c.UserContext(), trialOTPSystemUserID, account.ID, parsePhoneToJID(phone), otpMessage); err != nil {
			_ = Store.MarkTrialOTPSessionFailed(sessionID)
			recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_send_failed")
			return c.Status(502).JSON(fiber.Map{"error": "Gagal mengirim OTP ke WhatsApp. Pastikan nomor valid lalu coba lagi."})
		}

		recordTrialSignupAttempt(email, clientIP, userAgent, false, "otp_sent")
		return c.JSON(fiber.Map{
			"status":        "otp_sent",
			"session_id":    sessionID,
			"expires_at":    expiresAt,
			"ttl_minutes":   otpCfg.TTLMinutes,
			"phone_masked":  maskPhoneNumber(phone),
			"trial_days":    config.TrialActiveDays,
			"otp_required":  true,
			"login_message": "Kode OTP telah dikirim ke WhatsApp Anda",
		})
	})
	api.Post("/auth/trial/verify-otp", func(c *fiber.Ctx) error {
		var body struct {
			SessionID string `json:"session_id"`
			OTPCode   string `json:"otp_code"`
		}
		clientIP := trialSignupClientIP(c)
		userAgent := strings.TrimSpace(c.Get("User-Agent"))
		if err := c.BodyParser(&body); err != nil {
			recordTrialSignupAttempt("", clientIP, userAgent, false, "invalid_verify_body")
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}

		sessionID := strings.TrimSpace(body.SessionID)
		otpCode := strings.TrimSpace(body.OTPCode)
		if sessionID == "" || otpCode == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Session OTP dan kode OTP wajib diisi"})
		}

		otpSession, err := Store.GetTrialOTPSession(sessionID)
		if err != nil {
			recordTrialSignupAttempt("", clientIP, userAgent, false, "otp_session_not_found")
			return c.Status(404).JSON(fiber.Map{"error": "Sesi OTP tidak ditemukan. Ulangi daftar trial."})
		}
		if otpSession.Status == "used" {
			return c.Status(400).JSON(fiber.Map{"error": "OTP ini sudah dipakai. Silakan login atau ulangi daftar trial."})
		}
		if otpSession.Status == "expired" || otpSession.ExpiresAt.Before(time.Now()) {
			_ = Store.MarkTrialOTPSessionExpired(sessionID)
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, false, "otp_expired")
			return c.Status(400).JSON(fiber.Map{"error": "Kode OTP sudah kedaluwarsa. Minta OTP baru lagi."})
		}
		if otpSession.Status == "failed" || otpSession.Status == "replaced" {
			return c.Status(400).JSON(fiber.Map{"error": "Sesi OTP ini sudah tidak aktif. Minta OTP baru lagi."})
		}
		maxVerifyAttempts := config.TrialOTPMaxVerifyAttempts
		if maxVerifyAttempts <= 0 {
			maxVerifyAttempts = 5
		}
		if otpSession.Attempts >= maxVerifyAttempts {
			_ = Store.MarkTrialOTPSessionFailed(sessionID)
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, false, "otp_attempts_exceeded")
			return c.Status(429).JSON(fiber.Map{"error": "Percobaan OTP terlalu banyak. Minta OTP baru lagi."})
		}
		if otpSession.OTPCode != otpCode {
			_ = Store.IncrementTrialOTPSessionAttempts(sessionID)
			if otpSession.Attempts+1 >= maxVerifyAttempts {
				_ = Store.MarkTrialOTPSessionFailed(sessionID)
			}
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, false, "otp_invalid")
			return c.Status(400).JSON(fiber.Map{"error": "Kode OTP salah"})
		}
		if _, err := Store.GetUserByEmail(otpSession.Email); err == nil {
			_ = Store.MarkTrialOTPSessionFailed(sessionID)
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, false, "email_exists_on_verify")
			return c.Status(400).JSON(fiber.Map{"error": "Email sudah terdaftar. Silakan login."})
		}
		if usedByPhone, err := Store.CountUsedTrialOTPSessionsByPhone(otpSession.Phone); err == nil && usedByPhone > 0 {
			_ = Store.MarkTrialOTPSessionFailed(sessionID)
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, false, "phone_already_used_on_verify")
			return c.Status(429).JSON(fiber.Map{"error": "Nomor WhatsApp ini sudah pernah dipakai untuk trial."})
		}
		plainPassword := otpSession.PlainPassword
		expiresAt := time.Now().Add(time.Duration(config.TrialActiveDays) * 24 * time.Hour)
		createdUser, err := Store.CreateUser(storage.CreateUserInput{
			Email:      otpSession.Email,
			Password:   plainPassword,
			IsAdmin:    false,
			IsTrial:    true,
			CanUseAI:   true,
			MaxDevices: config.TrialMaxDevices,
			ExpiresAt:  expiresAt,
		})
		if err != nil {
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, false, "create_user_failed_on_verify")
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		_ = Store.MarkTrialOTPSessionVerified(sessionID)

		if account, err := ensureTrialOTPVerifierAccount(); err == nil && whatsapp.IsClientConnectedForAccountForUser(trialOTPSystemUserID, account.ID) {
			successMessage := renderTrialOTPTemplate(loadTrialOTPAdminConfig().SuccessTemplate, map[string]string{
				"email":      otpSession.Email,
				"password":   plainPassword,
				"phone":      otpSession.Phone,
				"login_url":  strings.TrimRight(c.BaseURL(), "/") + "/login",
				"trial_days": fmt.Sprintf("%d", config.TrialActiveDays),
			})
			if strings.TrimSpace(successMessage) != "" {
				if err := whatsapp.SendTextForUserAccount(c.UserContext(), trialOTPSystemUserID, account.ID, parsePhoneToJID(otpSession.Phone), successMessage); err != nil {
					logrus.WithError(err).Warn("failed to send trial otp success message")
				}
			}
		}

		user, session, err := AuthService.Login(otpSession.Email, plainPassword)
		if err != nil {
			logrus.WithField("email", otpSession.Email).Errorf("failed to auto-login new otp trial user: %v", err)
			_ = Store.MarkTrialOTPSessionUsed(sessionID)
			recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, true, "trial_created_via_otp_login_failed")
			return c.Status(500).JSON(fiber.Map{"error": "Akun trial berhasil dibuat, tetapi login otomatis gagal. Silakan login manual."})
		}
		_ = Store.MarkTrialOTPSessionUsed(sessionID)
		recordTrialSignupAttempt(otpSession.Email, otpSession.IPAddress, otpSession.UserAgent, true, "trial_created_via_otp")

		c.Cookie(&fiber.Cookie{
			Name:     auth.SessionCookieName,
			Value:    session.Token,
			HTTPOnly: true,
			SameSite: "Lax",
			Path:     "/",
			Expires:  session.ExpiresAt,
		})
		return c.JSON(fiber.Map{
			"user": fiber.Map{
				"id":          user.ID,
				"email":       user.Email,
				"is_admin":    user.IsAdmin,
				"is_trial":    user.IsTrial,
				"can_use_ai":  user.CanUseAI,
				"max_devices": effectiveUserMaxDevices(user),
				"expires_at":  user.ExpiresAt,
				"trial_days":  config.TrialActiveDays,
			},
			"trial_login": fiber.Map{
				"email":     createdUser.Email,
				"password":  plainPassword,
				"login_url": strings.TrimRight(c.BaseURL(), "/") + "/login",
			},
		})
	})
	api.Post("/auth/register-trial", func(c *fiber.Ctx) error {
		var body struct {
			Email           string `json:"email"`
			Password        string `json:"password"`
			ConfirmPassword string `json:"confirm_password"`
			Website         string `json:"website"`
			FormStartedAt   int64  `json:"form_started_at"`
		}
		clientIP := trialSignupClientIP(c)
		userAgent := strings.TrimSpace(c.Get("User-Agent"))
		if err := c.BodyParser(&body); err != nil {
			recordTrialSignupAttempt("", clientIP, userAgent, false, "invalid_body")
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		recordTrialSignupAttempt(strings.TrimSpace(body.Email), clientIP, userAgent, false, "otp_required")
		return c.Status(400).JSON(fiber.Map{"error": "Trial sekarang selalu wajib verifikasi OTP WhatsApp. Silakan gunakan alur OTP di form trial."})
	})
	api.Post("/auth/logout", func(c *fiber.Ctx) error {
		if AuthService != nil {
			AuthService.Logout(c.Cookies(auth.SessionCookieName))
		}
		c.Cookie(&fiber.Cookie{
			Name:     auth.SessionCookieName,
			Value:    "",
			HTTPOnly: true,
			Path:     "/",
			Expires:  time.Now().Add(-1 * time.Hour),
		})
		return c.JSON(fiber.Map{"status": "logged_out"})
	})
	api.Get("/me", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
		}
		return c.JSON(fiber.Map{
			"user": fiber.Map{
				"id":          user.ID,
				"email":       user.Email,
				"is_admin":    user.IsAdmin,
				"is_trial":    user.IsTrial,
				"can_use_ai":  user.CanUseAI,
				"max_devices": effectiveUserMaxDevices(user),
				"expires_at":  user.ExpiresAt,
			},
		})
	})
	api.Get("/admin/users", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		users, err := Store.ListUsers()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"users": users})
	})
	api.Post("/admin/users", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		var body struct {
			Email      string `json:"email"`
			Password   string `json:"password"`
			MaxDevices int    `json:"max_devices"`
			CanUseAI   bool   `json:"can_use_ai"`
			ActiveDays int    `json:"active_days"`
			IsAdmin    bool   `json:"is_admin"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if body.ActiveDays <= 0 {
			body.ActiveDays = 30
		}
		created, err := Store.CreateUser(storage.CreateUserInput{
			Email:      body.Email,
			Password:   body.Password,
			IsAdmin:    body.IsAdmin,
			IsTrial:    false,
			CanUseAI:   body.CanUseAI,
			MaxDevices: body.MaxDevices,
			ExpiresAt:  time.Now().Add(time.Duration(body.ActiveDays) * 24 * time.Hour),
		})
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"user": created})
	})
	api.Patch("/admin/users/:id", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		var body struct {
			Email      string `json:"email"`
			Password   string `json:"password"`
			MaxDevices int    `json:"max_devices"`
			CanUseAI   bool   `json:"can_use_ai"`
			ActiveDays int    `json:"active_days"`
			IsAdmin    bool   `json:"is_admin"`
			IsTrial    bool   `json:"is_trial"`
			IsActive   bool   `json:"is_active"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}

		targetID := strings.TrimSpace(c.Params("id"))
		existing, err := Store.GetUserByID(targetID)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"error": "User tidak ditemukan"})
		}
		if existing.IsAdmin {
			return c.Status(400).JSON(fiber.Map{"error": "User admin tidak bisa diedit dari menu ini"})
		}
		if body.ActiveDays <= 0 {
			body.ActiveDays = 1
		}
		updated, err := Store.UpdateUserByID(targetID, storage.UpdateUserInput{
			Email:      body.Email,
			Password:   body.Password,
			IsAdmin:    body.IsAdmin,
			IsTrial:    body.IsTrial,
			CanUseAI:   body.CanUseAI,
			MaxDevices: body.MaxDevices,
			ExpiresAt:  time.Now().Add(time.Duration(body.ActiveDays) * 24 * time.Hour),
			IsActive:   body.IsActive,
		})
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"user": updated})
	})
	api.Delete("/admin/users/:id", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		if err := Store.DeleteUserByID(c.Params("id")); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})
	api.Get("/admin/ai-config", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		return c.JSON(fiber.Map{
			"global_api_key": Store.GetPref("global_nvidia_api_key"),
		})
	})
	api.Post("/admin/ai-config", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		var body struct {
			GlobalAPIKey string `json:"global_api_key"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := Store.SetPref("global_nvidia_api_key", strings.TrimSpace(body.GlobalAPIKey)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved", "global_api_key": Store.GetPref("global_nvidia_api_key")})
	})
	api.Post("/broadcast/ai-helper", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
		}
		if !user.IsAdmin && !user.CanUseAI {
			return c.Status(403).JSON(fiber.Map{"error": "Akses AI belum aktif untuk user ini"})
		}
		var body broadcastAIHelperRequest
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		body.Mode = strings.ToLower(strings.TrimSpace(body.Mode))
		body.Message = strings.TrimSpace(body.Message)
		if body.Message == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Isi pesan wajib diisi"})
		}
		if len(body.Message) > 12000 {
			return c.Status(400).JSON(fiber.Map{"error": "Pesan terlalu panjang untuk dianalisa AI"})
		}
		if body.Mode != "analyze" && body.Mode != "spintax" {
			return c.Status(400).JSON(fiber.Map{"error": "Mode AI tidak dikenal"})
		}
		apiKey := strings.TrimSpace(ai.ResolveAPIKey())
		if apiKey == "" {
			return c.Status(400).JSON(fiber.Map{"error": "API key NVIDIA belum valid. Simpan API key yang diawali nvapi- di menu Admin"})
		}

		prompt := buildBroadcastAIHelperPrompt(body)
		content, err := callNvidiaBroadcastAssistant(c.UserContext(), apiKey, prompt, body.Mode == "spintax")
		if err != nil {
			return c.Status(502).JSON(fiber.Map{"error": err.Error()})
		}
		parsed := parseBroadcastAIJSON(content)
		if body.Mode == "spintax" {
			spintaxMessage := normalizeBroadcastAIText(parsed["spintax_message"])
			if spintaxMessage == "" {
				spintaxMessage = normalizeBroadcastAIText(content)
			}
			return c.JSON(fiber.Map{
				"mode":            "spintax",
				"spintax_message": spintaxMessage,
			})
		}

		analysis := normalizeBroadcastAIText(parsed["analysis"])
		if analysis == "" {
			analysis = normalizeBroadcastAIText(content)
		}
		return c.JSON(fiber.Map{
			"mode":             "analyze",
			"risk_level":       strings.TrimSpace(parsed["risk_level"]),
			"analysis":         analysis,
			"improved_message": normalizeBroadcastAIText(parsed["improved_message"]),
		})
	})
	api.Get("/admin/meta-config", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		return c.JSON(fiber.Map{
			"app_id":       Store.GetPref(metaAppIDPrefKey),
			"app_secret":   Store.GetPref(metaAppSecretPrefKey),
			"config_id":    Store.GetPref(metaConfigIDPrefKey),
			"redirect_uri": Store.GetPref(metaRedirectURIPrefKey),
			"verify_token": Store.GetPref(metaVerifyTokenPrefKey),
		})
	})
	api.Post("/admin/meta-config", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		var body struct {
			AppID       string `json:"app_id"`
			AppSecret   string `json:"app_secret"`
			ConfigID    string `json:"config_id"`
			RedirectURI string `json:"redirect_uri"`
			VerifyToken string `json:"verify_token"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := Store.SetPref(metaAppIDPrefKey, strings.TrimSpace(body.AppID)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := Store.SetPref(metaAppSecretPrefKey, strings.TrimSpace(body.AppSecret)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := Store.SetPref(metaConfigIDPrefKey, strings.TrimSpace(body.ConfigID)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := Store.SetPref(metaRedirectURIPrefKey, strings.TrimSpace(body.RedirectURI)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := Store.SetPref(metaVerifyTokenPrefKey, strings.TrimSpace(body.VerifyToken)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})
	api.Get("/admin/trial-otp-config", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		cfg := loadTrialOTPAdminConfig()
		account, accountErr := ensureTrialOTPVerifierAccount()
		connected := false
		if accountErr == nil {
			connected = whatsapp.IsClientConnectedForAccountForUser(trialOTPSystemUserID, account.ID)
		}
		return c.JSON(fiber.Map{
			"enabled":          true,
			"ttl_minutes":      cfg.TTLMinutes,
			"message_template": cfg.MessageTemplate,
			"success_template": cfg.SuccessTemplate,
			"account":          account,
			"connected":        connected,
			"login_url":        strings.TrimRight(c.BaseURL(), "/") + "/login",
			"trial_days":       config.TrialActiveDays,
			"account_error": firstNonEmpty(func() string {
				if accountErr != nil {
					return accountErr.Error()
				}
				return ""
			}()),
		})
	})
	api.Post("/admin/trial-otp-config", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		var body struct {
			Enabled         bool   `json:"enabled"`
			TTLMinutes      int    `json:"ttl_minutes"`
			MessageTemplate string `json:"message_template"`
			SuccessTemplate string `json:"success_template"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		cfg := trialOTPAdminConfig{
			Enabled:         true,
			TTLMinutes:      body.TTLMinutes,
			MessageTemplate: body.MessageTemplate,
			SuccessTemplate: body.SuccessTemplate,
		}
		if err := saveTrialOTPAdminConfig(cfg); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})
	api.Get("/admin/trial-otp-status", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		account, err := ensureTrialOTPVerifierAccount()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{
			"account":   account,
			"connected": whatsapp.IsClientConnectedForAccountForUser(trialOTPSystemUserID, account.ID),
			"jid":       whatsapp.GetClientJIDForAccountForUser(trialOTPSystemUserID, account.ID),
		})
	})
	api.Get("/admin/trial-otp/qr", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		account, err := ensureTrialOTPVerifierAccount()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		client := whatsapp.GetClientByAccountForUser(trialOTPSystemUserID, account.ID)
		if client == nil {
			return c.Status(404).JSON(fiber.Map{"error": "Akun verifier OTP tidak ditemukan"})
		}
		if client.IsLoggedIn() {
			return c.JSON(fiber.Map{
				"status":    "already_logged_in",
				"jid":       whatsapp.GetClientJIDForAccountForUser(trialOTPSystemUserID, account.ID),
				"account":   account,
				"connected": whatsapp.IsClientConnectedForAccountForUser(trialOTPSystemUserID, account.ID),
			})
		}
		if strings.EqualFold(c.Query("fresh"), "1") || strings.EqualFold(c.Query("fresh"), "true") {
			resetCtx, resetCancel := context.WithTimeout(context.Background(), 25*time.Second)
			resetAccount, resetErr := whatsapp.ResetForPairingForUser(resetCtx, trialOTPSystemUserID, account.ID)
			resetCancel()
			if resetErr != nil {
				return c.Status(500).JSON(fiber.Map{"error": resetErr.Error()})
			}
			account = resetAccount
			client = whatsapp.GetClientByAccountForUser(trialOTPSystemUserID, account.ID)
			if client == nil {
				return c.Status(404).JSON(fiber.Map{"error": "Akun verifier OTP tidak ditemukan setelah reset QR"})
			}
		}
		qrCtx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		keepQRAlive := false
		defer func() {
			if !keepQRAlive {
				cancel()
			}
		}()
		if client.IsConnected() && !client.IsLoggedIn() {
			client.Disconnect()
		}
		qrChan, err := client.GetQRChannel(qrCtx)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := client.ConnectContext(context.Background()); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		for {
			select {
			case <-qrCtx.Done():
				return c.JSON(fiber.Map{"status": "timeout", "account": account, "connected": false})
			case evt, ok := <-qrChan:
				if !ok {
					return c.JSON(fiber.Map{"status": "timeout", "account": account, "connected": false})
				}
				switch evt.Event {
				case "code":
					png, err := qrcode.Encode(evt.Code, qrcode.Medium, 512)
					if err != nil {
						return c.Status(500).JSON(fiber.Map{"error": "Failed to generate QR"})
					}
					// Keep the socket alive after sending the QR so WhatsApp can finish pairing.
					keepQRAlive = true
					return c.JSON(fiber.Map{
						"status":    "qr",
						"qr":        "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
						"code":      evt.Code,
						"account":   account,
						"connected": false,
					})
				case "success":
					updatedAccount, _ := whatsapp.GetAccountForUser(trialOTPSystemUserID, account.ID)
					return c.JSON(fiber.Map{
						"status":    "success",
						"jid":       updatedAccount.Phone,
						"account":   updatedAccount,
						"connected": true,
					})
				}
				if evt.Event == whatsmeow.QRChannelEventError {
					msg := "QR pairing error"
					if evt.Error != nil {
						msg = evt.Error.Error()
					}
					return c.Status(500).JSON(fiber.Map{"error": msg, "status": evt.Event, "account": account, "connected": false})
				}
			}
		}
	})
	api.Post("/admin/trial-otp/reconnect", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		account, err := ensureTrialOTPVerifierAccount()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		reconnectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := whatsapp.ReconnectForAccountForUser(reconnectCtx, trialOTPSystemUserID, account.ID); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "reconnected"})
	})
	api.Post("/admin/trial-otp/logout", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil || !user.IsAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "Forbidden"})
		}
		account, err := ensureTrialOTPVerifierAccount()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := whatsapp.LogoutForAccountForUser(trialOTPSystemUserID, account.ID); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "logged_out"})
	})

	api.Get("/meta/accounts", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accounts, err := tenantCtx.Store.ListMetaWABAAccounts()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"accounts": accounts})
	})
	api.Post("/meta/manual/connect", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccessToken string `json:"access_token"`
			WABAID      string `json:"waba_id"`
			Name        string `json:"name"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if strings.TrimSpace(body.AccessToken) == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Access token wajib diisi"})
		}
		if strings.TrimSpace(body.WABAID) == "" {
			return c.Status(400).JSON(fiber.Map{"error": "WABA ID wajib diisi"})
		}
		cfg := metaConfigFromStore(c)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		item, warnings, err := finalizeMetaManualConnect(
			ctx,
			tenantCtx.Store,
			cfg,
			strings.TrimSpace(body.AccessToken),
			strings.TrimSpace(body.WABAID),
			strings.TrimSpace(body.Name),
		)
		if err != nil {
			statusCode := 500
			lowerErr := strings.ToLower(err.Error())
			if strings.Contains(lowerErr, "meta graph error") || strings.Contains(lowerErr, "access token") || strings.Contains(lowerErr, "waba id") || strings.Contains(lowerErr, "permission") {
				statusCode = 400
			}
			return c.Status(statusCode).JSON(fiber.Map{"error": err.Error()})
		}
		logrus.WithFields(logrus.Fields{
			"user_id":         user.ID,
			"waba_id":         item.WABAID,
			"phone_number_id": item.PhoneNumberID,
			"status":          item.Status,
			"onboarding":      item.OnboardingStatus,
		}).Info("Meta WABA manual connection saved")
		return c.JSON(fiber.Map{"status": "saved", "account": item, "warnings": warnings})
	})
	api.Get("/meta/signup/session", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		cfg := metaConfigFromStore(c)
		if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.ConfigID) == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Meta Embedded Signup belum dikonfigurasi admin"})
		}
		state, err := issueMetaSignupState(user.ID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		redirectURI := cfg.RedirectURI
		if strings.TrimSpace(redirectURI) == "" {
			redirectURI = strings.TrimRight(c.BaseURL(), "/") + "/api/meta/signup/callback"
		}
		extrasJSON := `{"setup":{},"feature":"whatsapp_embedded_signup","featureType":"whatsapp_business_app_onboarding","sessionInfoVersion":"3"}`
		fallbackRedirectURI := strings.TrimRight(c.BaseURL(), "/") + "/"
		launchURL := fmt.Sprintf(
			"https://www.facebook.com/%s/dialog/oauth?app_id=%s&client_id=%s&redirect_uri=%s&fallback_redirect_uri=%s&state=%s&response_type=code&scope=%s&config_id=%s&display=popup&override_default_response_type=true&extras=%s",
			url.QueryEscape(cfg.GraphVersion),
			url.QueryEscape(cfg.AppID),
			url.QueryEscape(cfg.AppID),
			url.QueryEscape(redirectURI),
			url.QueryEscape(fallbackRedirectURI),
			url.QueryEscape(state),
			url.QueryEscape("whatsapp_business_management,whatsapp_business_messaging"),
			url.QueryEscape(cfg.ConfigID),
			url.QueryEscape(extrasJSON),
		)
		return c.JSON(fiber.Map{
			"state":         state,
			"launch_url":    launchURL,
			"app_id":        cfg.AppID,
			"config_id":     cfg.ConfigID,
			"redirect_uri":  redirectURI,
			"graph_version": cfg.GraphVersion,
			"user_id":       user.ID,
		})
	})
	api.Post("/meta/signup/complete", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			State         string `json:"state"`
			Code          string `json:"code"`
			Name          string `json:"name"`
			BusinessID    string `json:"business_id"`
			WABAID        string `json:"waba_id"`
			PhoneNumberID string `json:"phone_number_id"`
			DisplayPhone  string `json:"display_phone_number"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		cfg := metaConfigFromStore(c)
		if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" || strings.TrimSpace(cfg.ConfigID) == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Konfigurasi Meta belum lengkap. Isi App ID, App Secret, dan Config ID terlebih dahulu."})
		}
		if strings.TrimSpace(body.Code) == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Kode otorisasi Meta tidak ditemukan. Ulangi proses signup dari awal."})
		}
		if err := consumeMetaSignupState(user.ID, body.State); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		item, warnings, err := finalizeMetaSignup(ctx, tenantCtx.Store, cfg, metaSignupCompletePayload{
			Name:          strings.TrimSpace(body.Name),
			Code:          strings.TrimSpace(body.Code),
			BusinessID:    strings.TrimSpace(body.BusinessID),
			WABAID:        strings.TrimSpace(body.WABAID),
			PhoneNumberID: strings.TrimSpace(body.PhoneNumberID),
			DisplayPhone:  strings.TrimSpace(body.DisplayPhone),
		})
		if err != nil {
			statusCode := 500
			lowerErr := strings.ToLower(err.Error())
			if strings.Contains(lowerErr, "meta graph error") || strings.Contains(lowerErr, "kode otorisasi") || strings.Contains(lowerErr, "meta tidak mengirim") || strings.Contains(lowerErr, "app id") {
				statusCode = 400
			}
			return c.Status(statusCode).JSON(fiber.Map{"error": err.Error()})
		}
		logrus.WithFields(logrus.Fields{
			"user_id":         user.ID,
			"business_id":     item.BusinessID,
			"waba_id":         item.WABAID,
			"phone_number_id": item.PhoneNumberID,
			"status":          item.Status,
			"onboarding":      item.OnboardingStatus,
		}).Info("Meta WABA signup completed")
		return c.JSON(fiber.Map{"status": "saved", "account": item, "warnings": warnings})
	})
	api.Get("/meta/signup/callback", func(c *fiber.Ctx) error {
		code := strings.TrimSpace(c.Query("code"))
		state := strings.TrimSpace(c.Query("state"))
		errCode := strings.TrimSpace(c.Query("error"))
		errDesc := strings.TrimSpace(c.Query("error_description"))
		payload, _ := json.Marshal(fiber.Map{
			"type":              "meta_embedded_signup_callback",
			"code":              code,
			"state":             state,
			"error":             errCode,
			"error_description": errDesc,
		})
		html := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>Meta Signup Callback</title></head><body style="font-family:Arial,sans-serif;padding:24px;"><h3>Meta signup selesai</h3><p>Jendela ini bisa ditutup.</p><script>const payload=%s;if(window.opener){window.opener.postMessage(payload, window.location.origin);}setTimeout(()=>window.close(), 500);</script></body></html>`, string(payload))
		c.Set("Content-Type", "text/html")
		return c.SendString(html)
	})
	api.Get("/meta/webhook", func(c *fiber.Ctx) error {
		mode := strings.TrimSpace(c.Query("hub.mode"))
		verifyToken := strings.TrimSpace(c.Query("hub.verify_token"))
		challenge := strings.TrimSpace(c.Query("hub.challenge"))
		cfg := metaConfigFromStore(c)
		if mode == "subscribe" && cfg.VerifyToken != "" && verifyToken == cfg.VerifyToken {
			return c.SendString(challenge)
		}
		return c.Status(403).SendString("Forbidden")
	})
	api.Post("/meta/webhook", func(c *fiber.Ctx) error {
		logrus.Infof("Meta webhook received: %s", string(c.Body()))
		return c.JSON(fiber.Map{"status": "ok"})
	})
	api.Post("/meta/data-deletion", func(c *fiber.Ctx) error {
		signedRequest := strings.TrimSpace(c.FormValue("signed_request"))
		if signedRequest == "" {
			var body struct {
				SignedRequest string `json:"signed_request"`
			}
			_ = c.BodyParser(&body)
			signedRequest = strings.TrimSpace(body.SignedRequest)
		}
		if signedRequest == "" {
			return c.Status(400).JSON(fiber.Map{"error": "signed_request wajib diisi"})
		}

		cfg := metaConfigFromStore(c)
		payload, err := parseMetaSignedRequest(signedRequest, cfg.AppSecret)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}

		code, err := issueMetaDeletionConfirmationCode()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		statusURL := strings.TrimRight(c.BaseURL(), "/") + "/data-deletion-status?code=" + url.QueryEscape(code)

		logrus.WithFields(logrus.Fields{
			"algorithm": payload.Algorithm,
			"user_id":   payload.UserID,
			"issued_at": payload.IssuedAt,
		}).Info("Meta data deletion callback received")

		return c.JSON(fiber.Map{
			"url":               statusURL,
			"confirmation_code": code,
		})
	})

	api.Get("/accounts", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{
			"accounts":          whatsapp.ListAccountsForUser(user.ID),
			"active_account_id": whatsapp.GetActiveAccountIDForUser(user.ID),
			"user_email":        user.Email,
			"tenant_ready":      tenantCtx != nil,
		})
	})

	api.Post("/accounts", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := c.BodyParser(&body); err != nil && len(c.Body()) > 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if len(whatsapp.ListAccountsForUser(user.ID)) >= effectiveUserMaxDevices(user) {
			return c.Status(400).JSON(fiber.Map{"error": "Maksimal device login tercapai"})
		}
		account, err := whatsapp.CreateAccountForUser(user.ID, body.Name)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		_ = whatsapp.SetActiveAccountForUser(user.ID, account.ID)
		return c.JSON(fiber.Map{"account": account, "active_account_id": account.ID})
	})

	api.Post("/accounts/active", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID string `json:"account_id"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := whatsapp.SetActiveAccountForUser(user.ID, body.AccountID); err != nil {
			return c.Status(404).JSON(fiber.Map{"error": err.Error()})
		}
		resolvedAccountID := whatsapp.GetActiveAccountIDForUser(user.ID)
		go func() {
			connectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := whatsapp.EnsureClientConnectedForAccountForUser(connectCtx, user.ID, resolvedAccountID); err != nil {
				logrus.WithError(err).WithField("account_id", resolvedAccountID).Debug("active WhatsApp account background reconnect skipped")
			}
		}()
		return c.JSON(fiber.Map{"status": "ok", "active_account_id": resolvedAccountID})
	})

	api.Patch("/accounts/:id", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := whatsapp.RenameAccountForUser(user.ID, c.Params("id"), body.Name); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		account, _ := whatsapp.GetAccountForUser(user.ID, c.Params("id"))
		return c.JSON(fiber.Map{"account": account})
	})

	api.Patch("/accounts/:id/webhook", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Enabled bool   `json:"enabled"`
			URL     string `json:"url"`
			Secret  string `json:"secret"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}

		webhookURL := strings.TrimSpace(body.URL)
		if body.Enabled {
			parsedURL, err := url.Parse(webhookURL)
			if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
				return c.Status(400).JSON(fiber.Map{"error": "URL webhook tidak valid"})
			}
			if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
				return c.Status(400).JSON(fiber.Map{"error": "URL webhook harus memakai http atau https"})
			}
		}

		account, err := whatsapp.SetAccountWebhookForUser(user.ID, c.Params("id"), body.Enabled, webhookURL, body.Secret)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"account": account})
	})

	api.Delete("/accounts/:id", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if err := whatsapp.DeleteAccountForUser(context.Background(), user.ID, c.Params("id")); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{
			"status":            "deleted",
			"active_account_id": whatsapp.GetActiveAccountIDForUser(user.ID),
			"accounts":          whatsapp.ListAccountsForUser(user.ID),
		})
	})

	api.Get("/status", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromQuery(c)
		account, err := whatsapp.GetAccountForUser(user.ID, accountID)
		if err != nil {
			return c.JSON(fiber.Map{
				"connected":         false,
				"jid":               "",
				"account_id":        whatsapp.ResolveAccountIDForUser(user.ID, accountID),
				"active_account_id": whatsapp.GetActiveAccountIDForUser(user.ID),
				"accounts":          whatsapp.ListAccountsForUser(user.ID),
			})
		}
		if account.LoggedIn && !account.Connected {
			connectCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			if err := whatsapp.EnsureClientConnectedForAccountForUser(connectCtx, user.ID, account.ID); err != nil {
				logrus.WithError(err).WithField("account_id", account.ID).Debug("WhatsApp status reconnect skipped")
			} else if refreshed, refreshErr := whatsapp.GetAccountForUser(user.ID, account.ID); refreshErr == nil {
				account = refreshed
			}
			cancel()
		}
		return c.JSON(fiber.Map{
			"connected":         account.Connected && account.LoggedIn,
			"jid":               account.Phone,
			"account_id":        account.ID,
			"active_account_id": whatsapp.GetActiveAccountIDForUser(user.ID),
			"accounts":          whatsapp.ListAccountsForUser(user.ID),
		})
	})

	api.Post("/pair-code", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID string `json:"account_id"`
			Phone     string `json:"phone"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		phone := normalizePhone(body.Phone)
		if phone == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Nomor WhatsApp wajib diisi"})
		}
		accountID := strings.TrimSpace(body.AccountID)
		if accountID == "" {
			account, err := ensureAtLeastOneAccount(user.ID)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"error": err.Error()})
			}
			accountID = account.ID
		}
		account, err := whatsapp.GetAccountForUser(user.ID, accountID)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"error": err.Error()})
		}
		if account.LoggedIn {
			return c.JSON(fiber.Map{
				"status":     "already_logged_in",
				"account_id": account.ID,
				"account":    account,
			})
		}

		pairCtx, cancel := context.WithTimeout(context.Background(), 160*time.Second)
		keepPairingAlive := false
		defer func() {
			if !keepPairingAlive {
				cancel()
			}
		}()
		code, account, err := whatsapp.PairCodeForUser(pairCtx, user.ID, accountID, phone)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error(), "account_id": accountID})
		}
		keepPairingAlive = true
		return c.JSON(fiber.Map{
			"status":     "pair_code",
			"pair_code":  code,
			"phone":      phone,
			"account_id": account.ID,
			"account":    account,
		})
	})

	api.Get("/qr", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromQuery(c)
		if accountID == "" {
			account, err := ensureAtLeastOneAccount(user.ID)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"error": err.Error()})
			}
			accountID = account.ID
		}

		client := whatsapp.GetClientByAccountForUser(user.ID, accountID)
		if client == nil {
			return c.Status(404).JSON(fiber.Map{"error": "Account not found"})
		}
		if client.IsLoggedIn() {
			return c.JSON(fiber.Map{
				"status":     "already_logged_in",
				"jid":        whatsapp.GetClientJIDForAccountForUser(user.ID, accountID),
				"account_id": accountID,
			})
		}
		if strings.EqualFold(c.Query("fresh"), "1") || strings.EqualFold(c.Query("fresh"), "true") {
			resetCtx, resetCancel := context.WithTimeout(context.Background(), 25*time.Second)
			_, resetErr := whatsapp.ResetForPairingForUser(resetCtx, user.ID, accountID)
			resetCancel()
			if resetErr != nil {
				return c.Status(500).JSON(fiber.Map{"error": resetErr.Error()})
			}
			client = whatsapp.GetClientByAccountForUser(user.ID, accountID)
			if client == nil {
				return c.Status(404).JSON(fiber.Map{"error": "Account not found after QR reset"})
			}
		}

		qrCtx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		keepQRAlive := false
		defer func() {
			if !keepQRAlive {
				cancel()
			}
		}()
		if client.IsConnected() && !client.IsLoggedIn() {
			client.Disconnect()
		}
		qrChan, err := client.GetQRChannel(qrCtx)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := client.ConnectContext(context.Background()); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		for {
			select {
			case <-qrCtx.Done():
				return c.JSON(fiber.Map{"status": "timeout", "account_id": accountID})
			case evt, ok := <-qrChan:
				if !ok {
					return c.JSON(fiber.Map{"status": "timeout", "account_id": accountID})
				}
				switch evt.Event {
				case "code":
					png, err := qrcode.Encode(evt.Code, qrcode.Medium, 512)
					if err != nil {
						return c.Status(500).JSON(fiber.Map{"error": "Failed to generate QR"})
					}
					// Keep the socket alive after sending the QR so WhatsApp can finish pairing.
					keepQRAlive = true
					return c.JSON(fiber.Map{
						"status":     "qr",
						"qr":         "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
						"code":       evt.Code,
						"account_id": accountID,
					})
				case "success":
					account, _ := whatsapp.GetAccountForUser(user.ID, accountID)
					return c.JSON(fiber.Map{
						"status":     "success",
						"jid":        account.Phone,
						"account_id": accountID,
						"account":    account,
					})
				}
				if evt.Event == whatsmeow.QRChannelEventError {
					msg := "QR pairing error"
					if evt.Error != nil {
						msg = evt.Error.Error()
					}
					return c.Status(500).JSON(fiber.Map{"error": msg, "status": evt.Event, "account_id": accountID})
				}
			}
		}
	})

	api.Post("/reconnect", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromBodyOrQuery(c)
		reconnectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := whatsapp.ReconnectForAccountForUser(reconnectCtx, user.ID, accountID); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "reconnected", "account_id": whatsapp.ResolveAccountIDForUser(user.ID, accountID)})
	})

	api.Post("/logout", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromBodyOrQuery(c)
		if err := whatsapp.LogoutForAccountForUser(user.ID, accountID); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "logged_out", "account_id": whatsapp.ResolveAccountIDForUser(user.ID, accountID)})
	})

	api.Get("/warming/settings", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(tenantCtx.Warming.GetStatus())
	})

	api.Post("/warming/settings", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body warming.Settings
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		status, err := tenantCtx.Warming.SaveSettings(body)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if status.Enabled {
			broadcastWSLog("Warming up berhasil disimpan dan siap dijalankan", "info")
		} else {
			broadcastWSLog("Konfigurasi warming up disimpan", "info")
		}
		return c.JSON(status)
	})

	api.Post("/warming/start", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		status, err := tenantCtx.Warming.Start()
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		broadcastWSLog("Warming up WhatsApp dimulai", "success")
		return c.JSON(status)
	})

	api.Post("/warming/stop", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		status, err := tenantCtx.Warming.Stop()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		broadcastWSLog("Warming up WhatsApp dihentikan", "warning")
		return c.JSON(status)
	})

	api.Post("/send/text", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID string `json:"account_id"`
			Phone     string `json:"phone"`
			Message   string `json:"message"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := whatsapp.EnsureClientConnectedForAccountForUser(c.UserContext(), user.ID, body.AccountID); err != nil {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected: " + err.Error()})
		}
		jid := parsePhoneToJID(body.Phone)
		if err := whatsapp.SendTextForUserAccount(context.Background(), user.ID, body.AccountID, jid, body.Message); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "sent"})
	})

	api.Post("/send/image", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID string `json:"account_id"`
			Phone     string `json:"phone"`
			Caption   string `json:"caption"`
			ImageB64  string `json:"image"`
			Mimetype  string `json:"mimetype"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := whatsapp.EnsureClientConnectedForAccountForUser(c.UserContext(), user.ID, body.AccountID); err != nil {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected: " + err.Error()})
		}
		imgData, err := base64.StdEncoding.DecodeString(body.ImageB64)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid image data"})
		}
		if body.Mimetype == "" {
			body.Mimetype = "image/jpeg"
		}
		jid := parsePhoneToJID(body.Phone)
		if err := whatsapp.SendImageForUserAccount(context.Background(), user.ID, body.AccountID, jid, imgData, body.Mimetype, body.Caption); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "sent"})
	})

	api.Post("/validate", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID string   `json:"account_id"`
			Numbers   []string `json:"numbers"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, body.AccountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
		}
		checkNums := make([]string, len(body.Numbers))
		for i, n := range body.Numbers {
			checkNums[i] = "+" + strings.TrimLeft(n, "+")
		}
		results, err := whatsapp.IsOnWhatsAppForUserAccount(user.ID, body.AccountID, checkNums)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		validationResults := make([]fiber.Map, 0, len(results))
		for _, r := range results {
			validationResults = append(validationResults, fiber.Map{
				"query": r.Query,
				"is_in": r.IsIn,
				"jid":   r.JID.String(),
			})
		}
		return c.JSON(fiber.Map{"results": validationResults})
	})

	api.Get("/groups", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromQuery(c)
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, accountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
		}
		groups, err := whatsapp.GetJoinedGroupsForUserAccount(user.ID, accountID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		groupList := make([]fiber.Map, 0, len(groups))
		for _, g := range groups {
			groupList = append(groupList, fiber.Map{
				"id":      g.JID.String(),
				"name":    g.Name,
				"members": len(g.Participants),
			})
		}
		return c.JSON(fiber.Map{"groups": groupList})
	})

	api.Get("/groups/:id/members", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromQuery(c)
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, accountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
		}
		groupID := c.Params("id")
		jid, err := parseGroupJID(groupID)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid group ID"})
		}
		info, err := whatsapp.GetGroupInfoForUserAccount(user.ID, accountID, jid)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		members := make([]fiber.Map, 0, len(info.Participants))
		skippedHidden := 0
		for _, p := range info.Participants {
			phone := strings.TrimSpace(firstNonEmpty(p.PhoneNumber.User, p.JID.User))
			if phone == "" {
				skippedHidden++
				continue
			}
			name := strings.TrimSpace(p.DisplayName)
			if name == "" {
				name = phone
			}
			members = append(members, fiber.Map{
				"jid":    p.JID.String(),
				"phone":  phone,
				"name":   name,
				"admin":  p.IsAdmin || p.IsSuperAdmin,
				"hidden": false,
			})
		}

		return c.JSON(fiber.Map{
			"group_name":     info.Name,
			"members":        members,
			"skipped_hidden": skippedHidden,
		})
	})

	api.Post("/broadcast/start", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID      string              `json:"account_id"`
			Name           string              `json:"name"`
			ContactGroupID string              `json:"contact_group_id"`
			Numbers        string              `json:"numbers"`
			ContactRows    []map[string]string `json:"contact_rows"`
			Message        string              `json:"message"`
			UseSpintax     bool                `json:"use_spintax"`
			ImageB64       string              `json:"image"`
			ImageMime      string              `json:"image_mime"`
			Images         []imagePayload      `json:"images"`
			DelaySeconds   int                 `json:"delay_seconds"`
			RandomDelay    bool                `json:"random_delay"`
			DelayMin       int                 `json:"delay_min"`
			DelayMax       int                 `json:"delay_max"`
			BurstEvery     int                 `json:"burst_every"`
			BurstPause     int                 `json:"burst_pause"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := whatsapp.EnsureClientConnectedForAccountForUser(c.UserContext(), user.ID, body.AccountID); err != nil {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected: " + err.Error()})
		}
		nums := broadcast.ParseNumbers(body.Numbers)
		if len(nums) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "No valid numbers"})
		}
		unsubscribeSet, _ := loadUnsubscribePhoneSet(tenantCtx.Store)
		filteredNums, skipped := filterNumbersAgainstUnsubscribe(nums, unsubscribeSet)
		if len(filteredNums) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Semua nomor tujuan sudah masuk daftar unsubscribe"})
		}

		account, _ := whatsapp.GetAccountForUser(user.ID, body.AccountID)
		campaignName := strings.TrimSpace(body.Name)
		if campaignName == "" {
			campaignName = "Broadcast " + time.Now().Format("02/01/2006 15:04")
		}
		contactRows := buildBroadcastContactRows(user.ID, body.AccountID, filteredNums, body.ContactRows)
		_ = saveResolvedWANamesToContactList(tenantCtx.Store, body.ContactGroupID, contactRows)
		cfg := broadcast.Config{
			OwnerID:      user.ID,
			AccountID:    body.AccountID,
			AccountName:  account.Name,
			CampaignName: campaignName,
			Numbers:      filteredNums,
			ContactRows:  contactRows,
			Message:      appendUnsubscribeInstruction(tenantCtx.Store, body.Message),
			UseSpintax:   body.UseSpintax,
			DelaySeconds: body.DelaySeconds,
			RandomDelay:  body.RandomDelay,
			DelayMin:     body.DelayMin,
			DelayMax:     body.DelayMax,
			BurstEvery:   body.BurstEvery,
			BurstPause:   body.BurstPause,
		}
		images, _, err := decodeImagePayloads(body.Images, body.ImageB64, body.ImageMime)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if len(images) > 0 {
			cfg.Images = images
		}
		if err := broadcast.GetEngineForUser(user.ID).Start(cfg); err != nil {
			return c.Status(409).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "started", "total": len(filteredNums), "skipped_unsubscribe": skipped})
	})

	api.Post("/broadcast/personal", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID    string         `json:"account_id"`
			CSVData      string         `json:"csv_data"`
			Message      string         `json:"message"`
			UseSpintax   bool           `json:"use_spintax"`
			ImageB64     string         `json:"image"`
			ImageMime    string         `json:"image_mime"`
			Images       []imagePayload `json:"images"`
			DelaySeconds int            `json:"delay_seconds"`
			RandomDelay  bool           `json:"random_delay"`
			DelayMin     int            `json:"delay_min"`
			DelayMax     int            `json:"delay_max"`
			BurstEvery   int            `json:"burst_every"`
			BurstPause   int            `json:"burst_pause"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := whatsapp.EnsureClientConnectedForAccountForUser(c.UserContext(), user.ID, body.AccountID); err != nil {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected: " + err.Error()})
		}

		headers, data, err := parsePersonalCSVData(body.CSVData)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if len(data) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "No valid data in CSV"})
		}
		unsubscribeSet, _ := loadUnsubscribePhoneSet(tenantCtx.Store)
		filteredData, skipped := filterPersonalRowsAgainstUnsubscribe(data, unsubscribeSet)
		if len(filteredData) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Semua nomor di data personalisasi sudah unsubscribe"})
		}

		account, _ := whatsapp.GetAccountForUser(user.ID, body.AccountID)
		cfg := broadcast.PersonalConfig{
			OwnerID:      user.ID,
			AccountID:    body.AccountID,
			AccountName:  account.Name,
			Data:         filteredData,
			Message:      appendUnsubscribeInstruction(tenantCtx.Store, body.Message),
			UseSpintax:   body.UseSpintax,
			DelaySeconds: body.DelaySeconds,
			RandomDelay:  body.RandomDelay,
			DelayMin:     body.DelayMin,
			DelayMax:     body.DelayMax,
			BurstEvery:   body.BurstEvery,
			BurstPause:   body.BurstPause,
		}
		images, _, err := decodeImagePayloads(body.Images, body.ImageB64, body.ImageMime)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if len(images) > 0 {
			cfg.Images = images
		}
		if err := broadcast.GetEngineForUser(user.ID).StartPersonal(cfg); err != nil {
			return c.Status(409).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "started", "total": len(filteredData), "columns": headers, "skipped_unsubscribe": skipped})
	})

	api.Post("/broadcast/pause", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		broadcast.GetEngineForUser(user.ID).Pause()
		broadcastWSProgress()
		return c.JSON(fiber.Map{"status": "paused"})
	})
	api.Post("/broadcast/resume", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		broadcast.GetEngineForUser(user.ID).Resume()
		broadcastWSProgress()
		return c.JSON(fiber.Map{"status": "resumed"})
	})
	api.Post("/broadcast/stop", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		broadcast.GetEngineForUser(user.ID).Stop()
		broadcastWSProgress()
		return c.JSON(fiber.Map{"status": "stopped"})
	})
	api.Get("/broadcast/progress", func(c *fiber.Ctx) error {
		user, err := currentUser(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		progress := broadcast.GetEngineForUser(user.ID).GetProgress()
		if progress.OwnerID != "" && progress.OwnerID != user.ID {
			return c.JSON(broadcast.Progress{Status: broadcast.StatusIdle})
		}
		return c.JSON(progress)
	})
	api.Get("/broadcast/schedules", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if tenantCtx.Scheduler == nil {
			return c.Status(500).JSON(fiber.Map{"error": "Scheduler not initialized"})
		}
		records, err := tenantCtx.Scheduler.ListSchedules(strings.TrimSpace(c.Query("status")), strings.TrimSpace(c.Query("type")))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"schedules": records})
	})
	api.Post("/broadcast/schedules", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if tenantCtx.Scheduler == nil {
			return c.Status(500).JSON(fiber.Map{"error": "Scheduler not initialized"})
		}
		var body struct {
			ScheduleType string         `json:"schedule_type"`
			Name         string         `json:"name"`
			AccountID    string         `json:"account_id"`
			Numbers      string         `json:"numbers"`
			CSVData      string         `json:"csv_data"`
			Message      string         `json:"message"`
			UseSpintax   bool           `json:"use_spintax"`
			ImageB64     string         `json:"image"`
			ImageMime    string         `json:"image_mime"`
			Images       []imagePayload `json:"images"`
			DelaySeconds int            `json:"delay_seconds"`
			RandomDelay  bool           `json:"random_delay"`
			DelayMin     int            `json:"delay_min"`
			DelayMax     int            `json:"delay_max"`
			BurstEvery   int            `json:"burst_every"`
			BurstPause   int            `json:"burst_pause"`
			RunAt        string         `json:"run_at"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if strings.TrimSpace(body.ScheduleType) == "" {
			body.ScheduleType = "broadcast"
		}
		account, err := whatsapp.GetAccountForUser(user.ID, body.AccountID)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Akun WhatsApp tidak ditemukan"})
		}
		runAt, err := parseScheduleTime(body.RunAt)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if body.ScheduleType == "personalisasi" {
			if strings.TrimSpace(body.CSVData) == "" {
				return c.Status(400).JSON(fiber.Map{"error": "CSV data wajib diisi untuk jadwal personalisasi"})
			}
		} else if len(broadcast.ParseNumbers(body.Numbers)) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "No valid numbers"})
		}
		unsubscribeSet, _ := loadUnsubscribePhoneSet(tenantCtx.Store)
		if body.ScheduleType == "personalisasi" {
			headers, rows, err := parsePersonalCSVData(body.CSVData)
			if err != nil {
				return c.Status(400).JSON(fiber.Map{"error": err.Error()})
			}
			filteredRows, _ := filterPersonalRowsAgainstUnsubscribe(rows, unsubscribeSet)
			if len(filteredRows) == 0 {
				return c.Status(400).JSON(fiber.Map{"error": "Semua nomor di jadwal personalisasi sudah unsubscribe"})
			}
			body.CSVData, err = buildPersonalCSVData(headers, filteredRows)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "Gagal memproses CSV jadwal"})
			}
		} else {
			filteredNumbers, _ := filterNumbersAgainstUnsubscribe(broadcast.ParseNumbers(body.Numbers), unsubscribeSet)
			if len(filteredNumbers) == 0 {
				return c.Status(400).JSON(fiber.Map{"error": "Semua nomor di jadwal broadcast sudah unsubscribe"})
			}
			body.Numbers = strings.Join(filteredNumbers, "\n")
		}
		_, sanitizedImages, err := decodeImagePayloads(body.Images, body.ImageB64, body.ImageMime)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		imagesJSON := ""
		legacyImageB64 := strings.TrimSpace(body.ImageB64)
		legacyImageMime := strings.TrimSpace(body.ImageMime)
		if len(sanitizedImages) > 0 {
			rawJSON, err := json.Marshal(sanitizedImages)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "Gagal menyimpan data gambar"})
			}
			imagesJSON = string(rawJSON)
			if len(sanitizedImages) == 1 {
				legacyImageB64 = sanitizedImages[0].Data
				legacyImageMime = sanitizedImages[0].Mime
			} else {
				legacyImageB64 = ""
				legacyImageMime = ""
			}
		}
		rec := &storage.BroadcastSchedule{
			ScheduleType: strings.TrimSpace(body.ScheduleType),
			Name:         strings.TrimSpace(body.Name),
			AccountID:    body.AccountID,
			AccountName:  account.Name,
			Numbers:      body.Numbers,
			CSVData:      body.CSVData,
			Message:      appendUnsubscribeInstruction(tenantCtx.Store, body.Message),
			UseSpintax:   body.UseSpintax,
			ImageB64:     legacyImageB64,
			ImageMime:    legacyImageMime,
			ImagesJSON:   imagesJSON,
			DelaySeconds: body.DelaySeconds,
			RandomDelay:  body.RandomDelay,
			DelayMin:     body.DelayMin,
			DelayMax:     body.DelayMax,
			BurstEvery:   body.BurstEvery,
			BurstPause:   body.BurstPause,
			RunAt:        runAt,
			Status:       "pending",
		}
		if err := tenantCtx.Scheduler.SaveSchedule(rec); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		broadcastWSLog(fmt.Sprintf("Jadwal broadcast disimpan untuk akun %s", account.Name), "success")
		return c.JSON(fiber.Map{"status": "scheduled", "schedule": rec})
	})
	api.Delete("/broadcast/schedules/:id", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if tenantCtx.Scheduler == nil {
			return c.Status(500).JSON(fiber.Map{"error": "Scheduler not initialized"})
		}
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil || id <= 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid schedule ID"})
		}
		if err := tenantCtx.Scheduler.DeleteSchedule(id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})

	api.Get("/history", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		filter := c.Query("account", "all")
		records, err := tenantCtx.Store.GetBroadcastHistory(filter)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"history": records})
	})
	api.Post("/history", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var rec storage.BroadcastRecord
		if err := c.BodyParser(&rec); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		rec.Date = time.Now()
		if err := tenantCtx.Store.SaveBroadcast(&rec); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})
	api.Delete("/history", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if err := tenantCtx.Store.ClearBroadcastHistory(); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "cleared"})
	})

	api.Get("/history/chats", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromQuery(c)
		if accountID == "" {
			accountID = whatsapp.GetActiveAccountIDForUser(user.ID)
		}
		account, _ := whatsapp.GetAccountForUser(user.ID, accountID)
		client := whatsapp.GetClientByAccountForUser(user.ID, accountID)
		records, err := tenantCtx.Store.GetChatHistory(accountID, 1000)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if client != nil && tenantCtx.ChatHistory != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = tenantCtx.ChatHistory.SyncContacts(ctx, accountID, account.Name, client)
			}()
		}
		return c.JSON(fiber.Map{"history": records, "account_id": accountID})
	})

	api.Delete("/history/chats", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromQuery(c)
		if err := tenantCtx.Store.ClearChatHistory(accountID); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "cleared"})
	})

	api.Get("/groups/contacts", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		groups, err := tenantCtx.Store.GetContactGroups()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"groups": groups})
	})
	api.Post("/groups/contacts", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Name    string `json:"name"`
			Numbers string `json:"numbers"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := tenantCtx.Store.SaveContactGroup(body.Name, body.Numbers); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})
	api.Delete("/groups/contacts/:name", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if err := tenantCtx.Store.DeleteContactGroup(c.Params("name")); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})

	api.Get("/contacts/lists", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		lists, err := tenantCtx.Store.GetContactLists()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		items := make([]fiber.Map, 0, len(lists))
		for _, item := range lists {
			var columns []string
			var contacts []map[string]string
			_ = json.Unmarshal([]byte(item.ColumnsJSON), &columns)
			_ = json.Unmarshal([]byte(item.ContactsJSON), &contacts)
			items = append(items, fiber.Map{
				"id":         item.ID,
				"name":       item.Name,
				"columns":    columns,
				"contacts":   contacts,
				"numbers":    item.Numbers,
				"count":      item.Count,
				"created_at": item.CreatedAt,
				"updated_at": item.UpdatedAt,
			})
		}
		return c.JSON(fiber.Map{"groups": items})
	})
	api.Post("/contacts/lists", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Name     string              `json:"name"`
			Columns  []string            `json:"columns"`
			Contacts []map[string]string `json:"contacts"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Nama grup kontak wajib diisi"})
		}
		if len(body.Contacts) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Kontak belum ada"})
		}
		columnsJSON, _ := json.Marshal(body.Columns)
		contactsJSON, _ := json.Marshal(body.Contacts)
		numbers := make([]string, 0, len(body.Contacts))
		for _, row := range body.Contacts {
			number := ""
			for key, value := range row {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "nomor", "phone", "no", "whatsapp", "wa", "hp", "telepon":
					number = strings.TrimSpace(value)
				}
				if number != "" {
					break
				}
			}
			if number != "" {
				numbers = append(numbers, number)
			}
		}
		if len(numbers) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Kolom nomor/phone/wa tidak ditemukan"})
		}
		if err := tenantCtx.Store.SaveContactList(name, string(columnsJSON), string(contactsJSON), strings.Join(numbers, "\n"), len(numbers)); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})
	api.Delete("/contacts/lists/:id", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil || id <= 0 {
			return c.Status(400).JSON(fiber.Map{"error": "ID kontak tidak valid"})
		}
		if err := tenantCtx.Store.DeleteContactList(id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})
	api.Get("/contacts/unsubscribe/settings", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		settings, err := tenantCtx.Store.GetUnsubscribeSettings()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(settings)
	})
	api.Post("/contacts/unsubscribe/settings", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body storage.UnsubscribeSettings
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if err := tenantCtx.Store.SaveUnsubscribeSettings(body); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		settings, _ := tenantCtx.Store.GetUnsubscribeSettings()
		return c.JSON(settings)
	})
	api.Get("/contacts/unsubscribe", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		items, err := tenantCtx.Store.ListUnsubscribedContacts()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"items": items})
	})
	api.Delete("/contacts/unsubscribe/:id", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil || id <= 0 {
			return c.Status(400).JSON(fiber.Map{"error": "ID unsubscribe tidak valid"})
		}
		if err := tenantCtx.Store.DeleteUnsubscribedContact(id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})

	api.Get("/media/files", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		files, err := tenantCtx.Store.GetMediaFiles()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		items := make([]fiber.Map, 0, len(files))
		for _, item := range files {
			items = append(items, fiber.Map{
				"id":            item.ID,
				"name":          item.Name,
				"original_name": item.OriginalName,
				"mime":          item.Mime,
				"size":          item.Size,
				"url":           mediaFileURL(item.Name),
				"created_at":    item.CreatedAt,
			})
		}
		return c.JSON(fiber.Map{"files": items})
	})
	api.Post("/media/files", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		fileHeader, err := c.FormFile("file")
		if err != nil || fileHeader == nil {
			return c.Status(400).JSON(fiber.Map{"error": "File wajib diupload"})
		}
		mimeType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		savedName, err := issueManagedFileName(fileHeader.Filename)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		mediaDir := filepath.Join(tenantCtx.BaseDir, "media-manager")
		if err := os.MkdirAll(mediaDir, 0o755); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		dst := filepath.Join(mediaDir, savedName)
		if err := c.SaveFile(fileHeader, dst); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		rec := storage.MediaFile{
			Name:         savedName,
			OriginalName: strings.TrimSpace(fileHeader.Filename),
			Mime:         mimeType,
			Size:         fileHeader.Size,
			Path:         dst,
		}
		if err := tenantCtx.Store.SaveMediaFile(rec); err != nil {
			_ = os.Remove(dst)
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "uploaded", "file": fiber.Map{
			"name":          rec.Name,
			"original_name": rec.OriginalName,
			"mime":          rec.Mime,
			"size":          rec.Size,
			"url":           mediaFileURL(rec.Name),
		}})
	})
	api.Delete("/media/files/:id", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil || id <= 0 {
			return c.Status(400).JSON(fiber.Map{"error": "ID file tidak valid"})
		}
		file, err := tenantCtx.Store.DeleteMediaFile(id)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if file.Path != "" {
			_ = os.Remove(file.Path)
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})
	api.Get("/media/files/:name", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		name := sanitizeManagedAssetName(c.Params("name"))
		if name == "" {
			return c.Status(404).SendString("not found")
		}
		path := filepath.Join(tenantCtx.BaseDir, "media-manager", name)
		if _, err := os.Stat(path); err != nil {
			return c.Status(404).SendString("not found")
		}
		return c.SendFile(path)
	})

	api.Get("/templates/:type", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		templates, err := tenantCtx.Store.GetTemplates(c.Params("type"))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"templates": templates})
	})
	api.Post("/templates", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Name    string `json:"name"`
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if body.Type == "" {
			body.Type = "broadcast"
		}
		if err := tenantCtx.Store.SaveTemplate(body.Name, body.Message, body.Type); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})
	api.Delete("/templates/:type/:name", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if err := tenantCtx.Store.DeleteTemplate(c.Params("name"), c.Params("type")); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "deleted"})
	})

	api.Get("/settings", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		delay, _ := strconv.Atoi(tenantCtx.Store.GetPref("delay_seconds"))
		if delay == 0 {
			delay = 3
		}
		delayMin, _ := strconv.Atoi(tenantCtx.Store.GetPref("delay_min"))
		delayMax, _ := strconv.Atoi(tenantCtx.Store.GetPref("delay_max"))
		burstEvery, _ := strconv.Atoi(tenantCtx.Store.GetPref("burst_every"))
		burstPause, _ := strconv.Atoi(tenantCtx.Store.GetPref("burst_pause"))
		randomDelay := tenantCtx.Store.GetPref("random_delay") == "true"
		return c.JSON(fiber.Map{
			"delay_seconds": delay,
			"random_delay":  randomDelay,
			"delay_min":     delayMin,
			"delay_max":     delayMax,
			"burst_every":   burstEvery,
			"burst_pause":   burstPause,
		})
	})
	api.Post("/settings", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body map[string]interface{}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		for key, val := range body {
			_ = tenantCtx.Store.SetPref(key, fmt.Sprintf("%v", val))
		}
		return c.JSON(fiber.Map{"status": "saved"})
	})

	api.Get("/ai/settings", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		settings := tenantCtx.AI.GetSettings()
		if !user.CanUseAI {
			settings.Enabled = false
		}
		products := make([]ai.ProductKnowledge, 0, len(settings.Products))
		for _, item := range settings.Products {
			item.ImageURL = aiProductImageURL(item.ImagePath)
			if len(item.ImagePaths) > 0 {
				item.ImageURLs = make([]string, 0, len(item.ImagePaths))
				for _, path := range item.ImagePaths {
					if url := aiProductImageURL(path); url != "" {
						item.ImageURLs = append(item.ImageURLs, url)
					}
				}
				if item.ImageURL == "" && len(item.ImageURLs) > 0 {
					item.ImageURL = item.ImageURLs[0]
				}
			}
			products = append(products, item)
		}
		return c.JSON(fiber.Map{
			"enabled":               settings.Enabled,
			"api_key":               settings.APIKey,
			"instruction":           settings.Instruction,
			"product_info":          settings.ProductInfo,
			"products":              products,
			"account_product_ids":   settings.AccountProductIDs,
			"delay_ms":              settings.DelayMs,
			"max_history":           settings.MaxHistory,
			"batch_window_ms":       settings.BatchWindowMs,
			"vision_enabled":        settings.VisionEnabled,
			"account_ids":           settings.AccountIDs,
			"rajaongkir_enabled":    settings.RajaOngkirEnabled,
			"rajaongkir_api_key":    settings.RajaOngkirAPIKey,
			"rajaongkir_origin":     settings.RajaOngkirOrigin,
			"system_ongkir_enabled": settings.SystemOngkirEnabled,
			"system_ongkir_origin":  settings.SystemOngkirOrigin,
			"locked":                !user.CanUseAI,
		})
	})
	api.Post("/ai/settings", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if !user.CanUseAI {
			return c.Status(403).JSON(fiber.Map{"error": "Fitur InstaBlast AI dikunci untuk user ini"})
		}
		var body ai.Settings
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		settings, err := tenantCtx.AI.Save(tenantCtx.Store, body)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if settings.Enabled {
			broadcastWSLog("AI auto-reply aktif", "info")
		} else {
			broadcastWSLog("AI auto-reply nonaktif", "info")
		}
		for i := range settings.Products {
			settings.Products[i].ImageURL = aiProductImageURL(settings.Products[i].ImagePath)
			if len(settings.Products[i].ImagePaths) > 0 {
				settings.Products[i].ImageURLs = make([]string, 0, len(settings.Products[i].ImagePaths))
				for _, path := range settings.Products[i].ImagePaths {
					if url := aiProductImageURL(path); url != "" {
						settings.Products[i].ImageURLs = append(settings.Products[i].ImageURLs, url)
					}
				}
				if settings.Products[i].ImageURL == "" && len(settings.Products[i].ImageURLs) > 0 {
					settings.Products[i].ImageURL = settings.Products[i].ImageURLs[0]
				}
			}
		}
		return c.JSON(settings)
	})
	api.Post("/ai/products/upload", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if !user.CanUseAI {
			return c.Status(403).JSON(fiber.Map{"error": "Fitur InstaBlast AI dikunci untuk user ini"})
		}
		fileHeader, err := c.FormFile("image")
		if err != nil || fileHeader == nil {
			return c.Status(400).JSON(fiber.Map{"error": "File gambar wajib diisi"})
		}
		contentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
		if contentType != "" && !strings.HasPrefix(contentType, "image/") {
			return c.Status(400).JSON(fiber.Map{"error": "File harus berupa gambar"})
		}
		productDir := filepath.Join(tenantCtx.BaseDir, "ai-products")
		if err := os.MkdirAll(productDir, os.ModePerm); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Gagal menyiapkan folder gambar"})
		}
		savedName, err := issueAIProductAssetName(fileHeader.Filename)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		dst := filepath.Join(productDir, savedName)
		if err := c.SaveFile(fileHeader, dst); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Gagal menyimpan gambar"})
		}
		if err := syncAIProductImageToMediaCenter(tenantCtx, dst, fileHeader.Filename, contentType, fileHeader.Size); err != nil {
			broadcastWSLog("Sinkron media center untuk gambar produk gagal: "+err.Error(), "warning")
		}
		return c.JSON(fiber.Map{
			"image_path": savedName,
			"image_url":  aiProductImageURL(savedName),
		})
	})
	api.Post("/ai/products/import-media", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if !user.CanUseAI {
			return c.Status(403).JSON(fiber.Map{"error": "Fitur InstaBlast AI dikunci untuk user ini"})
		}
		var body struct {
			MediaIDs []int64 `json:"media_ids"`
		}
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		if len(body.MediaIDs) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Pilih minimal satu gambar dari Media Center"})
		}
		items := make([]fiber.Map, 0, len(body.MediaIDs))
		for _, mediaID := range body.MediaIDs {
			if mediaID <= 0 {
				continue
			}
			file, err := tenantCtx.Store.GetMediaFileByID(mediaID)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.Mime)), "image/") {
				continue
			}
			savedName, err := importMediaFileToAIProductAsset(tenantCtx, file)
			if err != nil {
				continue
			}
			items = append(items, fiber.Map{
				"media_id":    file.ID,
				"image_path":  savedName,
				"image_url":   aiProductImageURL(savedName),
				"source_name": file.OriginalName,
			})
		}
		if len(items) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Tidak ada gambar Media Center yang berhasil dipakai"})
		}
		return c.JSON(fiber.Map{"items": items})
	})
	api.Get("/ai/products/image/:name", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		name := sanitizeAIProductAssetName(c.Params("name"))
		if name == "" {
			return c.Status(404).JSON(fiber.Map{"error": "Gambar tidak ditemukan"})
		}
		path := filepath.Join(tenantCtx.BaseDir, "ai-products", name)
		if _, err := os.Stat(path); err != nil {
			return c.Status(404).JSON(fiber.Map{"error": "Gambar tidak ditemukan"})
		}
		c.Set("Cache-Control", "private, max-age=300")
		return c.SendFile(path)
	})
	api.Get("/ai/stats", func(c *fiber.Ctx) error {
		_, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(tenantCtx.AI.GetStats())
	})
	api.Post("/ai/test", func(c *fiber.Ctx) error {
		user, tenantCtx, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		if !user.CanUseAI {
			return c.Status(403).JSON(fiber.Map{"error": "Fitur InstaBlast AI dikunci untuk user ini"})
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := c.BodyParser(&body); err != nil && len(c.Body()) > 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
		}
		reply, err := tenantCtx.AI.Test(context.Background(), body.Prompt)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"reply": reply})
	})

	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	app.Get("/ws", websocket.New(func(c *websocket.Conn) {
		user, _ := c.Locals("current_user").(storage.AppUser)
		wsClients[c] = user.ID
		defer func() {
			delete(wsClients, c)
			_ = c.Close()
		}()

		connected := anyAccountConnected(user.ID)
		jid := whatsapp.GetClientJIDForUser(user.ID)
		_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(
			`{"type":"status","connected":%v,"jid":"%s"}`, connected, jid,
		)))
		progress := broadcast.GetEngineForUser(user.ID).GetProgress()
		_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(
			`{"type":"progress","status":"%s","total":%d,"sent":%d,"failed":%d,"current":%d,"current_num":"%s"}`,
			progress.Status, progress.Total, progress.Sent, progress.Failed, progress.Current, progress.CurrentNum,
		)))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
	}))

	addr := config.AppHost + ":" + config.AppPort
	logrus.Infof("InstaBlast Pro running on http://%s", addr)
	fmt.Printf("\nOpen http://localhost:%s in your browser\n\n", config.AppPort)
	if err := app.Listen(addr); err != nil {
		logrus.Fatalf("Failed to start server: %v", err)
	}
}

func buildBroadcastAIHelperPrompt(body broadcastAIHelperRequest) string {
	variables := make([]string, 0, len(body.Variables))
	for _, item := range body.Variables {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		variables = append(variables, "{"+strings.Trim(item, "{}")+"}")
	}
	variableHint := "Tidak ada variabel kontak tambahan."
	if len(variables) > 0 {
		variableHint = "Variabel kontak yang wajib dipertahankan apa adanya: " + strings.Join(variables, ", ")
	}

	baseRules := `Kamu adalah copywriter WhatsApp broadcast yang paham anti-spam dan deliverability.
Bahasa wajib Indonesia natural. Jangan membuat klaim palsu, jangan hard selling berlebihan, jangan memakai terlalu banyak huruf kapital, emoji, tanda seru, atau kata pemicu spam.
Jangan merusak URL, nomor telepon, harga, nama brand, atau placeholder variabel seperti {Nama}. Placeholder variabel tidak boleh diubah menjadi spintax.
Balas hanya JSON valid tanpa markdown dan tanpa code fence.`

	if body.Mode == "spintax" {
		return fmt.Sprintf(`%s

Tugas:
Ubah pesan berikut menjadi versi spintax berat namun tetap natural untuk broadcast WhatsApp.
Gunakan format spintax {opsi satu|opsi dua|opsi tiga}.
Spin banyak bagian kalimat, sapaan, transisi, CTA, dan variasi kata, tetapi makna harus tetap sama.
Jangan menambah informasi baru.
Pertahankan format pesan aslinya. Jika pesan asli memakai enter atau paragraf terpisah, hasil akhir juga harus tetap memakai enter asli, bukan karakter literal \n.
%s

Output JSON:
{"spintax_message":"teks spintax final"}

Pesan:
%s`, baseRules, variableHint, body.Message)
	}

	return fmt.Sprintf(`%s

Tugas:
Analisa pesan broadcast WhatsApp berikut apakah berisiko dianggap spam.
Berikan pendapat singkat, tingkat risiko, dan versi teks yang lebih aman serta tetap menjual.
Teks perbaikan harus lebih natural, ramah, tidak terlalu agresif, tetap jelas CTA-nya, dan cocok untuk broadcast.
Pertahankan format pesan aslinya. Jika pesan asli memakai enter atau paragraf terpisah, hasil akhir juga harus tetap memakai enter asli, bukan karakter literal \n.
%s

Output JSON:
{"risk_level":"rendah|sedang|tinggi","analysis":"pendapat singkat dan saran praktis","improved_message":"teks perbaikan yang lebih aman"}

Pesan:
%s`, baseRules, variableHint, body.Message)
}

func callNvidiaBroadcastAssistant(ctx context.Context, apiKey, prompt string, creative bool) (string, error) {
	temperature := 0.65
	if creative {
		temperature = 1.0
	}
	payload := nvidiaChatRequest{
		Model: config.WhatsappAIModel,
		Messages: []nvidiaChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: temperature,
		TopP:        0.95,
		MaxTokens:   4096,
		Stream:      true,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		content, retry, err := doNvidiaBroadcastAssistantRequest(reqCtx, apiKey, raw)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !retry || attempt == 3 {
			break
		}
		select {
		case <-reqCtx.Done():
			return "", reqCtx.Err()
		case <-time.After(time.Duration(attempt) * 1500 * time.Millisecond):
		}
	}
	return "", lastErr
}

func doNvidiaBroadcastAssistantRequest(ctx context.Context, apiKey string, raw []byte) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.WhatsappAIEndpoint, bytes.NewReader(raw))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("gagal menghubungi NVIDIA AI: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024))
		cleanBody := strings.TrimSpace(string(body))
		retry := res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
		return "", retry, fmt.Errorf("NVIDIA AI error %d: %s", res.StatusCode, cleanNvidiaErrorBody(cleanBody))
	}
	contentType := strings.ToLower(res.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") || strings.Contains(contentType, "stream") {
		content, err := readNvidiaStreamResponse(res.Body)
		if err != nil {
			return "", true, err
		}
		return content, false, nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024))
	var parsed nvidiaChatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, fmt.Errorf("respons NVIDIA AI tidak valid: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return "", false, fmt.Errorf("NVIDIA AI: %s", strings.TrimSpace(parsed.Error.Message))
	}
	if len(parsed.Choices) == 0 {
		return "", true, fmt.Errorf("NVIDIA AI tidak mengembalikan pilihan jawaban")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return "", true, fmt.Errorf("NVIDIA AI mengembalikan jawaban kosong")
	}
	return content, false, nil
}

func readNvidiaStreamResponse(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk nvidiaChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
			return "", fmt.Errorf("NVIDIA AI: %s", strings.TrimSpace(chunk.Error.Message))
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		out.WriteString(chunk.Choices[0].Delta.Content)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("gagal membaca stream NVIDIA AI: %w", err)
	}
	content := strings.TrimSpace(out.String())
	if content == "" {
		return "", fmt.Errorf("NVIDIA AI mengembalikan stream kosong")
	}
	return content, nil
}

func cleanNvidiaErrorBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "respons kosong"
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<html") {
		if strings.Contains(lower, "bad gateway") {
			return "Bad Gateway dari NVIDIA, silakan coba lagi"
		}
		return "respons HTML dari NVIDIA, silakan coba lagi"
	}
	if len(body) > 500 {
		return body[:500] + "..."
	}
	return body
}

func parseBroadcastAIJSON(content string) map[string]string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	if start := strings.Index(content, "{"); start >= 0 {
		if end := strings.LastIndex(content, "}"); end > start {
			content = content[start : end+1]
		}
	}
	var raw map[string]interface{}
	result := map[string]string{}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return result
	}
	for key, value := range raw {
		result[key] = strings.TrimSpace(fmt.Sprint(value))
	}
	return result
}

func normalizeBroadcastAIText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.NewReplacer(
		"\r\n", "\n",
		"\r", "\n",
		`\\r\\n`, "\n",
		`\\n`, "\n",
		`\\r`, "\n",
		`\r\n`, "\n",
		`\n`, "\n",
		`\r`, "\n",
	).Replace(value)
	return strings.TrimSpace(value)
}

func ensureAtLeastOneAccount(userID string) (whatsapp.AccountInfo, error) {
	accounts := whatsapp.ListAccountsForUser(userID)
	if len(accounts) > 0 {
		if whatsapp.GetActiveAccountIDForUser(userID) == "" {
			_ = whatsapp.SetActiveAccountForUser(userID, accounts[0].ID)
		}
		return accounts[0], nil
	}
	return whatsapp.CreateAccountForUser(userID, "")
}

func anyAccountConnected(userID string) bool {
	for _, account := range whatsapp.ListAccountsForUser(userID) {
		if account.Connected && account.LoggedIn {
			return true
		}
	}
	return false
}

func anyAccountConnectedAnyUser() bool {
	users, err := Store.ListUsers()
	if err != nil {
		return false
	}
	for _, user := range users {
		if anyAccountConnected(user.ID) {
			return true
		}
	}
	return false
}

func effectiveUserMaxDevices(user storage.AppUser) int {
	if user.IsTrial {
		return 1
	}
	if user.MaxDevices <= 0 {
		return 1
	}
	return user.MaxDevices
}

func currentUser(c *fiber.Ctx) (storage.AppUser, error) {
	user, ok := c.Locals("current_user").(storage.AppUser)
	if !ok || user.ID == "" {
		return storage.AppUser{}, fmt.Errorf("unauthorized")
	}
	return user, nil
}

func currentTenant(c *fiber.Ctx) (storage.AppUser, *tenantpkg.Tenant, error) {
	user, err := currentUser(c)
	if err != nil {
		return storage.AppUser{}, nil, err
	}
	if TenantManager == nil {
		return storage.AppUser{}, nil, fmt.Errorf("tenant manager not initialized")
	}
	tenantCtx, err := TenantManager.Get(user)
	if err != nil {
		return storage.AppUser{}, nil, err
	}
	return user, tenantCtx, nil
}

func accountIDFromQuery(c *fiber.Ctx) string {
	return strings.TrimSpace(c.Query("account_id"))
}

func accountIDFromBodyOrQuery(c *fiber.Ctx) string {
	var body struct {
		AccountID string `json:"account_id"`
	}
	_ = c.BodyParser(&body)
	if strings.TrimSpace(body.AccountID) != "" {
		return strings.TrimSpace(body.AccountID)
	}
	return accountIDFromQuery(c)
}

func normalizePhone(phone string) string {
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "-", "")
	phone = strings.ReplaceAll(phone, "+", "")
	if strings.HasPrefix(phone, "08") {
		phone = "62" + phone[1:]
	}
	return phone
}

func buildBroadcastContactRows(userID, accountID string, numbers []string, submittedRows []map[string]string) []map[string]string {
	submittedByPhone := make(map[string]map[string]string, len(submittedRows))
	for _, row := range submittedRows {
		if len(row) == 0 {
			continue
		}
		phone := normalizePhone(firstMapValue(row, "nomor", "phone", "wa", "whatsapp", "no", "hp", "telepon"))
		if phone == "" {
			continue
		}
		submittedByPhone[phone] = copyStringMap(row)
	}

	contacts, _ := whatsapp.GetAllContactsForUserAccount(userID, accountID)
	verifiedNames := resolveVerifiedWANames(userID, accountID, numbers)

	result := make([]map[string]string, 0, len(numbers))
	for _, number := range numbers {
		phone := normalizePhone(number)
		if phone == "" {
			continue
		}
		row := copyStringMap(submittedByPhone[phone])
		if row == nil {
			row = map[string]string{}
		}
		row["nomor"] = phone
		row["phone"] = phone
		row["wa"] = phone
		row["whatsapp"] = phone

		waName := strings.TrimSpace(firstNonEmpty(
			resolveCachedWAName(phone, contacts),
			verifiedNames[phone],
		))
		if waName == "" {
			waName = strings.TrimSpace(firstMapValue(row, "nama", "name", "customer", "pelanggan"))
		}
		if waName == "" {
			waName = phone
		}
		row["nama_wa"] = waName
		row["wa_name"] = waName
		if strings.TrimSpace(firstMapValue(row, "nama", "name")) == "" {
			row["nama"] = waName
			row["name"] = waName
		}
		result = append(result, row)
	}
	return result
}

func saveResolvedWANamesToContactList(store *storage.Storage, contactGroupID string, resolvedRows []map[string]string) error {
	if store == nil || strings.TrimSpace(contactGroupID) == "" || len(resolvedRows) == 0 {
		return nil
	}
	groupID, err := strconv.ParseInt(strings.TrimSpace(contactGroupID), 10, 64)
	if err != nil || groupID <= 0 {
		return nil
	}
	lists, err := store.GetContactLists()
	if err != nil {
		return err
	}
	var target *storage.ContactList
	for i := range lists {
		if lists[i].ID == groupID {
			target = &lists[i]
			break
		}
	}
	if target == nil {
		return nil
	}
	var contacts []map[string]string
	_ = json.Unmarshal([]byte(target.ContactsJSON), &contacts)
	if len(contacts) == 0 {
		contacts = resolvedRows
	}
	resolvedByPhone := make(map[string]map[string]string, len(resolvedRows))
	for _, row := range resolvedRows {
		phone := normalizePhone(firstMapValue(row, "nomor", "phone", "wa", "whatsapp", "no", "hp", "telepon"))
		if phone != "" {
			resolvedByPhone[phone] = row
		}
	}
	for idx, row := range contacts {
		phone := normalizePhone(firstMapValue(row, "nomor", "phone", "wa", "whatsapp", "no", "hp", "telepon"))
		resolved := resolvedByPhone[phone]
		if resolved == nil {
			continue
		}
		if contacts[idx] == nil {
			contacts[idx] = map[string]string{}
		}
		contacts[idx]["nama_wa"] = strings.TrimSpace(resolved["nama_wa"])
		contacts[idx]["wa_name"] = strings.TrimSpace(resolved["wa_name"])
	}
	columns := []string{}
	_ = json.Unmarshal([]byte(target.ColumnsJSON), &columns)
	columns = ensureColumns(columns, "nama_wa", "wa_name")
	columnsJSON, _ := json.Marshal(columns)
	contactsJSON, _ := json.Marshal(contacts)
	return store.SaveContactList(target.Name, string(columnsJSON), string(contactsJSON), target.Numbers, target.Count)
}

func ensureColumns(columns []string, names ...string) []string {
	seen := make(map[string]bool, len(columns)+len(names))
	result := make([]string, 0, len(columns)+len(names))
	for _, col := range columns {
		col = strings.TrimSpace(col)
		if col == "" || seen[strings.ToLower(col)] {
			continue
		}
		seen[strings.ToLower(col)] = true
		result = append(result, col)
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		result = append(result, name)
	}
	return result
}

func resolveCachedWAName(phone string, contacts map[types.JID]types.ContactInfo) string {
	if len(contacts) == 0 {
		return ""
	}
	jid := types.NewJID(phone, types.DefaultUserServer)
	info, ok := contacts[jid]
	if !ok {
		for contactJID, contactInfo := range contacts {
			if normalizePhone(contactJID.User) == phone {
				info = contactInfo
				ok = true
				break
			}
		}
	}
	if !ok {
		return ""
	}
	return firstNonEmpty(info.FullName, info.BusinessName, info.PushName, info.FirstName)
}

func resolveVerifiedWANames(userID, accountID string, numbers []string) map[string]string {
	result := make(map[string]string)
	if len(numbers) == 0 {
		return result
	}
	checkNums := make([]string, 0, len(numbers))
	for _, number := range numbers {
		phone := normalizePhone(number)
		if phone != "" {
			checkNums = append(checkNums, "+"+phone)
		}
	}
	responses, err := whatsapp.IsOnWhatsAppForUserAccount(userID, accountID, checkNums)
	if err != nil {
		return result
	}
	for _, response := range responses {
		phone := normalizePhone(firstNonEmpty(response.JID.User, response.Query))
		if phone == "" || response.VerifiedName == nil || response.VerifiedName.Details == nil {
			continue
		}
		if name := strings.TrimSpace(response.VerifiedName.Details.GetVerifiedName()); name != "" {
			result[phone] = name
		}
	}
	return result
}

func firstMapValue(row map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(row[key]); value != "" {
			return value
		}
		for actualKey, value := range row {
			if strings.EqualFold(actualKey, key) && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func copyStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return output
}

func appendUnsubscribeInstruction(store *storage.Storage, message string) string {
	base := strings.TrimSpace(message)
	if store == nil {
		return base
	}
	settings, err := store.GetUnsubscribeSettings()
	if err != nil || !settings.Enabled {
		return base
	}
	instruction := strings.TrimSpace(settings.Instruction)
	if instruction == "" {
		return base
	}
	if containsNormalizedText(base, instruction) {
		return base
	}
	if base == "" {
		return formatUnsubscribeFooter(instruction)
	}
	return base + "\n\n" + formatUnsubscribeFooter(instruction)
}

func formatUnsubscribeFooter(instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return ""
	}
	return "---\n_" + strings.Trim(instruction, "_") + "_"
}

func containsNormalizedText(haystack, needle string) bool {
	haystack = normalizeTextForCompare(haystack)
	needle = normalizeTextForCompare(needle)
	return haystack != "" && needle != "" && strings.Contains(haystack, needle)
}

func normalizeTextForCompare(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("*", "", "_", "", "~", "", "`", "", "-", "", "—", "", "\r", "\n").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func loadUnsubscribePhoneSet(store *storage.Storage) (map[string]storage.UnsubscribedContact, error) {
	result := make(map[string]storage.UnsubscribedContact)
	if store == nil {
		return result, nil
	}
	items, err := store.ListUnsubscribedContacts()
	if err != nil {
		return result, err
	}
	for _, item := range items {
		phone := normalizePhone(item.Phone)
		if phone == "" {
			continue
		}
		item.Phone = phone
		result[phone] = item
	}
	return result, nil
}

func filterNumbersAgainstUnsubscribe(numbers []string, unsubscribed map[string]storage.UnsubscribedContact) ([]string, int) {
	if len(unsubscribed) == 0 {
		return numbers, 0
	}
	filtered := make([]string, 0, len(numbers))
	skipped := 0
	for _, number := range numbers {
		normalized := normalizePhone(number)
		if normalized == "" {
			continue
		}
		if _, blocked := unsubscribed[normalized]; blocked {
			skipped++
			continue
		}
		filtered = append(filtered, normalized)
	}
	return filtered, skipped
}

func extractPersonalRowPhone(row map[string]string) string {
	for key, value := range row {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "nomor", "phone", "no", "whatsapp", "wa", "hp", "telepon":
			return normalizePhone(value)
		}
	}
	return ""
}

func filterPersonalRowsAgainstUnsubscribe(rows []map[string]string, unsubscribed map[string]storage.UnsubscribedContact) ([]map[string]string, int) {
	if len(unsubscribed) == 0 {
		return rows, 0
	}
	filtered := make([]map[string]string, 0, len(rows))
	skipped := 0
	for _, row := range rows {
		phone := extractPersonalRowPhone(row)
		if phone == "" {
			continue
		}
		if _, blocked := unsubscribed[phone]; blocked {
			skipped++
			continue
		}
		clone := make(map[string]string, len(row))
		for key, value := range row {
			clone[key] = strings.TrimSpace(value)
		}
		for key := range clone {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "nomor", "phone", "no", "whatsapp", "wa", "hp", "telepon":
				clone[key] = phone
			}
		}
		filtered = append(filtered, clone)
	}
	return filtered, skipped
}

func parsePersonalCSVData(raw string) ([]string, []map[string]string, error) {
	reader := csv.NewReader(strings.NewReader(raw))
	headers, err := reader.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("Invalid CSV: no headers")
	}
	for i, h := range headers {
		headers[i] = strings.TrimSpace(strings.ToLower(h))
	}
	var data []map[string]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		row := make(map[string]string)
		for i, val := range record {
			if i < len(headers) {
				row[headers[i]] = strings.TrimSpace(val)
			}
		}
		phone := extractPersonalRowPhone(row)
		if phone != "" {
			for key := range row {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "nomor", "phone", "no", "whatsapp", "wa", "hp", "telepon":
					row[key] = phone
				}
			}
			data = append(data, row)
		}
	}
	return headers, data, nil
}

func buildPersonalCSVData(headers []string, rows []map[string]string) (string, error) {
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	if err := writer.Write(headers); err != nil {
		return "", err
	}
	for _, row := range rows {
		record := make([]string, len(headers))
		for i, header := range headers {
			record[i] = strings.TrimSpace(row[header])
		}
		if err := writer.Write(record); err != nil {
			return "", err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func parseScheduleTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("Waktu jadwal wajib diisi")
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			if ts.Before(time.Now().Add(-1 * time.Minute)) {
				return time.Time{}, fmt.Errorf("Waktu jadwal sudah lewat")
			}
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("Format waktu jadwal tidak valid")
}

func parsePhoneToJID(phone string) types.JID {
	return types.NewJID(normalizePhone(phone), types.DefaultUserServer)
}

func parseGroupJID(id string) (types.JID, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return types.JID{}, fmt.Errorf("empty group id")
	}
	if decoded, err := url.PathUnescape(id); err == nil && decoded != "" {
		id = decoded
	}
	id = strings.ReplaceAll(id, "%40", "@")
	if strings.Contains(id, "@") {
		return types.ParseJID(id)
	}
	if strings.HasSuffix(id, types.GroupServer) {
		id = strings.TrimSuffix(id, types.GroupServer)
		id = strings.TrimSuffix(id, "@")
	}
	return types.NewJID(id, types.GroupServer), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type metaConfig struct {
	AppID        string
	AppSecret    string
	ConfigID     string
	RedirectURI  string
	VerifyToken  string
	GraphVersion string
}

func metaConfigFromStore(c *fiber.Ctx) metaConfig {
	_ = c
	if Store == nil {
		return metaConfig{}
	}
	return metaConfig{
		AppID:        strings.TrimSpace(Store.GetPref(metaAppIDPrefKey)),
		AppSecret:    strings.TrimSpace(Store.GetPref(metaAppSecretPrefKey)),
		ConfigID:     strings.TrimSpace(Store.GetPref(metaConfigIDPrefKey)),
		RedirectURI:  strings.TrimSpace(Store.GetPref(metaRedirectURIPrefKey)),
		VerifyToken:  strings.TrimSpace(Store.GetPref(metaVerifyTokenPrefKey)),
		GraphVersion: metaGraphDefaultAPIVerion,
	}
}

func issueMetaSignupState(userID string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := hex.EncodeToString(buf)
	metaSignupStatesMu.Lock()
	defer metaSignupStatesMu.Unlock()
	metaSignupStates[state] = metaSignupState{
		UserID:    strings.TrimSpace(userID),
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	return state, nil
}

func consumeMetaSignupState(userID, state string) error {
	metaSignupStatesMu.Lock()
	defer metaSignupStatesMu.Unlock()
	session, ok := metaSignupStates[strings.TrimSpace(state)]
	if !ok {
		return fmt.Errorf("state signup Meta tidak valid")
	}
	delete(metaSignupStates, strings.TrimSpace(state))
	if time.Now().After(session.ExpiresAt) {
		return fmt.Errorf("state signup Meta sudah kedaluwarsa")
	}
	if session.UserID != strings.TrimSpace(userID) {
		return fmt.Errorf("state signup Meta tidak cocok dengan user login")
	}
	return nil
}

type legalSection struct {
	Title string
	Body  []string
}

type legalPageContent struct {
	Title       string
	Heading     string
	Description string
	Sections    []legalSection
}

func legalPageHTML(content legalPageContent) string {
	var sections strings.Builder
	for _, section := range content.Sections {
		sections.WriteString("<section class=\"legal-section\">")
		sections.WriteString("<h2>" + htmlEscape(section.Title) + "</h2>")
		for _, paragraph := range section.Body {
			sections.WriteString("<p>" + htmlEscape(paragraph) + "</p>")
		}
		sections.WriteString("</section>")
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="id">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f3faf7;
      --card: #ffffff;
      --text: #14221c;
      --muted: #5e7167;
      --line: rgba(0, 168, 132, 0.16);
      --accent: #00a884;
      --accent-soft: rgba(0, 168, 132, 0.08);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: Arial, Helvetica, sans-serif;
      background:
        radial-gradient(circle at top left, rgba(37, 211, 102, 0.12), transparent 34%%),
        linear-gradient(180deg, #f7fbfa 0%%, var(--bg) 100%%);
      color: var(--text);
      line-height: 1.65;
    }
    .wrap {
      width: min(900px, calc(100%% - 32px));
      margin: 40px auto;
      background: rgba(255,255,255,0.95);
      border: 1px solid var(--line);
      border-radius: 22px;
      box-shadow: 0 24px 80px rgba(17, 27, 33, 0.08);
      overflow: hidden;
    }
    .hero {
      padding: 28px 28px 18px;
      background: linear-gradient(135deg, rgba(0,168,132,0.12), rgba(255,255,255,0.95));
      border-bottom: 1px solid var(--line);
    }
    .eyebrow {
      display: inline-block;
      padding: 6px 10px;
      border-radius: 999px;
      background: var(--accent-soft);
      color: var(--accent);
      font-size: 12px;
      font-weight: 700;
      letter-spacing: 0.04em;
      text-transform: uppercase;
    }
    h1 {
      margin: 16px 0 10px;
      font-size: clamp(28px, 4vw, 40px);
      line-height: 1.08;
    }
    .desc {
      margin: 0;
      max-width: 60ch;
      color: var(--muted);
      font-size: 16px;
    }
    .body {
      padding: 10px 28px 28px;
    }
    .legal-section {
      padding: 22px 0;
      border-bottom: 1px solid rgba(20, 34, 28, 0.08);
    }
    .legal-section:last-child { border-bottom: none; }
    h2 {
      margin: 0 0 12px;
      font-size: 21px;
    }
    p {
      margin: 0 0 10px;
      color: var(--text);
    }
    .footer {
      margin-top: 8px;
      padding-top: 20px;
      border-top: 1px dashed rgba(20, 34, 28, 0.14);
      color: var(--muted);
      font-size: 14px;
    }
  </style>
</head>
<body>
  <main class="wrap">
    <header class="hero">
      <div class="eyebrow">InstaBlast Pro</div>
      <h1>%s</h1>
      <p class="desc">%s</p>
    </header>
    <div class="body">
      %s
      <div class="footer">
        Halaman ini disediakan untuk keperluan kepatuhan integrasi Meta, Facebook Login for Business, dan WhatsApp Business.
      </div>
    </div>
  </main>
</body>
</html>`,
		htmlEscape(content.Title),
		htmlEscape(content.Heading),
		htmlEscape(content.Description),
		sections.String(),
	)
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

type metaSignedRequestPayload struct {
	Algorithm string `json:"algorithm"`
	IssuedAt  int64  `json:"issued_at"`
	UserID    string `json:"user_id"`
}

func parseMetaSignedRequest(signedRequest, appSecret string) (metaSignedRequestPayload, error) {
	var payload metaSignedRequestPayload

	parts := strings.SplitN(strings.TrimSpace(signedRequest), ".", 2)
	if len(parts) != 2 {
		return payload, fmt.Errorf("format signed_request tidak valid")
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return payload, fmt.Errorf("signature signed_request tidak valid")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return payload, fmt.Errorf("payload signed_request tidak valid")
	}

	if secret := strings.TrimSpace(appSecret); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		if _, err := mac.Write([]byte(parts[1])); err != nil {
			return payload, fmt.Errorf("gagal memverifikasi signed_request")
		}
		expected := mac.Sum(nil)
		if !hmac.Equal(signature, expected) {
			return payload, fmt.Errorf("signature signed_request tidak cocok")
		}
	}

	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return payload, fmt.Errorf("payload signed_request tidak dapat dibaca")
	}

	if payload.Algorithm != "" && !strings.EqualFold(payload.Algorithm, "HMAC-SHA256") {
		return payload, fmt.Errorf("algoritma signed_request tidak didukung")
	}

	return payload, nil
}

func issueMetaDeletionConfirmationCode() (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("gagal membuat kode konfirmasi")
	}
	return hex.EncodeToString(token[:]), nil
}

func sanitizeAIProductAssetName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == "/" || name == `\` {
		return ""
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return ""
	}
	return name
}

func issueAIProductAssetName(original string) (string, error) {
	var token [10]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("gagal membuat nama gambar")
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(original)))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
	default:
		ext = ".jpg"
	}
	return "product-" + hex.EncodeToString(token[:]) + ext, nil
}

func aiProductImageURL(imagePath string) string {
	imagePath = sanitizeAIProductAssetName(imagePath)
	if imagePath == "" {
		return ""
	}
	return "/api/ai/products/image/" + url.PathEscape(imagePath)
}

func sanitizeManagedAssetName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == "/" || name == `\` {
		return ""
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return ""
	}
	return name
}

func issueManagedFileName(original string) (string, error) {
	var token [10]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("gagal membuat nama file")
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(original)))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".pdf", ".mp4", ".mp3", ".ogg", ".wav":
	default:
		ext = ".bin"
	}
	return "media-" + hex.EncodeToString(token[:]) + ext, nil
}

func syncAIProductImageToMediaCenter(tenantCtx *tenantpkg.Tenant, sourcePath, originalName, mimeType string, size int64) error {
	if tenantCtx == nil || tenantCtx.Store == nil {
		return fmt.Errorf("tenant tidak tersedia")
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("source path kosong")
	}
	managedName, err := issueManagedFileName(originalName)
	if err != nil {
		return err
	}
	mediaDir := filepath.Join(tenantCtx.BaseDir, "media-manager")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(mediaDir, managedName)
	srcFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = dstFile.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := dstFile.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	rec := storage.MediaFile{
		Name:         managedName,
		OriginalName: strings.TrimSpace(originalName),
		Mime:         strings.TrimSpace(mimeType),
		Size:         size,
		Path:         dst,
	}
	if rec.Mime == "" {
		rec.Mime = "image/jpeg"
	}
	if err := tenantCtx.Store.SaveMediaFile(rec); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

func importMediaFileToAIProductAsset(tenantCtx *tenantpkg.Tenant, file storage.MediaFile) (string, error) {
	if tenantCtx == nil {
		return "", fmt.Errorf("tenant tidak tersedia")
	}
	sourcePath := strings.TrimSpace(file.Path)
	if sourcePath == "" {
		return "", fmt.Errorf("path media kosong")
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.Mime)), "image/") {
		return "", fmt.Errorf("media bukan gambar")
	}
	savedName, err := issueAIProductAssetName(file.OriginalName)
	if err != nil {
		return "", err
	}
	productDir := filepath.Join(tenantCtx.BaseDir, "ai-products")
	if err := os.MkdirAll(productDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(productDir, savedName)
	srcFile, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = dstFile.Close()
		_ = os.Remove(dst)
		return "", err
	}
	if err := dstFile.Close(); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return savedName, nil
}

func mediaFileURL(name string) string {
	name = sanitizeManagedAssetName(name)
	if name == "" {
		return ""
	}
	return "/api/media/files/" + url.PathEscape(name)
}

var _ = whatsmeow.MediaImage
