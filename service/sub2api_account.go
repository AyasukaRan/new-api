package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const sub2APIUserAgent = "new-api/Sub2API-account"

// Sub2APIError never embeds an upstream body, URL or credential.
type Sub2APIError struct {
	Code   string
	Status int
	Stage  string
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

// NewSub2APITransportError classifies a connection failure without retaining
// its message, URL, certificate contents or authentication material.
func NewSub2APITransportError(err error) *Sub2APIError {
	code := "SUB2API_NETWORK_FAILED"
	var network net.Error
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		code = "SUB2API_TIMEOUT"
	case errors.As(err, &dns):
		code = "SUB2API_DNS_FAILED"
	case errors.As(err, &certificate), errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &record), errors.As(err, &alert):
		code = "SUB2API_TLS_FAILED"
	}
	return &Sub2APIError{Code: code}
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
	origin, err := NormalizeSub2APIOrigin(channel.GetBaseURL())
	return channel, origin, err
}

// NormalizeSub2APIOrigin includes the deployment path; distinct prefixes and
// ports are separate suppliers even when their DNS host is the same.
func NormalizeSub2APIOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || len(raw) > 512 || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", sub2APIError("SUB2API_HTTPS_REQUIRED")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	parsed.Host = host
	parsed.Path = strings.TrimSuffix(path.Clean("/"+strings.TrimPrefix(parsed.Path, "/")), "/")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/v1")
	parsed.RawPath = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// Legacy credentials retain their original Origin in the authenticated data;
// canonical comparison must not rewrite that persisted cryptographic scope.
func sameSub2APIOrigin(left, right string) bool {
	leftOrigin, leftErr := NormalizeSub2APIOrigin(left)
	rightOrigin, rightErr := NormalizeSub2APIOrigin(right)
	return leftErr == nil && rightErr == nil && leftOrigin == rightOrigin
}

func sub2APIRequest(ctx context.Context, origin, proxy, token, method, path string, query url.Values, payload, result any, rawResponse bool) (requestErr error) {
	stage := "upstream_request"
	switch path {
	case "/api/v1/auth/me":
		stage = "auth_me"
	case "/api/v1/auth/login":
		stage = "login"
	case "/api/v1/auth/login/2fa":
		stage = "login_2fa"
	case "/api/v1/auth/refresh":
		stage = "refresh"
	case "/api/v1/auth/logout":
		stage = "logout"
	case "/api/v1/keys":
		stage = "keys"
	case "/api/v1/usage":
		stage = "usage"
	case "/v1/sub2api/billing":
		stage = "billing"
	}
	defer func() {
		var failure *Sub2APIError
		if errors.As(requestErr, &failure) {
			failure.Stage = stage
		}
	}()
	if !strings.HasPrefix(path, "/api/v1/") && path != "/v1/sub2api/billing" {
		return sub2APIError("SUB2API_PATH_INVALID")
	}
	if strings.ContainsAny(path, "?#\\") || strings.Contains(path, "..") {
		return sub2APIError("SUB2API_PATH_INVALID")
	}
	if strings.ContainsFunc(token, func(char rune) bool { return unicode.IsControl(char) || unicode.IsSpace(char) }) {
		return sub2APIError("SUB2API_CREDENTIAL_INVALID")
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
		return NewSub2APITransportError(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "SUB2API_UPSTREAM_ERROR"
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
		if err == nil && len(data) <= 4<<20 && common.Unmarshal(data, &failure) == nil && (strings.Contains(failure.Reason, "CAPTCHA") || strings.Contains(failure.Reason, "TURNSTILE")) {
			code = "SUB2API_CAPTCHA_REQUIRED"
		}
		return &Sub2APIError{Code: code, Status: response.StatusCode}
	}
	if err != nil {
		failure := NewSub2APITransportError(err)
		failure.Status = response.StatusCode
		return failure
	}
	if len(data) > 4<<20 {
		return &Sub2APIError{Code: "SUB2API_RESPONSE_INVALID", Status: response.StatusCode}
	}
	if rawResponse {
		if common.Unmarshal(data, result) != nil {
			return &Sub2APIError{Code: "SUB2API_RESPONSE_INVALID", Status: response.StatusCode}
		}
		return nil
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if common.Unmarshal(data, &envelope) != nil || envelope.Code != 0 || len(envelope.Data) == 0 {
		return &Sub2APIError{Code: "SUB2API_RESPONSE_INVALID", Status: response.StatusCode}
	}
	if result != nil && common.Unmarshal(envelope.Data, result) != nil {
		return &Sub2APIError{Code: "SUB2API_RESPONSE_INVALID", Status: response.StatusCode}
	}
	return nil
}

func verifySub2APICredential(ctx context.Context, channel *model.Channel, origin, proxy string, credential Sub2APICredential) (sub2APIUser, []model.Sub2APIKeyBinding, error) {
	var user sub2APIUser
	if credential.AccessToken == "" || len(credential.AccessToken) > 16384 || len(credential.RefreshToken) > 16384 || credential.ExpiresIn < 0 || credential.ExpiresIn > 365*86400 {
		return user, nil, sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	if err := sub2APIRequest(ctx, origin, proxy, credential.AccessToken, http.MethodGet, "/api/v1/auth/me", nil, nil, &user, false); err != nil {
		return user, nil, err
	}
	if user.ID <= 0 || len(user.Email) > 320 {
		return user, nil, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	if channel == nil {
		return user, nil, nil
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
	user, bindings, err := verifySub2APICredential(ctx, channel, origin, channel.GetSetting().Proxy, credential)
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

func sub2APIAccountConnection(account *model.Sub2APIAccount) (string, error) {
	if account.SharedIdentity != nil {
		var proxy string
		if account.ProxyCredential == "" {
			return "", sub2APIError("SUB2API_LOGIN_REQUIRED")
		}
		if err := sub2APIOpen(account.ProxyCredential, sub2APIAccountScope(account)+":proxy", &proxy); err != nil {
			return "", err
		}
		return proxy, nil
	}
	channel, origin, err := sub2APIChannel(account.ChannelID)
	if err != nil {
		return "", err
	}
	if !sameSub2APIOrigin(account.Origin, origin) || account.ProxyIdentity != sub2APIProxyIdentity(channel.GetSetting().Proxy) {
		return "", sub2APIError("SUB2API_CHANNEL_CHANGED")
	}
	return channel.GetSetting().Proxy, nil
}

func sub2APILoadAccount(accountID int64) (*model.Sub2APIAccount, string, Sub2APICredential, error) {
	var credential Sub2APICredential
	account, err := model.GetSub2APIAccount(accountID)
	if err != nil {
		return nil, "", credential, sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	if account.NeedsLogin || account.Credential == "" {
		return nil, "", credential, sub2APIError("SUB2API_LOGIN_REQUIRED")
	}
	proxy, err := sub2APIAccountConnection(account)
	if err != nil {
		return nil, "", credential, err
	}
	err = sub2APIOpen(account.Credential, sub2APIAccountScope(account), &credential)
	return account, proxy, credential, err
}

func RefreshSub2APIAccount(ctx context.Context, accountID int64, expectedRevision int64) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	account, err := model.GetSub2APIAccount(accountID)
	if err != nil {
		return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	if expectedRevision == 0 {
		expectedRevision = account.Revision
	}
	return model.WithSub2APIAccountLock(ctx, accountID, func() error {
		account, proxy, credential, err := sub2APILoadAccount(accountID)
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
		err = sub2APIRequest(ctx, account.Origin, proxy, "", http.MethodPost, "/api/v1/auth/refresh", nil, map[string]string{"refresh_token": credential.RefreshToken}, &next, false)
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
		if err := sub2APIRequest(ctx, account.Origin, proxy, next.AccessToken, http.MethodGet, "/api/v1/auth/me", nil, nil, &refreshedUser, false); err != nil || refreshedUser.ID != account.UpstreamUserID {
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
	account, proxy, credential, err := sub2APILoadAccount(accountID)
	if err != nil {
		return err
	}
	if account.ExpiresAt > 0 && account.ExpiresAt <= time.Now().Unix()+30 {
		if err := RefreshSub2APIAccount(ctx, accountID, account.Revision); err != nil {
			return err
		}
		account, proxy, credential, err = sub2APILoadAccount(accountID)
		if err != nil {
			return err
		}
	}
	err = sub2APIRequest(ctx, account.Origin, proxy, credential.AccessToken, method, path, query, payload, result, false)
	var upstream *Sub2APIError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusUnauthorized {
		return err
	}
	if err := RefreshSub2APIAccount(ctx, accountID, account.Revision); err != nil {
		return err
	}
	account, proxy, credential, err = sub2APILoadAccount(accountID)
	if err != nil {
		return err
	}
	return sub2APIRequest(ctx, account.Origin, proxy, credential.AccessToken, method, path, query, payload, result, false)
}

func DisconnectSub2API(ctx context.Context, channelID int, accountID int64, authorization *model.AuthFlowAuthorization) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return model.WithSub2APIAccountLock(ctx, accountID, func() error {
		account, proxy, credential, err := sub2APILoadAccount(accountID)
		if err == nil && (account.ChannelID != channelID || account.SharedIdentity != nil) {
			return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
		}
		if err == nil && credential.RefreshToken != "" {
			_ = sub2APIRequest(ctx, account.Origin, proxy, "", http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{"refresh_token": credential.RefreshToken}, nil, false)
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
		stored, proxy, credential, err := sub2APILoadAccount(account.ID)
		if err != nil {
			lastErr = err
			continue
		}
		if !sameSub2APIOrigin(stored.Origin, origin) {
			return sub2APIError("SUB2API_CHANNEL_CHANGED")
		}
		verified, bindings, err := verifySub2APICredential(ctx, channel, origin, proxy, credential)
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
		if err := model.ReplaceSub2APIAccountBindings(stored, channel, bindings); err != nil {
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

// Independent connections are fixed independently of channel routing settings.
func sub2APIIndependentConnection(accountID int64, origin, proxy string) (string, string, error) {
	if !model.Sub2APIStorageReady() {
		return "", "", sub2APIError("SUB2API_STORAGE_UNAVAILABLE")
	}
	if accountID > 0 {
		account, err := model.GetSub2APIAccount(accountID)
		if err != nil {
			return "", "", sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
		}
		storedProxy, err := sub2APIAccountConnection(account)
		return account.Origin, storedProxy, err
	}
	canonical, err := NormalizeSub2APIOrigin(origin)
	if err != nil {
		return "", "", err
	}
	if proxy != "" {
		parsed, parseErr := url.Parse(proxy)
		if parseErr != nil || len(proxy) > 2048 || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" && parsed.Scheme != "socks5h" {
			return "", "", sub2APIError("SUB2API_CREDENTIAL_INVALID")
		}
	}
	return canonical, proxy, nil
}

func connectIndependentSub2API(ctx context.Context, accountID int64, origin, proxy string, credential Sub2APICredential, authorization *model.AuthFlowAuthorization) (Sub2APILoginResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if authorization == nil || !Sub2APIEncryptionReady() {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_ENCRYPTION_UNAVAILABLE")
	}
	user, _, err := verifySub2APICredential(ctx, nil, origin, proxy, credential)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	canonicalOrigin, err := NormalizeSub2APIOrigin(origin)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("sub2api:shared:%s:%d", canonicalOrigin, user.ID)))
	shared := hex.EncodeToString(digest[:])
	var result Sub2APILoginResult
	err = model.WithSub2APIIdentityLock(ctx, shared, func() error {
		existing, lookupErr := model.FindIndependentSub2APIAccount(shared)
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		if lookupErr == nil {
			if accountID > 0 && existing.ID != accountID {
				return sub2APIError("SUB2API_ACCOUNT_CONFLICT")
			}
			accountID = existing.ID
		}
		return model.WithSub2APIAccountLock(ctx, accountID, func() error {
			account := &model.Sub2APIAccount{Origin: origin, UpstreamUserID: user.ID}
			previousRevision := int64(0)
			if accountID > 0 {
				var loadErr error
				account, loadErr = model.GetSub2APIAccount(accountID)
				if loadErr != nil {
					return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
				}
				if !sameSub2APIOrigin(account.Origin, origin) || account.UpstreamUserID != user.ID || account.ProxyIdentity != sub2APIProxyIdentity(proxy) {
					return sub2APIError("SUB2API_CREDENTIAL_INVALID")
				}
				previousRevision = account.Revision
			}
			account.SharedIdentity, account.Email = &shared, user.Email
			account.ProxyIdentity = sub2APIProxyIdentity(proxy)
			account.Revision, account.NeedsLogin, account.LastError = previousRevision+1, false, ""
			account.HasRefreshToken, account.ExpiresAt = credential.RefreshToken != "", 0
			if credential.ExpiresIn > 0 {
				account.ExpiresAt = time.Now().Unix() + credential.ExpiresIn
			}
			if account.Identity == "" {
				hash := sha256.Sum256([]byte(sub2APIAccountScope(account)))
				account.Identity = hex.EncodeToString(hash[:])
			}
			account.Credential, err = sub2APISeal(credential, sub2APIAccountScope(account))
			if err != nil {
				return err
			}
			account.ProxyCredential, err = sub2APISeal(proxy, sub2APIAccountScope(account)+":proxy")
			if err != nil {
				return err
			}
			if err := model.StoreIndependentSub2APIAccount(ctx, account, previousRevision, authorization); err != nil {
				return err
			}
			result.AccountID = account.ID
			return nil
		})
	})
	return result, err
}

type sub2APIIndependentChallenge struct {
	Operation     Sub2APIAccountManageContext `json:"operation"`
	Origin        string                      `json:"origin"`
	Proxy         string                      `json:"proxy"`
	TempToken     string                      `json:"temp_token"`
	Authorization model.AuthFlowAuthorization `json:"authorization"`
}

func LoginIndependentSub2API(ctx context.Context, operation Sub2APIAccountManageContext, proxy string, input Sub2APILoginInput, authorization *model.AuthFlowAuthorization) (Sub2APILoginResult, error) {
	if !Sub2APIEncryptionReady() || authorization == nil {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_ENCRYPTION_UNAVAILABLE")
	}
	origin, proxy, err := sub2APIIndependentConnection(operation.AccountID, operation.Origin, proxy)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	if input.Email == "" || len(input.Email) > 320 || input.Password == "" || len(input.Password) > 4096 {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	var response sub2APILoginResponse
	if err := sub2APIRequest(ctx, origin, proxy, "", http.MethodPost, "/api/v1/auth/login", nil, input, &response, false); err != nil {
		return Sub2APILoginResult{}, err
	}
	if !response.Requires2FA {
		return connectIndependentSub2API(ctx, operation.AccountID, origin, proxy, response.Sub2APICredential, authorization)
	}
	if response.TempToken == "" || len(response.TempToken) > 4096 {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	challenge := sub2APIIndependentChallenge{operation, origin, proxy, response.TempToken, *authorization}
	sealed, err := sub2APISeal(challenge, "sub2api:independent-challenge")
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	flow, _, err := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: "sub2api_account_login", Provider: "sub2api", Intent: "bind", UserId: authorization.UserID, SessionId: authorization.SessionID, Payload: sealed, ExpiresAt: time.Now().Add(5 * time.Minute)})
	return Sub2APILoginResult{Requires2FA: true, Flow: flow, EmailMasked: response.UserEmailMasked}, err
}

func CompleteIndependentSub2APILogin(ctx context.Context, flow, code string, identity AuthIdentity) (Sub2APILoginResult, error) {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_2FA_INVALID")
	}
	stored, err := model.ConsumeAuthFlow(flow, model.AuthFlowMatch{Purpose: "sub2api_account_login", Provider: "sub2api", Intent: "bind", UserId: identity.UserID, SessionId: identity.SessionID})
	if err != nil {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_FLOW_EXPIRED")
	}
	var challenge sub2APIIndependentChallenge
	if err := sub2APIOpen(stored.Payload, "sub2api:independent-challenge", &challenge); err != nil {
		return Sub2APILoginResult{}, err
	}
	operation, _ := common.Marshal(challenge.Operation)
	if err := ValidateFlowAuthorization(identity, VerificationOperation{Scope: VerificationScopeSub2APIAccountManage, Context: operation}, &challenge.Authorization); err != nil {
		return Sub2APILoginResult{}, sub2APIError("SUB2API_FLOW_EXPIRED")
	}
	if challenge.Operation.AccountID > 0 {
		origin, proxy, err := sub2APIIndependentConnection(challenge.Operation.AccountID, "", "")
		if err != nil || origin != challenge.Origin || proxy != challenge.Proxy {
			return Sub2APILoginResult{}, sub2APIError("SUB2API_CHANNEL_CHANGED")
		}
	}
	var response sub2APILoginResponse
	if err := sub2APIRequest(ctx, challenge.Origin, challenge.Proxy, "", http.MethodPost, "/api/v1/auth/login/2fa", nil, map[string]string{"temp_token": challenge.TempToken, "totp_code": code}, &response, false); err != nil {
		return Sub2APILoginResult{}, err
	}
	return connectIndependentSub2API(ctx, challenge.Operation.AccountID, challenge.Origin, challenge.Proxy, response.Sub2APICredential, &challenge.Authorization)
}

func ImportIndependentSub2API(ctx context.Context, operation Sub2APIAccountManageContext, proxy string, credential Sub2APICredential, authorization *model.AuthFlowAuthorization) (Sub2APILoginResult, error) {
	origin, proxy, err := sub2APIIndependentConnection(operation.AccountID, operation.Origin, proxy)
	if err != nil {
		return Sub2APILoginResult{}, err
	}
	return connectIndependentSub2API(ctx, operation.AccountID, origin, proxy, credential, authorization)
}

func AttachIndependentSub2API(ctx context.Context, channelID int, accountID int64, authorization *model.AuthFlowAuthorization) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return model.WithSub2APIAccountLock(ctx, accountID, func() error {
		channel, origin, err := sub2APIChannel(channelID)
		if err != nil {
			return err
		}
		account, proxy, credential, err := sub2APILoadAccount(accountID)
		if err != nil {
			return err
		}
		if account.SharedIdentity == nil || !sameSub2APIOrigin(origin, account.Origin) {
			return sub2APIError("SUB2API_CHANNEL_CHANGED")
		}
		user, bindings, err := verifySub2APICredential(ctx, channel, origin, proxy, credential)
		if err != nil {
			return err
		}
		if user.ID != account.UpstreamUserID {
			return sub2APIError("SUB2API_CREDENTIAL_INVALID")
		}
		if len(bindings) == 0 {
			return sub2APIError("SUB2API_NO_MATCHING_KEYS")
		}
		return model.AttachSub2APIAccount(channel, account, bindings, authorization)
	})
}

func DisconnectIndependentSub2API(ctx context.Context, accountID int64, authorization *model.AuthFlowAuthorization) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return model.WithSub2APIAccountLock(ctx, accountID, func() error {
		account, proxy, credential, loadErr := sub2APILoadAccount(accountID)
		if err := model.DisconnectIndependentSub2APIAccount(accountID, authorization); err != nil {
			if errors.Is(err, model.ErrSub2APIAccountLinked) {
				return sub2APIError("SUB2API_ACCOUNT_LINKED")
			}
			return err
		}
		if loadErr == nil && credential.RefreshToken != "" {
			_ = sub2APIRequest(ctx, account.Origin, proxy, "", http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{"refresh_token": credential.RefreshToken}, nil, false)
		}
		return nil
	})
}

func Sub2APIAccountHasProxy(account *model.Sub2APIAccount) bool {
	return account.ProxyIdentity != sub2APIProxyIdentity("")
}
