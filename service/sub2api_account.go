package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

const sub2APIUserAgent = "new-api/Sub2API-account"

// Sub2APIError never embeds an upstream body, URL or credential.
type Sub2APIError struct {
	Code   string
	Status int
}

func (e *Sub2APIError) Error() string { return e.Code }

func sub2APIError(code string) error { return &Sub2APIError{Code: code} }

func Sub2APIErrorCode(err error) string {
	var upstream *Sub2APIError
	if errors.As(err, &upstream) {
		return upstream.Code
	}
	return "SUB2API_SYNC_FAILED"
}

type Sub2APICredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

type Sub2APILoginInput struct {
	Email                 string `json:"email"`
	Password              string `json:"password"`
	TurnstileToken        string `json:"turnstile_token,omitempty"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket,omitempty"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr,omitempty"`
}

type Sub2APILoginResult struct {
	Requires2FA bool   `json:"requires_2fa"`
	Flow        string `json:"flow,omitempty"`
	EmailMasked string `json:"email_masked,omitempty"`
	AccountID   int64  `json:"account_id,omitempty"`
}

type sub2APIUser struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

type sub2APIKey struct {
	ID      int64  `json:"id"`
	UserID  int64  `json:"user_id"`
	GroupID int64  `json:"group_id"`
	Key     string `json:"key"`
}

type sub2APILoginResponse struct {
	Sub2APICredential
	User            sub2APIUser `json:"user"`
	Requires2FA     bool        `json:"requires_2fa"`
	TempToken       string      `json:"temp_token"`
	UserEmailMasked string      `json:"user_email_masked"`
}

type sub2APIChallenge struct {
	ChannelID     int                         `json:"channel_id"`
	Origin        string                      `json:"origin"`
	ProxyIdentity string                      `json:"proxy_identity"`
	TempToken     string                      `json:"temp_token"`
	Authorization model.AuthFlowAuthorization `json:"authorization"`
}

func sub2APISecretCipher() (cipher.AEAD, error) {
	secret := os.Getenv("SUB2API_CREDENTIAL_SECRET")
	if secret == "" {
		secret = os.Getenv("CRYPTO_SECRET")
	}
	if len(secret) < 32 {
		return nil, sub2APIError("SUB2API_ENCRYPTION_UNAVAILABLE")
	}
	key := sha256.Sum256([]byte("new-api:sub2api:credentials:v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Sub2APIEncryptionReady() bool {
	_, err := sub2APISecretCipher()
	return err == nil
}

func sub2APISeal(value any, scope string) (string, error) {
	aead, err := sub2APISecretCipher()
	if err != nil {
		return "", err
	}
	plain, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	encrypted := aead.Seal(nonce, nonce, plain, []byte(scope))
	return "v1." + base64.RawStdEncoding.EncodeToString(encrypted), nil
}

func sub2APIOpen(value, scope string, target any) error {
	aead, err := sub2APISecretCipher()
	if err != nil {
		return err
	}
	encoded, ok := strings.CutPrefix(value, "v1.")
	if !ok || len(encoded) > 65536 {
		return sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(scope))
	if err != nil || common.Unmarshal(plain, target) != nil {
		return sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	return nil
}

func sub2APIAccountScope(account *model.Sub2APIAccount) string {
	return fmt.Sprintf("sub2api:account:%d:%s:%d", account.ChannelID, account.Origin, account.UpstreamUserID)
}

func sub2APIProxyIdentity(proxy string) string {
	hash := sha256.Sum256([]byte(proxy))
	return hex.EncodeToString(hash[:])
}

func sub2APIChannel(channelID int) (*model.Channel, string, error) {
	if !model.Sub2APIStorageReady() {
		return nil, "", sub2APIError("SUB2API_STORAGE_UNAVAILABLE")
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil || channel.Type != constant.ChannelTypeSub2API {
		return nil, "", sub2APIError("SUB2API_CHANNEL_INVALID")
	}
	parsed, err := url.Parse(channel.GetBaseURL())
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, "", sub2APIError("SUB2API_HTTPS_REQUIRED")
	}
	parsed.Path = strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), "/v1")
	parsed.RawPath = ""
	return channel, strings.TrimSuffix(parsed.String(), "/"), nil
}

func sub2APIRequest(ctx context.Context, origin, proxy, token, method, path string, query url.Values, payload, result any, rawResponse bool) error {
	if !strings.HasPrefix(path, "/api/v1/") && path != "/v1/sub2api/billing" {
		return sub2APIError("SUB2API_PATH_INVALID")
	}
	if strings.ContainsAny(path, "?#\\") || strings.Contains(path, "..") {
		return sub2APIError("SUB2API_PATH_INVALID")
	}
	target := origin + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := common.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return sub2APIError("SUB2API_REQUEST_FAILED")
	}
	request.Header.Set("User-Agent", sub2APIUserAgent)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	var proxyURL *url.URL
	if proxy != "" {
		proxyURL, err = url.Parse(proxy)
		if err != nil {
			return sub2APIError("SUB2API_REQUEST_FAILED")
		}
	}
	// Account secrets never inherit the relay's optional insecure-TLS setting.
	// Keep configured trust roots, including private enterprise CAs.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport.TLSClientConfig != nil {
		tlsConfig.RootCAs = transport.TLSClientConfig.RootCAs
	}
	policy := defaultHTTPTransportPolicy()
	policy.TLSInsecureSkipVerify = false
	client, err := newHTTPClientFromPolicy(policy, proxyURL, tlsConfig)
	if err != nil {
		return sub2APIError("SUB2API_REQUEST_FAILED")
	}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return sub2APIError("SUB2API_REQUEST_FAILED")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "SUB2API_REQUEST_FAILED"
		switch response.StatusCode {
		case http.StatusUnauthorized:
			code = "SUB2API_LOGIN_REQUIRED"
		case http.StatusForbidden:
			code = "SUB2API_ACCESS_DENIED"
		case http.StatusNotFound:
			code = "SUB2API_UNSUPPORTED"
		case http.StatusTooManyRequests:
			code = "SUB2API_RATE_LIMITED"
		}
		// Only recognize captcha categories, never relay arbitrary upstream text.
		var failure struct {
			Reason string `json:"reason"`
		}
		if common.Unmarshal(data, &failure) == nil && (strings.Contains(failure.Reason, "CAPTCHA") || strings.Contains(failure.Reason, "TURNSTILE")) {
			code = "SUB2API_CAPTCHA_REQUIRED"
		}
		return &Sub2APIError{Code: code, Status: response.StatusCode}
	}
	if rawResponse {
		if common.Unmarshal(data, result) != nil {
			return sub2APIError("SUB2API_RESPONSE_INVALID")
		}
		return nil
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if common.Unmarshal(data, &envelope) != nil || envelope.Code != 0 || len(envelope.Data) == 0 {
		return sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	if result != nil && common.Unmarshal(envelope.Data, result) != nil {
		return sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	return nil
}

func verifySub2APICredential(ctx context.Context, channel *model.Channel, origin string, credential Sub2APICredential) (sub2APIUser, []model.Sub2APIKeyBinding, error) {
	var user sub2APIUser
	if credential.AccessToken == "" || len(credential.AccessToken) > 16384 || len(credential.RefreshToken) > 16384 || credential.ExpiresIn < 0 || credential.ExpiresIn > 365*86400 {
		return user, nil, sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	proxy := channel.GetSetting().Proxy
	if err := sub2APIRequest(ctx, origin, proxy, credential.AccessToken, http.MethodGet, "/api/v1/auth/me", nil, nil, &user, false); err != nil {
		return user, nil, err
	}
	if user.ID <= 0 || len(user.Email) > 320 {
		return user, nil, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	keys := make(map[string]bool)
	for _, key := range channel.GetKeys() {
		keys[channel.KeyIdentity(key)] = true
	}
	bindings := make([]model.Sub2APIKeyBinding, 0)
	matched := make(map[string]int64)
	for page := 1; page <= 100; page++ {
		var list struct {
			Items []sub2APIKey `json:"items"`
			Pages int          `json:"pages"`
		}
		query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {"100"}}
		if err := sub2APIRequest(ctx, origin, proxy, credential.AccessToken, http.MethodGet, "/api/v1/keys", query, nil, &list, false); err != nil {
			return user, nil, err
		}
		for _, key := range list.Items {
			identity := channel.KeyIdentity(key.Key)
			if key.Key != "" && keys[identity] && key.ID > 0 && key.UserID == user.ID {
				if previous, exists := matched[identity]; exists {
					if previous != key.ID {
						return user, nil, sub2APIError("SUB2API_RESPONSE_INVALID")
					}
					continue
				}
				matched[identity] = key.ID
				bindings = append(bindings, model.Sub2APIKeyBinding{ChannelID: channel.Id, KeyIdentity: identity, UpstreamAPIKeyID: key.ID, UpstreamGroupID: key.GroupID})
			}
		}
		if page >= list.Pages || len(list.Items) == 0 {
			return user, bindings, nil
		}
	}
	return user, nil, sub2APIError("SUB2API_RESPONSE_INVALID")
}

func connectSub2APICredential(ctx context.Context, channel *model.Channel, origin string, credential Sub2APICredential, authorization *model.AuthFlowAuthorization) (Sub2APILoginResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	user, bindings, err := verifySub2APICredential(ctx, channel, origin, credential)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	if len(bindings) == 0 {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_NO_MATCHING_KEYS")
	}
	account := model.Sub2APIAccount{ChannelID: channel.Id, Origin: origin, ProxyIdentity: sub2APIProxyIdentity(channel.GetSetting().Proxy), UpstreamUserID: user.ID, Email: user.Email, HasRefreshToken: credential.RefreshToken != "", Revision: time.Now().UnixNano()}
	if credential.ExpiresIn > 0 {
		account.ExpiresAt = time.Now().Unix() + credential.ExpiresIn
	}
	digest := sha256.Sum256([]byte(sub2APIAccountScope(&account)))
	account.Identity = hex.EncodeToString(digest[:])
	account.Credential, err = sub2APISeal(credential, sub2APIAccountScope(&account))
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	err = model.WithPluginCredentialRefreshLock(ctx, channel.Id, func() error {
		current, currentOrigin, err := sub2APIChannel(channel.Id)
		if err != nil || currentOrigin != origin || sub2APIProxyIdentity(current.GetSetting().Proxy) != account.ProxyIdentity {
			return sub2APIError("SUB2API_CHANNEL_CHANGED")
		}
		return model.StoreSub2APIAccount(ctx, &account, bindings, authorization)
	})
	return Sub2APILoginResult{AccountID: account.ID}, err
}

func LoginSub2API(ctx context.Context, channelID int, input Sub2APILoginInput, authorization *model.AuthFlowAuthorization) (Sub2APILoginResult, error) {
	if !Sub2APIEncryptionReady() || authorization == nil {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_ENCRYPTION_UNAVAILABLE")
	}
	channel, origin, err := sub2APIChannel(channelID)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	if input.Email == "" || len(input.Email) > 320 || input.Password == "" || len(input.Password) > 4096 {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	var response sub2APILoginResponse
	if err := sub2APIRequest(ctx, origin, channel.GetSetting().Proxy, "", http.MethodPost, "/api/v1/auth/login", nil, input, &response, false); err != nil {
		return Sub2APILoginResult{}, err
	}
	if !response.Requires2FA {
		return connectSub2APICredential(ctx, channel, origin, response.Sub2APICredential, authorization)
	}
	if response.TempToken == "" || len(response.TempToken) > 4096 {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	challenge := sub2APIChallenge{channelID, origin, sub2APIProxyIdentity(channel.GetSetting().Proxy), response.TempToken, *authorization}
	sealed, err := sub2APISeal(challenge, "sub2api:challenge:"+strconv.Itoa(channelID))
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	flow, _, err := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: "sub2api_login", Provider: strconv.Itoa(channelID), Intent: "bind", UserId: authorization.UserID, SessionId: authorization.SessionID, Payload: sealed, ExpiresAt: time.Now().Add(5 * time.Minute)})
	return Sub2APILoginResult{Requires2FA: true, Flow: flow, EmailMasked: response.UserEmailMasked}, err
}

func CompleteSub2APILogin(ctx context.Context, channelID int, flow, code string, identity AuthIdentity) (Sub2APILoginResult, error) {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_2FA_INVALID")
	}
	stored, err := model.ConsumeAuthFlow(flow, model.AuthFlowMatch{Purpose: "sub2api_login", Provider: strconv.Itoa(channelID), Intent: "bind", UserId: identity.UserID, SessionId: identity.SessionID})
	if err != nil {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_FLOW_EXPIRED")
	}
	var challenge sub2APIChallenge
	if err := sub2APIOpen(stored.Payload, "sub2api:challenge:"+strconv.Itoa(channelID), &challenge); err != nil {
		return Sub2APILoginResult{}, err
	}
	operationContext, _ := common.Marshal(Sub2APIManageContext{ChannelID: channelID, Action: "login"})
	if err := ValidateFlowAuthorization(identity, VerificationOperation{Scope: VerificationScopeSub2APIManage, Context: operationContext}, &challenge.Authorization); err != nil {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_FLOW_EXPIRED")
	}
	channel, origin, err := sub2APIChannel(channelID)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	if challenge.Origin != origin || challenge.ProxyIdentity != sub2APIProxyIdentity(channel.GetSetting().Proxy) {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_CHANNEL_CHANGED")
	}
	var response sub2APILoginResponse
	err = sub2APIRequest(ctx, origin, channel.GetSetting().Proxy, "", http.MethodPost, "/api/v1/auth/login/2fa", nil, map[string]string{"temp_token": challenge.TempToken, "totp_code": code}, &response, false)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	return connectSub2APICredential(ctx, channel, origin, response.Sub2APICredential, &challenge.Authorization)
}

func ImportSub2API(ctx context.Context, channelID int, credential Sub2APICredential, authorization *model.AuthFlowAuthorization) (Sub2APILoginResult, error) {
	channel, origin, err := sub2APIChannel(channelID)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	return connectSub2APICredential(ctx, channel, origin, credential, authorization)
}

func sub2APILoadAccount(accountID int64) (*model.Sub2APIAccount, *model.Channel, Sub2APICredential, error) {
	var credential Sub2APICredential
	account, err := model.GetSub2APIAccount(accountID)
	if err != nil {
		return nil, nil, credential, sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	channel, origin, err := sub2APIChannel(account.ChannelID)
	if err != nil {
		return nil, nil, credential, err
	}
	if account.NeedsLogin {
		return nil, nil, credential, sub2APIError("SUB2API_LOGIN_REQUIRED")
	}
	if account.Origin != origin || account.ProxyIdentity != sub2APIProxyIdentity(channel.GetSetting().Proxy) {
		return nil, nil, credential, sub2APIError("SUB2API_CHANNEL_CHANGED")
	}
	err = sub2APIOpen(account.Credential, sub2APIAccountScope(account), &credential)
	return account, channel, credential, err
}

func RefreshSub2APIAccount(ctx context.Context, accountID int64, expectedRevision int64) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	account, err := model.GetSub2APIAccount(accountID)
	if err != nil {
		return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	return model.WithPluginCredentialRefreshLock(ctx, account.ChannelID, func() error {
		account, channel, credential, err := sub2APILoadAccount(accountID)
		if err != nil {
			return err
		}
		if expectedRevision > 0 && account.Revision != expectedRevision {
			return nil
		}
		if credential.RefreshToken == "" {
			return sub2APIError("SUB2API_LOGIN_REQUIRED")
		}
		// A crash or an uncertain network result must never replay a rotated
		// refresh token. Persist the fail-closed state before sending it.
		account.NeedsLogin, account.LastError = true, "SUB2API_LOGIN_REQUIRED"
		marked, err := model.UpdateSub2APICredential(account, account.Revision)
		if err != nil {
			return err
		}
		if !marked {
			return sub2APIError("SUB2API_CHANNEL_CHANGED")
		}
		account.Revision++
		var next Sub2APICredential
		err = sub2APIRequest(ctx, account.Origin, channel.GetSetting().Proxy, "", http.MethodPost, "/api/v1/auth/refresh", nil, map[string]string{"refresh_token": credential.RefreshToken}, &next, false)
		if err != nil || next.AccessToken == "" || next.RefreshToken == "" || next.ExpiresIn <= 0 || next.ExpiresIn > 365*86400 {
			// The upstream may already have consumed this token. Do not replay it.
			account.NeedsLogin, account.LastError = true, "SUB2API_LOGIN_REQUIRED"
			_, _ = model.UpdateSub2APICredential(account, account.Revision)
			return sub2APIError("SUB2API_LOGIN_REQUIRED")
		}
		account.Credential, err = sub2APISeal(next, sub2APIAccountScope(account))
		if err != nil {
			return err
		}
		var refreshedUser sub2APIUser
		if err := sub2APIRequest(ctx, account.Origin, channel.GetSetting().Proxy, next.AccessToken, http.MethodGet, "/api/v1/auth/me", nil, nil, &refreshedUser, false); err != nil || refreshedUser.ID != account.UpstreamUserID {
			account.NeedsLogin, account.LastError = true, "SUB2API_LOGIN_REQUIRED"
			_, _ = model.UpdateSub2APICredential(account, account.Revision)
			return sub2APIError("SUB2API_LOGIN_REQUIRED")
		}
		account.ExpiresAt, account.HasRefreshToken, account.NeedsLogin, account.LastError = time.Now().Unix()+next.ExpiresIn, true, false, ""
		stored, err := model.UpdateSub2APICredential(account, account.Revision)
		if err == nil && !stored {
			return sub2APIError("SUB2API_CHANNEL_CHANGED")
		}
		return err
	})
}

// Sub2APIAccountRequest is the only management-token client exposed to billing
// reconciliation. It returns typed decoded data, never the bearer credential.
func Sub2APIAccountRequest(ctx context.Context, accountID int64, method, path string, query url.Values, payload, result any) error {
	if method != http.MethodGet || path != "/api/v1/usage" && path != "/api/v1/keys" && path != "/api/v1/auth/me" && !strings.HasPrefix(path, "/api/v1/usage/") {
		return sub2APIError("SUB2API_PATH_INVALID")
	}
	account, channel, credential, err := sub2APILoadAccount(accountID)
	if err != nil {
		return err
	}
	if account.ExpiresAt > 0 && account.ExpiresAt <= time.Now().Unix()+30 {
		if err := RefreshSub2APIAccount(ctx, accountID, account.Revision); err != nil {
			return err
		}
		account, channel, credential, err = sub2APILoadAccount(accountID)
		if err != nil {
			return err
		}
	}
	err = sub2APIRequest(ctx, account.Origin, channel.GetSetting().Proxy, credential.AccessToken, method, path, query, payload, result, false)
	var upstream *Sub2APIError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusUnauthorized {
		return err
	}
	if err := RefreshSub2APIAccount(ctx, accountID, account.Revision); err != nil {
		return err
	}
	account, channel, credential, err = sub2APILoadAccount(accountID)
	if err != nil {
		return err
	}
	return sub2APIRequest(ctx, account.Origin, channel.GetSetting().Proxy, credential.AccessToken, method, path, query, payload, result, false)
}

func DisconnectSub2API(ctx context.Context, channelID int, accountID int64, authorization *model.AuthFlowAuthorization) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return model.WithPluginCredentialRefreshLock(ctx, channelID, func() error {
		account, channel, credential, err := sub2APILoadAccount(accountID)
		if err == nil && account.ChannelID != channelID {
			return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
		}
		if err == nil && credential.RefreshToken != "" {
			_ = sub2APIRequest(ctx, account.Origin, channel.GetSetting().Proxy, "", http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{"refresh_token": credential.RefreshToken}, nil, false)
		}
		return model.DeleteSub2APIAccount(channelID, accountID, authorization)
	})
}

// SyncSub2APIChannel refreshes only supplier metadata, not user model prices.
func SyncSub2APIChannel(ctx context.Context, channelID int, accountID int64) error {
	channel, origin, err := sub2APIChannel(channelID)
	if err != nil {
		return err
	}
	accounts, err := model.ListSub2APIAccounts(channelID)
	if err != nil {
		return err
	}
	var lastErr error
	for _, account := range accounts {
		if accountID > 0 && account.ID != accountID {
			continue
		}
		var user sub2APIUser
		if err := Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/auth/me", nil, nil, &user); err != nil {
			lastErr = err
			if saveErr := model.UpdateSub2APIAccountSync(account.ID, Sub2APIErrorCode(err)); saveErr != nil {
				return saveErr
			}
			continue
		}
		stored, _, credential, err := sub2APILoadAccount(account.ID)
		if err != nil {
			lastErr = err
			continue
		}
		verified, bindings, err := verifySub2APICredential(ctx, channel, origin, credential)
		if err != nil || verified.ID != stored.UpstreamUserID {
			if err == nil {
				err = sub2APIError("SUB2API_CREDENTIAL_INVALID")
			}
			lastErr = err
			if saveErr := model.UpdateSub2APIAccountSync(account.ID, Sub2APIErrorCode(err)); saveErr != nil {
				return saveErr
			}
			continue
		}
		if err := model.ReplaceSub2APIAccountBindings(stored, bindings); err != nil {
			return err
		}
		if err := model.UpdateSub2APIAccountSync(account.ID, ""); err != nil {
			return err
		}
	}
	for _, key := range channel.GetKeys() {
		identity := channel.KeyIdentity(key)
		var response struct {
			model.Sub2APIRate
			Object        string   `json:"object"`
			SchemaVersion int      `json:"schema_version"`
			BillingScope  string   `json:"billing_scope"`
			Resolved      *float64 `json:"resolved_rate_multiplier"`
			Effective     *float64 `json:"effective_rate_multiplier"`
			PeakEnabled   *bool    `json:"peak_rate_enabled"`
			PeakRate      *float64 `json:"peak_rate_multiplier"`
		}
		err := sub2APIRequest(ctx, origin, channel.GetSetting().Proxy, key, http.MethodGet, "/v1/sub2api/billing", nil, nil, &response, true)
		if err == nil {
			_, observedErr := time.Parse(time.RFC3339Nano, response.ObservedAt)
			if response.Resolved == nil || response.Effective == nil || response.PeakEnabled == nil || observedErr != nil || response.Object != "sub2api.key_billing" || response.SchemaVersion != 1 || response.BillingScope != "token" {
				err = sub2APIError("SUB2API_RESPONSE_INVALID")
			} else {
				response.ResolvedRateMultiplier, response.EffectiveRateMultiplier, response.PeakRateEnabled = *response.Resolved, *response.Effective, *response.PeakEnabled
				if response.PeakRate != nil {
					response.PeakRateMultiplier = *response.PeakRate
				}
				if _, valid := response.EffectiveAt(time.Now()); !valid || response.PeakRateEnabled && response.PeakRate == nil {
					err = sub2APIError("SUB2API_RESPONSE_INVALID")
				}
			}
		}
		if err != nil {
			lastErr = err
			if storeErr := model.StoreSub2APIRate(channelID, identity, nil, Sub2APIErrorCode(err)); storeErr != nil {
				return storeErr
			}
			continue
		}
		response.SourceIdentity = channel.Sub2APIConnectionIdentity()
		if err := model.StoreSub2APIRate(channelID, identity, &response.Sub2APIRate, ""); err != nil {
			return err
		}
	}
	return lastErr
}
