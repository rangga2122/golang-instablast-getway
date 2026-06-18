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

// maxResultsInMemory limits how many result entries we keep in RAM.
// Counts (Sent/Failed) are always accurate; only the detail list is trimmed.
const maxResultsInMemory = 200

// maxConsecutiveFailures triggers an automatic reconnect when this many
// sends fail in a row, before the engine gives up on the next batch.
const maxConsecutiveFailures = 5

// sendRetries is how many times a single send is retried on transient errors.
const sendRetries = 2

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
	// Return a copy with trimmed results to avoid large payloads
	p := e.progress
	if len(p.Results) > maxResultsInMemory {
		p.Results = p.Results[len(p.Results)-maxResultsInMemory:]
	}
	return p
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

// ---------- helpers ----------

// contextSleep sleeps for d but returns early if ctx is cancelled or the
// broadcast status moves to Done.  Returns true when interrupted.
func (e *Engine) contextSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-t.C:
			return false
		default:
		}
		// Also honour pause / stop while sleeping
		e.mu.Lock()
		st := e.progress.Status
		e.mu.Unlock()
		if st == StatusDone {
			return true
		}
		if st == StatusPaused {
			// small tick while paused – re-check every 500ms
			select {
			case <-ctx.Done():
				return true
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		// Normal path: block until timer or cancel
		select {
		case <-ctx.Done():
			return true
		case <-t.C:
			return false
		}
	}
}

// waitForPause blocks while the engine is paused, respecting cancellation.
// Returns true if the broadcast should stop (cancelled or Done).
func (e *Engine) waitForPause(ctx context.Context) bool {
	for {
		e.mu.Lock()
		st := e.progress.Status
		e.mu.Unlock()
		if st == StatusDone {
			return true
		}
		if st != StatusPaused {
			return false
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ensureConnectionHealthy proactively checks and restores the WhatsApp
// connection.  Returns nil when the account is ready to send.
func (e *Engine) ensureConnectionHealthy(ctx context.Context, ownerID, accountID string) error {
	if whatsapp.IsClientConnectedForAccountForUser(ownerID, accountID) {
		return nil
	}
	e.log("Koneksi WhatsApp terdeteksi putus, mencoba menyambungkan ulang…", "warning")
	return whatsapp.EnsureClientConnectedForAccountForUser(ctx, ownerID, accountID)
}

// forceReconnectAfterFailure tears down and rebuilds the WhatsApp connection
// unconditionally.  After QR re-pairing the client often reports IsConnected()
// = true while the underlying websocket is already dead, so a health check
// alone is not enough — we must force a full reconnect cycle with a
// stabilisation delay.
func (e *Engine) forceReconnectAfterFailure(ctx context.Context, ownerID, accountID string) error {
	e.log("Force reconnect: memutuskan dan menyambungkan ulang koneksi WhatsApp…", "warning")
	reconnCtx, reconnCancel := context.WithTimeout(ctx, 60*time.Second)
	defer reconnCancel()
	if err := whatsapp.ForceReconnectForAccountForUser(reconnCtx, ownerID, accountID); err != nil {
		return fmt.Errorf("force reconnect gagal: %w", err)
	}
	e.log("Force reconnect: koneksi WhatsApp berhasil disambungkan ulang ✓", "info")
	return nil
}

// sendWithRetry tries to send a message up to sendRetries+1 times.
// The whatsapp send path already serializes sends and performs a reconnect on
// retryable send failures. Avoid doing an additional forced reconnect here: it
// can tear down the websocket around whatsmeow's pre-send usync/device-list
// lock and produce "failed to acquire lock: context canceled" after QR pairing.
func (e *Engine) sendWithRetry(ctx context.Context, ownerID, accountID string, jid types.JID, mediaItems []MediaItem, msg string) error {
	var lastErr error
	for attempt := 0; attempt <= sendRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 5s, 10s. Keep the socket stable between
			// attempts; SendMessageForUserAccount handles reconnect internally.
			backoff := time.Duration(attempt*5) * time.Second
			e.log(fmt.Sprintf("Percobaan ulang ke-%d untuk %s — tunggu %ds…", attempt, jid.User, int(backoff.Seconds())), "info")
			if interrupted := e.contextSleep(ctx, backoff); interrupted {
				return fmt.Errorf("broadcast dibatalkan saat retry")
			}
		}
		lastErr = sendMediaAndMessage(ctx, ownerID, accountID, jid, mediaItems, msg)
		if lastErr == nil {
			return nil
		}
		errLower := strings.ToLower(lastErr.Error())
		// Only retry on transient / connection errors
		isTransient := strings.Contains(errLower, "not connected") ||
			strings.Contains(errLower, "connection") ||
			strings.Contains(errLower, "timeout") ||
			strings.Contains(errLower, "timed out") ||
			strings.Contains(errLower, "closed") ||
			strings.Contains(errLower, "websocket") ||
			strings.Contains(errLower, "server returned error") ||
			strings.Contains(errLower, "unavailable") ||
			strings.Contains(errLower, "context canceled") ||
			strings.Contains(errLower, "disconnected") ||
			strings.Contains(errLower, "reconnect") ||
			strings.Contains(errLower, "acquire lock") ||
			strings.Contains(errLower, "usync")
		if !isTransient {
			return lastErr // permanent error, no point retrying
		}
	}
	return lastErr
}

// recordResult appends a result to progress and trims the in-memory list.
func (e *Engine) recordResult(result Result) {
	e.mu.Lock()
	e.progress.Results = append(e.progress.Results, result)
	// Trim old results to save memory – counts stay accurate
	if len(e.progress.Results) > maxResultsInMemory {
		excess := len(e.progress.Results) - maxResultsInMemory
		e.progress.Results = e.progress.Results[excess:]
	}
	if result.Success {
		e.progress.Sent++
	} else {
		e.progress.Failed++
	}
	e.mu.Unlock()
}

// ---------- broadcast loops ----------

func (e *Engine) runBroadcast(ctx context.Context, cfg Config) {
	cancelled := false
	defer func() {
		e.finish(cancelled)
	}()

	// Pre-flight: ensure the account is connected, but do not force a reconnect
	// when it already reports online. Tearing down a freshly paired session right
	// before the first send can race with whatsmeow's usync/device-list lock.
	preCtx, preCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if err := e.ensureConnectionHealthy(preCtx, cfg.OwnerID, cfg.AccountID); err != nil {
		preCancel()
		e.log(fmt.Sprintf("Gagal memulai broadcast: koneksi WhatsApp tidak tersedia (%v)", err), "error")
		return
	}
	preCancel()

	mediaItems := normalizeMediaItems(cfg.Images, cfg.ImageData, cfg.ImageMime)
	contactRows := contactRowsByNumber(cfg.ContactRows)

	consecutiveFails := 0

	for i, number := range cfg.Numbers {
		// Check cancellation
		select {
		case <-ctx.Done():
			cancelled = true
			e.log("Broadcast dihentikan", "warning")
			return
		default:
		}

		// Wait if paused (context-aware)
		if shouldStop := e.waitForPause(ctx); shouldStop {
			cancelled = true
			return
		}

		e.mu.Lock()
		if e.progress.Status == StatusDone {
			e.mu.Unlock()
			return
		}
		e.progress.Current = i + 1
		e.progress.CurrentNum = number
		e.mu.Unlock()

		// Periodic health check: every 10 sends, verify connection
		if i > 0 && i%10 == 0 {
			healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := e.ensureConnectionHealthy(healthCtx, cfg.OwnerID, cfg.AccountID); err != nil {
				healthCancel()
				e.log(fmt.Sprintf("Health check gagal di pesan #%d: %v", i+1, err), "warning")
			} else {
				healthCancel()
			}
		}

		// Auto-reconnect after too many consecutive failures — force full
		// reconnect cycle with stabilisation, not just a health check.
		if consecutiveFails >= maxConsecutiveFailures {
			e.log(fmt.Sprintf("%d pengiriman berturut-turut gagal, force reconnect…", consecutiveFails), "warning")
			reconnCtx, reconnCancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := e.forceReconnectAfterFailure(reconnCtx, cfg.OwnerID, cfg.AccountID); err != nil {
				reconnCancel()
				e.log(fmt.Sprintf("Force reconnect gagal: %v — broadcast dilanjutkan", err), "error")
			} else {
				reconnCancel()
				e.log("Force reconnect berhasil, melanjutkan broadcast", "info")
				consecutiveFails = 0
			}
		}

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

		err := e.sendWithRetry(ctx, cfg.OwnerID, cfg.AccountID, jid, mediaItems, msg)

		result := Result{Number: number, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
			e.log(fmt.Sprintf("❌ Gagal kirim ke %s: %s", number, err.Error()), "error")
			consecutiveFails++
		} else {
			e.log(fmt.Sprintf("✅ Terkirim ke %s", number), "success")
			consecutiveFails = 0
		}

		e.recordResult(result)

		// Delay (context-aware)
		if i < len(cfg.Numbers)-1 {
			delay := e.calculateDelay(cfg.DelaySeconds, cfg.RandomDelay, cfg.DelayMin, cfg.DelayMax)

			// Burst pause
			if cfg.BurstEvery > 0 && cfg.BurstPause > 0 && (i+1)%cfg.BurstEvery == 0 {
				e.log(fmt.Sprintf("⏸ Burst pause %d detik setelah %d pesan", cfg.BurstPause, cfg.BurstEvery), "info")
				if interrupted := e.contextSleep(ctx, time.Duration(cfg.BurstPause)*time.Second); interrupted {
					cancelled = true
					return
				}
			} else {
				if interrupted := e.contextSleep(ctx, time.Duration(delay)*time.Second); interrupted {
					cancelled = true
					return
				}
			}
		}
	}

	e.log(fmt.Sprintf("✅ Broadcast selesai: %d terkirim, %d gagal", e.progress.Sent, e.progress.Failed), "success")
}

func (e *Engine) runPersonalBroadcast(ctx context.Context, cfg PersonalConfig) {
	cancelled := false
	defer func() {
		e.finish(cancelled)
	}()

	// Pre-flight: ensure connected without tearing down a freshly paired session.
	preCtx, preCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if err := e.ensureConnectionHealthy(preCtx, cfg.OwnerID, cfg.AccountID); err != nil {
		preCancel()
		e.log(fmt.Sprintf("Gagal memulai broadcast personalisasi: koneksi WhatsApp tidak tersedia (%v)", err), "error")
		return
	}
	preCancel()

	mediaItems := normalizeMediaItems(cfg.Images, cfg.ImageData, cfg.ImageMime)

	consecutiveFails := 0

	for i, row := range cfg.Data {
		select {
		case <-ctx.Done():
			cancelled = true
			e.log("Broadcast personalisasi dihentikan", "warning")
			return
		default:
		}

		// Wait if paused (context-aware)
		if shouldStop := e.waitForPause(ctx); shouldStop {
			cancelled = true
			return
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

		// Periodic health check: every 10 sends
		if i > 0 && i%10 == 0 {
			healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := e.ensureConnectionHealthy(healthCtx, cfg.OwnerID, cfg.AccountID); err != nil {
				healthCancel()
				e.log(fmt.Sprintf("Health check gagal di pesan #%d: %v", i+1, err), "warning")
			} else {
				healthCancel()
			}
		}

		// Auto-reconnect after too many consecutive failures — force full
		// reconnect cycle with stabilisation, not just a health check.
		if consecutiveFails >= maxConsecutiveFailures {
			e.log(fmt.Sprintf("%d pengiriman berturut-turut gagal, force reconnect…", consecutiveFails), "warning")
			reconnCtx, reconnCancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := e.forceReconnectAfterFailure(reconnCtx, cfg.OwnerID, cfg.AccountID); err != nil {
				reconnCancel()
				e.log(fmt.Sprintf("Force reconnect gagal: %v — broadcast dilanjutkan", err), "error")
			} else {
				reconnCancel()
				e.log("Force reconnect berhasil, melanjutkan broadcast", "info")
				consecutiveFails = 0
			}
		}

		// Replace placeholders in message
		msg := cfg.Message
		for key, value := range row {
			msg = strings.ReplaceAll(msg, "{"+key+"}", value)
		}
		if cfg.UseSpintax {
			msg = ProcessSpintax(msg)
		}

		jid := types.NewJID(number, types.DefaultUserServer)

		err := e.sendWithRetry(ctx, cfg.OwnerID, cfg.AccountID, jid, mediaItems, msg)

		result := Result{Number: number, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
			e.log(fmt.Sprintf("❌ Gagal kirim ke %s: %s", number, err.Error()), "error")
			consecutiveFails++
		} else {
			e.log(fmt.Sprintf("✅ Terkirim ke %s (%s)", number, row["nama"]), "success")
			consecutiveFails = 0
		}

		e.recordResult(result)

		// Delay (context-aware)
		if i < len(cfg.Data)-1 {
			delay := e.calculateDelay(cfg.DelaySeconds, cfg.RandomDelay, cfg.DelayMin, cfg.DelayMax)
			if cfg.BurstEvery > 0 && cfg.BurstPause > 0 && (i+1)%cfg.BurstEvery == 0 {
				e.log(fmt.Sprintf("⏸ Burst pause %d detik", cfg.BurstPause), "info")
				if interrupted := e.contextSleep(ctx, time.Duration(cfg.BurstPause)*time.Second); interrupted {
					cancelled = true
					return
				}
			} else {
				if interrupted := e.contextSleep(ctx, time.Duration(delay)*time.Second); interrupted {
					cancelled = true
					return
				}
			}
		}
	}

	e.log(fmt.Sprintf("✅ Broadcast personalisasi selesai: %d terkirim, %d gagal", e.progress.Sent, e.progress.Failed), "success")
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
