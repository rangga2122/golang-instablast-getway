package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/azkazamdigital/wa-gateway/config"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	SettingsPrefKey      = "ai_settings"
	ProviderDefault      = "default"
	ProviderLocalHF      = "local_hf"
	localHFEndpoint      = "https://digital423-local-llm.hf.space/v1/chat/completions"
	localHFModel         = "qwen2.5-0.5b-instruct-q4_k_m.gguf"
	localHFApiKey        = "dummy"
	defaultDelayMs       = 10000
	defaultMaxHistory    = 15
	defaultBatchWindowMs = 4500
	visionRequestTimeout = 90 * time.Second
	maxHistoryLimit      = 50
	maxTrackedChats      = 500
	desktopAPIKey        = "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"
	visionModel          = "nvidia/nemotron-nano-12b-v2-vl"
	systemOngkirBaseURL  = "https://app.maukirim.id"
	systemOngkirAreasURL = systemOngkirBaseURL + "/json/kecamatan001.json"
	systemOngkirRatesURL = systemOngkirBaseURL + "/cek-ongkir/ekspedisi"
)

var (
	reBold                = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic1             = regexp.MustCompile(`__(.+?)__`)
	reItalic2             = regexp.MustCompile(`_(.+?)_`)
	reHeading             = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reCodeBlock           = regexp.MustCompile("```[\\s\\S]*?```")
	reInlineCode          = regexp.MustCompile("`(.+?)`")
	reManyNL              = regexp.MustCompile(`\n{3,}`)
	globalAPIKeyProvider  func() string
	systemOngkirAreaCache struct {
		mu        sync.Mutex
		expiresAt time.Time
		rows      []systemOngkirArea
	}
)

type PreferenceStore interface {
	GetPrefJSON(key string, target interface{}) error
	SetPrefJSON(key string, value interface{}) error
}

type LoggerFunc func(msg, level string)

type Settings struct {
	Enabled             bool                `json:"enabled"`
	Provider            string              `json:"provider"`
	APIKey              string              `json:"api_key"`
	Instruction         string              `json:"instruction"`
	ProductInfo         string              `json:"product_info"`
	Products            []ProductKnowledge  `json:"products"`
	AccountProductIDs   map[string][]string `json:"account_product_ids"`
	DelayMs             int                 `json:"delay_ms"`
	MaxHistory          int                 `json:"max_history"`
	BatchWindowMs       int                 `json:"batch_window_ms"`
	VisionEnabled       bool                `json:"vision_enabled"`
	AccountIDs          []string            `json:"account_ids"`
	RajaOngkirEnabled   bool                `json:"rajaongkir_enabled"`
	RajaOngkirAPIKey    string              `json:"rajaongkir_api_key"`
	RajaOngkirOrigin    string              `json:"rajaongkir_origin"`
	SystemOngkirEnabled bool                `json:"system_ongkir_enabled"`
	SystemOngkirOrigin  string              `json:"system_ongkir_origin"`
}

type ProductKnowledge struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Content    string   `json:"content"`
	ImagePath  string   `json:"image_path,omitempty"`
	ImageURL   string   `json:"image_url,omitempty"`
	ImagePaths []string `json:"image_paths,omitempty"`
	ImageURLs  []string `json:"image_urls,omitempty"`
}

type Stats struct {
	Received      int       `json:"received"`
	Replied       int       `json:"replied"`
	Failed        int       `json:"failed"`
	LastMessageAt time.Time `json:"last_message_at"`
	LastReplyAt   time.Time `json:"last_reply_at"`
	LastError     string    `json:"last_error"`
}

type chatTurn struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	TS      time.Time `json:"ts"`
}

type pendingChat struct {
	AccountID  string
	ChatJID    types.JID
	SenderJID  types.JID
	MessageIDs []types.MessageID
	Parts      []pendingPart
	Timer      *time.Timer
}

type pendingPart struct {
	Text      string
	Caption   string
	ImageData []byte
	MimeType  string
	HasImage  bool
}

type incomingPayload struct {
	Text         string
	Caption      string
	ImageMessage *waE2E.ImageMessage
}

type desktopSettings struct {
	Instruction string `json:"instruction"`
	Product     string `json:"product"`
	DelayMs     int    `json:"delayMs"`
	APIKey      string `json:"apiKey"`
}

type rajaOngkirDestination struct {
	ID              int    `json:"id"`
	Label           string `json:"label"`
	ProvinceName    string `json:"province_name"`
	CityName        string `json:"city_name"`
	DistrictName    string `json:"district_name"`
	SubdistrictName string `json:"subdistrict_name"`
	ZipCode         string `json:"zip_code"`
}

type rajaOngkirDestinationResponse struct {
	Meta struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
		Status  string `json:"status"`
	} `json:"meta"`
	Data []rajaOngkirDestination `json:"data"`
}

type rajaOngkirCost struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Service     string `json:"service"`
	Description string `json:"description"`
	Cost        int    `json:"cost"`
	ETD         string `json:"etd"`
}

type rajaOngkirCostResponse struct {
	Meta struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
		Status  string `json:"status"`
	} `json:"meta"`
	Data []rajaOngkirCost `json:"data"`
}

type systemOngkirArea struct {
	ID          string
	Province    string
	Regency     string
	SubDistrict string
	Label       string
	SearchText  string
}

type systemOngkirRate struct {
	CourierName         string
	ServiceName         string
	ServiceCode         string
	LogoURL             string
	DiscountedCost      int
	DiscountedCostLabel string
	OriginalCost        int
	OriginalCostLabel   string
	HasDiscount         bool
	CashbackText        string
}

type Service struct {
	mu         sync.Mutex
	settings   Settings
	stats      Stats
	histories  map[string][]chatTurn
	pending    map[string]*pendingChat
	assetDir   string
	httpClient *http.Client
	logger     LoggerFunc
	seen       map[string]time.Time
}

func NewService(logger LoggerFunc) *Service {
	return &Service{
		settings:  defaultSettings(),
		histories: make(map[string][]chatTurn),
		pending:   make(map[string]*pendingChat),
		httpClient: &http.Client{
			Timeout: time.Duration(config.WhatsappAIRequestTimeoutSec) * time.Second,
		},
		logger: logger,
		seen:   make(map[string]time.Time),
	}
}

func SetGlobalAPIKeyProvider(fn func() string) {
	globalAPIKeyProvider = fn
}

func (s *Service) SetAssetDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assetDir = strings.TrimSpace(dir)
}

func defaultSettings() Settings {
	return mergeDesktopDefaults(Settings{
		Enabled:           false,
		Provider:          ProviderDefault,
		DelayMs:           defaultDelayMs,
		MaxHistory:        defaultMaxHistory,
		BatchWindowMs:     defaultBatchWindowMs,
		VisionEnabled:     true,
		AccountIDs:        []string{},
		Products:          []ProductKnowledge{},
		AccountProductIDs: map[string][]string{},
	})
}

func sanitizeSettings(s Settings) Settings {
	s.Provider = normalizeProvider(s.Provider)
	if s.Provider == ProviderLocalHF {
		s.APIKey = strings.TrimSpace(s.APIKey)
	} else {
		s.APIKey = normalizeAPIKey(s.APIKey)
	}
	s.Instruction = strings.TrimSpace(s.Instruction)
	s.ProductInfo = strings.TrimSpace(s.ProductInfo)
	s.RajaOngkirAPIKey = strings.TrimSpace(s.RajaOngkirAPIKey)
	s.RajaOngkirOrigin = strings.TrimSpace(s.RajaOngkirOrigin)
	s.SystemOngkirOrigin = strings.TrimSpace(s.SystemOngkirOrigin)
	s.Products = sanitizeProducts(s.Products)

	if s.DelayMs < 0 {
		s.DelayMs = 0
	}
	if s.MaxHistory <= 0 {
		s.MaxHistory = defaultMaxHistory
	}
	if s.MaxHistory > maxHistoryLimit {
		s.MaxHistory = maxHistoryLimit
	}
	if s.BatchWindowMs <= 0 {
		s.BatchWindowMs = defaultBatchWindowMs
	}
	if s.BatchWindowMs > 30000 {
		s.BatchWindowMs = 30000
	}
	if len(s.AccountIDs) > 0 {
		seen := make(map[string]struct{}, len(s.AccountIDs))
		filtered := make([]string, 0, len(s.AccountIDs))
		for _, id := range s.AccountIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			filtered = append(filtered, id)
		}
		s.AccountIDs = filtered
	}
	s.AccountProductIDs = sanitizeAccountProductMap(s.AccountProductIDs, s.AccountIDs, s.Products)
	return mergeDesktopDefaults(s)
}

func (s *Service) Load(store PreferenceStore) error {
	if store == nil {
		return nil
	}

	settings := defaultSettings()
	if err := store.GetPrefJSON(SettingsPrefKey, &settings); err != nil {
		return err
	}

	s.mu.Lock()
	s.settings = sanitizeSettings(settings)
	s.mu.Unlock()
	return nil
}

func (s *Service) Save(store PreferenceStore, settings Settings) (Settings, error) {
	settings = sanitizeSettings(settings)
	if settings.Enabled && len(settings.AccountIDs) == 0 {
		return settings, fmt.Errorf("Pilih minimal satu akun WhatsApp untuk InstaBlast AI")
	}
	if settings.SystemOngkirEnabled {
		settings.RajaOngkirEnabled = false
	}
	if settings.RajaOngkirEnabled {
		if settings.RajaOngkirAPIKey == "" {
			return settings, fmt.Errorf("API key RajaOngkir wajib diisi saat integrasi diaktifkan")
		}
		if settings.RajaOngkirOrigin == "" {
			return settings, fmt.Errorf("Lokasi origin RajaOngkir wajib diisi saat integrasi diaktifkan")
		}
	}
	if settings.SystemOngkirEnabled && settings.SystemOngkirOrigin == "" {
		return settings, fmt.Errorf("Lokasi origin Perhitungan Ongkir Sistem wajib diisi saat fitur diaktifkan")
	}
	if store != nil {
		if err := store.SetPrefJSON(SettingsPrefKey, settings); err != nil {
			return settings, err
		}
	}
	s.mu.Lock()
	s.resetConversationStateLocked()
	s.settings = settings
	s.mu.Unlock()
	return settings, nil
}

func (s *Service) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Service) GetStats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Service) Test(ctx context.Context, prompt string) (string, error) {
	settings := s.GetSettings()
	if prompt == "" {
		prompt = "Halo, balas singkat dalam 1 kalimat bahasa Indonesia."
	}
	reply, err := s.generateReply(ctx, settings, "", nil, prompt)
	if err != nil {
		return "", err
	}
	return cleanReply(reply), nil
}

func (s *Service) HandleEvent(scope string, evt *events.Message, client *whatsmeow.Client) {
	if evt == nil || client == nil {
		return
	}

	settings := s.GetSettings()
	if !settings.Enabled {
		return
	}
	if !accountAllowed(scope, settings.AccountIDs) {
		return
	}

	payload, ok := extractIncomingPayload(evt.Message)
	if !ok {
		return
	}

	if shouldSkipEvent(evt) {
		return
	}

	part := pendingPart{
		Text:     payload.Text,
		Caption:  payload.Caption,
		HasImage: payload.ImageMessage != nil,
	}
	if payload.ImageMessage != nil {
		part.MimeType = strings.TrimSpace(payload.ImageMessage.GetMimetype())
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		imageData, err := client.Download(ctx, payload.ImageMessage)
		cancel()
		if err != nil {
			s.log(fmt.Sprintf("AI gagal download gambar dari %s: %v", compactChatID(evt.Info.Chat.String()), err), "warning")
		} else if len(imageData) > 0 {
			part.ImageData = imageData
			s.log(fmt.Sprintf("AI berhasil download gambar dari %s (%d KB)", compactChatID(evt.Info.Chat.String()), len(imageData)/1024), "info")
		}
	}
	if strings.TrimSpace(part.Text) == "" && len(part.ImageData) == 0 {
		return
	}

	chatID := scopedChatID(scope, evt.Info.Chat.String())
	messageKey := chatID + ":" + string(evt.Info.ID)

	s.mu.Lock()
	s.cleanupSeenLocked()
	if _, exists := s.seen[messageKey]; exists {
		s.mu.Unlock()
		return
	}
	s.seen[messageKey] = time.Now()

	entry := s.pending[chatID]
	if entry == nil {
		entry = &pendingChat{
			AccountID: scope,
			ChatJID:   evt.Info.Chat,
			SenderJID: evt.Info.Sender,
		}
		s.pending[chatID] = entry
	}
	entry.AccountID = scope
	entry.ChatJID = evt.Info.Chat
	entry.SenderJID = evt.Info.Sender
	entry.MessageIDs = append(entry.MessageIDs, evt.Info.ID)
	entry.Parts = append(entry.Parts, part)
	if entry.Timer != nil {
		entry.Timer.Stop()
	}

	window := time.Duration(settings.BatchWindowMs) * time.Millisecond
	entry.Timer = time.AfterFunc(window, func() {
		s.processPendingChat(chatID, client)
	})

	s.stats.Received++
	s.stats.LastMessageAt = time.Now()
	s.mu.Unlock()

	s.log(fmt.Sprintf("AI menerima pesan dari %s", compactChatID(chatID)), "info")
}

func (s *Service) processPendingChat(chatID string, client *whatsmeow.Client) {
	if client == nil {
		return
	}

	settings := s.GetSettings()
	if !settings.Enabled {
		return
	}

	s.mu.Lock()
	entry := s.pending[chatID]
	if entry == nil {
		s.mu.Unlock()
		return
	}
	delete(s.pending, chatID)

	chatJID := entry.ChatJID
	senderJID := entry.SenderJID
	accountID := entry.AccountID
	messageIDs := append([]types.MessageID(nil), entry.MessageIDs...)
	parts := append([]pendingPart(nil), entry.Parts...)
	history := append([]chatTurn(nil), s.histories[chatID]...)
	s.mu.Unlock()

	if len(parts) == 0 {
		return
	}

	if err := s.markRead(client, chatJID, senderJID, messageIDs); err != nil {
		s.log(fmt.Sprintf("AI gagal mark read %s: %v", compactChatID(chatID), err), "warning")
	}

	var userSegments []string
	var memorySegments []string
	for _, part := range parts {
		userSegment, memorySegment := s.prepareUserSegment(context.Background(), settings, part)
		if strings.TrimSpace(userSegment) == "" {
			continue
		}
		userSegments = append(userSegments, userSegment)
		if strings.TrimSpace(memorySegment) != "" {
			memorySegments = append(memorySegments, memorySegment)
		} else {
			memorySegments = append(memorySegments, userSegment)
		}
	}
	if len(userSegments) == 0 {
		return
	}

	userText := strings.Join(userSegments, "\n")
	if len(userSegments) > 1 {
		userText = "Ringkas dan jawab gabungan beberapa pesan berikut:\n- " + strings.Join(userSegments, "\n- ")
	}
	memoryText := strings.Join(memorySegments, "\n")
	if len(memorySegments) > 1 {
		memoryText = "Gabungan pesan user:\n- " + strings.Join(memorySegments, "\n- ")
	}

	s.remember(chatID, "user", memoryText, settings.MaxHistory)

	if strings.TrimSpace(userText) != "" {
		if directReply, provider, handled, directErr := s.tryAnswerShipping(context.Background(), settings, history, userText); handled {
			if directErr != nil {
				s.log(fmt.Sprintf("Integrasi %s gagal untuk %s: %v", provider, compactChatID(chatID), directErr), "warning")
			}
			cleaned := cleanReply(directReply)
			if cleaned == "" {
				cleaned = "Maaf, saya belum bisa mengambil data ongkir saat ini."
			}
			if err := s.sendReply(client, chatJID, cleaned, settings.DelayMs); err != nil {
				s.setFailure(err)
				s.log(fmt.Sprintf("AI gagal mengirim balasan %s ke %s: %v", provider, compactChatID(chatID), err), "error")
				return
			}
			s.remember(chatID, "assistant", cleaned, settings.MaxHistory)
			s.mu.Lock()
			s.stats.Replied++
			s.stats.LastReplyAt = time.Now()
			s.stats.LastError = ""
			s.mu.Unlock()
			s.log(fmt.Sprintf("AI membalas via %s ke %s", provider, compactChatID(chatID)), "success")
			return
		}
	}

	reply, err := s.generateReply(context.Background(), settings, accountID, history, userText)
	if err != nil {
		s.setFailure(err)
		s.log(fmt.Sprintf("AI gagal membuat balasan ke %s: %v", compactChatID(chatID), err), "error")
		return
	}

	cleaned := cleanReply(reply)
	if cleaned == "" {
		fallback := buildVisionFallbackReply(userText)
		if fallback != "" {
			cleaned = fallback
			s.log(fmt.Sprintf("AI menghasilkan respons kosong untuk %s, fallback vision dipakai", compactChatID(chatID)), "warning")
		} else {
			s.setFailure(fmt.Errorf("respons AI kosong"))
			s.log(fmt.Sprintf("AI menghasilkan respons kosong untuk %s", compactChatID(chatID)), "warning")
			return
		}
	}

	if err := s.sendReply(client, chatJID, cleaned, settings.DelayMs); err != nil {
		s.setFailure(err)
		s.log(fmt.Sprintf("AI gagal mengirim balasan ke %s: %v", compactChatID(chatID), err), "error")
		return
	}
	if product := pickProductForImageRequest(userText, history, selectProductsForAccount(settings, accountID)); product != nil {
		if err := s.sendProductReferenceImages(client, chatJID, *product, settings.DelayMs); err != nil {
			s.log(fmt.Sprintf("AI gagal mengirim gambar referensi ke %s: %v", compactChatID(chatID), err), "warning")
		}
	}

	s.remember(chatID, "assistant", cleaned, settings.MaxHistory)

	s.mu.Lock()
	s.stats.Replied++
	s.stats.LastReplyAt = time.Now()
	s.stats.LastError = ""
	s.mu.Unlock()

	s.log(fmt.Sprintf("AI membalas ke %s", compactChatID(chatID)), "success")
}

func (s *Service) markRead(client *whatsmeow.Client, chatJID, senderJID types.JID, ids []types.MessageID) error {
	if client == nil || len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return client.MarkRead(ctx, ids, time.Now(), chatJID, senderJID)
}

func (s *Service) sendReply(client *whatsmeow.Client, chatJID types.JID, reply string, delayMs int) error {
	parts := splitReply(reply)
	if len(parts) == 0 {
		return fmt.Errorf("reply kosong")
	}

	for idx, part := range parts {
		if part == "" {
			continue
		}

		typingMs := typingDelayFor(part, delayMs, idx == 0)
		if typingMs > 0 {
			ctxTyping, cancelTyping := context.WithTimeout(context.Background(), 10*time.Second)
			_ = client.SendChatPresence(ctxTyping, chatJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
			cancelTyping()
			time.Sleep(time.Duration(typingMs) * time.Millisecond)
		}

		ctxSend, cancelSend := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := client.SendMessage(ctxSend, chatJID, &waProto.Message{
			Conversation: proto.String(part),
		})
		cancelSend()

		ctxPause, cancelPause := context.WithTimeout(context.Background(), 10*time.Second)
		_ = client.SendChatPresence(ctxPause, chatJID, types.ChatPresencePaused, types.ChatPresenceMediaText)
		cancelPause()

		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) sendProductReferenceImages(client *whatsmeow.Client, chatJID types.JID, product ProductKnowledge, delayMs int) error {
	if client == nil {
		return fmt.Errorf("client nil")
	}
	imagePaths := productReferenceImagePaths(product)
	if len(imagePaths) == 0 {
		return fmt.Errorf("produk tidak memiliki gambar referensi")
	}

	baseDir := strings.TrimSpace(s.assetDir)
	if baseDir == "" {
		return fmt.Errorf("asset dir belum tersedia")
	}

	sent := 0
	for idx, imageName := range imagePaths {
		path := filepath.Join(baseDir, sanitizeProductAssetLocalName(imageName))
		imageBytes, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		mimeType := http.DetectContentType(imageBytes)
		caption := ""
		if idx == 0 {
			caption = fmt.Sprintf("Berikut gambar referensi untuk *%s* ya.", strings.TrimSpace(product.Name))
		}
		if delayMs > 0 {
			time.Sleep(time.Duration(min(delayMs, 2500)) * time.Millisecond)
		}
		ctxSend, cancelSend := context.WithTimeout(context.Background(), 30*time.Second)
		uploaded, err := client.Upload(ctxSend, imageBytes, whatsmeow.MediaImage)
		cancelSend()
		if err != nil {
			continue
		}
		msg := &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				Caption:       proto.String(caption),
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mimeType),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
			},
		}
		ctxMessage, cancelMessage := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = client.SendMessage(ctxMessage, chatJID, msg)
		cancelMessage()
		if err != nil {
			continue
		}
		sent++
		if sent >= 5 {
			break
		}
	}

	if sent == 0 {
		return fmt.Errorf("tidak ada gambar referensi yang berhasil dikirim")
	}
	s.log(fmt.Sprintf("AI mengirim %d gambar referensi produk %s", sent, strings.TrimSpace(product.Name)), "success")
	return nil
}

func sanitizeProductAssetLocalName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == "/" || name == `\` {
		return ""
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return ""
	}
	return name
}

func (s *Service) prepareUserSegment(ctx context.Context, settings Settings, part pendingPart) (string, string) {
	text := strings.TrimSpace(part.Text)
	if !part.HasImage {
		return text, text
	}

	caption := strings.TrimSpace(part.Caption)
	if !settings.VisionEnabled {
		if text != "" {
			return text, text
		}
		return "User mengirim gambar tanpa teks. Tanyakan apa yang ingin mereka sampaikan.", "[Gambar]"
	}

	if len(part.ImageData) > 0 {
		s.log("AI Vision sedang membaca gambar...", "info")
		visionText, method, err := s.extractImageInsight(ctx, settings, part.ImageData, part.MimeType, caption)
		if err == nil && len(strings.TrimSpace(visionText)) > 5 {
			s.log(fmt.Sprintf("AI Vision berhasil lewat %s: %s", method, truncateSingleLine(visionText, 80)), "success")
			contextPrefix := ""
			if caption != "" {
				contextPrefix = "Caption user: " + caption + "\n\n"
			}
			userContent := contextPrefix + "Berikut analisa gambar yang dikirim user:\n---\n" + strings.TrimSpace(visionText) + "\n---\nJawab user berdasarkan isi gambar tersebut."
			return userContent, "[Gambar Vision] " + truncateSingleLine(visionText, 150)
		}
		if err != nil {
			s.log(fmt.Sprintf("AI Vision gagal: %v", err), "warning")
		}
	}

	if text != "" {
		return text, text
	}
	return "User mengirim gambar, tetapi analisa gambar tidak tersedia. Tanyakan apa yang ingin mereka sampaikan.", "[Gambar] Analisa vision gagal"
}

func (s *Service) generateReply(ctx context.Context, settings Settings, accountID string, history []chatTurn, userText string) (string, error) {
	apiKey := effectiveAPIKey(settings)
	if apiKey == "" {
		return "", fmt.Errorf("api key AI belum diisi")
	}

	systemPrompt := buildSystemPrompt(settings, accountID)
	messages := make([]map[string]interface{}, 0, len(history)+2)
	messages = append(messages, map[string]interface{}{
		"role":    "system",
		"content": systemPrompt,
	})
	for _, turn := range clampHistory(history, settings.MaxHistory) {
		messages = append(messages, map[string]interface{}{
			"role":    turn.Role,
			"content": turn.Content,
		})
	}
	messages = append(messages, map[string]interface{}{
		"role":    "user",
		"content": userText,
	})
	return s.doChatCompletion(ctx, settings, apiKey, messages)
}

func (s *Service) generateReplyFromParts(ctx context.Context, settings Settings, accountID string, history []chatTurn, parts []pendingPart) (string, error) {
	userSegments := make([]string, 0, len(parts))
	for _, part := range parts {
		userSegment, _ := s.prepareUserSegment(ctx, settings, part)
		if strings.TrimSpace(userSegment) == "" {
			continue
		}
		userSegments = append(userSegments, userSegment)
	}
	if len(userSegments) == 0 {
		return "", fmt.Errorf("konten user tidak terbaca")
	}
	userText := strings.Join(userSegments, "\n")
	if len(userSegments) > 1 {
		userText = "Ringkas dan jawab gabungan beberapa pesan berikut:\n- " + strings.Join(userSegments, "\n- ")
	}
	return s.generateReply(ctx, settings, accountID, history, userText)
}

func (s *Service) doChatCompletion(ctx context.Context, settings Settings, apiKey string, messages []map[string]interface{}) (string, error) {
	if normalizeProvider(settings.Provider) == ProviderLocalHF {
		maxTokens := config.WhatsappAIMaxTokens
		if maxTokens > 192 {
			maxTokens = 192
		}
		key := firstNonEmpty(strings.TrimSpace(apiKey), localHFApiKey)
		return s.doOpenAIChatCompletion(ctx, localHFEndpoint, localHFModel, key, maxTokens, messages)
	}
	return s.doNvidiaChatCompletion(ctx, apiKey, messages)
}

func (s *Service) doOpenAIChatCompletion(ctx context.Context, endpoint, model, apiKey string, maxTokens int, messages []map[string]interface{}) (string, error) {
	if maxTokens <= 0 {
		maxTokens = config.WhatsappAIMaxTokens
	}
	payload := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": 0.7,
		"top_p":       1.0,
		"stream":      false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, time.Duration(config.WhatsappAIRequestTimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ai api status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("respons AI tidak memiliki choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func (s *Service) doNvidiaChatCompletion(ctx context.Context, apiKey string, messages []map[string]interface{}) (string, error) {
	payload := map[string]interface{}{
		"model":       config.WhatsappAIModel,
		"messages":    messages,
		"max_tokens":  config.WhatsappAIMaxTokens,
		"temperature": 1.0,
		"top_p":       1.0,
		"stream":      false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, time.Duration(config.WhatsappAIRequestTimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, config.WhatsappAIEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ai api status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("respons AI tidak memiliki choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func readNvidiaAssistantStream(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
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

func buildIncomingTextContext(parts []pendingPart) (string, string) {
	userSegments := make([]string, 0, len(parts))
	memorySegments := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		caption := strings.TrimSpace(part.Caption)
		if text != "" {
			userSegments = append(userSegments, text)
			memorySegments = append(memorySegments, text)
			continue
		}
		if caption != "" {
			userSegments = append(userSegments, caption)
			memorySegments = append(memorySegments, "User mengirim gambar dengan caption: "+caption)
			continue
		}
		if part.HasImage {
			memorySegments = append(memorySegments, "User mengirim gambar.")
		}
	}

	userText := strings.Join(userSegments, "\n")
	if len(userSegments) > 1 {
		userText = "Ringkas dan jawab gabungan beberapa pesan berikut:\n- " + strings.Join(userSegments, "\n- ")
	}
	memoryText := strings.Join(memorySegments, "\n")
	if len(memorySegments) > 1 {
		memoryText = "Gabungan pesan user:\n- " + strings.Join(memorySegments, "\n- ")
	}
	return strings.TrimSpace(userText), strings.TrimSpace(memoryText)
}

func hasImagePart(parts []pendingPart) bool {
	for _, part := range parts {
		if part.HasImage && len(part.ImageData) > 0 {
			return true
		}
	}
	return false
}

func buildOmniUserContent(parts []pendingPart) []map[string]interface{} {
	content := make([]map[string]interface{}, 0, len(parts)*2+1)
	hasText := false
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		caption := strings.TrimSpace(part.Caption)
		if text != "" {
			content = append(content, map[string]interface{}{
				"type": "text",
				"text": text,
			})
			hasText = true
		} else if caption != "" {
			content = append(content, map[string]interface{}{
				"type": "text",
				"text": caption,
			})
			hasText = true
		}

		if part.HasImage && len(part.ImageData) > 0 {
			mimeType := strings.TrimSpace(part.MimeType)
			if mimeType == "" {
				mimeType = http.DetectContentType(part.ImageData)
			}
			content = append(content, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]string{
					"url": fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(part.ImageData)),
				},
			})
		}
	}
	if !hasText {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": "Tolong lihat gambar yang saya kirim dan jawab pertanyaan user berdasarkan isi gambarnya dalam bahasa Indonesia yang singkat dan natural.",
		})
	}
	return content
}

func (s *Service) extractImageInsight(ctx context.Context, settings Settings, imageData []byte, mimeType, caption string) (string, string, error) {
	text, err := s.runVisionAnalysis(ctx, settings, imageData, mimeType, caption)
	if err != nil {
		return "", "", err
	}
	text = cleanVisionText(text)
	if strings.TrimSpace(text) == "" {
		return "", "", fmt.Errorf("vision tidak menghasilkan analisa gambar")
	}
	return text, visionModel, nil
}

func (s *Service) runVisionAnalysis(ctx context.Context, settings Settings, imageData []byte, mimeType, caption string) (string, error) {
	apiKey := effectiveAPIKey(settings)
	if apiKey == "" {
		return "", fmt.Errorf("api key AI belum diisi")
	}
	if len(imageData) == 0 {
		return "", fmt.Errorf("data gambar kosong")
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(imageData)
	}

	prompt := "Analisa gambar ini dengan teliti dalam bahasa Indonesia. Baca detail dulu sebelum menyimpulkan. Jika ada teks, salin teks penting secara akurat. Jika ada angka, harga, ukuran, warna, nama produk, nama toko, bukti transfer, resi, alamat, atau status pembayaran, sebutkan jelas. Jika ini screenshot chat/promosi/produk/dokumen, ringkas poin pentingnya secara rapi lalu simpulkan konteks utama gambar."
	if strings.TrimSpace(caption) != "" {
		prompt += "\nCaption user: " + strings.TrimSpace(caption)
	}

	payload := map[string]interface{}{
		"model": visionModel,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": "/think",
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": prompt,
					},
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(imageData)),
						},
					},
				},
			},
		},
		"max_tokens":        4096,
		"temperature":       1.0,
		"top_p":             1.0,
		"frequency_penalty": 0.0,
		"presence_penalty":  0.0,
		"stream":            false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, visionRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, config.WhatsappAIEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("vision api status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("respons vision tidak memiliki choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func (s *Service) remember(chatID, role, content string, maxHistory int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rememberLocked(chatID, role, content, maxHistory)
}

func (s *Service) rememberLocked(chatID, role, content string, maxHistory int) {
	if maxHistory <= 0 {
		maxHistory = defaultMaxHistory
	}
	history := s.histories[chatID]
	history = append(history, chatTurn{
		Role:    role,
		Content: strings.TrimSpace(content),
		TS:      time.Now(),
	})
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}
	s.histories[chatID] = history

	if len(s.histories) > maxTrackedChats {
		var oldestKey string
		var oldestTime time.Time
		for key, turns := range s.histories {
			if len(turns) == 0 {
				oldestKey = key
				break
			}
			if oldestKey == "" || turns[0].TS.Before(oldestTime) {
				oldestKey = key
				oldestTime = turns[0].TS
			}
		}
		if oldestKey != "" {
			delete(s.histories, oldestKey)
		}
	}
}

func (s *Service) cleanupSeenLocked() {
	if len(s.seen) < 3000 {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for key, ts := range s.seen {
		if ts.Before(cutoff) {
			delete(s.seen, key)
		}
	}
}

func (s *Service) setFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Failed++
	s.stats.LastError = err.Error()
}

func (s *Service) log(msg, level string) {
	if s.logger != nil {
		s.logger(msg, level)
	}
}

func shouldSkipEvent(evt *events.Message) bool {
	if evt == nil {
		return true
	}
	if evt.Info.IsFromMe || evt.Info.IsIncomingBroadcast() {
		return true
	}

	chat := evt.Info.Chat
	chatID := chat.String()

	if chat.Server != types.DefaultUserServer && chat.Server != types.HiddenUserServer {
		return true
	}
	if strings.HasSuffix(chatID, "@g.us") || strings.HasSuffix(chatID, "@broadcast") {
		return true
	}
	if strings.Contains(chatID, "newsletter") || strings.HasPrefix(chatID, "status@") {
		return true
	}
	return false
}

func extractIncomingPayload(msg *waE2E.Message) (incomingPayload, bool) {
	if msg == nil {
		return incomingPayload{}, false
	}

	switch {
	case msg.GetConversation() != "":
		return incomingPayload{Text: strings.TrimSpace(msg.GetConversation())}, true
	case msg.GetExtendedTextMessage() != nil && msg.GetExtendedTextMessage().GetText() != "":
		return incomingPayload{Text: strings.TrimSpace(msg.GetExtendedTextMessage().GetText())}, true
	case msg.GetImageMessage() != nil:
		caption := strings.TrimSpace(msg.GetImageMessage().GetCaption())
		return incomingPayload{
			Text:         caption,
			Caption:      caption,
			ImageMessage: msg.GetImageMessage(),
		}, caption != "" || len(msg.GetImageMessage().GetURL()) > 0 || len(msg.GetImageMessage().GetDirectPath()) > 0
	case msg.GetVideoMessage() != nil && msg.GetVideoMessage().GetCaption() != "":
		return incomingPayload{Text: strings.TrimSpace(msg.GetVideoMessage().GetCaption())}, true
	case msg.GetDocumentMessage() != nil && msg.GetDocumentMessage().GetCaption() != "":
		return incomingPayload{Text: strings.TrimSpace(msg.GetDocumentMessage().GetCaption())}, true
	case msg.GetButtonsResponseMessage() != nil && msg.GetButtonsResponseMessage().GetSelectedDisplayText() != "":
		return incomingPayload{Text: strings.TrimSpace(msg.GetButtonsResponseMessage().GetSelectedDisplayText())}, true
	case msg.GetListResponseMessage() != nil && msg.GetListResponseMessage().GetTitle() != "":
		return incomingPayload{Text: strings.TrimSpace(msg.GetListResponseMessage().GetTitle())}, true
	case msg.GetProtocolMessage() != nil && msg.GetProtocolMessage().GetEditedMessage() != nil:
		return extractIncomingPayload(msg.GetProtocolMessage().GetEditedMessage())
	default:
		return incomingPayload{}, false
	}
}

func buildSystemPrompt(settings Settings, accountID string) string {
	noFormatRule := "ATURAN FORMAT: Kamu boleh menebalkan kata atau frasa penting memakai format WhatsApp *seperti ini*. Jangan gunakan **, _, #, atau markdown lain. Bold secukupnya saja, maksimal 2-3 frasa per pesan. Gunakan tanda hubung (-) untuk daftar poin."
	base := settings.Instruction
	if strings.TrimSpace(base) == "" {
		base = "Jawab dengan singkat, jelas, natural, dan sopan dalam bahasa Indonesia."
	}
	parts := []string{strings.TrimSpace(base)}
	if originText, providerLabel := activeShippingOrigin(settings); originText != "" {
		parts = append(parts, strings.TrimSpace(fmt.Sprintf(
			"ATURAN ONGKIR DAN ASAL PENGIRIMAN: Lokasi origin/gudang/pengirim default yang resmi adalah %s. Jika user menanyakan asal pengiriman, gudang, atau ongkir dari kota mana, gunakan origin ini sebagai sumber kebenaran utama. Jika ada info lain yang bertentangan di riwayat chat atau info produk, origin %s ini yang harus diprioritaskan. Jangan menyebut kota asal lain selain origin ini kecuali user secara eksplisit membahas cabang lain dan Anda memang punya data pasti.",
			originText,
			providerLabel,
		)))
	}
	if strings.TrimSpace(settings.ProductInfo) != "" {
		parts = append(parts, "Catatan Umum AI:\n"+strings.TrimSpace(settings.ProductInfo))
	}
	selectedProducts := selectProductsForAccount(settings, accountID)
	if len(selectedProducts) > 0 {
		lines := []string{}
		if strings.TrimSpace(accountID) != "" && len(settings.AccountProductIDs[strings.TrimSpace(accountID)]) > 0 {
			lines = append(lines, "KNOWLEDGE PRODUK UNTUK AKUN INI: Semua produk di bawah boleh dipakai sebagai acuan saat menjawab. Jika ada produk yang memang diprioritaskan untuk akun ini, dahulukan produk yang paling relevan, tetapi jangan mengabaikan knowledge produk lain yang juga ada di daftar.")
		} else {
			lines = append(lines, "KNOWLEDGE PRODUK: Gunakan semua produk di bawah sebagai acuan saat menjawab pertanyaan customer. Jangan mengarang di luar data yang tersedia.")
		}
		for idx, item := range selectedProducts {
			lines = append(lines, fmt.Sprintf("%d. %s", idx+1, strings.TrimSpace(item.Name)))
			if strings.TrimSpace(item.Content) != "" {
				lines = append(lines, strings.TrimSpace(item.Content))
			}
			if len(item.ImagePaths) > 0 || strings.TrimSpace(item.ImagePath) != "" {
				lines = append(lines, "Produk ini memiliki gambar referensi internal di sistem. Jika customer meminta foto atau gambar produk, arahkan singkat lalu sistem bisa ikut mengirim gambar referensinya.")
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	parts = append(parts, noFormatRule)
	return strings.Join(parts, "\n\n")
}

func sanitizeProducts(items []ProductKnowledge) []ProductKnowledge {
	if len(items) == 0 {
		return []ProductKnowledge{}
	}
	seen := make(map[string]struct{}, len(items))
	sanitized := make([]ProductKnowledge, 0, len(items))
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		item.Content = strings.TrimSpace(item.Content)
		item.ImagePath = strings.TrimSpace(item.ImagePath)
		item.ImageURL = strings.TrimSpace(item.ImageURL)
		item.ImagePaths = sanitizeStringList(item.ImagePaths)
		item.ImageURLs = sanitizeStringList(item.ImageURLs)
		if len(item.ImagePaths) == 0 && item.ImagePath != "" {
			item.ImagePaths = []string{item.ImagePath}
		}
		if len(item.ImageURLs) == 0 && item.ImageURL != "" {
			item.ImageURLs = []string{item.ImageURL}
		}
		if item.ImagePath == "" && len(item.ImagePaths) > 0 {
			item.ImagePath = item.ImagePaths[0]
		}
		if item.ImageURL == "" && len(item.ImageURLs) > 0 {
			item.ImageURL = item.ImageURLs[0]
		}
		if item.ID == "" || item.Name == "" {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		sanitized = append(sanitized, item)
	}
	return sanitized
}

func sanitizeAccountProductMap(raw map[string][]string, accountIDs []string, products []ProductKnowledge) map[string][]string {
	if len(raw) == 0 {
		return map[string][]string{}
	}
	validAccounts := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			validAccounts[id] = struct{}{}
		}
	}
	validProducts := make(map[string]struct{}, len(products))
	for _, item := range products {
		if item.ID != "" {
			validProducts[item.ID] = struct{}{}
		}
	}
	result := make(map[string][]string)
	for accountID, ids := range raw {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			continue
		}
		if len(validAccounts) > 0 {
			if _, ok := validAccounts[accountID]; !ok {
				continue
			}
		}
		seen := map[string]struct{}{}
		filtered := make([]string, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := validProducts[id]; !ok {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			filtered = append(filtered, id)
		}
		if len(filtered) > 0 {
			result[accountID] = filtered
		}
	}
	return result
}

func sanitizeStringList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func selectProductsForAccount(settings Settings, accountID string) []ProductKnowledge {
	if len(settings.Products) == 0 {
		return nil
	}
	if strings.TrimSpace(accountID) == "" {
		return append([]ProductKnowledge(nil), settings.Products...)
	}
	ids := settings.AccountProductIDs[strings.TrimSpace(accountID)]
	if len(ids) == 0 {
		return append([]ProductKnowledge(nil), settings.Products...)
	}
	lookup := make(map[string]ProductKnowledge, len(settings.Products))
	for _, item := range settings.Products {
		lookup[item.ID] = item
	}
	selected := make([]ProductKnowledge, 0, len(settings.Products))
	seen := make(map[string]struct{}, len(settings.Products))
	for _, id := range ids {
		if item, ok := lookup[id]; ok {
			selected = append(selected, item)
			seen[id] = struct{}{}
		}
	}
	for _, item := range settings.Products {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		selected = append(selected, item)
	}
	return selected
}

func productReferenceImagePaths(product ProductKnowledge) []string {
	if len(product.ImagePaths) > 0 {
		return product.ImagePaths
	}
	if strings.TrimSpace(product.ImagePath) != "" {
		return []string{strings.TrimSpace(product.ImagePath)}
	}
	return nil
}

func pickProductForImageRequest(userText string, history []chatTurn, products []ProductKnowledge) *ProductKnowledge {
	if len(products) == 0 || !looksLikeProductImageRequest(userText, history) {
		return nil
	}

	searchText := normalizeHumanText(strings.TrimSpace(userText + "\n" + latestUserTurn(history)))
	for i := range products {
		product := products[i]
		if len(productReferenceImagePaths(product)) == 0 {
			continue
		}
		nameTokens := strings.Fields(normalizeHumanText(product.Name))
		for _, token := range nameTokens {
			if len(token) < 4 {
				continue
			}
			if strings.Contains(searchText, token) {
				return &products[i]
			}
		}
	}

	if len(products) == 1 && len(productReferenceImagePaths(products[0])) > 0 {
		return &products[0]
	}
	return nil
}

func looksLikeProductImageRequest(userText string, history []chatTurn) bool {
	combined := normalizeHumanText(strings.TrimSpace(userText + "\n" + latestUserTurn(history)))
	patterns := []string{
		"foto",
		"gambar",
		"foto produk",
		"gambar produk",
		"minta foto",
		"kirim foto",
		"kirim gambar",
		"lihat foto",
		"lihat gambar",
		"foto asli",
		"gambar asli",
		"contoh model",
		"katalog",
	}
	for _, pattern := range patterns {
		if strings.Contains(combined, pattern) {
			return true
		}
	}
	return false
}

func activeShippingOrigin(settings Settings) (string, string) {
	if settings.SystemOngkirEnabled && strings.TrimSpace(settings.SystemOngkirOrigin) != "" {
		return settings.SystemOngkirOrigin, "Perhitungan Ongkir Sistem"
	}
	if settings.RajaOngkirEnabled && strings.TrimSpace(settings.RajaOngkirOrigin) != "" {
		return settings.RajaOngkirOrigin, "RajaOngkir"
	}
	return "", ""
}

func (s *Service) tryAnswerShipping(ctx context.Context, settings Settings, history []chatTurn, userText string) (string, string, bool, error) {
	if settings.SystemOngkirEnabled {
		reply, handled, err := s.tryAnswerSystemOngkir(ctx, settings, history, userText)
		if handled || err != nil {
			return reply, "Perhitungan Ongkir Sistem", handled, err
		}
	}
	reply, handled, err := s.tryAnswerRajaOngkir(ctx, settings, history, userText)
	if handled || err != nil {
		return reply, "RajaOngkir", handled, err
	}
	return "", "", false, nil
}

func (s *Service) tryAnswerSystemOngkir(ctx context.Context, settings Settings, history []chatTurn, userText string) (string, bool, error) {
	if !settings.SystemOngkirEnabled || strings.TrimSpace(settings.SystemOngkirOrigin) == "" {
		return "", false, nil
	}

	if looksLikeOriginQuestion(userText) {
		return fmt.Sprintf("Siap, pengiriman kami berasal dari *%s* ya.", settings.SystemOngkirOrigin), true, nil
	}

	combinedText := strings.TrimSpace(userText)
	if !looksLikeShippingCostQuestion(userText) {
		current := normalizeHumanText(userText)
		if strings.Contains(current, "sesuai") || strings.Contains(current, "sekitar") {
			if recent := latestUserTurn(history); recent != "" {
				combinedText = strings.TrimSpace(recent + "\n" + userText)
			}
		}
		if !looksLikeShippingCostQuestion(combinedText) {
			return "", false, nil
		}
	}

	destinationQuery := extractDestinationQuery(combinedText)
	if destinationQuery == "" {
		return "Siap, untuk cek ongkir yang lebih akurat kirim dulu *kecamatan/kelurahan tujuan* atau *kode pos tujuan* ya.", true, nil
	}

	weightGrams, assumedWeight := extractWeightGrams(combinedText)
	if weightGrams <= 0 {
		weightGrams = 1000
		assumedWeight = true
	}

	origin, err := s.searchSystemOngkirArea(ctx, settings.SystemOngkirOrigin)
	if err != nil {
		return "Maaf, saya belum bisa membaca data origin pengiriman sistem saat ini. Coba sebentar lagi ya.", true, err
	}
	destination, err := s.searchSystemOngkirArea(ctx, destinationQuery)
	if err != nil {
		return fmt.Sprintf("Lokasi tujuan *%s* belum ketemu di sistem. Coba kirim *kecamatan/kelurahan* atau *kode posnya* ya biar saya cekkan lagi.", destinationQuery), true, err
	}

	rates, err := s.calculateSystemOngkir(ctx, origin.ID, destination.ID, weightGrams)
	if err != nil {
		return "Maaf, data ongkir sistem sedang belum bisa saya ambil sekarang. Coba kirim ulang beberapa saat lagi ya.", true, err
	}
	if len(rates) == 0 {
		return fmt.Sprintf("Maaf ya, saat ini belum ada layanan ongkir yang tersedia untuk rute *%s* ke *%s*.", settings.SystemOngkirOrigin, destinationQuery), true, nil
	}

	reply := formatSystemOngkirReply(settings.SystemOngkirOrigin, origin, destination, rates, weightGrams, assumedWeight)
	return reply, true, nil
}

func (s *Service) tryAnswerRajaOngkir(ctx context.Context, settings Settings, history []chatTurn, userText string) (string, bool, error) {
	if !settings.RajaOngkirEnabled || strings.TrimSpace(settings.RajaOngkirAPIKey) == "" || strings.TrimSpace(settings.RajaOngkirOrigin) == "" {
		return "", false, nil
	}

	if looksLikeOriginQuestion(userText) {
		return fmt.Sprintf("Siap, pengiriman kami berasal dari *%s* ya.", settings.RajaOngkirOrigin), true, nil
	}

	combinedText := strings.TrimSpace(userText)
	if !looksLikeShippingCostQuestion(userText) {
		current := normalizeHumanText(userText)
		if strings.Contains(current, "raja ongkir") || strings.Contains(current, "sesuai") || strings.Contains(current, "sekitar") {
			if recent := latestUserTurn(history); recent != "" {
				combinedText = strings.TrimSpace(recent + "\n" + userText)
			}
		}
		if !looksLikeShippingCostQuestion(combinedText) {
			return "", false, nil
		}
	}

	destinationQuery := extractDestinationQuery(combinedText)
	if destinationQuery == "" {
		return "Siap, untuk cek ongkir yang lebih akurat kirim dulu *kecamatan/kelurahan tujuan* atau *kode pos tujuan* ya.", true, nil
	}

	weightGrams, assumedWeight := extractWeightGrams(combinedText)
	if weightGrams <= 0 {
		weightGrams = 1000
		assumedWeight = true
	}

	couriers := extractRequestedCouriers(combinedText)
	if couriers == "" {
		couriers = "jne:sicepat:tiki:pos"
	}

	origin, err := s.searchRajaOngkirDestination(ctx, settings.RajaOngkirAPIKey, settings.RajaOngkirOrigin)
	if err != nil {
		return "Maaf, saya belum bisa membaca data origin pengiriman saat ini. Coba sebentar lagi ya.", true, err
	}
	destination, err := s.searchRajaOngkirDestination(ctx, settings.RajaOngkirAPIKey, destinationQuery)
	if err != nil {
		return fmt.Sprintf("Lokasi tujuan *%s* belum ketemu di sistem. Coba kirim *kecamatan/kelurahan* atau *kode posnya* ya biar saya cekkan lagi.", destinationQuery), true, err
	}

	costs, err := s.calculateRajaOngkir(ctx, settings.RajaOngkirAPIKey, origin.ID, destination.ID, weightGrams, couriers)
	if err != nil {
		return "Maaf, data ongkir sedang belum bisa saya ambil sekarang. Coba kirim ulang beberapa saat lagi ya.", true, err
	}
	if len(costs) == 0 {
		return fmt.Sprintf("Maaf ya, saat ini belum ada layanan ongkir yang tersedia untuk rute *%s* ke *%s*.", settings.RajaOngkirOrigin, destinationQuery), true, nil
	}

	reply := formatRajaOngkirReply(settings.RajaOngkirOrigin, origin, destination, costs, weightGrams, assumedWeight)
	return reply, true, nil
}

func latestUserTurn(history []chatTurn) string {
	for i := len(history) - 1; i >= 0; i-- {
		turn := history[i]
		if strings.TrimSpace(turn.Role) == "user" && strings.TrimSpace(turn.Content) != "" {
			return strings.TrimSpace(turn.Content)
		}
	}
	return ""
}

func looksLikeOriginQuestion(text string) bool {
	normalized := normalizeHumanText(text)
	patterns := []string{
		"pengiriman dari mana",
		"asal pengiriman",
		"asal kirim",
		"dikirim dari mana",
		"gudang di mana",
		"gudang dimana",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func looksLikeShippingCostQuestion(text string) bool {
	normalized := normalizeHumanText(text)
	patterns := []string{
		"ongkir",
		"ongkos kirim",
		"biaya kirim",
		"tarif kirim",
		"cek ongkir",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func normalizeHumanText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", ",", " ", ".", " ", "?", " ", "!", " ", ":", " ", ";", " ")
	text = replacer.Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

func extractDestinationQuery(text string) string {
	raw := strings.TrimSpace(text)
	candidates := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:ongkir|ongkos kirim|biaya kirim|tarif kirim)[^a-z0-9]+(?:ke|tujuan)\s+(.+)$`),
		regexp.MustCompile(`(?i)\bke\s+(.+)$`),
	}
	for _, re := range candidates {
		match := re.FindStringSubmatch(raw)
		if len(match) < 2 {
			continue
		}
		result := cleanDestinationQuery(match[1])
		if result != "" {
			return result
		}
	}
	return ""
}

func cleanDestinationQuery(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.IndexAny(value, "?!\n\r"); idx >= 0 {
		value = value[:idx]
	}
	value = strings.Trim(value, " ,.;:-")
	stopwords := map[string]bool{
		"berapa": true,
		"brp":    true,
		"ya":     true,
		"yah":    true,
		"dong":   true,
		"kak":    true,
		"min":    true,
		"nih":    true,
	}
	fields := strings.Fields(value)
	for len(fields) > 0 && stopwords[strings.ToLower(fields[len(fields)-1])] {
		fields = fields[:len(fields)-1]
	}
	return strings.TrimSpace(strings.Join(fields, " "))
}

func extractWeightGrams(text string) (int, bool) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(kg|kilogram|kilo)\b`),
		regexp.MustCompile(`(?i)(\d+)\s*(gr|gram|g)\b`),
	}
	for _, re := range patterns {
		match := re.FindStringSubmatch(text)
		if len(match) < 3 {
			continue
		}
		raw := strings.ReplaceAll(match[1], ",", ".")
		unit := strings.ToLower(match[2])
		if strings.HasPrefix(unit, "k") {
			if val, err := strconv.ParseFloat(raw, 64); err == nil && val > 0 {
				return int(val * 1000), false
			}
		}
		if val, err := strconv.Atoi(strings.Split(raw, ".")[0]); err == nil && val > 0 {
			return val, false
		}
	}
	return 0, true
}

func extractRequestedCouriers(text string) string {
	normalized := normalizeHumanText(text)
	order := []string{"jne", "sicepat", "tiki", "pos", "jnt", "anteraja"}
	found := make([]string, 0, len(order))
	for _, courier := range order {
		if strings.Contains(normalized, courier) {
			found = append(found, courier)
		}
	}
	return strings.Join(found, ":")
}

func (s *Service) searchRajaOngkirDestination(ctx context.Context, apiKey, query string) (rajaOngkirDestination, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return rajaOngkirDestination{}, fmt.Errorf("empty location query")
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, 20*time.Second)
	defer cancel()

	endpoint := "https://rajaongkir.komerce.id/api/v1/destination/domestic-destination?search=" + url.QueryEscape(query) + "&limit=5&offset=0"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return rajaOngkirDestination{}, err
	}
	req.Header.Set("key", apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return rajaOngkirDestination{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return rajaOngkirDestination{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rajaOngkirDestination{}, fmt.Errorf("destination api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed rajaOngkirDestinationResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return rajaOngkirDestination{}, err
	}
	if len(parsed.Data) == 0 {
		return rajaOngkirDestination{}, fmt.Errorf("destination not found for %s", query)
	}
	return parsed.Data[0], nil
}

func (s *Service) calculateRajaOngkir(ctx context.Context, apiKey string, originID, destinationID, weightGrams int, couriers string) ([]rajaOngkirCost, error) {
	if originID <= 0 || destinationID <= 0 {
		return nil, fmt.Errorf("invalid origin/destination id")
	}
	if weightGrams <= 0 {
		weightGrams = 1000
	}
	form := url.Values{}
	form.Set("origin", strconv.Itoa(originID))
	form.Set("destination", strconv.Itoa(destinationID))
	form.Set("weight", strconv.Itoa(weightGrams))
	form.Set("courier", couriers)
	form.Set("price", "lowest")

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, 25*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "https://rajaongkir.komerce.id/api/v1/calculate/domestic-cost", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("key", apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cost api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed rajaOngkirCostResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	sort.Slice(parsed.Data, func(i, j int) bool {
		if parsed.Data[i].Cost == parsed.Data[j].Cost {
			return strings.Compare(parsed.Data[i].Code+parsed.Data[i].Service, parsed.Data[j].Code+parsed.Data[j].Service) < 0
		}
		return parsed.Data[i].Cost < parsed.Data[j].Cost
	})
	return parsed.Data, nil
}

func (s *Service) fetchSystemOngkirAreas(ctx context.Context) ([]systemOngkirArea, error) {
	systemOngkirAreaCache.mu.Lock()
	if len(systemOngkirAreaCache.rows) > 0 && time.Now().Before(systemOngkirAreaCache.expiresAt) {
		rows := append([]systemOngkirArea(nil), systemOngkirAreaCache.rows...)
		systemOngkirAreaCache.mu.Unlock()
		return rows, nil
	}
	systemOngkirAreaCache.mu.Unlock()

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, 25*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, systemOngkirAreasURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", systemOngkirBaseURL+"/cek-ongkir")
	req.Header.Set("User-Agent", "Mozilla/5.0 InstaBlast Ongkir")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("system ongkir area status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var payload []map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}

	rows := make([]systemOngkirArea, 0, len(payload))
	for _, item := range payload {
		id := parseSystemOngkirID(item["sub_district_id"])
		if id == "" || id == "0" {
			continue
		}
		area := systemOngkirArea{
			ID:          id,
			Province:    strings.TrimSpace(parseSystemOngkirString(item["province"])),
			Regency:     strings.TrimSpace(parseSystemOngkirString(item["regency"])),
			SubDistrict: strings.TrimSpace(parseSystemOngkirString(item["sub_district"])),
		}
		area.Label = buildSystemOngkirLabel(area)
		area.SearchText = normalizeLookupText(strings.Join([]string{
			area.Province,
			area.Regency,
			area.SubDistrict,
			area.ID,
		}, " "))
		rows = append(rows, area)
	}

	systemOngkirAreaCache.mu.Lock()
	systemOngkirAreaCache.rows = append([]systemOngkirArea(nil), rows...)
	systemOngkirAreaCache.expiresAt = time.Now().Add(24 * time.Hour)
	systemOngkirAreaCache.mu.Unlock()
	return rows, nil
}

func (s *Service) searchSystemOngkirArea(ctx context.Context, query string) (systemOngkirArea, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return systemOngkirArea{}, fmt.Errorf("empty location query")
	}
	rows, err := s.fetchSystemOngkirAreas(ctx)
	if err != nil {
		return systemOngkirArea{}, err
	}
	normalized := normalizeLookupText(query)
	if len(normalized) < 2 {
		return systemOngkirArea{}, fmt.Errorf("location query too short")
	}

	for _, item := range rows {
		if strings.Contains(item.SearchText, normalized) {
			return item, nil
		}
	}

	tokens := strings.Fields(normalized)
	for _, item := range rows {
		matched := true
		for _, token := range tokens {
			if !strings.Contains(item.SearchText, token) {
				matched = false
				break
			}
		}
		if matched {
			return item, nil
		}
	}

	return systemOngkirArea{}, fmt.Errorf("system ongkir destination not found for %s", query)
}

func (s *Service) calculateSystemOngkir(ctx context.Context, originID, destinationID string, weightGrams int) ([]systemOngkirRate, error) {
	originID = strings.TrimSpace(originID)
	destinationID = strings.TrimSpace(destinationID)
	if originID == "" || destinationID == "" {
		return nil, fmt.Errorf("invalid origin/destination id")
	}
	if weightGrams <= 0 {
		weightGrams = 1000
	}

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, 25*time.Second)
	defer cancel()

	requestURL, err := url.Parse(systemOngkirRatesURL)
	if err != nil {
		return nil, err
	}
	query := requestURL.Query()
	query.Set("addressIdPengirim", originID)
	query.Set("addressIdPenerima", destinationID)
	query.Set("beratBarang", strconv.Itoa(weightGrams))
	requestURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html, */*")
	req.Header.Set("Referer", systemOngkirBaseURL+"/cek-ongkir")
	req.Header.Set("User-Agent", "Mozilla/5.0 InstaBlast Ongkir")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("system ongkir rates status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseSystemOngkirRateCards(string(body))
}

func parseSystemOngkirRateCards(htmlText string) ([]systemOngkirRate, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<section id="engine-root">` + htmlText + `</section>`))
	if err != nil {
		return nil, err
	}

	rates := make([]systemOngkirRate, 0)
	doc.Find("#engine-root a.btn.btn-outline-primary").Each(func(_ int, selection *goquery.Selection) {
		metaText := selection.NextFiltered("div").Text()
		meta := parsePseudoPHPArray(metaText)
		strongSpans := selection.Find("span.fw-bold.text-danger")
		serviceName := strings.TrimSpace(textAtSelection(strongSpans, 0))
		if serviceName == "" {
			serviceName = strings.TrimSpace(firstNonEmpty(meta["name"], meta["code"]))
		}
		displayedPrice := strings.TrimSpace(textAtSelection(strongSpans, 1))
		cashbackText := strings.Join(strings.Fields(selection.Find("small.text-info").Text()), " ")
		logoURL, _ := selection.Find("img").Attr("src")
		courierName, _ := selection.Find("img").Attr("alt")
		if strings.TrimSpace(courierName) == "" {
			courierName = serviceName
		}
		discountedCost := parseIDRValue(meta["cost"])
		originalCost := parseIDRValue(meta["old_cost"])
		if serviceName == "" && strongSpans.Length() == 0 {
			return
		}
		rates = append(rates, systemOngkirRate{
			CourierName:         strings.TrimSpace(courierName),
			ServiceName:         serviceName,
			ServiceCode:         strings.TrimSpace(meta["code"]),
			LogoURL:             strings.TrimSpace(logoURL),
			DiscountedCost:      discountedCost,
			DiscountedCostLabel: firstNonEmpty(formatOptionalIDR(discountedCost), displayedPrice),
			OriginalCost:        originalCost,
			OriginalCostLabel:   formatOptionalIDR(originalCost),
			HasDiscount:         discountedCost > 0 && originalCost > 0 && discountedCost < originalCost,
			CashbackText:        strings.TrimSpace(cashbackText),
		})
	})

	sort.Slice(rates, func(i, j int) bool {
		left := rates[i].DiscountedCost
		right := rates[j].DiscountedCost
		if left <= 0 {
			left = int(^uint(0) >> 1)
		}
		if right <= 0 {
			right = int(^uint(0) >> 1)
		}
		if left == right {
			return strings.Compare(rates[i].CourierName+rates[i].ServiceName, rates[j].CourierName+rates[j].ServiceName) < 0
		}
		return left < right
	})
	return rates, nil
}

func formatRajaOngkirReply(originText string, origin, destination rajaOngkirDestination, costs []rajaOngkirCost, weightGrams int, assumedWeight bool) string {
	lines := []string{
		fmt.Sprintf("Siap, saya bantu cek estimasi ongkir dari *%s* ke *%s* ya.", originText, compactDestinationLabel(destination)),
	}
	if assumedWeight {
		lines = append(lines, fmt.Sprintf("Untuk sementara saya hitungkan dengan asumsi berat *%s* dulu.", formatWeight(weightGrams)))
	} else {
		lines = append(lines, fmt.Sprintf("Berikut estimasi untuk berat *%s*.", formatWeight(weightGrams)))
	}
	limit := 4
	if len(costs) < limit {
		limit = len(costs)
	}
	for i := 0; i < limit; i++ {
		item := costs[i]
		lines = append(lines, fmt.Sprintf("- *%s %s*: Rp%s, estimasi %s", strings.ToUpper(item.Code), item.Service, formatIDR(item.Cost), normalizeETD(item.ETD)))
	}
	lines = append(lines, fmt.Sprintf("Origin yang dipakai sistem: *%s*.", compactDestinationLabel(origin)))
	if assumedWeight {
		lines = append(lines, "Kalau Anda kirim *berat paket* yang pasti, saya bisa hitungkan lagi biar lebih akurat.")
	}
	lines = append(lines, "Kalau mau, saya lanjut bantu cekkan kurir yang paling hemat atau yang paling cepat.")
	return strings.Join(lines, "\n")
}

func formatSystemOngkirReply(originText string, origin, destination systemOngkirArea, rates []systemOngkirRate, weightGrams int, assumedWeight bool) string {
	lines := []string{
		fmt.Sprintf("Siap, saya bantu cek estimasi ongkir dari *%s* ke *%s* ya.", originText, destination.Label),
	}
	if assumedWeight {
		lines = append(lines, fmt.Sprintf("Untuk sementara saya hitungkan dengan asumsi berat *%s* dulu.", formatWeight(weightGrams)))
	} else {
		lines = append(lines, fmt.Sprintf("Berikut estimasi untuk berat *%s*.", formatWeight(weightGrams)))
	}
	limit := 4
	if len(rates) < limit {
		limit = len(rates)
	}
	for i := 0; i < limit; i++ {
		item := rates[i]
		serviceLabel := strings.TrimSpace(strings.Join([]string{strings.ToUpper(strings.TrimSpace(item.ServiceCode)), item.ServiceName}, " "))
		if serviceLabel == "" {
			serviceLabel = strings.TrimSpace(item.CourierName)
		}
		priceLabel := firstNonEmpty(item.DiscountedCostLabel, formatOptionalIDR(item.DiscountedCost))
		line := fmt.Sprintf("- *%s*: Rp%s", serviceLabel, priceLabel)
		if item.HasDiscount && item.OriginalCostLabel != "" {
			line += fmt.Sprintf(" (normal Rp%s)", item.OriginalCostLabel)
		}
		if item.CashbackText != "" {
			line += fmt.Sprintf(" - %s", item.CashbackText)
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("Origin yang dipakai sistem: *%s*.", origin.Label))
	if assumedWeight {
		lines = append(lines, "Kalau Anda kirim *berat paket* yang pasti, saya bisa hitungkan lagi biar lebih akurat.")
	}
	lines = append(lines, "Kalau mau, saya lanjut bantu pilihkan ongkir yang paling hemat.")
	return strings.Join(lines, "\n")
}

func compactDestinationLabel(item rajaOngkirDestination) string {
	parts := []string{
		strings.TrimSpace(item.SubdistrictName),
		strings.TrimSpace(item.DistrictName),
		strings.TrimSpace(item.CityName),
	}
	filtered := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if seen[key] {
			continue
		}
		seen[key] = true
		filtered = append(filtered, titleCaseWords(part))
	}
	if item.ZipCode != "" {
		filtered = append(filtered, item.ZipCode)
	}
	return strings.Join(filtered, ", ")
}

func titleCaseWords(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	for i, field := range fields {
		if field == "" {
			continue
		}
		fields[i] = strings.ToUpper(field[:1]) + field[1:]
	}
	return strings.Join(fields, " ")
}

func formatWeight(weightGrams int) string {
	if weightGrams%1000 == 0 {
		return fmt.Sprintf("%d kg", weightGrams/1000)
	}
	return fmt.Sprintf("%d gram", weightGrams)
}

func buildSystemOngkirLabel(area systemOngkirArea) string {
	parts := []string{area.Province, area.Regency, area.SubDistrict}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, titleCaseWords(part))
		}
	}
	return strings.Join(filtered, ", ")
}

func normalizeLookupText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", ",", " ", ".", " ", "?", " ", "!", " ", ":", " ", ";", " ", "-", " ", "/", " ")
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func parseSystemOngkirString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func parseSystemOngkirID(value any) string {
	id := strings.TrimSpace(parseSystemOngkirString(value))
	if strings.HasSuffix(id, ".0") {
		id = strings.TrimSuffix(id, ".0")
	}
	return id
}

func parsePseudoPHPArray(raw string) map[string]string {
	matches := regexp.MustCompile(`\[(.+?)\]\s*=>\s*(.+)`).FindAllStringSubmatch(raw, -1)
	result := make(map[string]string, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		result[strings.TrimSpace(match[1])] = strings.TrimSpace(match[2])
	}
	return result
}

func parseIDRValue(value string) int {
	digits := regexp.MustCompile(`[^\d]`).ReplaceAllString(strings.TrimSpace(value), "")
	if digits == "" {
		return 0
	}
	parsed, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return parsed
}

func formatOptionalIDR(value int) string {
	if value <= 0 {
		return ""
	}
	return formatIDR(value)
}

func textAtSelection(list *goquery.Selection, index int) string {
	if list == nil || index < 0 {
		return ""
	}
	item := list.Eq(index)
	if item.Length() == 0 {
		return ""
	}
	return item.Text()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func formatIDR(value int) string {
	raw := strconv.Itoa(value)
	if len(raw) <= 3 {
		return raw
	}
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	if raw != "" {
		parts = append([]string{raw}, parts...)
	}
	return strings.Join(parts, ".")
}

func normalizeETD(etd string) string {
	etd = strings.TrimSpace(etd)
	if etd == "" {
		return "estimasi belum tersedia"
	}
	return etd + " hari"
}

func (s *Service) resetConversationStateLocked() {
	for _, entry := range s.pending {
		if entry != nil && entry.Timer != nil {
			entry.Timer.Stop()
		}
	}
	s.pending = make(map[string]*pendingChat)
	s.histories = make(map[string][]chatTurn)
	s.seen = make(map[string]time.Time)
}

func accountAllowed(accountID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.TrimSpace(item) == strings.TrimSpace(accountID) {
			return true
		}
	}
	return false
}

func normalizeProvider(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case ProviderLocalHF:
		return ProviderLocalHF
	default:
		return ProviderDefault
	}
}

func normalizeAPIKey(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	masked := strings.Trim(apiKey, "*•. ")
	if masked == "" {
		return ""
	}
	if !strings.HasPrefix(apiKey, "nvapi-") {
		return ""
	}
	return apiKey
}

func effectiveAPIKey(settings Settings) string {
	if normalizeProvider(settings.Provider) == ProviderLocalHF {
		return firstNonEmpty(settings.APIKey, localHFApiKey)
	}
	if apiKey := normalizeAPIKey(settings.APIKey); apiKey != "" {
		return apiKey
	}
	if globalAPIKeyProvider != nil {
		if apiKey := normalizeAPIKey(globalAPIKeyProvider()); apiKey != "" {
			return apiKey
		}
	}
	if desktop := loadDesktopSettings(); desktop != nil {
		if apiKey := normalizeAPIKey(desktop.APIKey); apiKey != "" {
			return apiKey
		}
	}
	if desktopAPIKey != "" {
		return desktopAPIKey
	}
	return strings.TrimSpace(os.Getenv("NVIDIA_API_KEY"))
}

func ResolveAPIKey() string {
	return effectiveAPIKey(Settings{})
}

func mergeDesktopDefaults(settings Settings) Settings {
	desktop := loadDesktopSettings()
	if desktop == nil {
		return settings
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		settings.APIKey = normalizeAPIKey(desktop.APIKey)
	}
	if strings.TrimSpace(settings.Instruction) == "" {
		settings.Instruction = strings.TrimSpace(desktop.Instruction)
	}
	if strings.TrimSpace(settings.ProductInfo) == "" {
		settings.ProductInfo = strings.TrimSpace(desktop.Product)
	}
	if settings.DelayMs == 0 && desktop.DelayMs >= 0 {
		settings.DelayMs = desktop.DelayMs
	}
	return settings
}

func loadDesktopSettings() *desktopSettings {
	for _, candidate := range desktopPreferenceCandidates() {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}

		var prefs map[string]interface{}
		if err := json.Unmarshal(raw, &prefs); err != nil {
			continue
		}

		rawAI, _ := prefs["aiSettings"].(string)
		rawAI = strings.TrimSpace(rawAI)
		if rawAI == "" {
			continue
		}

		var parsed desktopSettings
		if err := json.Unmarshal([]byte(rawAI), &parsed); err != nil {
			continue
		}
		if normalizeAPIKey(parsed.APIKey) == "" && strings.TrimSpace(parsed.Instruction) == "" && strings.TrimSpace(parsed.Product) == "" && parsed.DelayMs == 0 {
			continue
		}
		parsed.APIKey = normalizeAPIKey(parsed.APIKey)
		parsed.Instruction = strings.TrimSpace(parsed.Instruction)
		parsed.Product = strings.TrimSpace(parsed.Product)
		return &parsed
	}
	return nil
}

func desktopPreferenceCandidates() []string {
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	if appData == "" {
		return nil
	}
	return []string{
		filepath.Join(appData, "blastmap-desktop", "blastmap-data", "preferences.json"),
		filepath.Join(appData, "InstaBlast Pro", "blastmap-data", "preferences.json"),
	}
}

func clampHistory(history []chatTurn, maxHistory int) []chatTurn {
	if maxHistory <= 0 || len(history) <= maxHistory {
		return history
	}
	return history[len(history)-maxHistory:]
}

func cleanReply(text string) string {
	clean := strings.TrimSpace(text)
	clean = reBold.ReplaceAllString(clean, `*$1*`)
	clean = reItalic1.ReplaceAllString(clean, `$1`)
	clean = reItalic2.ReplaceAllString(clean, `$1`)
	clean = reHeading.ReplaceAllString(clean, "")
	clean = reCodeBlock.ReplaceAllStringFunc(clean, func(match string) string {
		trimmed := strings.ReplaceAll(match, "```", "")
		return strings.TrimSpace(trimmed)
	})
	clean = reInlineCode.ReplaceAllString(clean, `$1`)
	clean = reManyNL.ReplaceAllString(clean, "\n\n")
	return strings.TrimSpace(clean)
}

func cleanVisionText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

func truncateSingleLine(text string, limit int) string {
	text = cleanVisionText(text)
	text = strings.ReplaceAll(text, "\n", " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func buildVisionFallbackReply(userText string) string {
	if !strings.Contains(userText, "Berikut analisa gambar yang dikirim user:") {
		return ""
	}

	visionText := cleanVisionText(extractVisionAnalysisBlock(userText))
	if visionText == "" {
		return "Saya sudah cek gambar yang Anda kirim. Kalau ada bagian tertentu yang ingin ditanyakan, langsung tulis ya."
	}

	normalized := normalizeHumanText(visionText)
	switch {
	case strings.Contains(normalized, "pembayaran qris berhasil"),
		strings.Contains(normalized, "transaksi qris"),
		(strings.Contains(normalized, "qris") && strings.Contains(normalized, "berhasil")),
		(strings.Contains(normalized, "pembayaran") && strings.Contains(normalized, "berhasil")):
		return "Saya sudah cek gambarnya. Terlihat bukti *pembayaran berhasil*. Kalau ini untuk konfirmasi order, data Anda sudah kami terima ya."
	case strings.Contains(normalized, "poster"),
		strings.Contains(normalized, "promo"),
		strings.Contains(normalized, "produk"):
		return "Saya sudah cek gambar yang Anda kirim. Terlihat ada info *produk atau promo*. Kalau mau, saya bantu jelaskan detailnya."
	default:
		summary := truncateSingleLine(stripVisionFormatting(visionText), 180)
		if summary == "" {
			return "Saya sudah cek gambar yang Anda kirim. Kalau ada bagian tertentu yang ingin ditanyakan, langsung tulis ya."
		}
		return "Saya sudah cek gambarnya. Ringkasnya terlihat: " + summary + ". Kalau mau, saya bantu jelaskan lebih detail."
	}
}

func extractVisionAnalysisBlock(userText string) string {
	startMarker := "Berikut analisa gambar yang dikirim user:"
	start := strings.Index(userText, startMarker)
	if start < 0 {
		return ""
	}
	block := strings.TrimSpace(userText[start+len(startMarker):])
	block = strings.TrimPrefix(block, "---")
	endMarker := "\n---\nJawab user berdasarkan isi gambar tersebut."
	if end := strings.Index(block, endMarker); end >= 0 {
		block = block[:end]
	} else if end := strings.Index(block, "\nJawab user berdasarkan isi gambar tersebut."); end >= 0 {
		block = block[:end]
	}
	return strings.TrimSpace(strings.Trim(block, "- \n"))
}

func stripVisionFormatting(text string) string {
	text = reBold.ReplaceAllString(text, `$1`)
	text = reItalic1.ReplaceAllString(text, `$1`)
	text = reItalic2.ReplaceAllString(text, `$1`)
	text = reHeading.ReplaceAllString(text, "")
	text = reInlineCode.ReplaceAllString(text, `$1`)
	text = strings.ReplaceAll(text, "*", "")
	return strings.TrimSpace(text)
}

func splitReply(text string) []string {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil
	}
	if len(raw) <= 180 {
		return []string{raw}
	}

	targetLen := 170
	maxParts := 3
	chunks := make([]string, 0, maxParts)
	rest := raw

	for rest != "" && len(chunks) < maxParts {
		if len(rest) <= targetLen+40 {
			chunks = append(chunks, strings.TrimSpace(rest))
			rest = ""
			break
		}

		slice := rest[:min(len(rest), targetLen+100)]
		idx := maxIndex(
			strings.LastIndex(slice, ". "),
			strings.LastIndex(slice, "! "),
			strings.LastIndex(slice, "? "),
		)
		if idx < targetLen-40 {
			idx = maxIndex(
				strings.LastIndex(slice, "\n"),
				strings.LastIndex(slice, "; "),
				strings.LastIndex(slice, ", "),
			)
		}
		if idx < targetLen-60 {
			idx = targetLen
		}

		head := strings.TrimSpace(rest[:idx+1])
		tail := strings.TrimSpace(rest[idx+1:])
		if head != "" {
			chunks = append(chunks, head)
		}
		rest = tail
	}
	if rest != "" {
		chunks = append(chunks, strings.TrimSpace(rest))
	}
	return chunks
}

func typingDelayFor(part string, configuredDelayMs int, first bool) int {
	base := 700 + (len([]rune(part)) * 12)
	if configuredDelayMs > 0 {
		if first {
			base += configuredDelayMs / 2
		} else {
			base += configuredDelayMs / 3
		}
	}
	if base < 900 {
		base = 900
	}
	if base > 5000 {
		base = 5000
	}
	return base
}

func compactChatID(chatID string) string {
	if idx := strings.Index(chatID, "::"); idx >= 0 {
		chatID = chatID[idx+2:]
	}
	if idx := strings.Index(chatID, "@"); idx >= 0 {
		return chatID[:idx]
	}
	return chatID
}

func scopedChatID(scope, chatID string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return chatID
	}
	return scope + "::" + chatID
}

func maxIndex(values ...int) int {
	best := -1
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
