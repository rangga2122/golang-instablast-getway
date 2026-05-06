package warming

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	waTypes "go.mau.fi/whatsmeow/types"
)

const settingsPrefKey = "warming_up_settings"

type Settings struct {
	Enabled            bool     `json:"enabled"`
	DelayMinMinutes    int      `json:"delay_min_minutes"`
	DelayMaxMinutes    int      `json:"delay_max_minutes"`
	MessagesPerDay     int      `json:"messages_per_day"`
	SelectedAccountIDs []string `json:"selected_account_ids"`
	MessageTemplates   []string `json:"message_templates"`
}

type Status struct {
	Settings
	Running        bool      `json:"running"`
	SentToday      int       `json:"sent_today"`
	RemainingToday int       `json:"remaining_today"`
	LastRunAt      time.Time `json:"last_run_at"`
	NextRunAt      time.Time `json:"next_run_at"`
	LastError      string    `json:"last_error"`
	LastPair       string    `json:"last_pair"`
	LastMessage    string    `json:"last_message"`
	SelectedCount  int       `json:"selected_count"`
	EligibleCount  int       `json:"eligible_count"`
}

type Service struct {
	mu        sync.RWMutex
	userID    string
	store     *storage.Storage
	logFn     func(string, string)
	settings  Settings
	rng       *rand.Rand
	stopCh    chan struct{}
	running   bool
	sentToday int
	sentDate  string
	lastRunAt time.Time
	nextRunAt time.Time
	lastError string
	lastPair  string
	lastMsg   string
}

func DefaultSettings() Settings {
	return Settings{
		Enabled:         false,
		DelayMinMinutes: 5,
		DelayMaxMinutes: 60,
		MessagesPerDay:  50,
		MessageTemplates: []string{
			"Halo {{receiver_name}}, selamat pagi. Semoga harinya lancar ya.",
			"Pagi {{receiver_name}}, semoga aktivitas hari ini ramai dan lancar.",
			"Hai {{receiver_name}}, sudah sarapan belum pagi ini?",
			"Halo {{receiver_name}}, semoga kabarnya baik hari ini.",
			"Pagi {{receiver_name}}, semoga semuanya aman dan nyaman.",
			"Hai {{receiver_name}}, semoga hari ini lebih produktif ya.",
			"Selamat pagi {{receiver_name}}, semoga urusan hari ini dimudahkan.",
			"Halo {{receiver_name}}, semoga hari ini bawa kabar baik.",
			"Hai {{receiver_name}}, semoga cuacanya mendukung aktivitas hari ini.",
			"Pagi {{receiver_name}}, jangan lupa jaga kesehatan juga ya.",
			"Halo {{receiver_name}}, lagi sibuk apa hari ini?",
			"Hai {{receiver_name}}, semoga target hari ini cepat tercapai.",
			"Selamat siang {{receiver_name}}, semoga semua pekerjaan lancar.",
			"Halo {{receiver_name}}, siang ini semoga tetap semangat ya.",
			"Hai {{receiver_name}}, makan siang jangan telat ya.",
			"Siang {{receiver_name}}, semoga kondisi hari ini tetap nyaman.",
			"Halo {{receiver_name}}, semoga orderan atau kerjaan hari ini ramai.",
			"Hai {{receiver_name}}, semoga semuanya tetap terkontrol dengan baik.",
			"Siang {{receiver_name}}, semoga masih semangat jalani aktivitas.",
			"Halo {{receiver_name}}, semoga siang ini membawa hasil bagus.",
			"Hai {{receiver_name}}, semoga tidak terlalu capek hari ini.",
			"Selamat sore {{receiver_name}}, bagaimana aktivitas hari ini?",
			"Halo {{receiver_name}}, semoga sore ini tetap lancar ya.",
			"Hai {{receiver_name}}, semoga hari ini berjalan sesuai rencana.",
			"Sore {{receiver_name}}, semoga masih ada tenaga buat lanjut aktivitas.",
			"Halo {{receiver_name}}, semoga hari ini ada progres yang baik.",
			"Hai {{receiver_name}}, sore ini semoga lebih santai sedikit.",
			"Sore {{receiver_name}}, semoga semua urusan hari ini beres.",
			"Halo {{receiver_name}}, semoga perjalanan pulangnya nanti aman.",
			"Hai {{receiver_name}}, semoga sore hari ini tetap produktif.",
			"Selamat malam {{receiver_name}}, semoga harinya tadi lancar.",
			"Halo {{receiver_name}}, malam ini jangan lupa istirahat cukup ya.",
			"Hai {{receiver_name}}, semoga malamnya tenang dan nyaman.",
			"Malam {{receiver_name}}, semoga besok lebih ramai dan lancar.",
			"Halo {{receiver_name}}, semoga hari ini ditutup dengan kabar baik.",
			"Hai {{receiver_name}}, istirahat yang cukup malam ini ya.",
			"Malam {{receiver_name}}, semoga besok makin semangat lagi.",
			"Halo {{receiver_name}}, semoga semuanya tetap aman sampai malam ini.",
			"Hai {{receiver_name}}, semoga badan tidak terlalu lelah hari ini.",
			"Malam {{receiver_name}}, semoga besok bisa mulai dengan lebih fresh.",
			"Halo {{receiver_name}}, semoga minggu ini berjalan baik semuanya.",
			"Hai {{receiver_name}}, semoga awal pekannya lancar ya.",
			"Halo {{receiver_name}}, semoga pertengahan minggu tetap stabil.",
			"Hai {{receiver_name}}, semoga akhir pekan nanti bisa lebih santai.",
			"Halo {{receiver_name}}, semoga usaha dan aktivitasnya terus membaik.",
			"Hai {{receiver_name}}, semoga hari ini tetap penuh hal positif.",
			"Halo {{receiver_name}}, semoga chat ini jadi penyemangat kecil hari ini.",
			"Hai {{receiver_name}}, semoga apa yang dikerjakan hari ini hasilnya bagus.",
			"Halo {{receiver_name}}, semoga semuanya sehat dan lancar selalu.",
			"Hai {{receiver_name}}, semoga besok hari yang lebih baik lagi.",
		},
	}
}

func NewService(userID string, store *storage.Storage, logFn func(string, string)) *Service {
	svc := &Service{
		userID: userID,
		store:  store,
		logFn:  logFn,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	svc.settings = DefaultSettings()
	_ = svc.load()
	if svc.settings.Enabled {
		if err := svc.startLocked(); err != nil {
			svc.settings.Enabled = false
			svc.lastError = err.Error()
			_ = svc.persistLocked()
		}
	}
	return svc
}

func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetDailyIfNeededLocked()
	return s.statusLocked()
}

func (s *Service) SaveSettings(input Settings) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := normalizeSettings(input)
	s.settings = settings
	if settings.Enabled {
		if err := s.validateLocked(); err != nil {
			s.settings.Enabled = false
			s.lastError = err.Error()
			if persistErr := s.persistLocked(); persistErr != nil {
				return Status{}, persistErr
			}
			s.stopLocked()
			return s.statusLocked(), err
		}
	}
	if err := s.persistLocked(); err != nil {
		return Status{}, err
	}
	if settings.Enabled {
		if err := s.startLocked(); err != nil {
			s.lastError = err.Error()
			return s.statusLocked(), err
		}
	} else {
		s.stopLocked()
	}
	return s.statusLocked(), nil
}

func (s *Service) Start() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings.Enabled = true
	s.settings = normalizeSettings(s.settings)
	if err := s.validateLocked(); err != nil {
		s.settings.Enabled = false
		s.lastError = err.Error()
		return s.statusLocked(), err
	}
	if err := s.persistLocked(); err != nil {
		return Status{}, err
	}
	if err := s.startLocked(); err != nil {
		s.lastError = err.Error()
		return s.statusLocked(), err
	}
	return s.statusLocked(), nil
}

func (s *Service) Stop() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings.Enabled = false
	if err := s.persistLocked(); err != nil {
		return Status{}, err
	}
	s.stopLocked()
	return s.statusLocked(), nil
}

func (s *Service) load() error {
	if s.store == nil {
		return nil
	}
	settings := DefaultSettings()
	if err := s.store.GetPrefJSON(settingsPrefKey, &settings); err != nil {
		return err
	}
	s.settings = normalizeSettings(settings)
	return nil
}

func (s *Service) persistLocked() error {
	if s.store == nil {
		return nil
	}
	return s.store.SetPrefJSON(settingsPrefKey, s.settings)
}

func (s *Service) startLocked() error {
	if !s.settings.Enabled {
		return nil
	}
	if err := s.validateLocked(); err != nil {
		return err
	}
	s.stopLocked()
	s.stopCh = make(chan struct{})
	s.running = true
	s.lastError = ""
	go s.run(s.stopCh)
	return nil
}

func (s *Service) stopLocked() {
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.running = false
	s.nextRunAt = time.Time{}
}

func (s *Service) validateLocked() error {
	if len(s.settings.SelectedAccountIDs) < 2 {
		return fmt.Errorf("pilih minimal 2 akun WhatsApp untuk warming up")
	}
	if len(s.settings.MessageTemplates) == 0 {
		return fmt.Errorf("isi minimal 1 template pesan warming up")
	}
	if len(s.eligibleAccountsLocked()) < 2 {
		return fmt.Errorf("minimal 2 akun WhatsApp harus online dan sudah login")
	}
	return nil
}

func (s *Service) statusLocked() Status {
	eligible := s.eligibleAccountsLocked()
	status := Status{
		Settings:       s.settings,
		Running:        s.running && s.settings.Enabled,
		SentToday:      s.sentToday,
		RemainingToday: maxInt(s.settings.MessagesPerDay-s.sentToday, 0),
		LastRunAt:      s.lastRunAt,
		NextRunAt:      s.nextRunAt,
		LastError:      s.lastError,
		LastPair:       s.lastPair,
		LastMessage:    s.lastMsg,
		SelectedCount:  len(s.settings.SelectedAccountIDs),
		EligibleCount:  len(eligible),
	}
	return status
}

func (s *Service) run(stopCh chan struct{}) {
	for {
		s.mu.Lock()
		if stopCh != s.stopCh || !s.settings.Enabled {
			s.running = false
			s.mu.Unlock()
			return
		}
		s.resetDailyIfNeededLocked()
		settings := s.settings
		if s.sentToday >= settings.MessagesPerDay {
			next := nextDayStart()
			s.nextRunAt = next
			s.mu.Unlock()
			if !waitFor(stopCh, time.Until(next)) {
				s.mu.Lock()
				s.running = false
				s.mu.Unlock()
				return
			}
			continue
		}
		eligible := s.eligibleAccountsLocked()
		if len(eligible) < 2 {
			s.lastError = "Minimal 2 akun harus online untuk menjalankan warming up"
			s.nextRunAt = time.Now().Add(45 * time.Second)
			s.mu.Unlock()
			if !waitFor(stopCh, 45*time.Second) {
				s.mu.Lock()
				s.running = false
				s.mu.Unlock()
				return
			}
			continue
		}

		sender, receiver := pickPair(s.rng, eligible)
		template := pickRandom(s.rng, settings.MessageTemplates)
		replyTemplate := pickRandom(s.rng, settings.MessageTemplates)
		s.lastError = ""
		s.mu.Unlock()

		message := renderTemplate(template, sender, receiver)
		if err := s.sendWarmMessage(sender, receiver, message); err != nil {
			s.setError(err)
			if !waitFor(stopCh, 30*time.Second) {
				s.mu.Lock()
				s.running = false
				s.mu.Unlock()
				return
			}
			continue
		}
		s.markSent(sender, receiver, message)

		replyDelay := randomDurationSeconds(s.rng, 18, 55)
		if !waitFor(stopCh, replyDelay) {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return
		}

		s.mu.Lock()
		s.resetDailyIfNeededLocked()
		canReply := s.settings.Enabled && s.sentToday < s.settings.MessagesPerDay
		s.mu.Unlock()
		if canReply {
			reply := renderTemplate(replyTemplate, receiver, sender)
			if err := s.sendWarmMessage(receiver, sender, reply); err != nil {
				s.setError(err)
			} else {
				s.markSent(receiver, sender, reply)
			}
		}

		delay := time.Duration(randomIntInRange(s.rng, settings.DelayMinMinutes, settings.DelayMaxMinutes)) * time.Minute
		s.mu.Lock()
		s.nextRunAt = time.Now().Add(delay)
		s.mu.Unlock()
		if !waitFor(stopCh, delay) {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return
		}
	}
}

func (s *Service) sendWarmMessage(sender, receiver whatsapp.AccountInfo, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	jid := waTypes.NewJID(strings.TrimSpace(receiver.Phone), waTypes.DefaultUserServer)
	if err := whatsapp.SendTextForUserAccount(ctx, s.userID, sender.ID, jid, message); err != nil {
		return fmt.Errorf("gagal kirim dari %s ke %s: %w", sender.Name, receiver.Name, err)
	}
	if s.logFn != nil {
		s.logFn(fmt.Sprintf("Warming up: %s mengirim ke %s", sender.Name, receiver.Name), "info")
	}
	return nil
}

func (s *Service) markSent(sender, receiver whatsapp.AccountInfo, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetDailyIfNeededLocked()
	s.sentToday++
	s.lastRunAt = time.Now()
	s.lastPair = fmt.Sprintf("%s -> %s", sender.Name, receiver.Name)
	s.lastMsg = message
	s.lastError = ""
}

func (s *Service) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err.Error()
	if s.logFn != nil {
		s.logFn("Warming up error: "+err.Error(), "error")
	}
}

func (s *Service) resetDailyIfNeededLocked() {
	today := time.Now().Format("2006-01-02")
	if s.sentDate == today {
		return
	}
	s.sentDate = today
	s.sentToday = 0
}

func (s *Service) eligibleAccountsLocked() []whatsapp.AccountInfo {
	if len(s.settings.SelectedAccountIDs) == 0 {
		return nil
	}
	selected := make(map[string]struct{}, len(s.settings.SelectedAccountIDs))
	for _, id := range s.settings.SelectedAccountIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			selected[id] = struct{}{}
		}
	}
	accounts := whatsapp.ListAccountsForUser(s.userID)
	eligible := make([]whatsapp.AccountInfo, 0, len(accounts))
	for _, account := range accounts {
		if _, ok := selected[account.ID]; !ok {
			continue
		}
		if !account.Connected || !account.LoggedIn || strings.TrimSpace(account.Phone) == "" {
			continue
		}
		eligible = append(eligible, account)
	}
	return eligible
}

func normalizeSettings(input Settings) Settings {
	settings := DefaultSettings()
	settings.Enabled = input.Enabled
	if input.DelayMinMinutes > 0 {
		settings.DelayMinMinutes = input.DelayMinMinutes
	}
	if input.DelayMaxMinutes > 0 {
		settings.DelayMaxMinutes = input.DelayMaxMinutes
	}
	if settings.DelayMaxMinutes < settings.DelayMinMinutes {
		settings.DelayMaxMinutes = settings.DelayMinMinutes
	}
	if input.MessagesPerDay > 0 {
		settings.MessagesPerDay = input.MessagesPerDay
	}
	if settings.MessagesPerDay < 2 {
		settings.MessagesPerDay = 2
	}
	settings.SelectedAccountIDs = uniqueTrimmed(input.SelectedAccountIDs)
	settings.MessageTemplates = normalizeMessages(input.MessageTemplates)
	return settings
}

func normalizeMessages(items []string) []string {
	var messages []string
	for _, item := range items {
		for _, line := range strings.Split(strings.ReplaceAll(item, "\r", ""), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				messages = append(messages, line)
			}
		}
	}
	if len(messages) == 0 {
		messages = DefaultSettings().MessageTemplates
	}
	return messages
}

func uniqueTrimmed(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func renderTemplate(template string, sender, receiver whatsapp.AccountInfo) string {
	replacer := strings.NewReplacer(
		"{{sender_name}}", fallbackValue(sender.Name, sender.Phone),
		"{{sender_phone}}", strings.TrimSpace(sender.Phone),
		"{{receiver_name}}", fallbackValue(receiver.Name, receiver.Phone),
		"{{receiver_phone}}", strings.TrimSpace(receiver.Phone),
		"{{date}}", time.Now().Format("02-01-2006"),
		"{{time}}", time.Now().Format("15:04"),
	)
	return replacer.Replace(strings.TrimSpace(template))
}

func fallbackValue(primary, secondary string) string {
	primary = strings.TrimSpace(primary)
	if primary != "" {
		return primary
	}
	secondary = strings.TrimSpace(secondary)
	if secondary != "" {
		return secondary
	}
	return "Akun WhatsApp"
}

func pickPair(rng *rand.Rand, accounts []whatsapp.AccountInfo) (whatsapp.AccountInfo, whatsapp.AccountInfo) {
	if len(accounts) < 2 {
		return whatsapp.AccountInfo{}, whatsapp.AccountInfo{}
	}
	senderIdx := rng.Intn(len(accounts))
	receiverIdx := senderIdx
	for receiverIdx == senderIdx {
		receiverIdx = rng.Intn(len(accounts))
	}
	return accounts[senderIdx], accounts[receiverIdx]
}

func pickRandom(rng *rand.Rand, items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[rng.Intn(len(items))]
}

func randomIntInRange(rng *rand.Rand, min, max int) int {
	if max < min {
		max = min
	}
	if max == min {
		return min
	}
	return min + rng.Intn(max-min+1)
}

func randomDurationSeconds(rng *rand.Rand, min, max int) time.Duration {
	return time.Duration(randomIntInRange(rng, min, max)) * time.Second
}

func waitFor(stopCh chan struct{}, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func nextDayStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 1, 0, 0, now.Location())
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
