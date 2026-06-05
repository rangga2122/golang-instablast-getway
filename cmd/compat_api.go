package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/auth"
	"github.com/azkazamdigital/wa-gateway/internal/storage"
	"github.com/azkazamdigital/wa-gateway/internal/whatsapp"
	"github.com/gofiber/fiber/v2"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const compatDeviceIDHeader = "X-Device-Id"

func registerCompatAPI(app fiber.Router) {
	app.Get("/app/login", compatAppLogin)
	app.Get("/app/login-with-code", compatAppLoginWithCode)
	app.Get("/app/logout", compatAppLogout)
	app.Get("/app/reconnect", compatAppReconnect)
	app.Get("/app/devices", compatAppDevices)
	app.Get("/app/status", compatAppStatus)

	app.Get("/devices", compatListDevices)
	app.Post("/devices", compatAddDevice)
	app.Get("/devices/:device_id", compatGetDevice)
	app.Delete("/devices/:device_id", compatDeleteDevice)
	app.Get("/devices/:device_id/login", compatDeviceLogin)
	app.Post("/devices/:device_id/login/code", compatDeviceLoginWithCode)
	app.Post("/devices/:device_id/logout", compatDeviceLogout)
	app.Post("/devices/:device_id/reconnect", compatDeviceReconnect)
	app.Get("/devices/:device_id/status", compatDeviceStatus)

	app.Post("/send/message", compatSendMessage)
	app.Post("/send/image", compatSendImage)
	app.Post("/send/poll", compatSendPoll)
	app.Post("/send/presence", compatSendPresence)
	app.Post("/send/chat-presence", compatSendChatPresence)

	app.Get("/user/check", compatUserCheck)
	app.Get("/user/info", compatUserInfo)
	app.Get("/user/avatar", compatUserAvatar)
	app.Get("/user/my/groups", compatMyGroups)
	app.Get("/user/my/contacts", compatMyContacts)
	app.Get("/user/my/privacy", compatMyPrivacy)
	app.Get("/user/business-profile", compatBusinessProfile)

	app.Post("/group", compatCreateGroup)
	app.Post("/group/join-with-link", compatJoinGroupWithLink)
	app.Get("/group/info-from-link", compatGroupInfoFromLink)
	app.Get("/group/info", compatGroupInfo)
	app.Post("/group/leave", compatLeaveGroup)
	app.Get("/group/participants", compatGroupParticipants)
	app.Get("/group/participants/export", compatGroupParticipantsExport)
	app.Get("/group/invite-link", compatGroupInviteLink)
}

func isCompatAPIPath(path string) bool {
	for _, prefix := range []string{"/app/", "/devices", "/send/", "/user/", "/group/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func compatOK(c *fiber.Ctx, message string, results any) error {
	return c.JSON(fiber.Map{
		"status":  200,
		"code":    "SUCCESS",
		"message": message,
		"results": results,
	})
}

func compatAPIError(c *fiber.Ctx, status int, code, message string, results any) error {
	return c.Status(status).JSON(fiber.Map{
		"status":  status,
		"code":    code,
		"message": message,
		"results": results,
	})
}

func compatUnauthorized(c *fiber.Ctx) error {
	return compatAPIError(c, fiber.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
}

func authenticateRequestUser(c *fiber.Ctx) (storage.AppUser, bool) {
	if AuthService == nil || Store == nil {
		return storage.AppUser{}, false
	}
	authHeader := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	if !strings.HasPrefix(strings.ToLower(authHeader), "basic ") {
		return storage.AppUser{}, false
	}
	payload := strings.TrimSpace(authHeader[6:])
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return storage.AppUser{}, false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return storage.AppUser{}, false
	}
	user, err := Store.AuthenticateUser(parts[0], parts[1])
	if err != nil {
		return storage.AppUser{}, false
	}
	c.Locals("current_user", user)
	c.Locals("current_session", auth.Session{
		UserID:    user.ID,
		Email:     user.Email,
		IsAdmin:   user.IsAdmin,
		ExpiresAt: user.ExpiresAt,
	})
	return user, true
}

func compatResolveAccount(c *fiber.Ctx, autoCreate bool) (storage.AppUser, string, *whatsmeow.Client, error) {
	return compatResolveAccountWithDevice(c, "", autoCreate)
}

func compatResolveAccountWithDevice(c *fiber.Ctx, explicitDeviceID string, autoCreate bool) (storage.AppUser, string, *whatsmeow.Client, error) {
	user, err := currentUser(c)
	if err != nil {
		return storage.AppUser{}, "", nil, err
	}
	deviceID := strings.TrimSpace(explicitDeviceID)
	if deviceID == "" {
		deviceID = strings.TrimSpace(c.Get(compatDeviceIDHeader))
	}
	if deviceID == "" {
		deviceID = strings.TrimSpace(c.Query("device_id"))
	}
	if deviceID == "" {
		deviceID = strings.TrimSpace(c.Params("device_id"))
	}
	if deviceID == "" {
		deviceID = whatsapp.GetActiveAccountIDForUser(user.ID)
	}
	if deviceID == "" && autoCreate {
		account, err := ensureAtLeastOneAccount(user.ID)
		if err != nil {
			return storage.AppUser{}, "", nil, err
		}
		deviceID = account.ID
	}
	if deviceID == "" {
		return storage.AppUser{}, "", nil, fmt.Errorf("device_id is required via X-Device-Id header or device_id query")
	}
	client := whatsapp.GetClientByAccountForUser(user.ID, deviceID)
	if client == nil {
		return storage.AppUser{}, "", nil, fmt.Errorf("device not found; create a device first from /devices or provide a valid X-Device-Id")
	}
	return user, deviceID, client, nil
}

func compatDeviceState(account whatsapp.AccountInfo) string {
	switch {
	case account.Connected && account.LoggedIn:
		return "connected"
	case account.Connected:
		return "connecting"
	case account.IsPending:
		return "pending"
	default:
		return "disconnected"
	}
}

func compatDeviceInfo(account whatsapp.AccountInfo) fiber.Map {
	return fiber.Map{
		"id":           account.ID,
		"device_id":    account.ID,
		"display_name": account.Name,
		"jid":          firstNonEmpty(account.JID, account.Phone),
		"phone":        account.Phone,
		"state":        compatDeviceState(account),
		"created_at":   account.CreatedAt,
		"is_connected": account.Connected,
		"is_logged_in": account.LoggedIn,
	}
}

func compatDeviceList(userID string) []fiber.Map {
	accounts := whatsapp.ListAccountsForUser(userID)
	results := make([]fiber.Map, 0, len(accounts))
	for _, account := range accounts {
		results = append(results, compatDeviceInfo(account))
	}
	return results
}

func compatReadUploadedMedia(c *fiber.Ctx, field, inlineBase64, defaultMime string) ([]byte, string, error) {
	if fileHeader, err := c.FormFile(field); err == nil && fileHeader != nil {
		file, err := fileHeader.Open()
		if err != nil {
			return nil, "", err
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return nil, "", err
		}
		mimeType := fileHeader.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = mime.TypeByExtension(filepath.Ext(fileHeader.Filename))
		}
		if mimeType == "" {
			mimeType = defaultMime
		}
		return data, mimeType, nil
	}
	raw := strings.TrimSpace(inlineBase64)
	if raw == "" {
		return nil, "", fmt.Errorf("%s wajib diisi", field)
	}
	mimeType := defaultMime
	if strings.HasPrefix(raw, "data:") {
		if semi := strings.Index(raw, ";base64,"); semi > 5 {
			mimeType = raw[5:semi]
			raw = raw[semi+8:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%s base64 tidak valid", field)
	}
	return data, mimeType, nil
}

func compatGroupInviteCode(input string) string {
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "https://chat.whatsapp.com/")
	input = strings.TrimPrefix(input, "http://chat.whatsapp.com/")
	input = strings.TrimPrefix(input, "chat.whatsapp.com/")
	return strings.TrimSpace(input)
}

func compatAppDevices(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	return compatOK(c, "Fetch device success", compatDeviceList(user.ID))
}

func compatAppStatus(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	account, err := whatsapp.GetAccountForUser(user.ID, accountID)
	if err != nil {
		return compatAPIError(c, fiber.StatusNotFound, "DEVICE_NOT_FOUND", err.Error(), fiber.Map{"device_id": accountID})
	}
	return compatOK(c, "Connection status retrieved", fiber.Map{
		"is_connected": account.Connected,
		"is_logged_in": account.LoggedIn,
		"device_id":    account.ID,
	})
}

func compatAppLogin(c *fiber.Ctx) error {
	return compatLoginViaQR(c, "")
}

func compatAppLoginWithCode(c *fiber.Ctx) error {
	return compatLoginWithPairCode(c, "")
}

func compatAppLogout(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	if err := whatsapp.LogoutForAccountForUser(user.ID, accountID); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "LOGOUT_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success logout", fiber.Map{"device_id": accountID})
}

func compatAppReconnect(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	if err := whatsapp.ReconnectForAccountForUser(context.Background(), user.ID, accountID); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "RECONNECT_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Reconnect success", fiber.Map{"device_id": accountID})
}

func compatListDevices(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	return compatOK(c, "List devices", compatDeviceList(user.ID))
}

func compatAddDevice(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	var body struct {
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
	}
	if err := c.BodyParser(&body); err != nil && len(c.Body()) > 0 {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	if len(whatsapp.ListAccountsForUser(user.ID)) >= user.MaxDevices {
		return compatAPIError(c, fiber.StatusBadRequest, "MAX_DEVICES_REACHED", "Maksimal device login tercapai", nil)
	}
	account, err := whatsapp.CreateAccountForUserWithID(user.ID, strings.TrimSpace(body.DeviceID), firstNonEmpty(strings.TrimSpace(body.Name), strings.TrimSpace(body.DeviceID)))
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_CREATE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Device added", compatDeviceInfo(account))
}

func compatGetDevice(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	deviceID := strings.TrimSpace(c.Params("device_id"))
	account, err := whatsapp.GetAccountForUser(user.ID, deviceID)
	if err != nil {
		return compatAPIError(c, fiber.StatusNotFound, "DEVICE_NOT_FOUND", err.Error(), nil)
	}
	return compatOK(c, "Device info", compatDeviceInfo(account))
}

func compatDeleteDevice(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	deviceID := strings.TrimSpace(c.Params("device_id"))
	if err := whatsapp.DeleteAccountForUser(context.Background(), user.ID, deviceID); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_DELETE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Device removed", nil)
}

func compatDeviceLogin(c *fiber.Ctx) error {
	return compatLoginViaQR(c, strings.TrimSpace(c.Params("device_id")))
}

func compatDeviceLoginWithCode(c *fiber.Ctx) error {
	return compatLoginWithPairCode(c, strings.TrimSpace(c.Params("device_id")))
}

func compatDeviceLogout(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	deviceID := strings.TrimSpace(c.Params("device_id"))
	if err := whatsapp.LogoutForAccountForUser(user.ID, deviceID); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "LOGOUT_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Logout requested", nil)
}

func compatDeviceReconnect(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	deviceID := strings.TrimSpace(c.Params("device_id"))
	if err := whatsapp.ReconnectForAccountForUser(context.Background(), user.ID, deviceID); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "RECONNECT_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Reconnect requested", nil)
}

func compatDeviceStatus(c *fiber.Ctx) error {
	user, err := currentUser(c)
	if err != nil {
		return compatUnauthorized(c)
	}
	deviceID := strings.TrimSpace(c.Params("device_id"))
	account, err := whatsapp.GetAccountForUser(user.ID, deviceID)
	if err != nil {
		return compatAPIError(c, fiber.StatusNotFound, "DEVICE_NOT_FOUND", err.Error(), nil)
	}
	return compatOK(c, "Device status", fiber.Map{
		"device_id":    deviceID,
		"is_connected": account.Connected,
		"is_logged_in": account.LoggedIn,
	})
}

func compatSendMessage(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Phone   string `json:"phone" form:"phone"`
		Message string `json:"message" form:"message"`
	}
	if err := c.BodyParser(&body); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	if strings.TrimSpace(body.Phone) == "" || strings.TrimSpace(body.Message) == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone dan message wajib diisi", nil)
	}
	if err := whatsapp.SendTextForUserAccount(c.UserContext(), user.ID, accountID, parsePhoneToJID(body.Phone), body.Message); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "SEND_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Message sent", fiber.Map{"device_id": accountID, "phone": normalizePhone(body.Phone), "status": "sent"})
}

func compatSendImage(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Phone   string `json:"phone" form:"phone"`
		Caption string `json:"caption" form:"caption"`
		Image   string `json:"image" form:"image"`
	}
	if err := c.BodyParser(&body); err != nil && len(c.Body()) > 0 {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	imageBytes, mimeType, err := compatReadUploadedMedia(c, "image", body.Image, "image/jpeg")
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", err.Error(), nil)
	}
	if strings.TrimSpace(body.Phone) == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	if err := whatsapp.SendImageForUserAccount(c.UserContext(), user.ID, accountID, parsePhoneToJID(body.Phone), imageBytes, mimeType, body.Caption); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "SEND_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Image sent", fiber.Map{"device_id": accountID, "phone": normalizePhone(body.Phone), "status": "sent"})
}

func compatSendPoll(c *fiber.Ctx) error {
	user, accountID, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Phone                  string   `json:"phone"`
		Name                   string   `json:"name"`
		Options                []string `json:"options"`
		SelectableOptionsCount int      `json:"selectable_options_count"`
	}
	if err := c.BodyParser(&body); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	if body.SelectableOptionsCount <= 0 {
		body.SelectableOptionsCount = 1
	}
	if strings.TrimSpace(body.Phone) == "" || strings.TrimSpace(body.Name) == "" || len(body.Options) == 0 {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone, name, dan options wajib diisi", nil)
	}
	msg := client.BuildPollCreation(body.Name, body.Options, body.SelectableOptionsCount)
	if err := whatsapp.SendMessageForUserAccount(c.UserContext(), user.ID, accountID, parsePhoneToJID(body.Phone), msg); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "SEND_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Poll sent", fiber.Map{"device_id": accountID, "phone": normalizePhone(body.Phone), "status": "sent"})
}

func compatSendPresence(c *fiber.Ctx) error {
	_, accountID, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = c.BodyParser(&body)
	state := types.Presence(strings.TrimSpace(body.Name))
	if state == "" {
		state = types.PresenceAvailable
	}
	if err := client.SendPresence(c.UserContext(), state); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "PRESENCE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Presence sent", fiber.Map{"device_id": accountID, "status": string(state)})
}

func compatSendChatPresence(c *fiber.Ctx) error {
	_, accountID, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Phone string `json:"phone"`
		Name  string `json:"name"`
		Media string `json:"media"`
	}
	if err := c.BodyParser(&body); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	if strings.TrimSpace(body.Phone) == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	state := types.ChatPresence(strings.TrimSpace(body.Name))
	if state == "" {
		state = types.ChatPresenceComposing
	}
	media := types.ChatPresenceMedia(strings.TrimSpace(body.Media))
	if err := client.SendChatPresence(c.UserContext(), parsePhoneToJID(body.Phone), state, media); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "PRESENCE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Chat presence sent", fiber.Map{"device_id": accountID, "phone": normalizePhone(body.Phone), "status": string(state)})
}

func compatUserCheck(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	phone := strings.TrimSpace(c.Query("phone"))
	if phone == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	results, err := whatsapp.IsOnWhatsAppForUserAccount(user.ID, accountID, []string{"+" + normalizePhone(phone)})
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "CHECK_FAILED", err.Error(), nil)
	}
	if len(results) == 0 {
		return compatOK(c, "Success check user", []any{})
	}
	return compatOK(c, "Success check user", results)
}

func compatUserInfo(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	phone := strings.TrimSpace(c.Query("phone"))
	if phone == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	jid := parsePhoneToJID(phone)
	info, err := client.GetUserInfo(c.UserContext(), []types.JID{jid})
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "INFO_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success get user info", info[jid])
}

func compatUserAvatar(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	phone := strings.TrimSpace(c.Query("phone"))
	if phone == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	avatar, err := client.GetProfilePictureInfo(c.UserContext(), parsePhoneToJID(phone), nil)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "AVATAR_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success get avatar", avatar)
}

func compatMyGroups(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	groups, err := whatsapp.GetJoinedGroupsForUserAccount(user.ID, accountID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUPS_FAILED", err.Error(), nil)
	}
	items := make([]fiber.Map, 0, len(groups))
	for _, g := range groups {
		if g == nil {
			continue
		}
		items = append(items, fiber.Map{
			"jid":                g.JID.String(),
			"group_id":           g.JID.String(),
			"name":               g.Name,
			"participants_count": len(g.Participants),
		})
	}
	return compatOK(c, "Success get list groups", items)
}

func compatMyContacts(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	contacts, err := whatsapp.GetAllContactsForUserAccount(user.ID, accountID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "CONTACTS_FAILED", err.Error(), nil)
	}
	items := make([]fiber.Map, 0, len(contacts))
	for jid, info := range contacts {
		items = append(items, fiber.Map{
			"jid":            jid.String(),
			"phone":          jid.User,
			"first_name":     info.FirstName,
			"full_name":      info.FullName,
			"push_name":      info.PushName,
			"business_name":  info.BusinessName,
			"redacted_phone": info.RedactedPhone,
		})
	}
	return compatOK(c, "Success get list contacts", items)
}

func compatMyPrivacy(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	return compatOK(c, "Success get privacy", client.GetPrivacySettings(c.UserContext()))
}

func compatBusinessProfile(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	phone := strings.TrimSpace(c.Query("phone"))
	if phone == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	profile, err := client.GetBusinessProfile(c.UserContext(), parsePhoneToJID(phone))
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BUSINESS_PROFILE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success get business profile", profile)
}

func compatCreateGroup(c *fiber.Ctx) error {
	_, accountID, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Name         string   `json:"name"`
		Participants []string `json:"participants"`
	}
	if err := c.BodyParser(&body); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	req := whatsmeow.ReqCreateGroup{Name: strings.TrimSpace(body.Name)}
	for _, participant := range body.Participants {
		participant = strings.TrimSpace(participant)
		if participant == "" {
			continue
		}
		req.Participants = append(req.Participants, parsePhoneToJID(participant))
	}
	groupInfo, err := client.CreateGroup(c.UserContext(), req)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_CREATE_FAILED", err.Error(), nil)
	}
	return compatOK(c, fmt.Sprintf("Success created group with id %s", groupInfo.JID.String()), fiber.Map{
		"group_id":  groupInfo.JID.String(),
		"device_id": accountID,
	})
}

func compatJoinGroupWithLink(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		Code string `json:"code"`
		Link string `json:"link"`
	}
	if err := c.BodyParser(&body); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	code := compatGroupInviteCode(firstNonEmpty(body.Code, body.Link))
	if code == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "code atau link wajib diisi", nil)
	}
	groupID, err := client.JoinGroupWithLink(c.UserContext(), code)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_JOIN_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success joined group", fiber.Map{"group_id": groupID.String()})
}

func compatGroupInfoFromLink(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	code := compatGroupInviteCode(firstNonEmpty(c.Query("code"), c.Query("link")))
	if code == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "code atau link wajib diisi", nil)
	}
	info, err := client.GetGroupInfoFromLink(c.UserContext(), code)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_INFO_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success get group info from link", fiber.Map{
		"group_id":           info.JID.String(),
		"name":               info.Name,
		"participants_count": len(info.Participants),
	})
}

func compatGroupInfo(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	groupID := firstNonEmpty(c.Query("group_id"), c.Query("jid"))
	jid, err := parseGroupJID(groupID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "INVALID_GROUP_ID", "Group ID cannot be empty", nil)
	}
	info, err := whatsapp.GetGroupInfoForUserAccount(user.ID, accountID, jid)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_INFO_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success get group info", fiber.Map{
		"group_id":           info.JID.String(),
		"name":               info.Name,
		"topic":              info.Topic,
		"locked":             info.IsLocked,
		"announce":           info.IsAnnounce,
		"participants_count": len(info.Participants),
	})
}

func compatLeaveGroup(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	var body struct {
		GroupID string `json:"group_id"`
	}
	if err := c.BodyParser(&body); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "Invalid request body", nil)
	}
	jid, err := parseGroupJID(body.GroupID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "INVALID_GROUP_ID", "Group ID cannot be empty", nil)
	}
	if err := client.LeaveGroup(c.UserContext(), jid); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_LEAVE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success leave group", nil)
}

func compatGroupParticipants(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	groupID := firstNonEmpty(c.Query("group_id"), c.Query("jid"))
	jid, err := parseGroupJID(groupID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "INVALID_GROUP_ID", "Group ID cannot be empty", nil)
	}
	info, err := whatsapp.GetGroupInfoForUserAccount(user.ID, accountID, jid)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_PARTICIPANTS_FAILED", err.Error(), nil)
	}
	participants := make([]fiber.Map, 0, len(info.Participants))
	for _, p := range info.Participants {
		role := "member"
		if p.IsSuperAdmin {
			role = "super_admin"
		} else if p.IsAdmin {
			role = "admin"
		}
		participants = append(participants, fiber.Map{
			"jid":           p.JID.String(),
			"phone_number":  firstNonEmpty(p.PhoneNumber.User, p.JID.User),
			"lid":           p.LID.String(),
			"display_name":  firstNonEmpty(p.DisplayName, p.PhoneNumber.User, p.JID.User),
			"role":          role,
			"is_admin":      p.IsAdmin,
			"is_superadmin": p.IsSuperAdmin,
		})
	}
	return compatOK(c, "Success getting group participants", fiber.Map{
		"group_id":     info.JID.String(),
		"group_name":   info.Name,
		"participants": participants,
	})
}

func compatGroupParticipantsExport(c *fiber.Ctx) error {
	user, accountID, _, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	groupID := firstNonEmpty(c.Query("group_id"), c.Query("jid"))
	jid, err := parseGroupJID(groupID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "INVALID_GROUP_ID", "Group ID cannot be empty", nil)
	}
	info, err := whatsapp.GetGroupInfoForUserAccount(user.ID, accountID, jid)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_PARTICIPANTS_FAILED", err.Error(), nil)
	}
	var sb strings.Builder
	sb.WriteString("participant_jid,phone_number,lid,display_name,role\n")
	for _, p := range info.Participants {
		role := "member"
		if p.IsSuperAdmin {
			role = "super_admin"
		} else if p.IsAdmin {
			role = "admin"
		}
		sb.WriteString(fmt.Sprintf("%q,%q,%q,%q,%q\n",
			p.JID.String(),
			firstNonEmpty(p.PhoneNumber.User, p.JID.User),
			p.LID.String(),
			firstNonEmpty(p.DisplayName, p.PhoneNumber.User, p.JID.User),
			role,
		))
	}
	c.Type("csv")
	c.Attachment(fmt.Sprintf("group-%s-participants.csv", strings.ReplaceAll(info.JID.String(), "@", "_")))
	return c.SendString(sb.String())
}

func compatGroupInviteLink(c *fiber.Ctx) error {
	_, _, client, err := compatResolveAccount(c, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	groupID := firstNonEmpty(c.Query("group_id"), c.Query("jid"))
	jid, err := parseGroupJID(groupID)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "INVALID_GROUP_ID", "Group ID cannot be empty", nil)
	}
	link, err := client.GetGroupInviteLink(c.UserContext(), jid, false)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "GROUP_INVITE_LINK_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Success get invite link", fiber.Map{"group_id": jid.String(), "invite_link": link})
}

func compatLoginViaQR(c *fiber.Ctx, deviceID string) error {
	user, accountID, client, err := compatResolveAccountWithDevice(c, deviceID, true)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	if client.IsLoggedIn() {
		return compatOK(c, "Login success", fiber.Map{
			"device_id":    accountID,
			"qr_link":      "",
			"qr_duration":  0,
			"is_logged_in": true,
		})
	}
	qrCtx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	keepQRAlive := false
	defer func() {
		if !keepQRAlive {
			cancel()
		}
	}()
	qrChan, err := client.GetQRChannel(qrCtx)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "QR_FAILED", err.Error(), nil)
	}
	if err := client.ConnectContext(qrCtx); err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "CONNECT_FAILED", err.Error(), nil)
	}
	for evt := range qrChan {
		switch evt.Event {
		case "code":
			png, err := qrcode.Encode(evt.Code, qrcode.Medium, 512)
			if err != nil {
				return compatAPIError(c, fiber.StatusInternalServerError, "QR_FAILED", "Failed to generate QR", nil)
			}
			// Keep the socket alive after sending the QR so WhatsApp can finish pairing.
			keepQRAlive = true
			return compatOK(c, "Login success", fiber.Map{
				"device_id":   accountID,
				"qr_link":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
				"qr_duration": 60,
			})
		case "success":
			account, _ := whatsapp.GetAccountForUser(user.ID, accountID)
			return compatOK(c, "Login success", fiber.Map{
				"device_id":    accountID,
				"is_logged_in": true,
				"jid":          account.JID,
			})
		case "error":
			msg := "QR pairing error"
			if evt.Error != nil {
				msg = evt.Error.Error()
			}
			return compatAPIError(c, fiber.StatusBadRequest, "QR_FAILED", msg, nil)
		}
	}
	return compatAPIError(c, fiber.StatusRequestTimeout, "QR_TIMEOUT", "QR timeout", nil)
}

func compatLoginWithPairCode(c *fiber.Ctx, deviceID string) error {
	_, accountID, client, err := compatResolveAccountWithDevice(c, deviceID, true)
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "DEVICE_ID_REQUIRED", err.Error(), nil)
	}
	phone := normalizePhone(c.Query("phone"))
	if phone == "" {
		return compatAPIError(c, fiber.StatusBadRequest, "BAD_REQUEST", "phone wajib diisi", nil)
	}
	if client.IsLoggedIn() {
		return compatOK(c, "Login with code success", fiber.Map{"device_id": accountID, "pair_code": ""})
	}
	if !client.IsConnected() {
		if err := client.Connect(); err != nil {
			return compatAPIError(c, fiber.StatusBadRequest, "CONNECT_FAILED", err.Error(), nil)
		}
		time.Sleep(2 * time.Second)
	}
	pairCode, err := client.PairPhone(c.UserContext(), phone, true, whatsmeow.PairClientChrome, "InstaBlast Pro")
	if err != nil {
		return compatAPIError(c, fiber.StatusBadRequest, "PAIR_CODE_FAILED", err.Error(), nil)
	}
	return compatOK(c, "Login with code success", fiber.Map{"device_id": accountID, "pair_code": pairCode})
}
