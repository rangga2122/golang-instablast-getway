package cmd

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
			return nil, nil, fmt.Errorf("invalid image data")
		}
		mimeType := strings.TrimSpace(item.Mime)
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		decoded = append(decoded, broadcast.MediaItem{
			Data: imgData,
			Mime: mimeType,
		})
		sanitized = append(sanitized, imagePayload{
			Data: raw,
			Mime: mimeType,
			Name: strings.TrimSpace(item.Name),
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

	app.Use("/assets", filesystem.New(filesystem.Config{
		Root:       http.FS(EmbedViews),
		PathPrefix: "views/assets",
		Browse:     false,
	}))

	app.Use(func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/assets") || path == "/icon.ico" || path == "/login" || path == "/health" || path == "/health/whatsapp" || path == "/privacy-policy" || path == "/terms-of-service" || path == "/data-deletion" || path == "/data-deletion-status" || path == "/api/auth/login" || path == "/api/meta/signup/callback" || path == "/api/meta/webhook" || path == "/api/meta/data-deletion" {
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
		data, err := EmbedViews.ReadFile("views/index.html")
		if err != nil {
			return c.Status(500).SendString("Failed to load UI")
		}
		c.Set("Content-Type", "text/html")
		return c.Send(data)
	})
	app.Get("/login", func(c *fiber.Ctx) error {
		if AuthService != nil {
			if _, _, err := AuthService.GetUserByToken(c.Cookies(auth.SessionCookieName)); err == nil {
				return c.Redirect("/")
			}
		}
		data, err := EmbedViews.ReadFile("views/login.html")
		if err != nil {
			return c.Status(500).SendString("Failed to load login UI")
		}
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
		return c.JSON(fiber.Map{
			"status":                "ok",
			"service":               "wa-gateway",
			"connected_any_account": anyAccountConnectedAnyUser(),
			"active_account_id":     "",
		})
	})

	app.Get("/health/whatsapp", func(c *fiber.Ctx) error {
		if anyAccountConnectedAnyUser() {
			return c.JSON(fiber.Map{"status": "ok", "connected": true})
		}
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"status": "not_connected", "connected": false})
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
				"can_use_ai":  user.CanUseAI,
				"max_devices": user.MaxDevices,
				"expires_at":  user.ExpiresAt,
			},
		})
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
				"can_use_ai":  user.CanUseAI,
				"max_devices": user.MaxDevices,
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
			CanUseAI:   body.CanUseAI,
			MaxDevices: body.MaxDevices,
			ExpiresAt:  time.Now().Add(time.Duration(body.ActiveDays) * 24 * time.Hour),
		})
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"user": created})
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
		if len(whatsapp.ListAccountsForUser(user.ID)) >= user.MaxDevices {
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
		return c.JSON(fiber.Map{"status": "ok", "active_account_id": whatsapp.GetActiveAccountIDForUser(user.ID)})
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
		return c.JSON(fiber.Map{
			"connected":         account.Connected && account.LoggedIn,
			"jid":               account.Phone,
			"account_id":        account.ID,
			"active_account_id": whatsapp.GetActiveAccountIDForUser(user.ID),
			"accounts":          whatsapp.ListAccountsForUser(user.ID),
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

		qrChan, err := client.GetQRChannel(context.Background())
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := client.Connect(); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		for evt := range qrChan {
			switch evt.Event {
			case "code":
				png, err := qrcode.Encode(evt.Code, qrcode.Medium, 512)
				if err != nil {
					return c.Status(500).JSON(fiber.Map{"error": "Failed to generate QR"})
				}
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
		}
		return c.JSON(fiber.Map{"status": "timeout", "account_id": accountID})
	})

	api.Post("/reconnect", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		accountID := accountIDFromBodyOrQuery(c)
		if err := whatsapp.ReconnectForAccountForUser(context.Background(), user.ID, accountID); err != nil {
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
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, body.AccountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
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
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, body.AccountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
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
		user, _, err := currentTenant(c)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			AccountID    string         `json:"account_id"`
			Numbers      string         `json:"numbers"`
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
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, body.AccountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
		}
		nums := broadcast.ParseNumbers(body.Numbers)
		if len(nums) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "No valid numbers"})
		}

		account, _ := whatsapp.GetAccountForUser(user.ID, body.AccountID)
		cfg := broadcast.Config{
			OwnerID:      user.ID,
			AccountID:    body.AccountID,
			AccountName:  account.Name,
			Numbers:      nums,
			Message:      body.Message,
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
		return c.JSON(fiber.Map{"status": "started", "total": len(nums)})
	})

	api.Post("/broadcast/personal", func(c *fiber.Ctx) error {
		user, _, err := currentTenant(c)
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
		if !whatsapp.IsClientConnectedForAccountForUser(user.ID, body.AccountID) {
			return c.Status(503).JSON(fiber.Map{"error": "WhatsApp not connected"})
		}

		reader := csv.NewReader(strings.NewReader(body.CSVData))
		headers, err := reader.Read()
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid CSV: no headers"})
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
			if num, ok := row["nomor"]; ok {
				row["nomor"] = normalizePhone(num)
			}
			if row["nomor"] != "" {
				data = append(data, row)
			}
		}
		if len(data) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "No valid data in CSV"})
		}

		account, _ := whatsapp.GetAccountForUser(user.ID, body.AccountID)
		cfg := broadcast.PersonalConfig{
			OwnerID:      user.ID,
			AccountID:    body.AccountID,
			AccountName:  account.Name,
			Data:         data,
			Message:      body.Message,
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
		return c.JSON(fiber.Map{"status": "started", "total": len(data), "columns": headers})
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
			Message:      body.Message,
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
		return c.JSON(fiber.Map{
			"enabled":            settings.Enabled,
			"api_key":            settings.APIKey,
			"instruction":        settings.Instruction,
			"product_info":       settings.ProductInfo,
			"delay_ms":           settings.DelayMs,
			"max_history":        settings.MaxHistory,
			"batch_window_ms":    settings.BatchWindowMs,
			"vision_enabled":     settings.VisionEnabled,
			"account_ids":        settings.AccountIDs,
			"rajaongkir_enabled": settings.RajaOngkirEnabled,
			"rajaongkir_api_key": settings.RajaOngkirAPIKey,
			"rajaongkir_origin":  settings.RajaOngkirOrigin,
			"locked":             !user.CanUseAI,
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
		return c.JSON(settings)
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

var _ = whatsmeow.MediaImage
