package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/config"
	_ "github.com/mattn/go-sqlite3"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

var (
	managerLock sync.RWMutex
	manager     *Manager
	managers    = make(map[string]*Manager)
	dbContainer *sqlstore.Container
)

// InitWaDB initializes the default WhatsApp database.
func InitWaDB(ctx context.Context) *sqlstore.Container {
	dbLog := waLog.Stdout("Database", config.WhatsappLogLevel, true)
	container, err := sqlstore.New(ctx, "sqlite", config.DBURI, dbLog)
	if err != nil {
		logrus.Fatalf("Failed to connect to database: %v", err)
	}
	dbContainer = container
	return container
}

// InitManager initializes the default WhatsApp multi-account manager.
func InitManager(ctx context.Context, container *sqlstore.Container, prefs PreferenceStore, handler SessionEventHandler) *Manager {
	mgr, err := initManagerWithContainer(ctx, "", container, prefs, handler)
	if err != nil {
		logrus.Fatalf("Failed to initialize WhatsApp manager: %v", err)
	}
	return mgr
}

func InitManagerForUser(ctx context.Context, userID, dbURI string, prefs PreferenceStore, handler SessionEventHandler) (*Manager, error) {
	managerLock.Lock()
	defer managerLock.Unlock()

	if existing, ok := managers[userID]; ok && existing != nil {
		return existing, nil
	}

	dbLog := waLog.Stdout("Database", config.WhatsappLogLevel, true)
	container, err := sqlstore.New(ctx, "sqlite", dbURI, dbLog)
	if err != nil {
		return nil, err
	}
	return initManagerWithContainerLocked(ctx, userID, container, prefs, handler)
}

func initManagerWithContainer(ctx context.Context, userID string, container *sqlstore.Container, prefs PreferenceStore, handler SessionEventHandler) (*Manager, error) {
	managerLock.Lock()
	defer managerLock.Unlock()
	return initManagerWithContainerLocked(ctx, userID, container, prefs, handler)
}

func initManagerWithContainerLocked(ctx context.Context, userID string, container *sqlstore.Container, prefs PreferenceStore, handler SessionEventHandler) (*Manager, error) {
	mgr, err := NewManager(ctx, container, prefs, handler)
	if err != nil {
		return nil, err
	}
	managers[userID] = mgr
	if userID == "" {
		manager = mgr
	}
	return mgr, nil
}

func GetManager() *Manager {
	return GetManagerForUser("")
}

func GetManagerForUser(userID string) *Manager {
	managerLock.RLock()
	defer managerLock.RUnlock()
	if mgr, ok := managers[userID]; ok {
		return mgr
	}
	if userID == "" {
		return manager
	}
	return nil
}

// InitWaCLI remains as a compatibility wrapper and returns the active client.
func InitWaCLI(ctx context.Context, container *sqlstore.Container) *whatsmeow.Client {
	_ = ctx
	_ = container
	return GetClient()
}

func GetClient() *whatsmeow.Client {
	return GetClientForUser("")
}

func GetClientForUser(userID string) *whatsmeow.Client {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return nil
	}
	return mgr.GetClient("")
}

func GetClientByAccount(accountID string) *whatsmeow.Client {
	return GetClientByAccountForUser("", accountID)
}

func GetClientByAccountForUser(userID, accountID string) *whatsmeow.Client {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return nil
	}
	return mgr.GetClient(accountID)
}

func GetDBContainer() *sqlstore.Container {
	return dbContainer
}

func ListAccounts() []AccountInfo {
	return ListAccountsForUser("")
}

func ListAccountsForUser(userID string) []AccountInfo {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return nil
	}
	return mgr.ListAccounts()
}

type ConnectionStats struct {
	TotalAccounts     int `json:"total_accounts"`
	LoggedInAccounts  int `json:"logged_in_accounts"`
	ConnectedAccounts int `json:"connected_accounts"`
	ProblemAccounts   int `json:"problem_accounts"`
}

func GetConnectionStats() ConnectionStats {
	managerLock.RLock()
	managerList := make([]*Manager, 0, len(managers))
	for _, mgr := range managers {
		if mgr != nil {
			managerList = append(managerList, mgr)
		}
	}
	managerLock.RUnlock()

	var stats ConnectionStats
	for _, mgr := range managerList {
		for _, account := range mgr.ListAccounts() {
			stats.TotalAccounts++
			if account.LoggedIn {
				stats.LoggedInAccounts++
			}
			if account.Connected && account.LoggedIn {
				stats.ConnectedAccounts++
			}
		}
	}
	stats.ProblemAccounts = stats.LoggedInAccounts - stats.ConnectedAccounts
	return stats
}

func CreateAccount(name string) (AccountInfo, error) {
	return CreateAccountForUser("", name)
}

func CreateAccountForUser(userID, name string) (AccountInfo, error) {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return AccountInfo{}, fmt.Errorf("manager not initialized")
	}
	return mgr.CreateAccount(name)
}

func CreateAccountForUserWithID(userID, accountID, name string) (AccountInfo, error) {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return AccountInfo{}, fmt.Errorf("manager not initialized")
	}
	return mgr.CreateAccountWithID(accountID, name)
}

func RenameAccount(accountID, name string) error {
	return RenameAccountForUser("", accountID, name)
}

func RenameAccountForUser(userID, accountID, name string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	return mgr.RenameAccount(accountID, name)
}

func SetAccountWebhookForUser(userID, accountID string, enabled bool, webhookURL, secret string) (AccountInfo, error) {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return AccountInfo{}, fmt.Errorf("manager not initialized")
	}
	return mgr.SetAccountWebhook(accountID, enabled, webhookURL, secret)
}

func DeleteAccount(ctx context.Context, accountID string) error {
	return DeleteAccountForUser(ctx, "", accountID)
}

func DeleteAccountForUser(ctx context.Context, userID, accountID string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	return mgr.DeleteAccount(ctx, accountID)
}

func SetActiveAccount(accountID string) error {
	return SetActiveAccountForUser("", accountID)
}

func SetActiveAccountForUser(userID, accountID string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	return mgr.SetActiveAccount(accountID)
}

func GetActiveAccountID() string {
	return GetActiveAccountIDForUser("")
}

func GetActiveAccountIDForUser(userID string) string {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return ""
	}
	return mgr.GetActiveAccountID()
}

func ResolveAccountID(accountID string) string {
	return ResolveAccountIDForUser("", accountID)
}

func ResolveAccountIDForUser(userID, accountID string) string {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return ""
	}
	return mgr.ResolveAccountID(accountID)
}

func GetAccount(accountID string) (AccountInfo, error) {
	return GetAccountForUser("", accountID)
}

func GetAccountForUser(userID, accountID string) (AccountInfo, error) {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return AccountInfo{}, fmt.Errorf("manager not initialized")
	}
	return mgr.GetAccount(accountID)
}

func IsClientConnected() bool {
	return IsClientConnectedForUser("")
}

func IsClientConnectedForUser(userID string) bool {
	return IsClientConnectedForAccountForUser(userID, "")
}

func IsClientConnectedForAccount(accountID string) bool {
	return IsClientConnectedForAccountForUser("", accountID)
}

func IsClientConnectedForAccountForUser(userID, accountID string) bool {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return false
	}
	return mgr.IsReady(accountID)
}

func EnsureClientConnectedForAccountForUser(ctx context.Context, userID, accountID string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	ctx, cancel := connectionContext(ctx)
	defer cancel()
	_, err := mgr.EnsureConnected(ctx, accountID)
	return err
}

func GetClientJID() string {
	return GetClientJIDForUser("")
}

func GetClientJIDForUser(userID string) string {
	return GetClientJIDForAccountForUser(userID, "")
}

func GetClientJIDForAccount(accountID string) string {
	return GetClientJIDForAccountForUser("", accountID)
}

func GetClientJIDForAccountForUser(userID, accountID string) string {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil || c.Store == nil || c.Store.ID == nil {
		return ""
	}
	return c.Store.ID.User
}

func Reconnect(ctx context.Context) error {
	return ReconnectForUser(ctx, "")
}

func ReconnectForUser(ctx context.Context, userID string) error {
	return ReconnectForAccountForUser(ctx, userID, "")
}

func ReconnectForAccount(ctx context.Context, accountID string) error {
	return ReconnectForAccountForUser(ctx, "", accountID)
}

func ReconnectForAccountForUser(ctx context.Context, userID, accountID string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	ctx, cancel := connectionContext(ctx)
	defer cancel()
	return mgr.Reconnect(ctx, accountID)
}

func Logout() error {
	return LogoutForUser("")
}

func LogoutForUser(userID string) error {
	return LogoutForAccountForUser(userID, "")
}

func LogoutForAccount(accountID string) error {
	return LogoutForAccountForUser("", accountID)
}

func LogoutForAccountForUser(userID, accountID string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return mgr.Logout(ctx, accountID)
}

func ResetForPairingForUser(ctx context.Context, userID, accountID string) (AccountInfo, error) {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return AccountInfo{}, fmt.Errorf("manager not initialized")
	}
	ctx, cancel := connectionContext(ctx)
	defer cancel()
	return mgr.ResetForPairing(ctx, accountID)
}

func PairCodeForUser(ctx context.Context, userID, accountID, phone string) (string, AccountInfo, error) {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return "", AccountInfo{}, fmt.Errorf("manager not initialized")
	}
	return mgr.PairCode(ctx, accountID, phone)
}

func SendText(ctx context.Context, jid types.JID, text string) error {
	return SendTextForUserAccount(ctx, "", "", jid, text)
}

func SendTextForAccount(ctx context.Context, accountID string, jid types.JID, text string) error {
	return SendTextForUserAccount(ctx, "", accountID, jid, text)
}

func SendTextForUserAccount(ctx context.Context, userID, accountID string, jid types.JID, text string) error {
	msg := &waProto.Message{Conversation: proto.String(text)}
	return SendMessageForUserAccount(ctx, userID, accountID, jid, msg)
}

func SendMessageForUserAccount(ctx context.Context, userID, accountID string, jid types.JID, msg *waProto.Message) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	accountID = mgr.ResolveAccountID(accountID)
	session := mgr.getSession(accountID)
	if session == nil {
		return fmt.Errorf("account not found")
	}

	session.sendMu.Lock()
	defer session.sendMu.Unlock()

	connectCtx, cancel := connectionContext(ctx)
	client, err := mgr.EnsureConnected(connectCtx, accountID)
	cancel()
	if err != nil {
		return fmt.Errorf("WhatsApp connection unavailable: %w", err)
	}
	disableLIDMigrationForSend(client)

	sendCtx, sendCancel := sendOperationContext(ctx)
	messageID := client.GenerateMessageID()
	_, err = client.SendMessage(sendCtx, jid, msg, whatsmeow.SendRequestExtra{ID: messageID})
	sendCancel()
	if err == nil || !isRetryableSendError(err) {
		return err
	}

	reconnectCtx, reconnectCancel := connectionContext(context.Background())
	reconnectErr := mgr.Reconnect(reconnectCtx, accountID)
	if reconnectErr != nil {
		reconnectCancel()
		return fmt.Errorf("send failed (%v), reconnect failed: %w", err, reconnectErr)
	}
	retryClient, retryClientErr := mgr.EnsureConnected(reconnectCtx, accountID)
	reconnectCancel()
	if retryClientErr != nil {
		return fmt.Errorf("send failed (%v), reconnect verification failed: %w", err, retryClientErr)
	}
	disableLIDMigrationForSend(retryClient)

	retryCtx, retryCancel := sendOperationContext(ctx)
	_, retryErr := retryClient.SendMessage(retryCtx, jid, msg, whatsmeow.SendRequestExtra{ID: messageID})
	retryCancel()
	if retryErr != nil {
		return fmt.Errorf("send failed after reconnect: %w", retryErr)
	}
	return nil
}

func disableLIDMigrationForSend(client *whatsmeow.Client) {
	if client == nil || client.Store == nil || client.Store.LIDMigrationTimestamp == 0 {
		return
	}
	// Avoid the fragile pre-send LID usync lookup for phone-number JIDs. In
	// production WhatsApp repeatedly closes the websocket during that query,
	// while direct PN sends can proceed and the normal retry still handles stale
	// sockets.
	client.Store.LIDMigrationTimestamp = 0
}

func SendImage(ctx context.Context, jid types.JID, imageBytes []byte, mimetype string, caption string) error {
	return SendImageForUserAccount(ctx, "", "", jid, imageBytes, mimetype, caption)
}

func SendImageForAccount(ctx context.Context, accountID string, jid types.JID, imageBytes []byte, mimetype string, caption string) error {
	return SendImageForUserAccount(ctx, "", accountID, jid, imageBytes, mimetype, caption)
}

func SendImageForUserAccount(ctx context.Context, userID, accountID string, jid types.JID, imageBytes []byte, mimetype string, caption string) error {
	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	connectCtx, cancel := connectionContext(ctx)
	c, err := mgr.EnsureConnected(connectCtx, accountID)
	cancel()
	if err != nil {
		return fmt.Errorf("WhatsApp connection unavailable: %w", err)
	}

	uploaded, err := c.Upload(ctx, imageBytes, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("failed to upload image: %w", err)
	}

	msg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			Caption:       proto.String(caption),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(mimetype),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		},
	}
	return SendMessageForUserAccount(ctx, userID, accountID, jid, msg)
}

func SendMediaForUserAccount(ctx context.Context, userID, accountID string, jid types.JID, mediaBytes []byte, mimetype, filename, caption string) error {
	mimetype = strings.TrimSpace(mimetype)
	if mimetype == "" {
		mimetype = "application/octet-stream"
	}
	if strings.HasPrefix(mimetype, "image/") {
		return SendImageForUserAccount(ctx, userID, accountID, jid, mediaBytes, mimetype, caption)
	}

	mgr := GetManagerForUser(userID)
	if mgr == nil {
		return fmt.Errorf("manager not initialized")
	}
	connectCtx, cancel := connectionContext(ctx)
	c, err := mgr.EnsureConnected(connectCtx, accountID)
	cancel()
	if err != nil {
		return fmt.Errorf("WhatsApp connection unavailable: %w", err)
	}

	if strings.HasPrefix(mimetype, "video/") {
		uploaded, err := c.Upload(ctx, mediaBytes, whatsmeow.MediaVideo)
		if err != nil {
			return fmt.Errorf("failed to upload video: %w", err)
		}
		msg := &waProto.Message{
			VideoMessage: &waProto.VideoMessage{
				Caption:       proto.String(caption),
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mimetype),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
			},
		}
		return SendMessageForUserAccount(ctx, userID, accountID, jid, msg)
	}

	uploaded, err := c.Upload(ctx, mediaBytes, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("failed to upload document: %w", err)
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "dokumen"
	}
	msg := &waProto.Message{
		DocumentMessage: &waProto.DocumentMessage{
			Caption:       proto.String(caption),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(mimetype),
			FileName:      proto.String(filename),
			Title:         proto.String(filename),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		},
	}
	return SendMessageForUserAccount(ctx, userID, accountID, jid, msg)
}

func connectionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 30*time.Second)
}

func sendOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// Sending may happen after the HTTP request that started a broadcast has
	// returned. Do not inherit request cancellation here; it causes whatsmeow
	// writes/usync locks to fail mid-send with context canceled.
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func isRetryableSendError(err error) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, context.Canceled) || strings.Contains(strings.ToLower(err.Error()), "context canceled") {
		return true
	}
	if errors.Is(err, whatsmeow.ErrNotConnected) || errors.Is(err, whatsmeow.ErrMessageTimedOut) {
		return true
	}
	var disconnectedErr *whatsmeow.DisconnectedError
	if errors.As(err, &disconnectedErr) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func IsOnWhatsApp(numbers []string) ([]types.IsOnWhatsAppResponse, error) {
	return IsOnWhatsAppForUserAccount("", "", numbers)
}

func IsOnWhatsAppForAccount(accountID string, numbers []string) ([]types.IsOnWhatsAppResponse, error) {
	return IsOnWhatsAppForUserAccount("", accountID, numbers)
}

func IsOnWhatsAppForUserAccount(userID, accountID string, numbers []string) ([]types.IsOnWhatsAppResponse, error) {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return nil, fmt.Errorf("client not initialized")
	}
	return c.IsOnWhatsApp(context.Background(), numbers)
}

func GetGroupInfo(jid types.JID) (*types.GroupInfo, error) {
	return GetGroupInfoForUserAccount("", "", jid)
}

func GetGroupInfoForAccount(accountID string, jid types.JID) (*types.GroupInfo, error) {
	return GetGroupInfoForUserAccount("", accountID, jid)
}

func GetGroupInfoForUserAccount(userID, accountID string, jid types.JID) (*types.GroupInfo, error) {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return nil, fmt.Errorf("client not initialized")
	}
	return c.GetGroupInfo(context.Background(), jid)
}

func GetJoinedGroups() ([]*types.GroupInfo, error) {
	return GetJoinedGroupsForUserAccount("", "")
}

func GetJoinedGroupsForAccount(accountID string) ([]*types.GroupInfo, error) {
	return GetJoinedGroupsForUserAccount("", accountID)
}

func GetJoinedGroupsForUserAccount(userID, accountID string) ([]*types.GroupInfo, error) {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return nil, fmt.Errorf("client not initialized")
	}
	return c.GetJoinedGroups(context.Background())
}

func GetAllContactsForAccount(accountID string) (map[types.JID]types.ContactInfo, error) {
	return GetAllContactsForUserAccount("", accountID)
}

func GetAllContactsForUserAccount(userID, accountID string) (map[types.JID]types.ContactInfo, error) {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil || c.Store == nil || c.Store.Contacts == nil {
		return nil, fmt.Errorf("client contacts not initialized")
	}
	return c.Store.Contacts.GetAllContacts(context.Background())
}

func CreateStorageFolders() {
	dirs := []string{
		config.PathStorages,
		config.PathQrCode,
		config.PathSendItems,
		config.PathMedia,
	}
	for _, d := range dirs {
		_ = os.MkdirAll(d, os.ModePerm)
	}
}

func BuildDefaultAccountName(idx int, jid string) string {
	phone := jid
	if strings.Contains(jid, "@") {
		phone = strings.SplitN(jid, "@", 2)[0]
	}
	if phone != "" {
		return "Akun " + phone
	}
	return fmt.Sprintf("Akun WA %d", idx+1)
}
