package whatsapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/config"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

const (
	accountMetaPrefKey   = "wa_accounts_meta"
	activeAccountPrefKey = "wa_active_account_id"
)

type PreferenceStore interface {
	GetPref(key string) string
	SetPref(key, value string) error
	GetPrefJSON(key string, target interface{}) error
	SetPrefJSON(key string, value interface{}) error
}

type SessionEventHandler func(accountID string, evt interface{}, client *whatsmeow.Client)

type AccountMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	JID       string    `json:"jid"`
	CreatedAt time.Time `json:"created_at"`
}

type AccountInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	JID       string    `json:"jid"`
	Connected bool      `json:"connected"`
	LoggedIn  bool      `json:"logged_in"`
	Active    bool      `json:"active"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	IsPending bool      `json:"is_pending"`
	Phone     string    `json:"phone"`
}

type Session struct {
	meta   AccountMeta
	device *store.Device
	client *whatsmeow.Client
}

type Manager struct {
	mu           sync.RWMutex
	container    *sqlstore.Container
	prefs        PreferenceStore
	eventHandler SessionEventHandler
	sessions     map[string]*Session
	activeID     string
}

func NewManager(ctx context.Context, container *sqlstore.Container, prefs PreferenceStore, eventHandler SessionEventHandler) (*Manager, error) {
	m := &Manager{
		container:    container,
		prefs:        prefs,
		eventHandler: eventHandler,
		sessions:     make(map[string]*Session),
	}
	if err := m.loadSessions(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) loadSessions(ctx context.Context) error {
	if m.container == nil {
		return fmt.Errorf("container not initialized")
	}

	var metas []AccountMeta
	if m.prefs != nil {
		_ = m.prefs.GetPrefJSON(accountMetaPrefKey, &metas)
		m.activeID = strings.TrimSpace(m.prefs.GetPref(activeAccountPrefKey))
	}

	metaByJID := make(map[string]AccountMeta, len(metas))
	for _, meta := range metas {
		if meta.JID != "" {
			metaByJID[meta.JID] = meta
		}
	}

	devices, err := m.container.GetAllDevices(ctx)
	if err != nil {
		return err
	}

	for idx, device := range devices {
		if device == nil {
			continue
		}

		meta := AccountMeta{
			ID:        "",
			Name:      "",
			JID:       "",
			CreatedAt: time.Now(),
		}

		if device.ID != nil {
			meta.JID = device.ID.String()
			if saved, ok := metaByJID[meta.JID]; ok {
				meta = saved
			} else {
				meta.ID = defaultAccountID(idx, device.ID.String())
				meta.Name = fmt.Sprintf("Akun WA %d", idx+1)
				meta.CreatedAt = time.Now()
			}
			if meta.ID == "" {
				meta.ID = defaultAccountID(idx, meta.JID)
			}
			if meta.Name == "" {
				meta.Name = fmt.Sprintf("Akun WA %d", idx+1)
			}
		}

		session := &Session{
			meta:   meta,
			device: device,
		}
		session.client = m.newClient(session)
		m.sessions[session.meta.ID] = session
	}

	if m.activeID == "" {
		for _, acc := range m.ListAccounts() {
			m.activeID = acc.ID
			break
		}
	}
	if _, ok := m.sessions[m.activeID]; !ok {
		m.activeID = ""
		for _, acc := range m.ListAccounts() {
			m.activeID = acc.ID
			break
		}
	}
	m.saveStateLocked()
	return nil
}

func (m *Manager) newClient(session *Session) *whatsmeow.Client {
	clientLog := waLog.Stdout("Client", config.WhatsappLogLevel, true)
	client := whatsmeow.NewClient(session.device, clientLog)
	client.AddEventHandler(func(evt interface{}) {
		m.onEvent(session.meta.ID, evt, client)
	})
	return client
}

func (m *Manager) onEvent(accountID string, evt interface{}, client *whatsmeow.Client) {
	m.mu.Lock()
	session := m.sessions[accountID]
	if session != nil && client != nil && client.Store != nil && client.Store.ID != nil {
		if session.meta.JID != client.Store.ID.String() {
			session.meta.JID = client.Store.ID.String()
		}
		if session.meta.Name == "" {
			session.meta.Name = fmt.Sprintf("Akun %s", client.Store.ID.User)
		}
		m.saveStateLocked()
	}
	m.mu.Unlock()

	if m.eventHandler != nil {
		m.eventHandler(accountID, evt, client)
	}
}

func (m *Manager) saveStateLocked() {
	if m.prefs == nil {
		return
	}

	metas := make([]AccountMeta, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil || session.meta.JID == "" {
			continue
		}
		metas = append(metas, session.meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].CreatedAt.Before(metas[j].CreatedAt)
	})
	_ = m.prefs.SetPrefJSON(accountMetaPrefKey, metas)
	_ = m.prefs.SetPref(activeAccountPrefKey, m.activeID)
}

func (m *Manager) AutoConnectAll() {
	for _, account := range m.ListAccounts() {
		if account.JID == "" {
			continue
		}
		go func(accountID string) {
			_ = m.Connect(context.Background(), accountID)
		}(account.ID)
	}
}

func (m *Manager) CreateAccount(name string) (AccountInfo, error) {
	return m.createAccount("", name)
}

func (m *Manager) CreateAccountWithID(accountID, name string) (AccountInfo, error) {
	return m.createAccount(accountID, name)
}

func (m *Manager) createAccount(accountID, name string) (AccountInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := strings.TrimSpace(accountID)
	if id == "" {
		id = fmt.Sprintf("acc-%d", time.Now().UnixNano())
	}
	if _, exists := m.sessions[id]; exists {
		return AccountInfo{}, fmt.Errorf("account already exists")
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("Akun WA %d", len(m.sessions)+1)
	}

	session := &Session{
		meta: AccountMeta{
			ID:        id,
			Name:      strings.TrimSpace(name),
			CreatedAt: time.Now(),
		},
		device: m.container.NewDevice(),
	}
	session.client = m.newClient(session)
	m.sessions[id] = session
	if m.activeID == "" {
		m.activeID = id
	}
	m.saveStateLocked()
	return m.accountInfoLocked(session), nil
}

func (m *Manager) RenameAccount(accountID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[accountID]
	if !ok {
		return fmt.Errorf("account not found")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	session.meta.Name = name
	m.saveStateLocked()
	return nil
}

func (m *Manager) DeleteAccount(ctx context.Context, accountID string) error {
	m.mu.Lock()
	session, ok := m.sessions[accountID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("account not found")
	}
	delete(m.sessions, accountID)
	if m.activeID == accountID {
		m.activeID = ""
		for _, session := range m.sessions {
			m.activeID = session.meta.ID
			break
		}
	}
	m.saveStateLocked()
	m.mu.Unlock()

	if session.client != nil && session.client.IsConnected() {
		session.client.Disconnect()
	}
	if session.device != nil && session.device.ID != nil {
		_ = session.device.Delete(ctx)
	}
	return nil
}

func (m *Manager) SetActiveAccount(accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if accountID == "" {
		m.activeID = ""
		m.saveStateLocked()
		return nil
	}
	if _, ok := m.sessions[accountID]; !ok {
		return fmt.Errorf("account not found")
	}
	m.activeID = accountID
	m.saveStateLocked()
	return nil
}

func (m *Manager) GetActiveAccountID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeID
}

func (m *Manager) ResolveAccountID(accountID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if accountID != "" {
		return accountID
	}
	return m.activeID
}

func (m *Manager) GetClient(accountID string) *whatsmeow.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[m.resolveLocked(accountID)]
	if !ok {
		return nil
	}
	return session.client
}

func (m *Manager) GetSession(accountID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[m.resolveLocked(accountID)]
}

func (m *Manager) GetAccount(accountID string) (AccountInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[m.resolveLocked(accountID)]
	if !ok {
		return AccountInfo{}, fmt.Errorf("account not found")
	}
	return m.accountInfoLocked(session), nil
}

func (m *Manager) ListAccounts() []AccountInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	accounts := make([]AccountInfo, 0, len(m.sessions))
	for _, session := range m.sessions {
		accounts = append(accounts, m.accountInfoLocked(session))
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].CreatedAt.Before(accounts[j].CreatedAt)
	})
	return accounts
}

func (m *Manager) Connect(ctx context.Context, accountID string) error {
	client := m.GetClient(accountID)
	if client == nil {
		return fmt.Errorf("account not found")
	}
	if client.IsConnected() {
		return nil
	}
	return client.Connect()
}

func (m *Manager) Disconnect(accountID string) error {
	client := m.GetClient(accountID)
	if client == nil {
		return fmt.Errorf("account not found")
	}
	if client.IsConnected() {
		client.Disconnect()
	}
	return nil
}

func (m *Manager) resolveLocked(accountID string) string {
	if accountID != "" {
		return accountID
	}
	return m.activeID
}

func (m *Manager) accountInfoLocked(session *Session) AccountInfo {
	info := AccountInfo{
		ID:        session.meta.ID,
		Name:      session.meta.Name,
		JID:       session.meta.JID,
		CreatedAt: session.meta.CreatedAt,
		Active:    session.meta.ID == m.activeID,
		IsPending: session.meta.JID == "",
		Status:    "Belum login",
	}
	if session.client != nil {
		info.Connected = session.client.IsConnected()
		info.LoggedIn = session.client.IsLoggedIn()
		switch {
		case info.Connected && info.LoggedIn:
			info.Status = "Online"
		case info.Connected:
			info.Status = "Terhubung"
		case info.IsPending:
			info.Status = "Siap scan QR"
		default:
			info.Status = "Offline"
		}
	}
	if session.client != nil && session.client.Store != nil && session.client.Store.ID != nil {
		info.Phone = session.client.Store.ID.User
		if info.JID == "" {
			info.JID = session.client.Store.ID.String()
		}
	}
	return info
}

func defaultAccountID(index int, jid string) string {
	if jid != "" {
		return strings.NewReplacer(":", "-", "@", "-", ".", "-").Replace(jid)
	}
	return fmt.Sprintf("acc-%d", index+1)
}
