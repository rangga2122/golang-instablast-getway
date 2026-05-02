package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

const messageReceivedEvent = "message.received"

type Payload struct {
	Event     string         `json:"event"`
	Timestamp time.Time      `json:"timestamp"`
	User      UserPayload    `json:"user"`
	Account   AccountPayload `json:"account"`
	Message   MessagePayload `json:"message"`
}

type UserPayload struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type AccountPayload struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	JID   string `json:"jid"`
	Phone string `json:"phone"`
}

type MessagePayload struct {
	ID        string    `json:"id"`
	FromMe    bool      `json:"from_me"`
	ChatJID   string    `json:"chat_jid"`
	SenderJID string    `json:"sender_jid"`
	PushName  string    `json:"push_name"`
	ChatType  string    `json:"chat_type"`
	Type      string    `json:"type"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

func DeliverMessage(ctx context.Context, user storage.AppUser, account whatsapp.AccountInfo, evt *events.Message) error {
	if evt == nil {
		return nil
	}
	if !account.WebhookEnabled || strings.TrimSpace(account.WebhookURL) == "" {
		return nil
	}
	if evt.Info.IsFromMe {
		return nil
	}

	text, _ := extractMessageText(evt.Message)
	payload := Payload{
		Event:     messageReceivedEvent,
		Timestamp: time.Now(),
		User: UserPayload{
			ID:    user.ID,
			Email: user.Email,
		},
		Account: AccountPayload{
			ID:    account.ID,
			Name:  account.Name,
			JID:   account.JID,
			Phone: account.Phone,
		},
		Message: MessagePayload{
			ID:        string(evt.Info.ID),
			FromMe:    evt.Info.IsFromMe,
			ChatJID:   evt.Info.Chat.String(),
			SenderJID: evt.Info.Sender.String(),
			PushName:  strings.TrimSpace(evt.Info.PushName),
			ChatType:  chatTypeFromJID(evt.Info.Chat.String()),
			Type:      messageType(evt.Message),
			Text:      text,
			Timestamp: evt.Info.Timestamp,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(account.WebhookURL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "InstaBlast-Pro-Webhook/1.0")
	req.Header.Set("X-InstaBlast-Event", messageReceivedEvent)
	req.Header.Set("X-InstaBlast-Account-ID", account.ID)

	if secret := strings.TrimSpace(account.WebhookSecret); secret != "" {
		req.Header.Set("X-InstaBlast-Signature", "sha256="+sign(body, secret))
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
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

func messageType(msg *waE2E.Message) string {
	if msg == nil {
		return "unknown"
	}
	switch {
	case msg.GetConversation() != "", msg.GetExtendedTextMessage() != nil:
		return "text"
	case msg.GetImageMessage() != nil:
		return "image"
	case msg.GetVideoMessage() != nil:
		return "video"
	case msg.GetAudioMessage() != nil:
		return "audio"
	case msg.GetDocumentMessage() != nil:
		return "document"
	case msg.GetStickerMessage() != nil:
		return "sticker"
	case msg.GetButtonsResponseMessage() != nil, msg.GetListResponseMessage() != nil:
		return "interactive"
	case msg.GetProtocolMessage() != nil && msg.GetProtocolMessage().GetEditedMessage() != nil:
		return messageType(msg.GetProtocolMessage().GetEditedMessage())
	default:
		return "unknown"
	}
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
