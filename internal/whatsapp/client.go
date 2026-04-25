package whatsapp

import (
	"context"
	"fmt"
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
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return false
	}
	return c.IsConnected() && c.IsLoggedIn()
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
	if err := mgr.Disconnect(accountID); err != nil {
		return err
	}
	time.Sleep(1 * time.Second)
	return mgr.Connect(ctx, accountID)
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
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return fmt.Errorf("client not initialized")
	}
	if err := c.Logout(context.Background()); err != nil {
		return err
	}
	c.Disconnect()
	return nil
}

func SendText(ctx context.Context, jid types.JID, text string) error {
	return SendTextForUserAccount(ctx, "", "", jid, text)
}

func SendTextForAccount(ctx context.Context, accountID string, jid types.JID, text string) error {
	return SendTextForUserAccount(ctx, "", accountID, jid, text)
}

func SendTextForUserAccount(ctx context.Context, userID, accountID string, jid types.JID, text string) error {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return fmt.Errorf("client not initialized")
	}
	msg := &waProto.Message{Conversation: proto.String(text)}
	_, err := c.SendMessage(ctx, jid, msg)
	return err
}

func SendImage(ctx context.Context, jid types.JID, imageBytes []byte, mimetype string, caption string) error {
	return SendImageForUserAccount(ctx, "", "", jid, imageBytes, mimetype, caption)
}

func SendImageForAccount(ctx context.Context, accountID string, jid types.JID, imageBytes []byte, mimetype string, caption string) error {
	return SendImageForUserAccount(ctx, "", accountID, jid, imageBytes, mimetype, caption)
}

func SendImageForUserAccount(ctx context.Context, userID, accountID string, jid types.JID, imageBytes []byte, mimetype string, caption string) error {
	c := GetClientByAccountForUser(userID, accountID)
	if c == nil {
		return fmt.Errorf("client not initialized")
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
	_, err = c.SendMessage(ctx, jid, msg)
	return err
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
