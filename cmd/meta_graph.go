package cmd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
)

const (
	metaGraphBaseURL          = "https://graph.facebook.com"
	metaGraphDefaultAPIVerion = "v22.0"
)

type metaGraphClient struct {
	appID      string
	appSecret  string
	apiVersion string
	httpClient *http.Client
}

type metaTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

type metaBusinessInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type metaPhoneNumberInfo struct {
	ID                     string `json:"id"`
	DisplayPhoneNumber     string `json:"display_phone_number"`
	VerifiedName           string `json:"verified_name"`
	CodeVerificationStatus string `json:"code_verification_status"`
	QualityRating          string `json:"quality_rating"`
	NameStatus             string `json:"name_status"`
	Status                 string `json:"status"`
}

type metaGraphErrorEnvelope struct {
	Error struct {
		Message      string `json:"message"`
		Type         string `json:"type"`
		Code         int    `json:"code"`
		ErrorSubcode int    `json:"error_subcode"`
		FBTraceID    string `json:"fbtrace_id"`
	} `json:"error"`
}

type metaPhoneNumbersResponse struct {
	Data []metaPhoneNumberInfo `json:"data"`
}

type metaGenericNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newMetaGraphClient(cfg metaConfig) *metaGraphClient {
	version := strings.TrimSpace(cfg.GraphVersion)
	if version == "" {
		version = metaGraphDefaultAPIVerion
	}
	return &metaGraphClient{
		appID:      strings.TrimSpace(cfg.AppID),
		appSecret:  strings.TrimSpace(cfg.AppSecret),
		apiVersion: version,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *metaGraphClient) ExchangeCode(ctx context.Context, code, redirectURI string) (metaTokenResponse, error) {
	tryExchange := func(includeRedirect bool) (metaTokenResponse, error) {
		values := url.Values{}
		values.Set("client_id", c.appID)
		values.Set("client_secret", c.appSecret)
		values.Set("code", strings.TrimSpace(code))
		if includeRedirect && strings.TrimSpace(redirectURI) != "" {
			values.Set("redirect_uri", strings.TrimSpace(redirectURI))
		}

		var out metaTokenResponse
		if err := c.doJSON(ctx, http.MethodGet, "/oauth/access_token", values, "", &out); err != nil {
			return metaTokenResponse{}, err
		}
		if strings.TrimSpace(out.AccessToken) == "" {
			return metaTokenResponse{}, fmt.Errorf("Meta tidak mengembalikan business access token")
		}
		return out, nil
	}

	out, err := tryExchange(false)
	if err == nil {
		return out, nil
	}
	if strings.TrimSpace(redirectURI) == "" {
		return metaTokenResponse{}, err
	}
	redirectOut, redirectErr := tryExchange(true)
	if redirectErr == nil {
		return redirectOut, nil
	}
	return metaTokenResponse{}, redirectErr
}

func (c *metaGraphClient) GetBusiness(ctx context.Context, businessID, accessToken string) (metaBusinessInfo, error) {
	values := url.Values{}
	values.Set("fields", "id,name")

	var out metaBusinessInfo
	if err := c.doJSON(ctx, http.MethodGet, "/"+strings.TrimSpace(businessID), values, accessToken, &out); err != nil {
		return metaBusinessInfo{}, err
	}
	return out, nil
}

func (c *metaGraphClient) GetWABA(ctx context.Context, wabaID, accessToken string) (metaGenericNode, error) {
	values := url.Values{}
	values.Set("fields", "id,name")

	var out metaGenericNode
	if err := c.doJSON(ctx, http.MethodGet, "/"+strings.TrimSpace(wabaID), values, accessToken, &out); err != nil {
		return metaGenericNode{}, err
	}
	return out, nil
}

func (c *metaGraphClient) GetPhoneNumber(ctx context.Context, phoneNumberID, accessToken string) (metaPhoneNumberInfo, error) {
	values := url.Values{}
	values.Set("fields", "id,display_phone_number,verified_name,code_verification_status,quality_rating,name_status,status")

	var out metaPhoneNumberInfo
	if err := c.doJSON(ctx, http.MethodGet, "/"+strings.TrimSpace(phoneNumberID), values, accessToken, &out); err != nil {
		return metaPhoneNumberInfo{}, err
	}
	return out, nil
}

func (c *metaGraphClient) ListPhoneNumbers(ctx context.Context, wabaID, accessToken string) ([]metaPhoneNumberInfo, error) {
	values := url.Values{}
	values.Set("fields", "id,display_phone_number,verified_name,quality_rating")

	var out metaPhoneNumbersResponse
	if err := c.doJSON(ctx, http.MethodGet, "/"+strings.TrimSpace(wabaID)+"/phone_numbers", values, accessToken, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *metaGraphClient) SubscribeApp(ctx context.Context, wabaID, accessToken string) error {
	if strings.TrimSpace(wabaID) == "" {
		return fmt.Errorf("waba_id kosong")
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/"+strings.TrimSpace(wabaID)+"/subscribed_apps", nil, accessToken, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("Meta tidak mengonfirmasi webhook subscription")
	}
	return nil
}

func (c *metaGraphClient) buildURL(path string, values url.Values) string {
	base := strings.TrimRight(metaGraphBaseURL, "/") + "/" + strings.TrimLeft(c.apiVersion, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if values == nil {
		values = url.Values{}
	}
	if encoded := values.Encode(); encoded != "" {
		return base + path + "?" + encoded
	}
	return base + path
}

func (c *metaGraphClient) appSecretProof(accessToken string) string {
	mac := hmac.New(sha256.New, []byte(c.appSecret))
	_, _ = mac.Write([]byte(accessToken))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *metaGraphClient) doJSON(ctx context.Context, method, path string, values url.Values, accessToken string, out interface{}) error {
	if values == nil {
		values = url.Values{}
	}

	var body io.Reader
	requestURL := c.buildURL(path, values)

	if method == http.MethodPost {
		form := url.Values{}
		for key, vals := range values {
			for _, val := range vals {
				form.Add(key, val)
			}
		}
		if strings.TrimSpace(accessToken) != "" {
			form.Set("access_token", accessToken)
			if strings.TrimSpace(c.appSecret) != "" {
				form.Set("appsecret_proof", c.appSecretProof(accessToken))
			}
		}
		body = strings.NewReader(form.Encode())
		requestURL = c.buildURL(path, nil)
	} else if strings.TrimSpace(accessToken) != "" {
		values.Set("access_token", accessToken)
		if strings.TrimSpace(c.appSecret) != "" {
			values.Set("appsecret_proof", c.appSecretProof(accessToken))
		}
		requestURL = c.buildURL(path, values)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		var envelope metaGraphErrorEnvelope
		if json.Unmarshal(raw, &envelope) == nil && strings.TrimSpace(envelope.Error.Message) != "" {
			return fmt.Errorf("Meta Graph error (%d): %s", envelope.Error.Code, envelope.Error.Message)
		}
		return fmt.Errorf("Meta Graph HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gagal membaca respons Meta: %w", err)
	}
	return nil
}

type metaSignupCompletePayload struct {
	Name          string
	Code          string
	BusinessID    string
	WABAID        string
	PhoneNumberID string
	DisplayPhone  string
}

func finalizeMetaSignup(ctx context.Context, tenantStore *storage.Storage, cfg metaConfig, payload metaSignupCompletePayload) (*storage.MetaWABAAccount, []string, error) {
	if tenantStore == nil {
		return nil, nil, fmt.Errorf("tenant storage belum siap")
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, nil, fmt.Errorf("App ID dan App Secret Meta wajib diisi")
	}
	if strings.TrimSpace(payload.Code) == "" {
		return nil, nil, fmt.Errorf("kode otorisasi Meta tidak ditemukan")
	}

	client := newMetaGraphClient(cfg)
	token, err := client.ExchangeCode(ctx, payload.Code, cfg.RedirectURI)
	if err != nil {
		return nil, nil, err
	}

	businessID := strings.TrimSpace(payload.BusinessID)
	wabaID := strings.TrimSpace(payload.WABAID)
	phoneNumberID := strings.TrimSpace(payload.PhoneNumberID)
	displayPhone := strings.TrimSpace(payload.DisplayPhone)
	accountName := strings.TrimSpace(payload.Name)
	warnings := make([]string, 0, 2)

	if strings.TrimSpace(accountName) == "" && businessID != "" {
		if business, bizErr := client.GetBusiness(ctx, businessID, token.AccessToken); bizErr != nil {
			warnings = append(warnings, "nama bisnis belum bisa diambil dari Meta: "+bizErr.Error())
		} else if strings.TrimSpace(business.Name) != "" {
			accountName = strings.TrimSpace(business.Name)
		}
	}

	var phoneInfo metaPhoneNumberInfo
	if wabaID != "" {
		phones, phoneListErr := client.ListPhoneNumbers(ctx, wabaID, token.AccessToken)
		if phoneListErr != nil {
			warnings = append(warnings, "daftar nomor WABA belum bisa disinkronkan: "+phoneListErr.Error())
		} else if len(phones) > 0 {
			if phoneNumberID != "" {
				for _, candidate := range phones {
					if strings.TrimSpace(candidate.ID) == phoneNumberID {
						phoneInfo = candidate
						break
					}
				}
			}
			if strings.TrimSpace(phoneInfo.ID) == "" {
				phoneInfo = phones[len(phones)-1]
			}
			if phoneNumberID == "" {
				phoneNumberID = strings.TrimSpace(phoneInfo.ID)
			}
		}
	}

	if phoneNumberID != "" {
		if singlePhone, phoneErr := client.GetPhoneNumber(ctx, phoneNumberID, token.AccessToken); phoneErr != nil {
			if strings.TrimSpace(phoneInfo.ID) == "" {
				warnings = append(warnings, "detail nomor belum bisa diambil dari Meta: "+phoneErr.Error())
			}
		} else {
			phoneInfo = singlePhone
		}
	}

	if strings.TrimSpace(phoneNumberID) == "" && strings.TrimSpace(phoneInfo.ID) != "" {
		phoneNumberID = strings.TrimSpace(phoneInfo.ID)
	}
	if strings.TrimSpace(displayPhone) == "" && strings.TrimSpace(phoneInfo.DisplayPhoneNumber) != "" {
		displayPhone = strings.TrimSpace(phoneInfo.DisplayPhoneNumber)
	}
	if strings.TrimSpace(accountName) == "" {
		accountName = firstNonEmpty(
			strings.TrimSpace(phoneInfo.VerifiedName),
			strings.TrimSpace(displayPhone),
			"WABA Cloud",
		)
	}

	if strings.TrimSpace(wabaID) == "" && strings.TrimSpace(phoneNumberID) == "" {
		return nil, warnings, fmt.Errorf("Meta tidak mengirim WABA ID atau Phone Number ID. Selesaikan flow sampai layar akhir lalu klik Finish.")
	}

	status := "active"
	onboardingStatus := "completed"
	if strings.TrimSpace(phoneInfo.Status) != "" && !strings.EqualFold(phoneInfo.Status, "CONNECTED") {
		status = "pending_setup"
		onboardingStatus = "number_status_" + strings.ToLower(strings.TrimSpace(phoneInfo.Status))
	} else if strings.TrimSpace(phoneInfo.CodeVerificationStatus) != "" && !strings.EqualFold(phoneInfo.CodeVerificationStatus, "VERIFIED") {
		status = "pending_setup"
		onboardingStatus = "code_verification_" + strings.ToLower(strings.TrimSpace(phoneInfo.CodeVerificationStatus))
	}

	if strings.TrimSpace(wabaID) != "" {
		if subscribeErr := client.SubscribeApp(ctx, wabaID, token.AccessToken); subscribeErr != nil {
			warnings = append(warnings, "webhook WABA belum berhasil disubscribe: "+subscribeErr.Error())
			if onboardingStatus == "completed" {
				onboardingStatus = "completed_with_webhook_warning"
			}
		}
	}

	account := &storage.MetaWABAAccount{
		Name:               accountName,
		BusinessID:         businessID,
		WABAID:             wabaID,
		PhoneNumberID:      phoneNumberID,
		DisplayPhoneNumber: displayPhone,
		Status:             status,
		OnboardingStatus:   onboardingStatus,
		AccessToken:        strings.TrimSpace(token.AccessToken),
		TokenType:          firstNonEmpty(strings.TrimSpace(token.TokenType), "business_integration_system_user"),
	}
	if token.ExpiresIn > 0 {
		account.TokenExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}

	if err := tenantStore.SaveMetaWABAAccount(account); err != nil {
		return nil, warnings, err
	}
	return account, warnings, nil
}

func finalizeMetaManualConnect(ctx context.Context, tenantStore *storage.Storage, cfg metaConfig, accessToken, wabaID, accountName string) (*storage.MetaWABAAccount, []string, error) {
	if tenantStore == nil {
		return nil, nil, fmt.Errorf("tenant storage belum siap")
	}
	accessToken = strings.TrimSpace(accessToken)
	wabaID = strings.TrimSpace(wabaID)
	accountName = strings.TrimSpace(accountName)
	if accessToken == "" {
		return nil, nil, fmt.Errorf("access token wajib diisi")
	}
	if wabaID == "" {
		return nil, nil, fmt.Errorf("WABA ID wajib diisi")
	}

	client := newMetaGraphClient(metaConfig{
		AppSecret:    cfg.AppSecret,
		GraphVersion: firstNonEmpty(cfg.GraphVersion, metaGraphDefaultAPIVerion),
	})
	warnings := make([]string, 0, 2)

	var wabaNode metaGenericNode
	if node, err := client.GetWABA(ctx, wabaID, accessToken); err == nil {
		wabaNode = node
	} else {
		warnings = append(warnings, "nama WABA belum bisa diambil: "+err.Error())
	}

	phones, err := client.ListPhoneNumbers(ctx, wabaID, accessToken)
	if err != nil {
		return nil, warnings, err
	}

	var phoneInfo metaPhoneNumberInfo
	if len(phones) > 0 {
		phoneInfo = phones[len(phones)-1]
		if singlePhone, phoneErr := client.GetPhoneNumber(ctx, phoneInfo.ID, accessToken); phoneErr == nil {
			phoneInfo = singlePhone
		} else {
			warnings = append(warnings, "detail nomor belum lengkap: "+phoneErr.Error())
		}
	}

	displayPhone := strings.TrimSpace(phoneInfo.DisplayPhoneNumber)
	phoneNumberID := strings.TrimSpace(phoneInfo.ID)
	accountName = firstNonEmpty(
		accountName,
		strings.TrimSpace(wabaNode.Name),
		strings.TrimSpace(phoneInfo.VerifiedName),
		displayPhone,
		"WABA Manual",
	)

	status := "active"
	onboardingStatus := "manual_connected"
	if phoneNumberID == "" {
		status = "pending_setup"
		onboardingStatus = "manual_connected_without_phone"
	} else if strings.TrimSpace(phoneInfo.Status) != "" && !strings.EqualFold(phoneInfo.Status, "CONNECTED") {
		status = "pending_setup"
		onboardingStatus = "number_status_" + strings.ToLower(strings.TrimSpace(phoneInfo.Status))
	} else if strings.TrimSpace(phoneInfo.CodeVerificationStatus) != "" && !strings.EqualFold(phoneInfo.CodeVerificationStatus, "VERIFIED") {
		status = "pending_setup"
		onboardingStatus = "code_verification_" + strings.ToLower(strings.TrimSpace(phoneInfo.CodeVerificationStatus))
	}

	if subscribeErr := client.SubscribeApp(ctx, wabaID, accessToken); subscribeErr != nil {
		warnings = append(warnings, "webhook WABA belum berhasil disubscribe: "+subscribeErr.Error())
		if onboardingStatus == "manual_connected" {
			onboardingStatus = "manual_connected_with_webhook_warning"
		}
	}

	account := &storage.MetaWABAAccount{
		Name:               accountName,
		BusinessID:         strings.TrimSpace(wabaNode.ID),
		WABAID:             wabaID,
		PhoneNumberID:      phoneNumberID,
		DisplayPhoneNumber: displayPhone,
		Status:             status,
		OnboardingStatus:   onboardingStatus,
		AccessToken:        accessToken,
		TokenType:          "manual_access_token",
	}
	if err := tenantStore.SaveMetaWABAAccount(account); err != nil {
		return nil, warnings, err
	}
	return account, warnings, nil
}
