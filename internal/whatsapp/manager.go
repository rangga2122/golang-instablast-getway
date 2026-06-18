package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/config"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

const (
	accountMetaPrefKey   = "wa_accounts_meta"
	activeAccountPrefKey = "wa_active_account_id"

	connectionSupervisorInterval       = 2 * time.Minute
	connectionSupervisorTimeout        = 35 * time.Second
	connectionSupervisorUnhealthyGrace = 60 * time.Second
	connectionHealthyGrace             = 2 * time.Minute
	connectionReconnectBackoffMin      = 15 * time.Second
	connectionReconnectBackoffMax      = 10 * time.Minute
)

type PreferenceStore interface {
	GetPref(key string) string
	SetPref(key, value string) error
	GetPrefJSON(key string, target interface{}) error
	SetPrefJSON(key string, value interface{}) error
}

type SessionEventHandler func(accountID string, evt interface{}, client *whatsmeow.Client)

type AccountMeta struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	JID            string    `json:"jid"`
	CreatedAt      time.Time `json:"created_at"`
	WebhookEnabled bool      `json:"webhook_enabled"`
	WebhookURL     string    `json:"webhook_url"`
	WebhookSecret  string    `json:"webhook_secret,omitempty"`
}

type AccountInfo struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	JID               string    `json:"jid"`
	Connected         bool      `json:"connected"`
	LoggedIn          bool      `json:"logged_in"`
	Active            bool      `json:"active"`
	Status            string    `json:"status"`
	StatusReason      string    `json:"status_reason,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	IsPending         bool      `json:"is_pending"`
	Phone             string    `json:"phone"`
	WebhookEnabled    bool      `json:"webhook_enabled"`
	WebhookURL        string    `json:"webhook_url"`
	WebhookSecret     string    `json:"webhook_secret,omitempty"`
	LastConnectedAt   time.Time `json:"last_connected_at,omitempty"`
	ReconnectFailures int       `json:"reconnect_failures"`
	NextReconnectAt   time.Time `json:"next_reconnect_at,omitempty"`
}

type Session struct {
	meta      AccountMeta
	device    *store.Device
	client    *whatsmeow.Client
	connectMu sync.Mutex
	sendMu    sync.Mutex
	healthy   bool

	unhealthySince           time.Time
	nextReconnectAt          time.Time
	consecutiveReconnectFail int
	autoReconnectBlocked     bool
	autoReconnectBlockedTill time.Time
	supervisorReconnectRun   bool
	lastConnectedAt          time.Time
	lastHealthyAt            time.Time
	lastDisconnectReason     string
}

type Manager struct {
	ownerID          string
	mu               sync.RWMutex
	container        *sqlstore.Container
	prefs            PreferenceStore
	eventHandler     SessionEventHandler
	sessions         map[string]*Session
	activeID         string
	supervisorCancel context.CancelFunc
}

func NewManager(ctx context.Context, container *sqlstore.Container, prefs PreferenceStore, eventHandler SessionEventHandler) (*Manager, error) {
	configureClientIdentity()
	m := &Manager{
		container:    container,
		prefs:        prefs,
		eventHandler: eventHandler,
		sessions:     make(map[string]*Session),
	}
	if err := m.loadSessions(ctx); err != nil {
		return nil, err
	}
	supervisorCtx, cancel := context.WithCancel(ctx)
	m.supervisorCancel = cancel
	go m.connectionSupervisor(supervisorCtx)
	return m, nil
}

func (m *Manager) SetOwnerID(ownerID string) {
	m.mu.Lock()
	m.ownerID = strings.TrimSpace(ownerID)
	m.mu.Unlock()
}

func (m *Manager) log() *logrus.Entry {
	m.mu.RLock()
	ownerID := m.ownerID
	m.mu.RUnlock()
	entry := logrus.WithField("manager_id", ownerID)
	if ownerID == "" {
		entry = logrus.WithField("manager_id", "default")
	}
	return entry
}

func configureClientIdentity() {
	store.SetOSInfo("Chrome", [3]uint32{143, 0, 0})
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
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
	// Disable whatsmeow's built-in auto-reconnect to prevent it from
	// fighting with our connection supervisor and EnsureConnected logic.
	// Our supervisor handles reconnection with proper backoff and mutex
	// coordination, which is safer during broadcasts.
	client.EnableAutoReconnect = false
	client.AutoTrustIdentity = true
	client.InitialAutoReconnect = false
	client.AddEventHandler(func(evt interface{}) {
		m.onEvent(session.meta.ID, evt, client)
	})
	return client
}

func (m *Manager) onEvent(accountID string, evt interface{}, client *whatsmeow.Client) {
	reconnectAfterPair := false
	m.mu.Lock()
	session := m.sessions[accountID]
	if session != nil {
		switch event := evt.(type) {
		case *events.Connected:
			m.markSessionHealthyLocked(session)
			session.lastConnectedAt = time.Now()
		case *events.PairSuccess:
			session.healthy = false
			session.lastDisconnectReason = "Menunggu reconnect setelah pairing"
			session.unhealthySince = time.Time{}
			session.nextReconnectAt = time.Time{}
			session.consecutiveReconnectFail = 0
			session.autoReconnectBlocked = false
			session.autoReconnectBlockedTill = time.Time{}
			reconnectAfterPair = true
		case *events.PairError:
			session.lastDisconnectReason = "Pairing gagal"
			m.markSessionUnhealthyLocked(session)
		case *events.KeepAliveRestored:
			m.markSessionHealthyLocked(session)
		case *events.KeepAliveTimeout:
			if event.ErrorCount >= 2 {
				session.lastDisconnectReason = fmt.Sprintf("Keepalive timeout (%d)", event.ErrorCount)
				m.markSessionUnhealthyLocked(session)
			}
		case *events.Disconnected, *events.StreamError:
			session.lastDisconnectReason = fmt.Sprintf("%T", evt)
			if session.lastHealthyAt.IsZero() || time.Since(session.lastHealthyAt) > connectionHealthyGrace {
				m.markSessionUnhealthyLocked(session)
			}
		case *events.LoggedOut:
			session.lastDisconnectReason = "WhatsApp logout/device dilepas"
			m.markSessionUnhealthyLocked(session)
			session.autoReconnectBlocked = true
		case *events.StreamReplaced:
			session.lastDisconnectReason = "Session dipakai di koneksi lain"
			m.markSessionUnhealthyLocked(session)
			session.autoReconnectBlocked = true
		case *events.TemporaryBan:
			session.lastDisconnectReason = fmt.Sprintf("Temporary ban %s", event.Expire)
			m.markSessionUnhealthyLocked(session)
			session.autoReconnectBlockedTill = time.Now().Add(event.Expire)
		case *events.ClientOutdated, *events.CATRefreshError:
			session.lastDisconnectReason = fmt.Sprintf("%T", evt)
			m.markSessionUnhealthyLocked(session)
			session.autoReconnectBlocked = true
		case *events.ConnectFailure:
			session.lastDisconnectReason = fmt.Sprintf("Connect failure %d %s", int(event.Reason), event.Message)
			m.markSessionUnhealthyLocked(session)
			if event.Reason.IsLoggedOut() {
				session.autoReconnectBlocked = true
			}
		}

		if client != nil && client.Store != nil && client.Store.ID != nil {
			if session.meta.JID != client.Store.ID.String() {
				session.meta.JID = client.Store.ID.String()
			}
			if session.meta.Name == "" {
				session.meta.Name = fmt.Sprintf("Akun %s", client.Store.ID.User)
			}
			m.saveStateLocked()
		}
	}
	m.mu.Unlock()

	if m.eventHandler != nil {
		m.eventHandler(accountID, evt, client)
	}
	if reconnectAfterPair {
		go m.reconnectAfterPair(accountID)
	}
}

func (m *Manager) reconnectAfterPair(accountID string) {
	// Give WhatsApp server enough time to finalize the pairing handshake
	// before we disconnect and reconnect. Too-early reconnects cause the
	// server to reject the session (stream-replaced / 400 errors).
	time.Sleep(4 * time.Second)

	// PairSuccess can happen while the UI immediately starts a broadcast.
	// Reconnect() disconnects the websocket before connecting again; doing that
	// concurrently with SendMessage makes whatsmeow fail the send path with
	// "failed to get device list", "context canceled", or "use of closed network
	// connection". Serialize this automatic post-pair reconnect with sends, just
	// like the connection supervisor does.
	session := m.getSession(accountID)
	if session == nil {
		return
	}
	session.sendMu.Lock()
	defer session.sendMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.Reconnect(ctx, accountID); err != nil {
		logrus.WithError(err).WithField("account_id", accountID).Warn("WhatsApp reconnect after QR pairing failed")
		// Try once more after a cooldown
		time.Sleep(5 * time.Second)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel2()
		if err2 := m.Reconnect(ctx2, accountID); err2 != nil {
			logrus.WithError(err2).WithField("account_id", accountID).Warn("WhatsApp second reconnect after pairing also failed")
			return
		}
	}
	logrus.WithField("account_id", accountID).Info("WhatsApp session reconnected after QR pairing")
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

func (m *Manager) Close() {
	if m.supervisorCancel != nil {
		m.supervisorCancel()
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

func (m *Manager) SetAccountWebhook(accountID string, enabled bool, webhookURL, secret string) (AccountInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[accountID]
	if !ok {
		return AccountInfo{}, fmt.Errorf("account not found")
	}

	session.meta.WebhookEnabled = enabled
	session.meta.WebhookURL = strings.TrimSpace(webhookURL)
	session.meta.WebhookSecret = strings.TrimSpace(secret)
	if session.meta.WebhookURL == "" {
		session.meta.WebhookEnabled = false
	}

	m.saveStateLocked()
	return m.accountInfoLocked(session), nil
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

	if session.client != nil {
		session.connectMu.Lock()
		if session.client.IsConnected() {
			session.client.Disconnect()
		}
		session.connectMu.Unlock()
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
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return fmt.Errorf("account not found")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	if session.client.IsConnected() {
		if session.client.IsLoggedIn() {
			m.setHealthy(accountID, true)
		}
		return nil
	}
	err := session.client.ConnectContext(ctx)
	if errors.Is(err, whatsmeow.ErrAlreadyConnected) {
		if session.client.IsLoggedIn() {
			m.setHealthy(accountID, true)
		}
		return nil
	}
	if err != nil {
		m.recordReconnectFailureWithReason(accountID, fmt.Sprintf("Connect gagal: %v", err))
		return err
	}
	if session.client.Store != nil && session.client.Store.ID != nil {
		if waitErr := m.waitUntilReady(ctx, accountID, session.client); waitErr != nil {
			m.recordReconnectFailureWithReason(accountID, fmt.Sprintf("Menunggu WhatsApp siap timeout: %v", waitErr))
			return waitErr
		}
	}
	return nil
}

func (m *Manager) Disconnect(accountID string) error {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return fmt.Errorf("account not found")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	m.setHealthy(accountID, false)
	if session.client.IsConnected() {
		session.client.Disconnect()
	}
	return nil
}

func (m *Manager) Logout(ctx context.Context, accountID string) error {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return fmt.Errorf("account not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m.container == nil {
		return fmt.Errorf("container not initialized")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	logoutErr := session.client.Logout(ctx)
	if logoutErr != nil {
		logrus.WithError(logoutErr).
			WithField("account_id", session.meta.ID).
			Warn("whatsapp logout request failed; clearing local session so QR login can be scanned again")
		session.client.Disconnect()
		if session.device != nil && session.device.ID != nil {
			if err := session.device.Delete(ctx); err != nil {
				return fmt.Errorf("logout failed (%v), and local session cleanup failed: %w", logoutErr, err)
			}
		}
	}

	m.mu.Lock()
	session.meta.JID = ""
	session.device = m.container.NewDevice()
	session.healthy = false
	session.unhealthySince = time.Time{}
	session.nextReconnectAt = time.Time{}
	session.consecutiveReconnectFail = 0
	session.autoReconnectBlocked = false
	session.autoReconnectBlockedTill = time.Time{}
	session.supervisorReconnectRun = false
	session.lastConnectedAt = time.Time{}
	session.client = m.newClient(session)
	m.saveStateLocked()
	m.mu.Unlock()
	return nil
}

func (m *Manager) ResetForPairing(ctx context.Context, accountID string) (AccountInfo, error) {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return AccountInfo{}, fmt.Errorf("account not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m.container == nil {
		return AccountInfo{}, fmt.Errorf("container not initialized")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	if session.client.IsConnected() {
		session.client.Disconnect()
	}
	if session.device != nil && session.device.ID != nil {
		if err := session.device.Delete(ctx); err != nil {
			return AccountInfo{}, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	session.meta.JID = ""
	session.device = m.container.NewDevice()
	session.healthy = false
	session.unhealthySince = time.Time{}
	session.nextReconnectAt = time.Time{}
	session.consecutiveReconnectFail = 0
	session.autoReconnectBlocked = false
	session.autoReconnectBlockedTill = time.Time{}
	session.supervisorReconnectRun = false
	session.lastConnectedAt = time.Time{}
	session.client = m.newClient(session)
	m.saveStateLocked()
	return m.accountInfoLocked(session), nil
}

func (m *Manager) PairCode(ctx context.Context, accountID, phone string) (string, AccountInfo, error) {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return "", AccountInfo{}, fmt.Errorf("account not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	phone = normalizePairPhone(phone)
	if len(phone) <= 6 {
		return "", AccountInfo{}, fmt.Errorf("nomor WhatsApp wajib memakai format internasional, contoh 628123456789")
	}
	if strings.HasPrefix(phone, "0") {
		return "", AccountInfo{}, fmt.Errorf("nomor WhatsApp jangan diawali 0, gunakan kode negara, contoh 628123456789")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	if session.client.IsLoggedIn() {
		m.mu.RLock()
		info := m.accountInfoLocked(session)
		m.mu.RUnlock()
		return "", info, fmt.Errorf("account already logged in")
	}
	if session.client.IsConnected() {
		session.client.Disconnect()
	}

	qrChan, err := session.client.GetQRChannel(ctx)
	if err != nil {
		return "", AccountInfo{}, err
	}
	if err := session.client.ConnectContext(ctx); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
		return "", AccountInfo{}, err
	}

	for {
		select {
		case <-ctx.Done():
			return "", AccountInfo{}, ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return "", AccountInfo{}, fmt.Errorf("QR pairing channel closed before pairing code was generated")
			}
			switch evt.Event {
			case whatsmeow.QRChannelEventCode:
				code, err := session.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
				if err != nil {
					return "", AccountInfo{}, err
				}
				m.mu.RLock()
				info := m.accountInfoLocked(session)
				m.mu.RUnlock()
				return code, info, nil
			case whatsmeow.QRChannelEventError:
				if evt.Error != nil {
					return "", AccountInfo{}, evt.Error
				}
				return "", AccountInfo{}, fmt.Errorf("QR pairing error")
			case "timeout":
				return "", AccountInfo{}, fmt.Errorf("QR pairing timeout")
			case "err-client-outdated":
				return "", AccountInfo{}, fmt.Errorf("WhatsApp client outdated")
			case "err-scanned-without-multidevice":
				return "", AccountInfo{}, fmt.Errorf("WhatsApp multi-device belum aktif di HP")
			}
		}
	}
}

func (m *Manager) Reconnect(ctx context.Context, accountID string) error {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return fmt.Errorf("account not found")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	m.clearReconnectBlock(accountID)
	m.setHealthy(accountID, false)
	if session.client.IsConnected() {
		session.client.Disconnect()
	}

	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if err := session.client.ConnectContext(ctx); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
		m.recordReconnectFailureWithReason(accountID, fmt.Sprintf("Reconnect gagal: %v", err))
		return err
	}
	if session.client.Store == nil || session.client.Store.ID == nil {
		return nil
	}
	err := m.waitUntilReady(ctx, accountID, session.client)
	if err != nil {
		m.recordReconnectFailureWithReason(accountID, fmt.Sprintf("Reconnect menunggu WhatsApp siap timeout: %v", err))
	}
	return err
}

func (m *Manager) EnsureConnected(ctx context.Context, accountID string) (*whatsmeow.Client, error) {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return nil, fmt.Errorf("account not found")
	}

	session.connectMu.Lock()
	defer session.connectMu.Unlock()

	if m.sessionReady(accountID, session.client) {
		return session.client, nil
	}
	if session.client.Store == nil || session.client.Store.ID == nil {
		return nil, fmt.Errorf("account is not logged in")
	}

	// Retry up to 2 attempts: disconnect → connect → wait.
	// Between attempts we give the socket a moment to settle.
	const maxAttempts = 2
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		m.setHealthy(accountID, false)
		m.mu.Lock()
		if current := m.sessions[m.resolveLocked(accountID)]; current != nil {
			current.lastDisconnectReason = fmt.Sprintf("Mencoba reconnect (attempt %d/%d)", attempt, maxAttempts)
		}
		m.mu.Unlock()

		// Force-clean any stale connection
		if session.client.IsConnected() {
			session.client.Disconnect()
			// Brief pause so the old websocket fully closes before we
			// open a new one. Without this the server sometimes rejects
			// the new connection with a 400 / stream-replaced.
			time.Sleep(500 * time.Millisecond)
		}

		if err := session.client.ConnectContext(ctx); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			lastErr = err
			m.recordReconnectFailureWithReason(accountID, fmt.Sprintf("Ensure connect gagal (attempt %d): %v", attempt, err))
			// Wait before retrying
			if attempt < maxAttempts {
				time.Sleep(2 * time.Second)
			}
			continue
		}

		if err := m.waitUntilReady(ctx, accountID, session.client); err != nil {
			lastErr = err
			m.recordReconnectFailureWithReason(accountID, fmt.Sprintf("Ensure menunggu WhatsApp siap timeout (attempt %d): %v", attempt, err))
			// The client may have disconnected during the wait. If we
			// still have time budget, retry the whole cycle.
			if attempt < maxAttempts {
				time.Sleep(2 * time.Second)
			}
			continue
		}

		// Success!
		return session.client, nil
	}

	return nil, fmt.Errorf("ensure connected failed after %d attempts: %w", maxAttempts, lastErr)
}

func (m *Manager) IsReady(accountID string) bool {
	session := m.getSession(accountID)
	if session == nil || session.client == nil {
		return false
	}
	return m.sessionReady(accountID, session.client)
}

func (m *Manager) getSession(accountID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[m.resolveLocked(accountID)]
}

func (m *Manager) sessionReady(accountID string, client *whatsmeow.Client) bool {
	if client == nil || !client.IsConnected() || !client.IsLoggedIn() {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	session := m.sessions[m.resolveLocked(accountID)]
	return session != nil && session.client == client
}

func (m *Manager) setHealthy(accountID string, healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[m.resolveLocked(accountID)]
	if session != nil {
		if healthy {
			m.markSessionHealthyLocked(session)
		} else {
			m.markSessionUnhealthyLocked(session)
		}
	}
}

func (m *Manager) waitUntilReady(ctx context.Context, accountID string, client *whatsmeow.Client) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	// Track how long the client has been disconnected during the wait.
	// If it disconnects and stays down for too long, attempt one reconnect.
	disconnectedSince := time.Time{}
	reconnected := false

	for {
		if client.IsConnected() && client.IsLoggedIn() {
			m.setHealthy(accountID, true)
			return nil
		}

		// Detect unexpected disconnect during wait
		if !client.IsConnected() {
			if disconnectedSince.IsZero() {
				disconnectedSince = time.Now()
			}
			// If disconnected for > 2s and we haven't retried, try once
			if !reconnected && time.Since(disconnectedSince) > 2*time.Second {
				reconnected = true
				session := m.getSession(accountID)
				if session != nil && session.client != nil {
					_ = session.client.ConnectContext(ctx) // best effort
				}
			}
		} else {
			// Client is connected but not yet logged in — reset disconnect tracker
			disconnectedSince = time.Time{}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) markSessionHealthyLocked(session *Session) {
	now := time.Now()
	session.healthy = true
	session.lastHealthyAt = now
	session.unhealthySince = time.Time{}
	session.nextReconnectAt = time.Time{}
	session.consecutiveReconnectFail = 0
	session.autoReconnectBlocked = false
	session.autoReconnectBlockedTill = time.Time{}
	session.lastDisconnectReason = ""
}

func (m *Manager) markSessionUnhealthyLocked(session *Session) {
	session.healthy = false
	if session.unhealthySince.IsZero() {
		session.unhealthySince = time.Now()
	}
}

func (m *Manager) clearReconnectBlock(accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[m.resolveLocked(accountID)]
	if session == nil {
		return
	}
	session.autoReconnectBlocked = false
	session.autoReconnectBlockedTill = time.Time{}
	session.nextReconnectAt = time.Time{}
}

func (m *Manager) recordReconnectFailure(accountID string) {
	m.recordReconnectFailureWithReason(accountID, "")
}

func (m *Manager) recordReconnectFailureWithReason(accountID, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[m.resolveLocked(accountID)]
	if session == nil {
		return
	}
	session.consecutiveReconnectFail++
	session.nextReconnectAt = time.Now().Add(reconnectBackoff(session.consecutiveReconnectFail))
	if strings.TrimSpace(reason) != "" {
		session.lastDisconnectReason = reason
	}
}

func reconnectBackoff(failures int) time.Duration {
	if failures <= 1 {
		return connectionReconnectBackoffMin
	}
	delay := connectionReconnectBackoffMin
	for i := 1; i < failures && delay < connectionReconnectBackoffMax; i++ {
		delay *= 2
	}
	if delay > connectionReconnectBackoffMax {
		return connectionReconnectBackoffMax
	}
	return delay
}

func (m *Manager) connectionSupervisor(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.superviseConnections(ctx)
			timer.Reset(connectionSupervisorInterval)
		}
	}
}

func (m *Manager) superviseConnections(ctx context.Context) {
	now := time.Now()
	accountIDs := make([]string, 0)

	m.mu.Lock()
	for accountID, session := range m.sessions {
		if session == nil || session.client == nil || session.client.Store == nil || session.client.Store.ID == nil {
			continue
		}
		if session.supervisorReconnectRun {
			continue
		}
		if session.autoReconnectBlocked {
			continue
		}
		if !session.autoReconnectBlockedTill.IsZero() && now.Before(session.autoReconnectBlockedTill) {
			continue
		}
		if !session.nextReconnectAt.IsZero() && now.Before(session.nextReconnectAt) {
			continue
		}
		if session.client.IsConnected() && session.client.IsLoggedIn() {
			if !session.healthy {
				m.markSessionHealthyLocked(session)
			}
			continue
		}
		session.supervisorReconnectRun = true
		accountIDs = append(accountIDs, accountID)
	}
	m.mu.Unlock()

	for _, accountID := range accountIDs {
		go m.superviseConnection(ctx, accountID)
	}
}

func (m *Manager) superviseConnection(ctx context.Context, accountID string) {
	defer func() {
		m.mu.Lock()
		if session := m.sessions[accountID]; session != nil {
			session.supervisorReconnectRun = false
		}
		m.mu.Unlock()
	}()

	session := m.getSession(accountID)
	if session == nil {
		return
	}

	// Do not reconnect/disconnect the socket while a broadcast/direct send is in
	// progress. EnsureConnected may call Disconnect() before ConnectContext(); if
	// the supervisor does that concurrently with SendMessage, WhatsApp can reject
	// the write with intermittent server 400 / closed websocket errors even while
	// the UI still reports Online.
	session.sendMu.Lock()
	defer session.sendMu.Unlock()

	connectCtx, cancel := context.WithTimeout(ctx, connectionSupervisorTimeout)
	_, err := m.EnsureConnected(connectCtx, accountID)
	cancel()
	if err != nil {
		m.log().WithError(err).WithField("account_id", accountID).Warn("WhatsApp connection supervisor reconnect failed")
		return
	}

	// If WhatsApp keeps dropping a socket shortly after reconnect, hammering
	// ConnectContext every few seconds makes sends less stable and looks
	// "Online" in the UI while the stream is constantly being replaced. After a
	// supervisor reconnect, give the socket a cooldown. On-demand sends still call
	// EnsureConnected themselves, so broadcasts are not blocked by this cooldown.
	m.mu.Lock()
	if current := m.sessions[accountID]; current != nil {
		current.nextReconnectAt = time.Now().Add(30 * time.Second)
	}
	m.mu.Unlock()
	m.log().WithField("account_id", accountID).Info("WhatsApp connection supervisor restored session")
}

func (m *Manager) resolveLocked(accountID string) string {
	if accountID != "" {
		return accountID
	}
	return m.activeID
}

func (m *Manager) accountInfoLocked(session *Session) AccountInfo {
	info := AccountInfo{
		ID:                session.meta.ID,
		Name:              session.meta.Name,
		JID:               session.meta.JID,
		CreatedAt:         session.meta.CreatedAt,
		Active:            session.meta.ID == m.activeID,
		IsPending:         session.meta.JID == "",
		Status:            "Belum login",
		WebhookEnabled:    session.meta.WebhookEnabled,
		WebhookURL:        session.meta.WebhookURL,
		WebhookSecret:     session.meta.WebhookSecret,
		LastConnectedAt:   session.lastConnectedAt,
		ReconnectFailures: session.consecutiveReconnectFail,
		NextReconnectAt:   session.nextReconnectAt,
		StatusReason:      session.lastDisconnectReason,
	}

	if session.client != nil && session.client.Store != nil && session.client.Store.ID != nil {
		info.LoggedIn = true
		info.Phone = session.client.Store.ID.User
		if info.JID == "" {
			info.JID = session.client.Store.ID.String()
		}
	} else if info.JID != "" {
		info.LoggedIn = true
		info.Phone = strings.Split(info.JID, "@")[0]
	}

	if session.client != nil {
		info.Connected = session.client.IsConnected() && info.LoggedIn
	}
	if session.healthy && info.LoggedIn {
		info.Connected = true
	}
	if !info.Connected && info.LoggedIn && !session.lastHealthyAt.IsZero() && time.Since(session.lastHealthyAt) <= connectionHealthyGrace {
		info.Connected = true
	}

	switch {
	case info.Connected && info.LoggedIn:
		info.Status = "Online"
	case info.LoggedIn:
		info.Status = "Koneksi bermasalah"
	case !info.IsPending && info.JID == "":
		info.Status = "Offline"
	case info.IsPending:
		info.Status = "Siap scan QR"
	default:
		info.Status = "Offline"
	}
	return info
}

func defaultAccountID(index int, jid string) string {
	if jid != "" {
		return strings.NewReplacer(":", "-", "@", "-", ".", "-").Replace(jid)
	}
	return fmt.Sprintf("acc-%d", index+1)
}

func normalizePairPhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
