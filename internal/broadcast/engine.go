package broadcast

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow/types"
)

// Status represents broadcast status
type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusPaused  Status = "paused"
	StatusDone    Status = "done"
)

// Result represents a single send result
type Result struct {
	Number  string `json:"number"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// Progress represents broadcast progress
type Progress struct {
	OwnerID      string    `json:"owner_id"`
	CampaignName string    `json:"campaign_name,omitempty"`
	Status       Status    `json:"status"`
	Total        int       `json:"total"`
	Sent         int       `json:"sent"`
	Failed       int       `json:"failed"`
	Current      int       `json:"current"`
	CurrentNum   string    `json:"current_num"`
	Results      []Result  `json:"results"`
	StartedAt    time.Time `json:"started_at"`
}

type MediaItem struct {
	Data []byte `json:"-"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

// Config holds broadcast configuration
type Config struct {
	OwnerID      string              `json:"owner_id"`
	AccountID    string              `json:"account_id"`
	AccountName  string              `json:"account_name"`
	CampaignName string              `json:"campaign_name"`
	ScheduleID   int64               `json:"schedule_id"`
	Numbers      []string            `json:"numbers"`
	ContactRows  []map[string]string `json:"contact_rows,omitempty"`
	Message      string              `json:"message"`
	UseSpintax   bool                `json:"use_spintax"`
	ImageData    []byte              `json:"-"`
	ImageMime    string              `json:"image_mime"`
	Images       []MediaItem         `json:"-"`
	DelaySeconds int                 `json:"delay_seconds"`
	RandomDelay  bool                `json:"random_delay"`
	DelayMin     int                 `json:"delay_min"`
	DelayMax     int                 `json:"delay_max"`
	BurstEvery   int                 `json:"burst_every"`
	BurstPause   int                 `json:"burst_pause"`
}

// PersonalConfig holds personalized broadcast configuration
type PersonalConfig struct {
	OwnerID      string              `json:"owner_id"`
	AccountID    string              `json:"account_id"`
	AccountName  string              `json:"account_name"`
	CampaignName string              `json:"campaign_name"`
	ScheduleID   int64               `json:"schedule_id"`
	Data         []map[string]string `json:"data"`
	Message      string              `json:"message"`
	UseSpintax   bool                `json:"use_spintax"`
	ImageData    []byte              `json:"-"`
	ImageMime    string              `json:"image_mime"`
	Images       []MediaItem         `json:"-"`
	DelaySeconds int                 `json:"delay_seconds"`
	RandomDelay  bool                `json:"random_delay"`
	DelayMin     int                 `json:"delay_min"`
	DelayMax     int                 `json:"delay_max"`
	BurstEvery   int                 `json:"burst_every"`
	BurstPause   int                 `json:"burst_pause"`
}

// Engine manages broadcast operations
type Engine struct {
	mu       sync.Mutex
	progress Progress
	cancel   context.CancelFunc
	onLog    func(string, string) // message, level
	onDone   func(Completion)
	current  *JobMeta
}

type JobMeta struct {
	OwnerID      string
	AccountID    string
	AccountName  string
	CampaignName string
	Message      string
	Type         string
	ScheduleID   int64
}

type Completion struct {
	Meta      JobMeta  `json:"meta"`
	Progress  Progress `json:"progress"`
	Cancelled bool     `json:"cancelled"`
}

var (
	enginesMu sync.Mutex
	engines   = map[string]*Engine{}
)

func newEngine() *Engine {
	return &Engine{
		progress: Progress{Status: StatusIdle},
	}
}

// GetEngine returns the default broadcast engine.
func GetEngine() *Engine {
	return GetEngineForUser("")
}

// GetEngineForUser returns a broadcast engine scoped to one user.
func GetEngineForUser(userID string) *Engine {
	userID = strings.TrimSpace(userID)

	enginesMu.Lock()
	defer enginesMu.Unlock()

	if eng, ok := engines[userID]; ok && eng != nil {
		return eng
	}

	eng := newEngine()
	engines[userID] = eng
	return eng
}

// GetProgress returns current progress
func (e *Engine) GetProgress() Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.progress
}

// Start begins a broadcast
func (e *Engine) Start(cfg Config) error {
	e.mu.Lock()
	if e.progress.Status == StatusRunning {
		e.mu.Unlock()
		return fmt.Errorf("broadcast already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.current = &JobMeta{
		OwnerID:      cfg.OwnerID,
		AccountID:    cfg.AccountID,
		AccountName:  cfg.AccountName,
		CampaignName: strings.TrimSpace(cfg.CampaignName),
		Message:      cfg.Message,
		Type:         "broadcast",
		ScheduleID:   cfg.ScheduleID,
	}
	e.progress = Progress{
		OwnerID:      cfg.OwnerID,
		CampaignName: strings.TrimSpace(cfg.CampaignName),
		Status:       StatusRunning,
		Total:        len(cfg.Numbers),
		StartedAt:    time.Now(),
		Results:      []Result{},
	}
	e.mu.Unlock()

	go e.runBroadcast(ctx, cfg)
	return nil
}

// StartPersonal begins a personalized broadcast
func (e *Engine) StartPersonal(cfg PersonalConfig) error {
	e.mu.Lock()
	if e.progress.Status == StatusRunning {
		e.mu.Unlock()
		return fmt.Errorf("broadcast already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.current = &JobMeta{
		OwnerID:      cfg.OwnerID,
		AccountID:    cfg.AccountID,
		AccountName:  cfg.AccountName,
		CampaignName: strings.TrimSpace(cfg.CampaignName),
		Message:      cfg.Message,
		Type:         "personalisasi",
		ScheduleID:   cfg.ScheduleID,
	}
	e.progress = Progress{
		OwnerID:      cfg.OwnerID,
		CampaignName: strings.TrimSpace(cfg.CampaignName),
		Status:       StatusRunning,
		Total:        len(cfg.Data),
		StartedAt:    time.Now(),
		Results:      []Result{},
	}
	e.mu.Unlock()

	go e.runPersonalBroadcast(ctx, cfg)
	return nil
}

// Pause pauses the broadcast
func (e *Engine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.progress.Status == StatusRunning {
		e.progress.Status = StatusPaused
	}
}

// Resume resumes a paused broadcast
func (e *Engine) Resume() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.progress.Status == StatusPaused {
		e.progress.Status = StatusRunning
	}
}

// Stop stops the broadcast
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
	e.progress.Status = StatusDone
}

// SetLogHandler sets the log callback
func (e *Engine) SetLogHandler(handler func(string, string)) {
	e.onLog = handler
}

func (e *Engine) SetDoneHandler(handler func(Completion)) {
	e.onDone = handler
}

func (e *Engine) log(msg, level string) {
	logrus.Infof("[Broadcast] %s", msg)
	if e.onLog != nil {
		e.onLog(msg, level)
	}
}

func (e *Engine) runBroadcast(ctx context.Context, cfg Config) {
	cancelled := false
	defer func() {
		e.finish(cancelled)
	}()

	mediaItems := normalizeMediaItems(cfg.Images, cfg.ImageData, cfg.ImageMime)
	contactRows := contactRowsByNumber(cfg.ContactRows)

	for i, number := range cfg.Numbers {
		select {
		case <-ctx.Done():
			cancelled = true
			e.log("Broadcast dihentikan", "warning")
			return
		default:
		}

		// Wait if paused
		for {
			e.mu.Lock()
			st := e.progress.Status
			e.mu.Unlock()
			if st != StatusPaused {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		e.mu.Lock()
		if e.progress.Status == StatusDone {
			e.mu.Unlock()
			return
		}
		e.progress.Current = i + 1
		e.progress.CurrentNum = number
		e.mu.Unlock()

		// Process message with spintax
		msg := cfg.Message
		if row, ok := contactRows[normalizeBroadcastPhone(number)]; ok {
			msg = replaceBroadcastVariables(msg, row)
		}
		if cfg.UseSpintax {
			msg = ProcessSpintax(msg)
		}

		// Build JID
		jid := types.NewJID(number, types.DefaultUserServer)

		err := sendMediaAndMessage(ctx, cfg.OwnerID, cfg.AccountID, jid, mediaItems, msg)

		result := Result{Number: number, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
			e.log(fmt.Sprintf("âŒ Gagal kirim ke %s: %s", number, err.Error()), "error")
		} else {
			e.log(fmt.Sprintf("âœ… Terkirim ke %s", number), "success")
		}

		e.mu.Lock()
		e.progress.Results = append(e.progress.Results, result)
		if err == nil {
			e.progress.Sent++
		} else {
			e.progress.Failed++
		}
		e.mu.Unlock()

		// Delay
		if i < len(cfg.Numbers)-1 {
			delay := e.calculateDelay(cfg.DelaySeconds, cfg.RandomDelay, cfg.DelayMin, cfg.DelayMax)

			// Burst pause
			if cfg.BurstEvery > 0 && cfg.BurstPause > 0 && (i+1)%cfg.BurstEvery == 0 {
				e.log(fmt.Sprintf("â¸ Burst pause %d detik setelah %d pesan", cfg.BurstPause, cfg.BurstEvery), "info")
				time.Sleep(time.Duration(cfg.BurstPause) * time.Second)
			} else {
				time.Sleep(time.Duration(delay) * time.Second)
			}
		}
	}

	e.log(fmt.Sprintf("âœ… Broadcast selesai: %d terkirim, %d gagal", e.progress.Sent, e.progress.Failed), "success")
}

func (e *Engine) runPersonalBroadcast(ctx context.Context, cfg PersonalConfig) {
	cancelled := false
	defer func() {
		e.finish(cancelled)
	}()

	mediaItems := normalizeMediaItems(cfg.Images, cfg.ImageData, cfg.ImageMime)

	for i, row := range cfg.Data {
		select {
		case <-ctx.Done():
			cancelled = true
			e.log("Broadcast personalisasi dihentikan", "warning")
			return
		default:
		}

		// Wait if paused
		for {
			e.mu.Lock()
			st := e.progress.Status
			e.mu.Unlock()
			if st != StatusPaused {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		number := row["nomor"]
		if number == "" {
			continue
		}

		e.mu.Lock()
		if e.progress.Status == StatusDone {
			e.mu.Unlock()
			return
		}
		e.progress.Current = i + 1
		e.progress.CurrentNum = number
		e.mu.Unlock()

		// Replace placeholders in message
		msg := cfg.Message
		for key, value := range row {
			msg = strings.ReplaceAll(msg, "{"+key+"}", value)
		}
		if cfg.UseSpintax {
			msg = ProcessSpintax(msg)
		}

		jid := types.NewJID(number, types.DefaultUserServer)

		err := sendMediaAndMessage(ctx, cfg.OwnerID, cfg.AccountID, jid, mediaItems, msg)

		result := Result{Number: number, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
			e.log(fmt.Sprintf("âŒ Gagal kirim ke %s: %s", number, err.Error()), "error")
		} else {
			e.log(fmt.Sprintf("âœ… Terkirim ke %s (%s)", number, row["nama"]), "success")
		}

		e.mu.Lock()
		e.progress.Results = append(e.progress.Results, result)
		if err == nil {
			e.progress.Sent++
		} else {
			e.progress.Failed++
		}
		e.mu.Unlock()

		// Delay
		if i < len(cfg.Data)-1 {
			delay := e.calculateDelay(cfg.DelaySeconds, cfg.RandomDelay, cfg.DelayMin, cfg.DelayMax)
			if cfg.BurstEvery > 0 && cfg.BurstPause > 0 && (i+1)%cfg.BurstEvery == 0 {
				e.log(fmt.Sprintf("â¸ Burst pause %d detik", cfg.BurstPause), "info")
				time.Sleep(time.Duration(cfg.BurstPause) * time.Second)
			} else {
				time.Sleep(time.Duration(delay) * time.Second)
			}
		}
	}

	e.log(fmt.Sprintf("âœ… Broadcast personalisasi selesai: %d terkirim, %d gagal", e.progress.Sent, e.progress.Failed), "success")
}

func contactRowsByNumber(rows []map[string]string) map[string]map[string]string {
	result := make(map[string]map[string]string, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		number := firstRowValue(row, "nomor", "phone", "wa", "whatsapp", "no", "hp", "telepon")
		normalized := normalizeBroadcastPhone(number)
		if normalized == "" {
			continue
		}
		result[normalized] = row
	}
	return result
}

func firstRowValue(row map[string]string, keys ...string) string {
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

func replaceBroadcastVariables(message string, row map[string]string) string {
	for key, value := range row {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		message = strings.ReplaceAll(message, "{"+key+"}", value)
		message = strings.ReplaceAll(message, "{"+strings.ToLower(key)+"}", value)
	}
	return message
}

func normalizeBroadcastPhone(input string) string {
	var b strings.Builder
	for _, r := range input {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	phone := b.String()
	if strings.HasPrefix(phone, "08") {
		phone = "62" + phone[1:]
	}
	return phone
}

func (e *Engine) calculateDelay(delaySec int, random bool, min, max int) int {
	if random && min > 0 && max > min {
		return min + rand.Intn(max-min+1)
	}
	if delaySec > 0 {
		return delaySec
	}
	return 3
}

func (e *Engine) finish(cancelled bool) {
	e.mu.Lock()
	e.progress.Status = StatusDone
	progress := e.progress
	meta := JobMeta{}
	if e.current != nil {
		meta = *e.current
	}
	e.current = nil
	e.cancel = nil
	handler := e.onDone
	e.mu.Unlock()

	if handler != nil {
		handler(Completion{
			Meta:      meta,
			Progress:  progress,
			Cancelled: cancelled,
		})
	}
}

func normalizeMediaItems(items []MediaItem, legacyData []byte, legacyMime string) []MediaItem {
	result := make([]MediaItem, 0, len(items)+1)
	for _, item := range items {
		if len(item.Data) == 0 {
			continue
		}
		mime := strings.TrimSpace(item.Mime)
		if mime == "" {
			mime = "image/jpeg"
		}
		result = append(result, MediaItem{
			Data: item.Data,
			Mime: mime,
			Name: strings.TrimSpace(item.Name),
		})
	}
	if len(result) == 0 && len(legacyData) > 0 {
		mime := strings.TrimSpace(legacyMime)
		if mime == "" {
			mime = "image/jpeg"
		}
		result = append(result, MediaItem{
			Data: legacyData,
			Mime: mime,
		})
	}
	return result
}

func sendMediaAndMessage(ctx context.Context, ownerID, accountID string, jid types.JID, mediaItems []MediaItem, message string) error {
	switch len(mediaItems) {
	case 0:
		return whatsapp.SendTextForUserAccount(ctx, ownerID, accountID, jid, message)
	case 1:
		return whatsapp.SendMediaForUserAccount(ctx, ownerID, accountID, jid, mediaItems[0].Data, mediaItems[0].Mime, mediaItems[0].Name, message)
	default:
		for _, item := range mediaItems {
			if err := whatsapp.SendMediaForUserAccount(ctx, ownerID, accountID, jid, item.Data, item.Mime, item.Name, ""); err != nil {
				return err
			}
		}
		if strings.TrimSpace(message) == "" {
			return nil
		}
		return whatsapp.SendTextForUserAccount(ctx, ownerID, accountID, jid, message)
	}
}

// ProcessSpintax processes {option1|option2|option3} syntax
func ProcessSpintax(text string) string {
	re := regexp.MustCompile(`\{([^{}]+)\}`)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		inner := match[1 : len(match)-1]
		options := strings.Split(inner, "|")
		if len(options) <= 1 {
			return match // Not spintax, likely a placeholder
		}
		return options[rand.Intn(len(options))]
	})
}

// ParseNumbers cleans and deduplicates phone numbers
func ParseNumbers(input string) []string {
	re := regexp.MustCompile(`\+?\d{5,}`)
	matches := re.FindAllString(input, -1)

	seen := make(map[string]bool)
	var result []string
	numRe := regexp.MustCompile(`\D+`)

	for _, m := range matches {
		n := numRe.ReplaceAllString(m, "")
		if len(n) < 6 {
			continue
		}
		if strings.HasPrefix(n, "08") {
			n = "62" + n[1:]
		}
		if !seen[n] {
			seen[n] = true
			result = append(result, n)
		}
	}
	return result
}
