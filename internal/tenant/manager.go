package tenant

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/ai"
	"github.com/azkazamdigital/wa-gateway/internal/broadcast"
	"github.com/azkazamdigital/wa-gateway/internal/chathistory"
	"github.com/azkazamdigital/wa-gateway/internal/storage"
	"github.com/azkazamdigital/wa-gateway/internal/warming"
	webhookpkg "github.com/azkazamdigital/wa-gateway/internal/webhook"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

type Tenant struct {
	User        storage.AppUser
	BaseDir     string
	Store       *storage.Storage
	AI          *ai.Service
	ChatHistory *chathistory.Service
	Scheduler   *broadcast.Scheduler
	Warming     *warming.Service
}

type Manager struct {
	mu          sync.Mutex
	systemStore *storage.Storage
	baseDir     string
	logFn       func(string, string)
	tenants     map[string]*Tenant
}

func NewManager(systemStore *storage.Storage, baseDir string, logFn func(string, string)) *Manager {
	return &Manager{
		systemStore: systemStore,
		baseDir:     baseDir,
		logFn:       logFn,
		tenants:     make(map[string]*Tenant),
	}
}

func (m *Manager) Get(user storage.AppUser) (*Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tenantCtx, ok := m.tenants[user.ID]; ok && tenantCtx != nil {
		tenantCtx.User = user
		return tenantCtx, nil
	}

	dir := filepath.Join(m.baseDir, user.ID)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return nil, err
	}
	if err := m.migrateLegacyIfNeeded(user, dir); err != nil {
		return nil, err
	}

	store, err := storage.New(filepath.Join(dir, "app.db"))
	if err != nil {
		return nil, err
	}
	aiSvc := ai.NewService(m.logFn)
	aiSvc.SetAssetDir(filepath.Join(dir, "ai-products"))
	_ = aiSvc.Load(store)
	chatSvc := chathistory.NewService(store)

	tenantCtx := &Tenant{
		User:        user,
		BaseDir:     dir,
		Store:       store,
		AI:          aiSvc,
		ChatHistory: chatSvc,
		Warming:     warming.NewService(user.ID, store, m.logFn),
	}

	eng := broadcast.GetEngineForUser(user.ID)
	eng.SetLogHandler(func(msg, level string) {
		if m.logFn != nil {
			m.logFn(msg, level)
		}
	})
	eng.SetDoneHandler(func(completion broadcast.Completion) {
		if completion.Meta.OwnerID == "" {
			return
		}
		currentUser, err := m.systemStore.GetUserByID(completion.Meta.OwnerID)
		if err == nil {
			tenantCtx.User = currentUser
		}

		accountName := completion.Meta.AccountName
		if accountName == "" {
			accountName = completion.Meta.AccountID
		}
		duration := time.Since(completion.Progress.StartedAt).Round(time.Second).String()
		if duration == "0s" {
			duration = "0s"
		}
		if completion.Progress.Total > 0 {
			_ = tenantCtx.Store.SaveBroadcast(&storage.BroadcastRecord{
				Date:     time.Now(),
				Account:  accountName,
				Total:    completion.Progress.Total,
				Sent:     completion.Progress.Sent,
				Failed:   completion.Progress.Failed,
				Message:  completion.Meta.Message,
				Duration: duration,
				Type:     completion.Meta.Type,
			})
		}
		if tenantCtx.Scheduler != nil {
			tenantCtx.Scheduler.HandleCompletion(completion)
		}
	})

	handler := func(accountID string, evt interface{}, client *whatsmeow.Client) {
		account, _ := whatsapp.GetAccountForUser(user.ID, accountID)
		currentUser, err := m.systemStore.GetUserByID(user.ID)
		if err == nil {
			tenantCtx.User = currentUser
		}
		switch v := evt.(type) {
		case *events.Message:
			isUnsubscribeKeyword, unsubscribeSettings := tenantCtx.ChatHistory.HandleUnsubscribeMessage(accountID, account.Name, v)
			if isUnsubscribeKeyword {
				reply := strings.TrimSpace(unsubscribeSettings.AutoReply)
				if reply != "" {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
						defer cancel()
						if err := whatsapp.SendTextForUserAccount(ctx, user.ID, accountID, v.Info.Chat, reply); err != nil && m.logFn != nil {
							m.logFn(fmt.Sprintf("Gagal kirim balasan unsubscribe ke %s: %v", v.Info.Chat.User, err), "warning")
						}
					}()
				}
				if m.logFn != nil {
					m.logFn(fmt.Sprintf("unsubscribe_updated:%s", v.Info.Chat.User), "unsubscribe")
				}
			}
			if tenantCtx.User.CanUseAI && !isUnsubscribeKeyword {
				tenantCtx.AI.HandleEvent(accountID, v, client)
			}
			tenantCtx.ChatHistory.HandleMessage(accountID, account.Name, v)
			if account.WebhookEnabled && account.WebhookURL != "" {
				go func(user storage.AppUser, account whatsapp.AccountInfo, evt *events.Message) {
					ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
					defer cancel()
					if err := webhookpkg.DeliverMessage(ctx, user, account, evt); err != nil && m.logFn != nil {
						m.logFn(fmt.Sprintf("Webhook %s gagal: %v", account.Name, err), "warning")
					}
				}(tenantCtx.User, account, v)
			}
		case *events.HistorySync:
			tenantCtx.ChatHistory.HandleHistorySync(accountID, account.Name, v.Data)
		}
	}

	dbURI := sqliteURI(filepath.Join(dir, "whatsapp.db"))
	mgr, err := whatsapp.InitManagerForUser(context.Background(), user.ID, dbURI, store, handler)
	if err != nil {
		return nil, err
	}
	mgr.AutoConnectAll()

	tenantCtx.Scheduler = broadcast.InitSchedulerForUser(user.ID, store, eng, m.logFn)
	m.tenants[user.ID] = tenantCtx
	return tenantCtx, nil
}

func sqliteURI(path string) string {
	safe := filepath.ToSlash(path)
	pragmas := "?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"
	if strings.HasPrefix(safe, "/") {
		return "file:" + safe + pragmas
	}
	return fmt.Sprintf("file:%s%s", safe, pragmas)
}

func (m *Manager) migrateLegacyIfNeeded(user storage.AppUser, dir string) error {
	if !user.IsAdmin {
		return nil
	}
	legacyApp := filepath.Join(filepath.Dir(m.baseDir), "app.db")
	legacyWA := filepath.Join(filepath.Dir(m.baseDir), "whatsapp.db")
	targetApp := filepath.Join(dir, "app.db")
	targetWA := filepath.Join(dir, "whatsapp.db")

	if _, err := os.Stat(targetApp); os.IsNotExist(err) {
		if _, err := os.Stat(legacyApp); err == nil {
			if err := copyFile(legacyApp, targetApp); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(targetWA); os.IsNotExist(err) {
		if _, err := os.Stat(legacyWA); err == nil {
			if err := copyFile(legacyWA, targetWA); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
