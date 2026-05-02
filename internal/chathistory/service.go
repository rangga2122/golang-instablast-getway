package chathistory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Service struct {
	store *storage.Storage
}

func NewService(store *storage.Storage) *Service {
	return &Service{store: store}
}

func (s *Service) HandleMessage(accountID, accountName string, evt *events.Message) {
	if s == nil || s.store == nil || evt == nil {
		return
	}

	chat := evt.Info.Chat
	chatID := chat.String()
	chatType := chatTypeFromJID(chatID)
	if chatType != "direct" {
		return
	}

	name := strings.TrimSpace(evt.Info.PushName)
	phone := phoneFromMessageInfo(evt.Info)
	text, _ := extractMessageText(evt.Message)

	_ = s.store.UpsertChatHistory(storage.ChatHistoryRecord{
		AccountID:   accountID,
		AccountName: accountName,
		ChatJID:     chatID,
		Phone:       phone,
		Name:        name,
		LastMessage: text,
		LastSeen:    evt.Info.Timestamp,
		ChatType:    chatType,
	})
}

func (s *Service) HandleUnsubscribeMessage(accountID, accountName string, evt *events.Message) (bool, storage.UnsubscribeSettings) {
	settings := storage.DefaultUnsubscribeSettings()
	if s == nil || s.store == nil || evt == nil {
		return false, settings
	}
	if evt.Info.IsFromMe {
		return false, settings
	}

	chat := evt.Info.Chat
	chatID := chat.String()
	if chatTypeFromJID(chatID) != "direct" {
		return false, settings
	}

	settings, err := s.store.GetUnsubscribeSettings()
	if err != nil || !settings.Enabled {
		return false, settings
	}

	text, ok := extractMessageText(evt.Message)
	if !ok {
		return false, settings
	}
	if strings.ToUpper(strings.TrimSpace(text)) != settings.Keyword {
		return false, settings
	}

	name := strings.TrimSpace(evt.Info.PushName)
	phone := normalizePhone(phoneFromMessageInfo(evt.Info))
	if phone == "" {
		return false, settings
	}
	_ = s.store.SaveUnsubscribedContact(phone, name, settings.Keyword, accountID, accountName)
	return true, settings
}

func (s *Service) HandleHistorySync(accountID, accountName string, data *waHistorySync.HistorySync) {
	if s == nil || s.store == nil || data == nil {
		return
	}

	for _, conv := range data.GetConversations() {
		if conv == nil {
			continue
		}
		chatID := conv.GetID()
		chatType := chatTypeFromJID(chatID)
		if chatType != "direct" {
			continue
		}

		name := strings.TrimSpace(firstNonEmpty(conv.GetDisplayName(), conv.GetName()))
		phone := phoneFromChatJID(chatID)
		lastSeen := unixToTime(conv.GetConversationTimestamp(), conv.GetLastMsgTimestamp())
		lastMessage := latestHistoryMessagePreview(conv.GetMessages())

		_ = s.store.UpsertChatHistory(storage.ChatHistoryRecord{
			AccountID:   accountID,
			AccountName: accountName,
			ChatJID:     chatID,
			Phone:       phone,
			Name:        name,
			LastMessage: lastMessage,
			LastSeen:    lastSeen,
			ChatType:    chatType,
		})
	}
}

func (s *Service) SyncContacts(ctx context.Context, accountID, accountName string, client *whatsmeow.Client) error {
	if s == nil || s.store == nil {
		return nil
	}
	if client == nil || client.Store == nil || client.Store.Contacts == nil {
		return fmt.Errorf("contacts not available")
	}

	contacts, err := client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return err
	}

	for jid, info := range contacts {
		if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
			continue
		}
		phone := jid.User
		if phone == "" {
			continue
		}
		name := strings.TrimSpace(firstNonEmpty(info.FullName, info.PushName, info.BusinessName, info.FirstName))
		_ = s.store.UpsertChatHistory(storage.ChatHistoryRecord{
			AccountID:   accountID,
			AccountName: accountName,
			ChatJID:     jid.String(),
			Phone:       phone,
			Name:        name,
			LastSeen:    time.Now(),
			ChatType:    "direct",
		})
	}
	return nil
}

func extractMessageText(msg *waE2E.Message) (string, bool) {
	if msg == nil {
		return "", false
	}
	switch {
	case msg.GetConversation() != "":
		return strings.TrimSpace(msg.GetConversation()), true
	case msg.GetExtendedTextMessage() != nil && msg.GetExtendedTextMessage().GetText() != "":
		return strings.TrimSpace(msg.GetExtendedTextMessage().GetText()), true
	case msg.GetImageMessage() != nil && msg.GetImageMessage().GetCaption() != "":
		return strings.TrimSpace(msg.GetImageMessage().GetCaption()), true
	case msg.GetVideoMessage() != nil && msg.GetVideoMessage().GetCaption() != "":
		return strings.TrimSpace(msg.GetVideoMessage().GetCaption()), true
	case msg.GetDocumentMessage() != nil && msg.GetDocumentMessage().GetCaption() != "":
		return strings.TrimSpace(msg.GetDocumentMessage().GetCaption()), true
	case msg.GetProtocolMessage() != nil && msg.GetProtocolMessage().GetEditedMessage() != nil:
		return extractMessageText(msg.GetProtocolMessage().GetEditedMessage())
	default:
		return "", false
	}
}

func latestHistoryMessagePreview(messages []*waHistorySync.HistorySyncMsg) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] == nil || messages[i].GetMessage() == nil {
			continue
		}
		if text, ok := extractMessageText(messages[i].GetMessage().GetMessage()); ok && text != "" {
			return text
		}
	}
	return ""
}

func unixToTime(values ...uint64) time.Time {
	for _, value := range values {
		if value > 0 {
			return time.Unix(int64(value), 0)
		}
	}
	return time.Now()
}

func chatTypeFromJID(chatID string) string {
	switch {
	case strings.HasSuffix(chatID, "@g.us"):
		return "group"
	case strings.HasSuffix(chatID, "@broadcast"), strings.Contains(chatID, "newsletter"), strings.HasPrefix(chatID, "status@"):
		return "system"
	default:
		return "direct"
	}
}

func phoneFromChatJID(chatID string) string {
	if idx := strings.Index(chatID, "@"); idx >= 0 {
		return chatID[:idx]
	}
	return chatID
}

func phoneFromMessageInfo(info types.MessageInfo) string {
	for _, jid := range []types.JID{
		info.Sender,
		info.SenderAlt,
		info.Chat,
		info.RecipientAlt,
	} {
		if phone := phoneFromPhoneJID(jid); phone != "" {
			return phone
		}
	}
	for _, jid := range []types.JID{info.Sender, info.SenderAlt, info.Chat, info.RecipientAlt} {
		if jid.User != "" {
			return jid.User
		}
	}
	return phoneFromChatJID(info.SourceString())
}

func phoneFromPhoneJID(jid types.JID) string {
	if jid.User == "" || jid.Server != types.DefaultUserServer {
		return ""
	}
	return jid.User
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizePhone(input string) string {
	input = strings.TrimSpace(strings.TrimPrefix(input, "+"))
	if input == "" {
		return ""
	}
	input = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(input)
	if strings.HasPrefix(input, "08") {
		input = "62" + input[1:]
	}
	var b strings.Builder
	for _, ch := range input {
		if ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		}
	}
	return b.String()
}
