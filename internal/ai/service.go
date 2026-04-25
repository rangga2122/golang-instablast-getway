package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

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
	defaultDelayMs       = 1200
	defaultMaxHistory    = 15
	defaultBatchWindowMs = 4500
	visionRequestTimeout = 90 * time.Second
	maxHistoryLimit      = 50
	maxTrackedChats      = 500
	desktopAPIKey        = "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"
	visionModel          = "nvidia/nemotron-nano-12b-v2-vl"
)

var (
	reBold               = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic1            = regexp.MustCompile(`__(.+?)__`)
	reItalic2            = regexp.MustCompile(`_(.+?)_`)
	reHeading            = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reCodeBlock          = regexp.MustCompile("```[\\s\\S]*?```")
	reInlineCode         = regexp.MustCompile("`(.+?)`")
	reManyNL             = regexp.MustCompile(`\n{3,}`)
	globalAPIKeyProvider func() string
)

type PreferenceStore interface {
	GetPrefJSON(key string, target interface{}) error
	SetPrefJSON(key string, value interface{}) error
}

type LoggerFunc func(msg, level string)

type Settings struct {
	Enabled       bool     `json:"enabled"`
	APIKey        string   `json:"api_key"`
	Instruction   string   `json:"instruction"`
	ProductInfo   string   `json:"product_info"`
	DelayMs       int      `json:"delay_ms"`
	MaxHistory    int      `json:"max_history"`
	BatchWindowMs int      `json:"batch_window_ms"`
	VisionEnabled bool     `json:"vision_enabled"`
	AccountIDs    []string `json:"account_ids"`
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

type Service struct {
	mu         sync.Mutex
	settings   Settings
	stats      Stats
	histories  map[string][]chatTurn
	pending    map[string]*pendingChat
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

func defaultSettings() Settings {
	return mergeDesktopDefaults(Settings{
		Enabled:       false,
		DelayMs:       defaultDelayMs,
		MaxHistory:    defaultMaxHistory,
		BatchWindowMs: defaultBatchWindowMs,
		VisionEnabled: true,
		AccountIDs:    []string{},
	})
}

func sanitizeSettings(s Settings) Settings {
	s.APIKey = normalizeAPIKey(s.APIKey)
	s.Instruction = strings.TrimSpace(s.Instruction)
	s.ProductInfo = strings.TrimSpace(s.ProductInfo)

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
	if store != nil {
		if err := store.SetPrefJSON(SettingsPrefKey, settings); err != nil {
			return settings, err
		}
	}
	s.mu.Lock()
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
	reply, err := s.generateReply(ctx, settings, nil, prompt)
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
			ChatJID:   evt.Info.Chat,
			SenderJID: evt.Info.Sender,
		}
		s.pending[chatID] = entry
	}
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

	reply, err := s.generateReply(context.Background(), settings, history, userText)
	if err != nil {
		s.setFailure(err)
		s.log(fmt.Sprintf("AI gagal membuat balasan ke %s: %v", compactChatID(chatID), err), "error")
		return
	}

	cleaned := cleanReply(reply)
	if cleaned == "" {
		s.setFailure(fmt.Errorf("respons AI kosong"))
		s.log(fmt.Sprintf("AI menghasilkan respons kosong untuk %s", compactChatID(chatID)), "warning")
		return
	}

	if err := s.sendReply(client, chatJID, cleaned, settings.DelayMs); err != nil {
		s.setFailure(err)
		s.log(fmt.Sprintf("AI gagal mengirim balasan ke %s: %v", compactChatID(chatID), err), "error")
		return
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

func (s *Service) generateReply(ctx context.Context, settings Settings, history []chatTurn, userText string) (string, error) {
	apiKey := effectiveAPIKey(settings)
	if apiKey == "" {
		return "", fmt.Errorf("api key AI belum diisi")
	}

	systemPrompt := buildSystemPrompt(settings)
	messages := make([]map[string]string, 0, len(history)+2)
	messages = append(messages, map[string]string{
		"role":    "system",
		"content": systemPrompt,
	})
	for _, turn := range clampHistory(history, settings.MaxHistory) {
		messages = append(messages, map[string]string{
			"role":    turn.Role,
			"content": turn.Content,
		})
	}
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": userText,
	})

	payload := map[string]interface{}{
		"model":       config.WhatsappAIModel,
		"messages":    messages,
		"max_tokens":  config.WhatsappAIMaxTokens,
		"temperature": 1,
		"top_p":       1,
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

func (s *Service) extractImageInsight(ctx context.Context, settings Settings, imageData []byte, mimeType, caption string) (string, string, error) {
	text, err := s.runVisionAnalysis(ctx, settings, imageData, mimeType, caption)
	if err != nil {
		return "", "", err
	}
	text = cleanVisionText(text)
	if strings.TrimSpace(text) == "" {
		return "", "", fmt.Errorf("vision tidak menghasilkan analisa gambar")
	}
	return text, "ai vision", nil
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

	prompt := "Analisa gambar ini. Jika ada teks pada gambar, salin teks pentingnya dengan akurat. Jika ini screenshot/chat/promosi/produk, ringkas poin pentingnya dalam bahasa Indonesia."
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

func buildSystemPrompt(settings Settings) string {
	noFormatRule := "ATURAN FORMAT: Kamu boleh menebalkan kata atau frasa penting memakai format WhatsApp *seperti ini*. Jangan gunakan **, _, #, atau markdown lain. Bold secukupnya saja, maksimal 2-3 frasa per pesan. Gunakan tanda hubung (-) untuk daftar poin."
	base := settings.Instruction
	if strings.TrimSpace(base) == "" {
		base = "Jawab dengan singkat, jelas, natural, dan sopan dalam bahasa Indonesia."
	}
	parts := []string{strings.TrimSpace(base)}
	if strings.TrimSpace(settings.ProductInfo) != "" {
		parts = append(parts, "Info Produk:\n"+strings.TrimSpace(settings.ProductInfo))
	}
	parts = append(parts, noFormatRule)
	return strings.Join(parts, "\n\n")
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
