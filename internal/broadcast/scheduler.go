package broadcast

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
)

type Scheduler struct {
	mu      sync.Mutex
	userID  string
	store   *storage.Storage
	engine  *Engine
	onLog   func(string, string)
	ticker  *time.Ticker
	stopCh  chan struct{}
	running bool
}

var scheduler *Scheduler

func InitScheduler(store *storage.Storage, engine *Engine, onLog func(string, string)) *Scheduler {
	return InitSchedulerForUser("", store, engine, onLog)
}

func InitSchedulerForUser(userID string, store *storage.Storage, engine *Engine, onLog func(string, string)) *Scheduler {
	if store == nil || engine == nil {
		return nil
	}
	if userID == "" && scheduler != nil {
		return scheduler
	}
	s := &Scheduler{
		userID: userID,
		store:  store,
		engine: engine,
		onLog:  onLog,
		stopCh: make(chan struct{}),
	}
	if userID == "" {
		scheduler = s
	}
	go s.loop()
	return s
}

func GetScheduler() *Scheduler {
	return scheduler
}

func (s *Scheduler) loop() {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.ticker = time.NewTicker(15 * time.Second)
	defer s.ticker.Stop()

	s.checkDueSchedules()
	for {
		select {
		case <-s.ticker.C:
			s.checkDueSchedules()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Scheduler) checkDueSchedules() {
	if s == nil || s.store == nil || s.engine == nil {
		return
	}
	progress := s.engine.GetProgress()
	if progress.Status == StatusRunning || progress.Status == StatusPaused {
		return
	}

	due, err := s.store.GetDueBroadcastSchedules(time.Now(), 5)
	if err != nil {
		s.log(fmt.Sprintf("Scheduler gagal memuat jadwal: %v", err), "error")
		return
	}
	for _, rec := range due {
		claimed, err := s.store.ClaimBroadcastSchedule(rec.ID)
		if err != nil {
			s.log(fmt.Sprintf("Scheduler gagal claim jadwal #%d: %v", rec.ID, err), "error")
			continue
		}
		if !claimed {
			continue
		}
		if err := s.startSchedule(rec); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already running") {
				_ = s.store.UpdateBroadcastScheduleStatus(rec.ID, "pending", rec.Sent, rec.Failed, "menunggu worker tersedia")
				s.log(fmt.Sprintf("Jadwal %q menunggu worker user tersedia", scheduleLabel(rec)), "info")
				return
			}
			_ = s.store.UpdateBroadcastScheduleStatus(rec.ID, "failed", 0, 0, err.Error())
			s.log(fmt.Sprintf("Jadwal broadcast gagal dijalankan: %v", err), "error")
			continue
		}
		break
	}
}

func (s *Scheduler) startSchedule(rec storage.BroadcastSchedule) error {
	if !whatsapp.IsClientConnectedForAccountForUser(s.userID, rec.AccountID) {
		return fmt.Errorf("akun WhatsApp %s tidak terhubung", rec.AccountName)
	}
	if strings.TrimSpace(rec.ScheduleType) == "" {
		rec.ScheduleType = "broadcast"
	}

	if rec.ScheduleType == "personalisasi" {
		data, err := parsePersonalScheduleCSV(rec.CSVData)
		if err != nil {
			return err
		}
		settings, _ := s.store.GetUnsubscribeSettings()
		unsubscribed, _ := s.store.ListUnsubscribedContacts()
		data = filterPersonalRowsForScheduler(data, unsubscribed)
		if len(data) == 0 {
			return fmt.Errorf("semua nomor pada jadwal personalisasi sudah unsubscribe")
		}
		cfg := PersonalConfig{
			OwnerID:      s.userID,
			AccountID:    rec.AccountID,
			AccountName:  rec.AccountName,
			CampaignName: scheduleLabel(rec),
			ScheduleID:   rec.ID,
			Data:         data,
			Message:      appendSchedulerUnsubscribeInstruction(rec.Message, settings),
			UseSpintax:   rec.UseSpintax,
			DelaySeconds: rec.DelaySeconds,
			RandomDelay:  rec.RandomDelay,
			DelayMin:     rec.DelayMin,
			DelayMax:     rec.DelayMax,
			BurstEvery:   rec.BurstEvery,
			BurstPause:   rec.BurstPause,
		}
		images, err := decodeScheduledImages(rec.ImagesJSON, rec.ImageB64, rec.ImageMime)
		if err != nil {
			return err
		}
		if len(images) > 0 {
			cfg.Images = images
		}
		s.log(fmt.Sprintf("Menjalankan jadwal personalisasi \"%s\" untuk akun %s", scheduleLabel(rec), rec.AccountName), "info")
		return s.engine.StartPersonal(cfg)
	}

	numbers := ParseNumbers(rec.Numbers)
	unsubscribed, _ := s.store.ListUnsubscribedContacts()
	numbers = filterNumbersForScheduler(numbers, unsubscribed)
	if len(numbers) == 0 {
		return fmt.Errorf("jadwal tidak memiliki nomor valid setelah filter unsubscribe")
	}
	settings, _ := s.store.GetUnsubscribeSettings()
	cfg := Config{
		OwnerID:      s.userID,
		AccountID:    rec.AccountID,
		AccountName:  rec.AccountName,
		CampaignName: scheduleLabel(rec),
		ScheduleID:   rec.ID,
		Numbers:      numbers,
		Message:      appendSchedulerUnsubscribeInstruction(rec.Message, settings),
		UseSpintax:   rec.UseSpintax,
		DelaySeconds: rec.DelaySeconds,
		RandomDelay:  rec.RandomDelay,
		DelayMin:     rec.DelayMin,
		DelayMax:     rec.DelayMax,
		BurstEvery:   rec.BurstEvery,
		BurstPause:   rec.BurstPause,
	}
	images, err := decodeScheduledImages(rec.ImagesJSON, rec.ImageB64, rec.ImageMime)
	if err != nil {
		return err
	}
	if len(images) > 0 {
		cfg.Images = images
	}

	s.log(fmt.Sprintf("Menjalankan jadwal broadcast \"%s\" untuk akun %s", scheduleLabel(rec), rec.AccountName), "info")
	return s.engine.Start(cfg)
}

func parsePersonalScheduleCSV(raw string) ([]map[string]string, error) {
	reader := csv.NewReader(strings.NewReader(raw))
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("jadwal personalisasi memiliki CSV tidak valid")
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
	if len(data) == 0 {
		return nil, fmt.Errorf("jadwal personalisasi tidak memiliki data CSV valid")
	}
	return data, nil
}

func normalizePhone(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	input = strings.TrimPrefix(input, "+")
	var b strings.Builder
	for _, ch := range input {
		if ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func appendSchedulerUnsubscribeInstruction(message string, settings storage.UnsubscribeSettings) string {
	base := strings.TrimSpace(message)
	if !settings.Enabled {
		return base
	}
	instruction := strings.TrimSpace(settings.Instruction)
	if instruction == "" {
		return base
	}
	if strings.Contains(strings.ToLower(base), strings.ToLower(instruction)) {
		return base
	}
	if base == "" {
		return instruction
	}
	return base + "\n\n" + instruction
}

func filterNumbersForScheduler(numbers []string, unsubscribed []storage.UnsubscribedContact) []string {
	if len(unsubscribed) == 0 {
		return numbers
	}
	blocked := make(map[string]struct{}, len(unsubscribed))
	for _, item := range unsubscribed {
		phone := normalizePhone(item.Phone)
		if phone != "" {
			blocked[phone] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(numbers))
	for _, number := range numbers {
		normalized := normalizePhone(number)
		if normalized == "" {
			continue
		}
		if _, exists := blocked[normalized]; exists {
			continue
		}
		filtered = append(filtered, normalized)
	}
	return filtered
}

func filterPersonalRowsForScheduler(rows []map[string]string, unsubscribed []storage.UnsubscribedContact) []map[string]string {
	if len(unsubscribed) == 0 {
		return rows
	}
	blocked := make(map[string]struct{}, len(unsubscribed))
	for _, item := range unsubscribed {
		phone := normalizePhone(item.Phone)
		if phone != "" {
			blocked[phone] = struct{}{}
		}
	}
	filtered := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		phone := extractPersonalRowPhone(row)
		if phone == "" {
			continue
		}
		if _, exists := blocked[phone]; exists {
			continue
		}
		clone := make(map[string]string, len(row))
		for key, value := range row {
			clone[key] = value
		}
		for key := range clone {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "nomor", "phone", "no", "whatsapp", "wa", "hp", "telepon":
				clone[key] = phone
			}
		}
		filtered = append(filtered, clone)
	}
	return filtered
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

type scheduledImagePayload struct {
	Data string `json:"data"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

func decodeScheduledImages(rawJSON, legacyB64, legacyMime string) ([]MediaItem, error) {
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" {
		if strings.TrimSpace(legacyB64) == "" {
			return nil, nil
		}
		img, err := base64.StdEncoding.DecodeString(legacyB64)
		if err != nil {
			return nil, fmt.Errorf("image jadwal tidak valid: %w", err)
		}
		mime := strings.TrimSpace(legacyMime)
		if mime == "" {
			mime = "image/jpeg"
		}
		return []MediaItem{{Data: img, Mime: mime}}, nil
	}

	var payloads []scheduledImagePayload
	if err := json.Unmarshal([]byte(rawJSON), &payloads); err != nil {
		return nil, fmt.Errorf("images jadwal tidak valid: %w", err)
	}

	result := make([]MediaItem, 0, len(payloads))
	for _, payload := range payloads {
		if strings.TrimSpace(payload.Data) == "" {
			continue
		}
		img, err := base64.StdEncoding.DecodeString(payload.Data)
		if err != nil {
			return nil, fmt.Errorf("images jadwal tidak valid: %w", err)
		}
		mime := strings.TrimSpace(payload.Mime)
		if mime == "" {
			mime = "image/jpeg"
		}
		result = append(result, MediaItem{Data: img, Mime: mime, Name: strings.TrimSpace(payload.Name)})
	}
	return result, nil
}

func (s *Scheduler) SaveSchedule(rec *storage.BroadcastSchedule) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	if strings.TrimSpace(rec.Status) == "" {
		rec.Status = "pending"
	}
	if strings.TrimSpace(rec.ScheduleType) == "" {
		rec.ScheduleType = "broadcast"
	}
	if rec.ScheduleType == "personalisasi" {
		data, err := parsePersonalScheduleCSV(rec.CSVData)
		if err != nil {
			return err
		}
		rec.Total = len(data)
	} else {
		rec.Total = len(ParseNumbers(rec.Numbers))
	}
	return s.store.SaveBroadcastSchedule(rec)
}

func (s *Scheduler) ListSchedules(status, scheduleType string) ([]storage.BroadcastSchedule, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("scheduler not initialized")
	}
	return s.store.GetBroadcastSchedules(status, scheduleType)
}

func (s *Scheduler) DeleteSchedule(id int64) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	return s.store.DeleteBroadcastSchedule(id)
}

func (s *Scheduler) HandleCompletion(completion Completion) {
	if s == nil || s.store == nil || completion.Meta.ScheduleID == 0 {
		return
	}
	status := "done"
	lastError := ""
	if completion.Cancelled {
		status = "cancelled"
		lastError = "broadcast dihentikan"
	} else if completion.Progress.Failed > 0 && completion.Progress.Sent == 0 {
		status = "failed"
		lastError = "semua pengiriman gagal"
	}
	_ = s.store.UpdateBroadcastScheduleStatus(
		completion.Meta.ScheduleID,
		status,
		completion.Progress.Sent,
		completion.Progress.Failed,
		lastError,
	)
}

func (s *Scheduler) log(msg, level string) {
	if s.onLog != nil {
		s.onLog(msg, level)
	}
}

func scheduleLabel(rec storage.BroadcastSchedule) string {
	if strings.TrimSpace(rec.Name) != "" {
		return rec.Name
	}
	return fmt.Sprintf("Jadwal #%d", rec.ID)
}

func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stopCh)
}
