package controller

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/keyusage"
	relayopenai "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const sub2APITestKey = "sk-sub2api-fixture-one"
const sub2APITestOtherKey = "sk-sub2api-fixture-two"

func TestSub2APISafeRequestFailures(t *testing.T) {
	const secret = "fixture-private-credential"
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"timeout", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "SUB2API_TIMEOUT"},
		{"DNS", &net.DNSError{Err: secret, Name: secret + ".invalid"}, "SUB2API_DNS_FAILED"},
		{"DNS timeout", &net.DNSError{Err: secret, Name: secret + ".invalid", IsTimeout: true}, "SUB2API_TIMEOUT"},
		{"certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "SUB2API_TLS_FAILED"},
		{"TLS record", tls.RecordHeaderError{Msg: secret}, "SUB2API_TLS_FAILED"},
		{"network", fmt.Errorf("%s: %w", secret, io.EOF), "SUB2API_NETWORK_FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := service.NewSub2APITransportError(&url.Error{Op: "Get", URL: "https://" + secret + ".invalid/private", Err: tc.err})
			assert.Equal(t, tc.code, failure.Code)
			assert.Equal(t, tc.code, failure.Error())
			assert.Zero(t, failure.Status)
			failure.Stage = "auth_me"
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			fields := sub2APIManagementError(c, failure)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Equal(t, "failure", fields["outcome"])
			assert.Equal(t, "auth_me", fields["error_stage"])
			assert.NotContains(t, fields, "upstream_status")
			encoded, err := common.Marshal(fields)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded)+response.Body.String(), secret)
			assert.NotContains(t, response.Body.String(), "https://")
		})
	}
}

func TestSub2APIEncryptionKeyWithoutPasswordLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin, _ := setupAccessTokenAudit(t)
	previousEnabled := common.PasswordLoginEncryptionEnabled
	common.PasswordLoginEncryptionEnabled = false
	t.Cleanup(func() { common.PasswordLoginEncryptionEnabled = previousEnabled })
	require.NoError(t, model.DB.AutoMigrate(&model.LoginEncryptionKey{}))
	require.NoError(t, model.InitPasswordEncryption())
	user := &model.User{Username: "encryption-user", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default", AffCode: "encryption-user"}
	require.NoError(t, model.DB.Create(user).Error)
	_, adminToken := createAccessTokenTestSession(t, admin.Id, "encryption-admin")
	_, userToken := createAccessTokenTestSession(t, user.Id, "encryption-user")
	readToken, _ := createScopedAccessToken(t, admin.Id, time.Now().Unix()+3600, "channel:read")
	unrelatedToken, _ := createScopedAccessToken(t, admin.Id, time.Now().Unix()+3600, "profile:read")

	const route = "/api/sub2api/accounts/encryption-key"
	middleware.DeclareAccessTokenPermissionRoute(http.MethodGet, route, authz.ChannelRead)
	router := gin.New()
	router.GET(route, middleware.AdminAuth(), middleware.DisableCache(), middleware.RequirePermission(authz.ChannelRead), GetSub2APIEncryptionKey)
	router.GET("/api/user/login/encryption-key", GetPasswordEncryptionKey)
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"ordinary user", userToken, http.StatusForbidden},
		{"administrator without token scope", unrelatedToken, http.StatusForbidden},
		{"administrator session", adminToken, http.StatusOK},
		{"administrator read token", readToken, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := accessTokenRequest(router, http.MethodGet, route, tc.token, "", "")
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status != http.StatusOK {
				assert.NotContains(t, response.Body.String(), "BEGIN PUBLIC KEY")
				return
			}
			var result struct {
				Success bool `json:"success"`
				Data    struct {
					KeyID     string `json:"kid"`
					PublicKey string `json:"public_key"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			require.True(t, result.Success)
			require.NotEmpty(t, result.Data.KeyID)
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			assert.NotContains(t, response.Body.String(), "PRIVATE KEY")
			block, _ := pem.Decode([]byte(result.Data.PublicKey))
			require.NotNil(t, block)
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			require.NoError(t, err)
			public, ok := parsed.(*rsa.PublicKey)
			require.True(t, ok)
			ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, public, []byte("fixture-channel-credential"), nil)
			require.NoError(t, err)
			plain, err := common.DecryptChannelCredential(base64.StdEncoding.EncodeToString(ciphertext), result.Data.KeyID)
			require.NoError(t, err)
			assert.Equal(t, "fixture-channel-credential", plain)
			longCredential := strings.Repeat("fixture-token-", 100)
			envelope, keyID := sub2APITestEncrypt(t, longCredential)
			require.Equal(t, result.Data.KeyID, keyID)
			plain, err = common.DecryptChannelCredential(envelope, result.Data.KeyID)
			require.NoError(t, err)
			assert.Equal(t, longCredential, plain)
		})
	}
	response := accessTokenRequest(router, http.MethodGet, "/api/user/login/encryption-key", "", "", "")
	assert.JSONEq(t, `{"success":true,"message":"","data":{"enabled":false}}`, response.Body.String())
}

func TestSub2APICanonicalOrigins(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://SUPPLIER.example:443/v1/", "https://supplier.example"},
		{"https://supplier.example/tenant/v1", "https://supplier.example/tenant"},
		{"https://supplier.example:8443/v1", "https://supplier.example:8443"},
		{"https://supplier.example/another/v1", "https://supplier.example/another"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := service.NormalizeSub2APIOrigin(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for _, invalid := range []string{"http://supplier.example", "https://user:password@supplier.example", "https://supplier.example?token=secret", "https://supplier.example#fragment"} {
		_, err := service.NormalizeSub2APIOrigin(invalid)
		require.Error(t, err)
	}
}

func TestSub2APIManagementHTTPCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, allowHTTP, scheme, forwarded, trusted string
		allowed                                     bool
	}{
		{name: "HTTP disabled by default", scheme: "http"},
		{name: "HTTP explicitly disabled", scheme: "http", allowHTTP: "false"},
		{name: "invalid setting stays disabled", scheme: "http", allowHTTP: "invalid"},
		{name: "HTTP compatibility enabled", scheme: "http", allowHTTP: "true", allowed: true},
		{name: "direct TLS", scheme: "https", allowed: true},
		{name: "forged forwarding header", scheme: "http", forwarded: "https"},
		{name: "trusted TLS terminator", scheme: "http", forwarded: "https", trusted: "192.0.2.12/32", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUB2API_ALLOW_HTTP_MANAGEMENT", tc.allowHTTP)
			t.Setenv("SUB2API_TRUSTED_HTTPS_PROXIES", tc.trusted)
			request := httptest.NewRequest(http.MethodPost, tc.scheme+"://gateway.example/channel/1/sub2api/login", strings.NewReader(`{}`))
			request.RemoteAddr = "192.0.2.12:1234"
			request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			request.Header.Set("SUB2API_ALLOW_HTTP_MANAGEMENT", "true")
			assert.Equal(t, tc.allowed, sub2APIManagementAllowed(request))
			router := gin.New()
			router.POST("/channel/:id/sub2api/login", ManageSub2APIChannel)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.False(t, result.Success, "transport compatibility must not grant authentication")
			if tc.allowed {
				assert.Equal(t, "AUTH_UNAUTHORIZED", result.Code)
			} else {
				assert.Equal(t, "SUB2API_SECURE_ENTRY_REQUIRED", result.Code)
			}
		})
	}
}

// The same public contracts run against all supported main databases. The
// upgrade fixture creates existing users/channels before adding the new tables.
func TestSub2APIAccountDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ name, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.name, func(t *testing.T) {
			t.Setenv("SUB2API_ALLOW_HTTP_MANAGEMENT", "")
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.name, os.Getenv(dialect.env))
			t.Setenv("SUB2API_CREDENTIAL_SECRET", strings.Repeat("fixture-secret-", 4))
			require.NoError(t, i18n.Init())
			gin.SetMode(gin.TestMode)
			t.Run("credential encryption works without password login encryption", func(t *testing.T) {
				previousEnabled := common.PasswordLoginEncryptionEnabled
				common.PasswordLoginEncryptionEnabled = false
				t.Cleanup(func() { common.PasswordLoginEncryptionEnabled = previousEnabled })
				require.NoError(t, db.AutoMigrate(&model.LoginEncryptionKey{}))
				var count int64
				require.NoError(t, db.Model(&model.LoginEncryptionKey{}).Count(&count).Error)
				require.Zero(t, count)
				require.NoError(t, model.InitPasswordEncryption())
				keyID, publicKey := common.PasswordEncryptionPublicKey()
				require.NotEmpty(t, keyID)
				require.NotEmpty(t, publicKey)
				var stored model.LoginEncryptionKey
				require.NoError(t, db.First(&stored).Error)
				errors := make(chan error, 2)
				var replicas sync.WaitGroup
				for range 2 {
					replicas.Go(func() { errors <- model.InitPasswordEncryption() })
				}
				replicas.Wait()
				close(errors)
				for err := range errors {
					require.NoError(t, err)
				}
				currentID, currentPublic := common.PasswordEncryptionPublicKey()
				assert.Equal(t, keyID, currentID)
				assert.Equal(t, publicKey, currentPublic)
				require.NoError(t, db.Model(&model.LoginEncryptionKey{}).Count(&count).Error)
				assert.EqualValues(t, 1, count)
				var reloaded model.LoginEncryptionKey
				require.NoError(t, db.First(&reloaded).Error)
				assert.True(t, stored.PrivateKeyPEM == reloaded.PrivateKeyPEM, "replicas must retain the persisted private key")
			})
			previousLogin, previousSession := common.PasswordLoginEnabled, common.SessionSecret
			common.PasswordLoginEnabled, common.SessionSecret = true, "sub2api-session-fixture"
			t.Cleanup(func() { common.PasswordLoginEnabled, common.SessionSecret = previousLogin, previousSession })
			require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.UserOAuthBinding{}, &model.UserAccessToken{}))
			password, err := common.Password2Hash("local-password")
			require.NoError(t, err)
			user := model.User{Username: "sub2api-admin", Password: password, Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, Quota: 123456}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, model.PublishUserAuthCache(user.Id))
			bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "sub2api-test")
			require.NoError(t, err)
			identity, err := service.ParseAccessToken(bundle.AccessToken)
			require.NoError(t, err)

			var refreshes, completions, redirectHits, identityStatus, identityRequests atomic.Int32
			var redirectMode, incompleteRate, ledgerFailure, refreshFailure, blockKeys, blockRefresh atomic.Bool
			var poolMode, poolReads, rateReads, poolAccountID atomic.Int64
			keysEntered, keysRelease := make(chan struct{}, 1), make(chan struct{})
			refreshEntered, refreshRelease := make(chan struct{}, 1), make(chan struct{})
			var refreshReleaseOnce sync.Once
			var keysReleaseOnce sync.Once
			t.Cleanup(func() { keysReleaseOnce.Do(func() { close(keysRelease) }) })
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				assert.Equal(t, "new-api/Sub2API-account", r.UserAgent())
				respond := func(data any) {
					encoded, marshalErr := common.Marshal(map[string]any{"code": 0, "data": data})
					assert.NoError(t, marshalErr)
					_, _ = w.Write(encoded)
				}
				switch r.URL.Path {
				case "/api/v1/auth/login":
					var input service.Sub2APILoginInput
					assert.NoError(t, common.DecodeJson(r.Body, &input))
					if input.Password == "captcha" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"reason":"TURNSTILE_REQUIRED"}`))
						return
					}
					respond(map[string]any{"requires_2fa": true, "temp_token": "private-upstream-challenge", "user_email_masked": "a***@example.com"})
				case "/api/v1/auth/login/2fa":
					completions.Add(1)
					var input map[string]string
					assert.NoError(t, common.DecodeJson(r.Body, &input))
					assert.Equal(t, "private-upstream-challenge", input["temp_token"])
					assert.Equal(t, "123456", input["totp_code"])
					respond(service.Sub2APICredential{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresIn: 3600})
				case "/api/v1/auth/me":
					identityRequests.Add(1)
					if status := identityStatus.Load(); status != 0 {
						w.WriteHeader(int(status))
						_, _ = w.Write([]byte(`{"error":"fixture-private-upstream-body"}`))
						return
					}
					if redirectMode.Load() {
						http.Redirect(w, r, server.URL+"/redirect-target", http.StatusFound)
						return
					}
					if r.Header.Get("Authorization") == "Bearer parallel-access" {
						respond(map[string]any{"id": 88, "email": "parallel@example.com"})
						return
					}
					if r.Header.Get("Authorization") == "Bearer foreign-access" {
						respond(map[string]any{"id": 99, "email": "foreign@example.com"})
						return
					}
					respond(map[string]any{"id": 7, "email": "owner@example.com"})
				case "/api/v1/keys":
					poolReads.Add(1)
					if blockKeys.CompareAndSwap(true, false) {
						keysEntered <- struct{}{}
						<-keysRelease
					}
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					mode := poolMode.Load()
					if mode == 5 || mode == 6 {
						if mode == 5 {
							w.Header().Set("Retry-After", "120")
						} else {
							w.Header().Set("Retry-After", time.Now().Add(3*time.Minute).UTC().Format(http.TimeFormat))
						}
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if mode == 1 && page == 2 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if mode == 7 {
						items := []any{}
						for i := range 100 {
							id := int64(1000 + (page-1)*100 + i)
							raw := fmt.Sprintf("fixture-key-%d", id)
							if page == 1 && i == 0 {
								id = 71
								raw = sub2APITestKey
							}
							items = append(items, map[string]any{"id": id, "user_id": 7, "group_id": 3, "status": "active", "name": strings.Repeat("inventory", 100), "key": raw})
						}
						respond(map[string]any{"page": page, "page_size": 100, "total": 200, "pages": 2, "items": items})
						return
					}
					if r.Header.Get("Authorization") == "Bearer foreign-access" {
						respond(map[string]any{"page": 1, "page_size": 1, "total": 1, "pages": 1, "items": []any{map[string]any{"id": 99, "user_id": 99, "key": "sk-unrelated"}}})
						return
					}
					id, userID, total, pages := int64(9), int64(7), int64(2), 2
					raw, groupID := "sk-unrelated", 0
					if page == 2 {
						id, raw, groupID = 71, sub2APITestKey, 3
					}
					if page == 2 && mode == 2 {
						id = 9
					}
					if page == 2 && mode == 3 {
						userID = 99
					}
					if page == 2 && mode == 4 {
						total, pages = 3, 3
					}
					if page == 2 && mode == 10 {
						assert.NoError(t, db.Model(&model.Sub2APIAccount{}).Where("id = ?", poolAccountID.Load()).Update("revision", gorm.Expr("revision + 1")).Error)
					}
					if mode == 8 {
						total, pages = math.MaxInt64, 1
					}
					size := 1
					if mode == 9 {
						if page == 1 {
							respond(map[string]any{"page": 1, "page_size": 1, "total": 4, "pages": 4, "items": []any{map[string]any{"id": 9, "user_id": 7, "key": "sk-unrelated"}}})
						} else {
							respond(map[string]any{"page": 2, "page_size": 2, "total": 4, "pages": 2, "items": []any{
								map[string]any{"id": 71, "user_id": 7, "key": sub2APITestKey, "group_id": 3},
								map[string]any{"id": 72, "user_id": 7, "key": sub2APITestOtherKey},
							}})
						}
						return
					}
					respond(map[string]any{"page": page, "page_size": size, "total": total, "pages": pages, "items": []any{map[string]any{"id": id, "user_id": userID, "group_id": groupID, "status": "active", "key": raw}}})
				case "/api/v1/groups/available":
					if poolMode.Load() == 11 {
						respond([]any{map[string]any{"id": 3, "peak_rate_enabled": false}})
						return
					}
					if poolMode.Load() == 12 {
						respond([]any{map[string]any{"id": 3, "rate_multiplier": 1}})
						return
					}
					if poolMode.Load() == 13 {
						respond([]any{map[string]any{"id": 3, "rate_multiplier": 0, "peak_rate_enabled": false}})
						return
					}
					respond([]any{map[string]any{"id": 3, "name": "Fixture group", "platform": "openai", "rate_multiplier": 1, "peak_rate_enabled": incompleteRate.Load(), "peak_start": "08:00", "peak_end": "20:00", "peak_rate_multiplier": 1.5}})
				case "/api/v1/groups/rates":
					if poolMode.Load() == 13 {
						respond(map[string]float64{"3": 0})
						return
					}
					respond(map[string]float64{"3": 0.5})
				case "/api/v1/auth/refresh":
					refreshes.Add(1)
					if blockRefresh.CompareAndSwap(true, false) {
						refreshEntered <- struct{}{}
						<-refreshRelease
					}
					if refreshFailure.Load() {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					var input map[string]string
					assert.NoError(t, common.DecodeJson(r.Body, &input))
					assert.Equal(t, "fixture-refresh", input["refresh_token"])
					respond(service.Sub2APICredential{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", ExpiresIn: 3600})
				case "/api/v1/auth/logout":
					respond(map[string]any{})
				case "/api/v1/usage":
					assert.Equal(t, "71", r.URL.Query().Get("api_key_id"))
					if ledgerFailure.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					rows := []any{
						map[string]any{"id": 110, "api_key_id": 72, "request_id": "client:wrong-key", "actual_cost": 1, "billing_type": 0},
						map[string]any{"id": 111, "api_key_id": 71, "request_id": "client:missing-amount", "billing_type": 0},
						map[string]any{"id": 113, "api_key_id": 71, "request_id": "resp_http", "actual_cost": 0.000001, "billing_type": 0},
						map[string]any{"id": 114, "api_key_id": 71, "request_id": "resp_ws_second", "actual_cost": 0.000002, "billing_type": 0},
						map[string]any{"id": 112, "api_key_id": 71, "request_id": "client:exact", "actual_cost": 0, "billing_type": 1, "rate_multiplier": 0.5},
						map[string]any{"id": 115, "api_key_id": 71, "request_id": "client:detached", "actual_cost": 0.000003, "billing_type": 0},
						map[string]any{"id": 116, "api_key_id": 71, "request_id": "client:disconnected", "actual_cost": 0.000004, "billing_type": 0},
					}
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					if page == 1 {
						rows = rows[:4]
					} else {
						rows = rows[4:]
					}
					respond(map[string]any{"page": page, "page_size": 4, "total": 7, "pages": 2, "items": rows})
				case "/v1/sub2api/billing":
					rateReads.Add(1)
					if incompleteRate.Load() {
						_, _ = w.Write([]byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token"}`))
						return
					}
					_, _ = w.Write([]byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","resolved_rate_multiplier":0.5,"effective_rate_multiplier":0.5,"peak_rate_enabled":false,"observed_at":"2026-11-26T00:00:00Z"}`))
				case "/redirect-target":
					redirectHits.Add(1)
					respond(map[string]any{})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			previousTransport, previousInsecure := http.DefaultTransport, common.TLSInsecureSkipVerify
			transport := http.DefaultTransport.(*http.Transport).Clone()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			http.DefaultTransport = transport
			common.TLSInsecureSkipVerify = true // Management secrets must still verify TLS.
			t.Cleanup(func() {
				http.DefaultTransport, common.TLSInsecureSkipVerify = previousTransport, previousInsecure
				transport.CloseIdleConnections()
			})

			channel := model.Channel{Type: constant.ChannelTypeSub2API, Name: "Existing supplier", BaseURL: common.GetPointer(server.URL), Key: sub2APITestKey + "\n" + sub2APITestOtherKey, Group: "default", Models: "fixture-model", Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			keyIdentity := channel.KeyIdentity(sub2APITestKey)
			channel.ChannelInfo.KeyCostRatios = map[string]float64{keyIdentity: 0.8}
			require.NoError(t, db.Model(&channel).Update("channel_info", channel.ChannelInfo).Error)
			assert.False(t, model.Sub2APIStorageReady())
			// Fresh tables and repeated startup, preserving pre-feature data.
			require.NoError(t, model.MigrateSub2APIStorage(db))
			require.NoError(t, model.MigrateSub2APIStorage(db))
			require.True(t, model.Sub2APIStorageReady())
			storedChannel, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, channel.Key, storedChannel.Key)

			authorize := func(action string, accountID int64, key string) *model.AuthFlowAuthorization {
				operation := sub2APITestOperation(t, channel.Id, action, accountID, key)
				proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
				authorization, authErr := service.ConsumeOperationProof(proof, identity, operation)
				require.NoError(t, authErr)
				return authorization
			}
			ctx := context.Background()
			loginInput := service.Sub2APILoginInput{Email: "owner@example.com", Password: "upstream-password"}
			flow, err := service.LoginSub2API(ctx, channel.Id, loginInput, authorize("login", 0, ""))
			require.NoError(t, err)
			require.True(t, flow.Requires2FA)
			require.NotEmpty(t, flow.Flow)
			encoded, err := common.Marshal(flow)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "private-upstream-challenge")
			wrongSession := identity
			wrongSession.SessionID = "wrong-session"
			_, err = service.CompleteSub2APILogin(ctx, channel.Id, flow.Flow, "123456", wrongSession)
			require.Error(t, err)
			assert.Zero(t, completions.Load())
			connected, err := service.CompleteSub2APILogin(ctx, channel.Id, flow.Flow, "123456", identity)
			require.NoError(t, err)
			require.Positive(t, connected.AccountID)
			_, err = service.CompleteSub2APILogin(ctx, channel.Id, flow.Flow, "123456", identity)
			require.Error(t, err)
			assert.EqualValues(t, 1, completions.Load())
			staleFlow, err := service.LoginSub2API(ctx, channel.Id, loginInput, authorize("login", 0, ""))
			require.NoError(t, err)
			staleIdentity := identity
			staleIdentity.SessionVersion++
			_, err = service.CompleteSub2APILogin(ctx, channel.Id, staleFlow.Flow, "123456", staleIdentity)
			assert.Equal(t, "SUB2API_FLOW_EXPIRED", service.Sub2APIErrorCode(err))
			assert.EqualValues(t, 1, completions.Load(), "revoked/version-changed sessions cannot continue the ceremony")

			// The browser envelope and the action proof are both checked by the
			// management API; reconnect updates the same account rather than duplicating it.
			credentialJSON, err := common.Marshal(service.Sub2APICredential{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresIn: 3600})
			require.NoError(t, err)
			credentialEnvelope, credentialKey := sub2APITestEncrypt(t, string(credentialJSON))
			importProof := issueSecurityEnrollmentProof(t, identity, sub2APITestOperation(t, channel.Id, "import", 0, ""), service.VerificationMethodPassword)
			importResponse := sub2APITestRequest(t, identity, channel.Id, "import", map[string]string{"credential_encrypted": credentialEnvelope, "encryption_key_id": credentialKey}, importProof)
			assert.Contains(t, importResponse.Body.String(), `"success":true`, importResponse.Body.String())
			assert.NotContains(t, importResponse.Body.String(), "fixture-access")
			accounts, err := model.ListSub2APIAccounts(channel.Id)
			require.NoError(t, err)
			require.Len(t, accounts, 1)
			assert.Equal(t, connected.AccountID, accounts[0].ID)
			account, err := model.GetSub2APIAccount(connected.AccountID)
			require.NoError(t, err)
			assert.NotContains(t, account.Credential, "fixture-access")
			assert.NotContains(t, account.Credential, "fixture-refresh")
			serialized, err := common.Marshal(account)
			require.NoError(t, err)
			assert.NotContains(t, string(serialized), "credential")
			assert.NotContains(t, string(serialized), account.Credential)
			binding, err := model.GetSub2APIKeyBinding(channel.Id, keyIdentity)
			require.NoError(t, err)
			assert.EqualValues(t, 71, binding.UpstreamAPIKeyID)
			assert.EqualValues(t, 3, binding.UpstreamGroupID)
			assert.False(t, binding.SyncBills)
			_, err = model.GetSub2APIKeyBinding(channel.Id, channel.KeyIdentity(sub2APITestOtherKey))
			require.Error(t, err, "an unowned key must not be bound to the account")

			// Login may challenge, but importing an unrelated account cannot claim keys.
			_, err = service.ImportSub2API(ctx, channel.Id, service.Sub2APICredential{AccessToken: "foreign-access"}, authorize("import", 0, ""))
			assert.Equal(t, "SUB2API_NO_MATCHING_KEYS", service.Sub2APIErrorCode(err))
			captchaInput := loginInput
			captchaInput.Password = "captcha"
			_, err = service.LoginSub2API(ctx, channel.Id, captchaInput, authorize("login", 0, ""))
			assert.Equal(t, "SUB2API_CAPTCHA_REQUIRED", service.Sub2APIErrorCode(err))
			expiring, err := service.LoginSub2API(ctx, channel.Id, loginInput, authorize("login", 0, ""))
			require.NoError(t, err)
			require.NoError(t, db.Model(&model.AuthFlow{}).Where("purpose = ? AND consumed_at IS NULL", "sub2api_login").Update("expires_at", time.Now().Add(-time.Minute)).Error)
			_, err = service.CompleteSub2APILogin(ctx, channel.Id, expiring.Flow, "123456", identity)
			assert.Equal(t, "SUB2API_FLOW_EXPIRED", service.Sub2APIErrorCode(err))
			assert.EqualValues(t, 1, completions.Load())

			// Two replicas observing the same expired revision perform one rotation.
			errors := make(chan error, 2)
			var workers sync.WaitGroup
			for range 2 {
				workers.Go(func() { errors <- service.RefreshSub2APIAccount(ctx, account.ID, account.Revision) })
			}
			workers.Wait()
			close(errors)
			for refreshErr := range errors {
				require.NoError(t, refreshErr)
			}
			assert.EqualValues(t, 1, refreshes.Load())
			refreshed, err := model.GetSub2APIAccount(account.ID)
			require.NoError(t, err)
			assert.Greater(t, refreshed.Revision, account.Revision)

			// Rate sync is opt-in and preserves operator overrides. Billing sync
			// additionally requires a proven physical-key/account association.
			require.NoError(t, service.SyncSub2APIChannel(ctx, channel.Id, account.ID))
			require.NotNil(t, channel.UpstreamCostRatioForKey(sub2APITestKey))
			assert.Equal(t, 0.8, *channel.UpstreamCostRatioForKey(sub2APITestKey))
			err = model.SetSub2APIKeySync(channel.Id, channel.KeyIdentity(sub2APITestOtherKey), false, true, authorize("keys", 0, channel.KeyIdentity(sub2APITestOtherKey)))
			require.Error(t, err)
			// Rate-only Keys remain usable without an imported account.
			otherIdentity := channel.KeyIdentity(sub2APITestOtherKey)
			require.NoError(t, model.SetSub2APIKeySync(channel.Id, otherIdentity, true, false, authorize("keys", 0, otherIdentity)))
			require.NoError(t, service.SyncSub2APIDirectKeyRates(ctx, &channel))
			require.NotNil(t, channel.UpstreamCostRatioForKey(sub2APITestOtherKey))
			assert.Equal(t, 0.5, *channel.UpstreamCostRatioForKey(sub2APITestOtherKey))
			require.NoError(t, model.SetSub2APIKeySync(channel.Id, otherIdentity, false, false, authorize("keys", 0, otherIdentity)))
			require.NoError(t, model.SetSub2APIKeySync(channel.Id, keyIdentity, true, true, authorize("keys", 0, keyIdentity)))
			assert.Equal(t, 0.5, *channel.UpstreamCostRatioForKey(sub2APITestKey))
			assert.Equal(t, 0.8, channel.ChannelInfo.KeyCostRatios[keyIdentity])
			// The ledger sync matches both the original upstream request ID and
			// verified key ID, follows pages, and never interprets missing data as free.
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			recorder := httptest.NewRecorder()
			billingContext, _ := gin.CreateTestContext(recorder)
			billingContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			relayInfo := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSub2API, ChannelId: channel.Id, ChannelBaseUrl: server.URL, ApiKey: sub2APITestKey}}
			service.BeginSub2APIBillingAttempt(billingContext, relayInfo)
			service.CaptureSub2APIBillingResponse(billingContext, http.Header{"X-Client-Request-Id": {"http-header"}})
			responseBody := `{"id":"resp_http","model":"fixture","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
			usage, relayErr := relayopenai.OaiResponsesHandler(billingContext, relayInfo, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody))})
			require.Nil(t, relayErr)
			assert.Equal(t, 3, usage.TotalTokens)
			assert.JSONEq(t, responseBody, recorder.Body.String(), "billing metadata must not rewrite the response")
			httpRef := sub2APIBillingRefFromLog(t, billingContext, relayInfo)
			assert.Equal(t, "client:http-header", httpRef.RequestID)
			assert.Equal(t, "resp_http", httpRef.ResponseID)
			service.BeginSub2APIBillingAttempt(billingContext, relayInfo)
			for _, invalidID := range []string{"", "resp\ninvalid", "resp\x7finvalid", " resp_space", strings.Repeat("x", 161), string([]byte{0xff})} {
				service.CaptureSub2APIResponseID(billingContext, invalidID, true)
			}
			assert.Empty(t, sub2APIBillingRefFromLog(t, billingContext, relayInfo).RequestID)
			service.CaptureSub2APIResponseID(billingContext, "resp_ws_first", true)
			assert.Equal(t, "resp_ws_first", sub2APIBillingRefFromLog(t, billingContext, relayInfo).RequestID)
			service.BeginSub2APIBillingAttempt(billingContext, relayInfo)
			assert.Empty(t, sub2APIBillingRefFromLog(t, billingContext, relayInfo).ResponseID, "reusing a connection must clear the previous generation")
			service.CaptureSub2APIResponseID(billingContext, "resp_ws_second", true)
			service.CaptureSub2APIResponseID(billingContext, "unrelated-response", true)
			wsRef := sub2APIBillingRefFromLog(t, billingContext, relayInfo)
			assert.Equal(t, "resp_ws_second", wsRef.RequestID)
			assert.Equal(t, "resp_ws_second", wsRef.ResponseID)
			for _, ref := range []model.Sub2APIBillingRef{httpRef, wsRef} {
				other, marshalErr := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": ref}})
				require.NoError(t, marshalErr)
				log := model.Log{UserId: user.Id, ChannelId: channel.Id, RequestId: "gateway-" + ref.RequestID, CreatedAt: time.Now().Unix(), Type: model.LogTypeConsume, Quota: 17, Other: string(other)}
				require.NoError(t, db.Create(&log).Error)
				require.NoError(t, model.QueueSub2APIBill(ctx, &log))
			}
			for _, requestID := range []string{"client:exact", "client:wrong-key", "client:missing-amount"} {
				other, marshalErr := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": model.Sub2APIBillingRef{AccountID: account.ID, APIKeyID: 71, KeyIdentity: keyIdentity, RequestID: requestID}}})
				require.NoError(t, marshalErr)
				log := model.Log{UserId: user.Id, ChannelId: channel.Id, RequestId: "gateway-" + requestID, CreatedAt: time.Now().Unix(), Type: model.LogTypeConsume, Quota: 17, Other: string(other)}
				require.NoError(t, db.Create(&log).Error)
				require.NoError(t, model.QueueSub2APIBill(ctx, &log))
			}
			require.NoError(t, db.Model(&model.Sub2APIBill{}).Where("channel_id = ?", channel.Id).Update("next_sync_at", 0).Error)
			ledgerFailure.Store(true)
			settled, syncErr := service.SyncSub2APIBills(ctx, channel.Id)
			require.Error(t, syncErr)
			assert.Zero(t, settled)
			ledgerFailure.Store(false)
			require.NoError(t, model.UpdateSub2APIInventory(ctx, account.ID, nil, func(state *model.Sub2APISyncState) error {
				state.LedgerCheckedAt = nil
				state.WindowAt = time.Now().Unix()
				state.Requests = 0
				state.UsageRequests = 39
				return nil
			}))
			require.NoError(t, db.Model(&model.Sub2APIBill{}).Where("channel_id = ?", channel.Id).Update("next_sync_at", 0).Error)
			settled, syncErr = service.SyncSub2APIBills(ctx, channel.Id)
			require.Error(t, syncErr)
			assert.Equal(t, "SUB2API_RATE_LIMITED", service.Sub2APIErrorCode(syncErr))
			assert.Equal(t, 2, settled, "already exact-matched rows may settle, while page2 remains pending")
			paused, pausedErr := model.GetSub2APIAccount(account.ID)
			require.NoError(t, pausedErr)
			pausedState, pausedErr := model.ReadSub2APISyncState(paused)
			require.NoError(t, pausedErr)
			assert.Contains(t, pausedState.LedgerPages, "71:"+time.Now().UTC().Format("2006-01-02"))
			require.NoError(t, model.UpdateSub2APIInventory(ctx, account.ID, nil, func(state *model.Sub2APISyncState) error { state.LedgerCheckedAt = nil; state.WindowAt = 0; return nil }))
			settled, syncErr = service.SyncSub2APIBills(ctx, channel.Id)
			require.NoError(t, syncErr)
			assert.Equal(t, 1, settled, "resume page2 without replaying settled page1 charges")
			var bills []model.Sub2APIBill
			require.NoError(t, db.Where("channel_id = ?", channel.Id).Find(&bills).Error)
			require.Len(t, bills, 5)
			for _, bill := range bills {
				if bill.RequestID == "client:exact" {
					require.NotNil(t, bill.CostQuota)
					assert.Zero(t, *bill.CostQuota)
					assert.EqualValues(t, 112, bill.UsageID)
				} else if bill.ResponseID == "resp_http" || bill.ResponseID == "resp_ws_second" {
					require.NotNil(t, bill.CostQuota)
					assert.Positive(t, *bill.CostQuota)
				} else {
					assert.Nil(t, bill.CostQuota)
				}
			}
			t.Run("routing invoice repair never changes customer charges", func(t *testing.T) {
				options := &redis.Options{Addr: os.Getenv("TEST_REDIS_ADDR"), Password: os.Getenv("TEST_REDIS_PASSWORD"), MaxRetries: -1}
				if options.Addr == "" {
					options.Addr = miniredis.RunT(t).Addr()
				}
				client := redis.NewClient(options)
				require.NoError(t, client.Ping(ctx).Err())
				oldRedis, oldEnabled := common.RDB, common.RedisEnabled
				common.RDB, common.RedisEnabled = client, true
				t.Cleanup(func() { common.RDB, common.RedisEnabled = oldRedis, oldEnabled; require.NoError(t, client.Close()) })
				beforeReport, err := keyusage.Query(ctx, channel.Id, 24)
				require.NoError(t, err)
				var before keyusage.KeyStats
				for _, stat := range beforeReport.Keys {
					if stat.KeyIdentity == keyIdentity {
						before = stat
					}
				}
				var walletBefore model.User
				require.NoError(t, db.First(&walletBefore, user.Id).Error)
				var paidBill, freeBill model.Sub2APIBill
				for _, bill := range bills {
					if bill.ResponseID == "resp_http" {
						paidBill = bill
					}
					if bill.RequestID == "client:exact" {
						freeBill = bill
					}
				}
				require.Positive(t, paidBill.ID)
				require.Positive(t, freeBill.ID)
				for _, bill := range []model.Sub2APIBill{paidBill, freeBill} {
					reservation, err := keyusage.Reserve(ctx, channel.Id, common.NewRequestId(), []keyusage.Candidate{{Identity: keyIdentity, Weight: 1, InputTokens: 40, OutputTokens: 10, CostUSD: common.GetPointer(0.005)}}, "")
					require.NoError(t, err)
					require.NoError(t, keyusage.Finish(ctx, reservation, &keyusage.Observation{InputTokens: 40, OutputTokens: 10, CostUSD: common.GetPointer(0.005)}))
					var source model.Log
					require.NoError(t, db.First(&source, bill.LogID).Error)
					var metadata map[string]any
					require.NoError(t, common.UnmarshalJsonStr(source.Other, &metadata))
					metadata["admin_info"].(map[string]any)["key_routing"] = reservation
					encoded, err := common.Marshal(metadata)
					require.NoError(t, err)
					require.NoError(t, db.Model(&source).Update("other", string(encoded)).Error)
					// The invoice is already durable. A projection outage must only
					// schedule repair, including when settlement itself is replayed.
					common.RedisEnabled = false
					require.NoError(t, model.SettleSub2APIBill(ctx, bill, bill.UsageID, bill.APIKeyID, bill.RequestID, "9", bill.BillingType, bill.RateMultiplier))
					var queued model.Sub2APIBill
					require.NoError(t, db.First(&queued, bill.ID).Error)
					assert.Positive(t, queued.NextSyncAt)
					assert.Equal(t, bill.ActualCostUSD, queued.ActualCostUSD, "replay must retain the committed supplier amount")
					common.RedisEnabled = true
				}
				require.NoError(t, model.RepairSub2APIKeyUsage(ctx, channel.Id))
				require.NoError(t, model.RepairSub2APIKeyUsage(ctx, channel.Id))
				report, err := keyusage.Query(ctx, channel.Id, 24)
				require.NoError(t, err)
				var found *keyusage.KeyStats
				for i := range report.Keys {
					if report.Keys[i].KeyIdentity == keyIdentity {
						found = &report.Keys[i]
					}
				}
				require.NotNil(t, found)
				require.NotNil(t, found.CostUSD)
				beforeCost := 0.0
				if before.CostUSD != nil {
					beforeCost = *before.CostUSD
				}
				assert.InDelta(t, 0.000001, *found.CostUSD-beforeCost, 1e-12)
				assert.EqualValues(t, 2, found.ActualCostRequests-before.ActualCostRequests, "zero-cost invoices are known actual costs")
				assert.EqualValues(t, 80, found.InputTokens-before.InputTokens)
				assert.EqualValues(t, 20, found.OutputTokens-before.OutputTokens)
				var walletAfter model.User
				require.NoError(t, db.First(&walletAfter, user.Id).Error)
				assert.Equal(t, walletBefore.Quota, walletAfter.Quota)
				var source model.Log
				require.NoError(t, db.First(&source, paidBill.LogID).Error)
				assert.Equal(t, 17, source.Quota)
				// A durable supplier amount may exceed the bounded routing
				// projection. Retain the invoice and stop retrying that amount.
				originalActualUSD, originalCostQuota := paidBill.ActualCostUSD, *paidBill.CostQuota
				tooLarge := float64(keyusage.MaxCostUSD + 1)
				require.NoError(t, db.Model(&paidBill).Updates(map[string]any{"actual_cost_usd": fmt.Sprint(tooLarge), "cost_quota": tooLarge * common.QuotaPerUnit, "next_sync_at": common.GetTimestamp()}).Error)
				assert.ErrorIs(t, model.RepairSub2APIKeyUsage(ctx, channel.Id), keyusage.ErrCostOutOfRange)
				var unprojectable model.Sub2APIBill
				require.NoError(t, db.First(&unprojectable, paidBill.ID).Error)
				assert.Equal(t, fmt.Sprint(tooLarge), unprojectable.ActualCostUSD)
				assert.Zero(t, unprojectable.NextSyncAt)
				require.NoError(t, model.RepairSub2APIKeyUsage(ctx, channel.Id))
				require.NoError(t, db.First(&walletAfter, user.Id).Error)
				assert.Equal(t, walletBefore.Quota, walletAfter.Quota)
				require.NoError(t, db.Model(&paidBill).Updates(map[string]any{"actual_cost_usd": originalActualUSD, "cost_quota": originalCostQuota, "next_sync_at": 0}).Error)
			})
			incompleteRate.Store(true)
			require.NoError(t, model.UpdateSub2APIInventory(ctx, account.ID, nil, func(state *model.Sub2APISyncState) error { state.NextAttemptAt = 0; return nil }))
			err = service.SyncSub2APIChannel(ctx, channel.Id, account.ID)
			assert.Equal(t, "SUB2API_RESPONSE_INVALID", service.Sub2APIErrorCode(err))
			assert.Equal(t, 0.5, *channel.UpstreamCostRatioForKey(sub2APITestKey), "missing amounts must not turn the supplier cost into zero")
			incompleteRate.Store(false)
			require.NoError(t, model.UpdateSub2APIInventory(ctx, account.ID, nil, func(state *model.Sub2APISyncState) error { state.NextAttemptAt = 0; return nil }))
			peak := model.Sub2APIRate{ResolvedRateMultiplier: 0.5, EffectiveRateMultiplier: 0.75, PeakRateEnabled: true, PeakStart: "08:00", PeakEnd: "20:00", PeakRateMultiplier: 1.5, Timezone: "Asia/Shanghai"}
			atPeak, valid := peak.EffectiveAt(time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC))
			assert.True(t, valid)
			assert.Equal(t, 0.75, atPeak)
			offPeak, valid := peak.EffectiveAt(time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
			assert.True(t, valid)
			assert.Equal(t, 0.5, offPeak)

			// Repeated migration keeps ciphertext, opt-ins and unique identities.
			require.NoError(t, model.MigrateSub2APIStorage(db))
			require.NoError(t, model.MigrateSub2APIStorage(db))
			afterUpgrade, err := model.GetSub2APIAccount(account.ID)
			require.NoError(t, err)
			assert.Equal(t, refreshed.Credential, afterUpgrade.Credential)
			binding, err = model.GetSub2APIKeyBinding(channel.Id, keyIdentity)
			require.NoError(t, err)
			assert.True(t, binding.SyncRates)
			assert.True(t, binding.SyncBills)
			duplicate := *binding
			duplicate.ID = 0
			require.Error(t, db.Create(&duplicate).Error, "migration must retain unique channel/key identity")

			// The HTTP boundary consumes action-bound proof, not just a session.
			keyPayload := map[string]any{"key_identity": keyIdentity, "sync_rates": true, "sync_bills": true}
			response := sub2APITestRequest(t, identity, channel.Id, "keys", keyPayload, "")
			assert.Contains(t, response.Body.String(), `"success":false`)
			proof := issueSecurityEnrollmentProof(t, identity, sub2APITestOperation(t, channel.Id, "keys", 0, keyIdentity), service.VerificationMethodPassword)
			response = sub2APITestRequest(t, identity, channel.Id, "keys", keyPayload, proof)
			assert.Contains(t, response.Body.String(), `"success":true`, response.Body.String())
			assert.Contains(t, response.Body.String(), `"data":{}`)
			response = sub2APITestRequest(t, identity, channel.Id, "keys", keyPayload, proof)
			assert.Contains(t, response.Body.String(), `"success":false`)
			get := sub2APITestRequest(t, identity, channel.Id, "", nil, "")
			assert.Contains(t, get.Body.String(), `"upstream_api_key_id":71`)
			assert.Contains(t, get.Body.String(), `"management_allowed":true`)
			httpStatus := sub2APITestRequest(t, identity, channel.Id, "", nil, "", false)
			assert.Contains(t, httpStatus.Body.String(), `"management_allowed":false`)
			t.Setenv("SUB2API_ALLOW_HTTP_MANAGEMENT", "true")
			httpStatus = sub2APITestRequest(t, identity, channel.Id, "", nil, "", false)
			assert.Contains(t, httpStatus.Body.String(), `"management_allowed":true`)
			response = sub2APITestRequest(t, identity, channel.Id, "keys", keyPayload, "", false)
			assert.Contains(t, response.Body.String(), `"success":false`, "HTTP compatibility still requires step-up")
			proof = issueSecurityEnrollmentProof(t, identity, sub2APITestOperation(t, channel.Id, "keys", 0, keyIdentity), service.VerificationMethodPassword)
			response = sub2APITestRequest(t, identity, channel.Id, "keys", keyPayload, proof, false)
			assert.Contains(t, response.Body.String(), `"success":true`, response.Body.String())
			response = sub2APITestRequest(t, identity, channel.Id, "keys", keyPayload, proof, false)
			assert.Contains(t, response.Body.String(), `"success":false`, "HTTP compatibility does not allow proof replay")
			t.Setenv("SUB2API_ALLOW_HTTP_MANAGEMENT", "")
			assert.NotContains(t, get.Body.String(), "rotated-access")
			assert.NotContains(t, get.Body.String(), sub2APITestKey)
			assert.Contains(t, get.Body.String(), "The Sub2API response is incomplete or invalid.")
			plainRequest := httptest.NewRequest(http.MethodPost, "http://example.com/", nil)
			plainRequest.Header.Set("X-Forwarded-Proto", "https")
			plainRequest.RemoteAddr = "192.0.2.12:1234"
			t.Setenv("SUB2API_TRUSTED_HTTPS_PROXIES", "")
			assert.False(t, sub2APISecureEntry(plainRequest), "an untrusted peer cannot attest TLS")
			t.Setenv("SUB2API_TRUSTED_HTTPS_PROXIES", "192.0.2.12/32")
			assert.True(t, sub2APISecureEntry(plainRequest))
			plainRequest.Header.Set("X-Forwarded-Proto", "http")
			assert.False(t, sub2APISecureEntry(plainRequest))

			// Redirects never follow a management bearer, even on the same origin.
			redirectMode.Store(true)
			var result map[string]any
			err = service.Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/auth/me", nil, nil, &result)
			require.Error(t, err)
			assert.Zero(t, redirectHits.Load())
			redirectMode.Store(false)
			// Global relay TLS opt-outs do not make an untrusted certificate valid.
			untrusted := transport.Clone()
			untrusted.TLSClientConfig = &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}
			http.DefaultTransport = untrusted
			err = service.Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/auth/me", nil, nil, &result)
			require.Error(t, err)
			assert.Equal(t, "SUB2API_TLS_FAILED", service.Sub2APIErrorCode(err))
			http.DefaultTransport = transport
			untrusted.CloseIdleConnections()
			changed := server.URL + "/different-origin-path"
			require.NoError(t, db.Model(&channel).Update("base_url", changed).Error)
			err = service.Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/auth/me", nil, nil, &result)
			assert.Equal(t, "SUB2API_CHANNEL_CHANGED", service.Sub2APIErrorCode(err))
			channel.BaseURL = &changed
			assert.Equal(t, 0.8, *channel.UpstreamCostRatioForKey(sub2APITestKey), "old-origin synced rates must not leak into a new supplier")
			require.NoError(t, db.Model(&channel).Update("base_url", server.URL).Error)
			channel.BaseURL = common.GetPointer(server.URL)
			require.NoError(t, db.Model(&model.Sub2APIAccount{}).Where("id = ?", account.ID).Update("upstream_user_id", 8).Error)
			err = service.Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/auth/me", nil, nil, &result)
			assert.Equal(t, "SUB2API_CREDENTIAL_INVALID", service.Sub2APIErrorCode(err), "ciphertext cannot be reassigned to another account")
			require.NoError(t, db.Model(&model.Sub2APIAccount{}).Where("id = ?", account.ID).Update("upstream_user_id", 7).Error)

			// Envelopes support long account tokens without weakening password limits.
			longCredential := strings.Repeat("a", 5000)
			encrypted, encryptionKey := sub2APITestEncrypt(t, longCredential)
			decrypted, err := common.DecryptChannelCredential(encrypted, encryptionKey)
			require.NoError(t, err)
			assert.Equal(t, longCredential, decrypted)
			_, err = common.DecryptPassword(encrypted, encryptionKey)
			require.Error(t, err)
			_, err = common.DecryptChannelCredential(encrypted, "wrong-id")
			require.Error(t, err)
			refreshFailure.Store(true)
			err = service.RefreshSub2APIAccount(ctx, account.ID, 0)
			assert.Equal(t, "SUB2API_LOGIN_REQUIRED", service.Sub2APIErrorCode(err))
			failedRefreshes := refreshes.Load()
			err = service.RefreshSub2APIAccount(ctx, account.ID, 0)
			assert.Equal(t, "SUB2API_LOGIN_REQUIRED", service.Sub2APIErrorCode(err))
			assert.Equal(t, failedRefreshes, refreshes.Load(), "an uncertain rotation must not replay its token")
			// Unlinking clears ownership and actual-cost sync without changing
			// channel relay credentials or the user's existing billing balance.
			require.NoError(t, service.DisconnectSub2API(ctx, channel.Id, account.ID, authorize("disconnect", account.ID, "")))
			binding, err = model.GetSub2APIKeyBinding(channel.Id, keyIdentity)
			require.NoError(t, err)
			assert.Zero(t, binding.AccountID)
			assert.False(t, binding.SyncBills)
			var afterUser model.User
			require.NoError(t, db.First(&afterUser, user.Id).Error)
			assert.Equal(t, 123456, afterUser.Quota)
			storedChannel, err = model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, channel.Key, storedChannel.Key)

			t.Run("independent accounts reuse verified ownership across channels", func(t *testing.T) {
				defer keysReleaseOnce.Do(func() { close(keysRelease) })
				defer refreshReleaseOnce.Do(func() { close(refreshRelease) })
				refreshFailure.Store(false)
				credential := service.Sub2APICredential{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresIn: 3600}
				sharedOperation := func(action string, id int64, origin string) service.Sub2APIAccountManageContext {
					return service.Sub2APIAccountManageContext{Action: action, AccountID: id, Origin: origin}
				}
				authorizeShared := func(operation service.Sub2APIAccountManageContext) *model.AuthFlowAuthorization {
					payload, marshalErr := common.Marshal(operation)
					require.NoError(t, marshalErr)
					target := service.VerificationOperation{Scope: service.VerificationScopeSub2APIAccountManage, Context: payload}
					proof := issueSecurityEnrollmentProof(t, identity, target, service.VerificationMethodPassword)
					authorization, consumeErr := service.ConsumeOperationProof(proof, identity, target)
					require.NoError(t, consumeErr)
					return authorization
				}
				authorizeChannel := func(id int, action string, accountID int64) *model.AuthFlowAuthorization {
					operation := sub2APITestOperation(t, id, action, accountID, "")
					proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
					authorization, consumeErr := service.ConsumeOperationProof(proof, identity, operation)
					require.NoError(t, consumeErr)
					return authorization
				}
				// A legacy row upgrades in place; another NULL identity remains independent.
				legacy, createErr := service.ImportSub2API(ctx, channel.Id, credential, authorize("import", 0, ""))
				require.NoError(t, createErr)
				legacyBefore, loadErr := model.GetSub2APIAccount(legacy.AccountID)
				require.NoError(t, loadErr)
				operation := sharedOperation("import", legacy.AccountID, "")
				upgraded, upgradeErr := service.ImportIndependentSub2API(ctx, operation, "", credential, authorizeShared(operation))
				require.NoError(t, upgradeErr)
				assert.Equal(t, legacy.AccountID, upgraded.AccountID)
				upgradedAccount, loadErr := model.GetSub2APIAccount(upgraded.AccountID)
				require.NoError(t, loadErr)
				assert.Equal(t, legacyBefore.Identity, upgradedAccount.Identity)
				assert.Equal(t, legacyBefore.ChannelID, upgradedAccount.ChannelID)
				require.NotNil(t, upgradedAccount.SharedIdentity)
				assert.NotEmpty(t, upgradedAccount.ProxyCredential)
				require.NoError(t, model.MigrateSub2APIStorage(db))
				require.NoError(t, model.MigrateSub2APIStorage(db))
				// Old channel endpoints may not overwrite an upgraded independent login.
				_, legacyErr := service.ImportSub2API(ctx, channel.Id, credential, authorize("import", 0, ""))
				require.Error(t, legacyErr)
				unchanged, loadErr := model.GetSub2APIAccount(upgraded.AccountID)
				require.NoError(t, loadErr)
				assert.Equal(t, upgradedAccount.Credential, unchanged.Credential)
				assert.Equal(t, upgradedAccount.ProxyCredential, unchanged.ProxyCredential)
				// New login of the canonical same site/user reuses its stable ID, no keys required.
				operation = sharedOperation("import", 0, server.URL+"/v1/")
				reused, reuseErr := service.ImportIndependentSub2API(ctx, operation, "", credential, authorizeShared(operation))
				require.NoError(t, reuseErr)
				assert.Equal(t, upgraded.AccountID, reused.AccountID)
				// The global API binds site/account/proxy/email to one-use local step-up.
				globalContext, marshalErr := common.Marshal(operation)
				require.NoError(t, marshalErr)
				globalTarget := service.VerificationOperation{Scope: service.VerificationScopeSub2APIAccountManage, Context: globalContext}
				globalProof := issueSecurityEnrollmentProof(t, identity, globalTarget, service.VerificationMethodPassword)
				wrongOrigin := sub2APITestRequest(t, identity, 0, "import", map[string]any{"origin": server.URL + "/wrong-site", "credential_encrypted": credentialEnvelope, "encryption_key_id": credentialKey}, globalProof)
				assert.NotContains(t, wrongOrigin.Body.String(), `"success":true`)
				globalProof = issueSecurityEnrollmentProof(t, identity, globalTarget, service.VerificationMethodPassword)
				input := map[string]any{"origin": server.URL + "/v1/", "credential_encrypted": credentialEnvelope, "encryption_key_id": credentialKey}
				imported := sub2APITestRequest(t, identity, 0, "import", input, globalProof)
				assert.Contains(t, imported.Body.String(), `"success":true`, imported.Body.String())
				replayed := sub2APITestRequest(t, identity, 0, "import", input, globalProof)
				assert.NotContains(t, replayed.Body.String(), `"success":true`)
				noProof := sub2APITestRequest(t, identity, 0, "import", input, "")
				assert.NotContains(t, noProof.Body.String(), `"success":true`)
				t.Run("failed imports retain safe upstream diagnostics and failed audit status", func(t *testing.T) {
					t.Cleanup(func() { identityStatus.Store(0) })
					for _, tc := range []struct {
						status int32
						code   string
					}{{401, "SUB2API_LOGIN_REQUIRED"}, {403, "SUB2API_ACCESS_DENIED"}, {404, "SUB2API_UNSUPPORTED"}, {429, "SUB2API_RATE_LIMITED"}, {503, "SUB2API_UPSTREAM_ERROR"}} {
						identityStatus.Store(tc.status)
						proof := issueSecurityEnrollmentProof(t, identity, globalTarget, service.VerificationMethodPassword)
						failed := sub2APITestRequest(t, identity, 0, "import", input, proof)
						expectedStatus := http.StatusBadRequest
						if tc.status == 429 {
							expectedStatus = http.StatusTooManyRequests
						}
						assert.Equal(t, expectedStatus, failed.Code)
						var payload map[string]any
						require.NoError(t, common.Unmarshal(failed.Body.Bytes(), &payload))
						assert.Equal(t, tc.code, payload["code"])
						assert.Equal(t, "auth_me", payload["error_stage"])
						assert.EqualValues(t, tc.status, payload["upstream_status"])
						assert.NotContains(t, failed.Body.String(), "fixture-private-upstream-body")
						var audit model.AuditLog
						require.NoError(t, model.LOG_DB.Where("route = ?", "/sub2api/accounts/import").Order("id DESC").First(&audit).Error)
						assert.Equal(t, expectedStatus, audit.Status)
						assert.False(t, audit.Success)
						serialized, marshalErr := common.Marshal(audit.Other)
						require.NoError(t, marshalErr)
						assert.Contains(t, string(serialized), tc.code)
						assert.Contains(t, string(serialized), `"error_stage":"auth_me"`)
						assert.NotContains(t, string(serialized), "fixture-private-upstream-body")
						assert.NotContains(t, string(serialized), credential.AccessToken)
					}
					for _, token := range []string{"invalid\ncredential", "invalid credential", "invalid\tcredential"} {
						before := identityRequests.Load()
						_, importErr := service.ImportIndependentSub2API(ctx, operation, "", service.Sub2APICredential{AccessToken: token}, authorizeShared(operation))
						assert.Equal(t, "SUB2API_CREDENTIAL_INVALID", service.Sub2APIErrorCode(importErr))
						assert.Equal(t, before, identityRequests.Load(), "invalid header values must never reach upstream")
					}
					before := identityRequests.Load()
					_, importErr := service.ImportIndependentSub2API(ctx, operation, "", service.Sub2APICredential{AccessToken: "opaque+=-_/credential"}, authorizeShared(operation))
					assert.Equal(t, "SUB2API_UPSTREAM_ERROR", service.Sub2APIErrorCode(importErr))
					assert.Equal(t, before+1, identityRequests.Load(), "opaque token punctuation remains accepted")
				})
				// Global MFA remains session-bound, expires, and never returns the temporary token.
				loginOperation := sharedOperation("login", upgraded.AccountID, "")
				loginOperation.Email = loginInput.Email
				sharedFlow, flowErr := service.LoginIndependentSub2API(ctx, loginOperation, "", loginInput, authorizeShared(loginOperation))
				require.NoError(t, flowErr)
				_, flowErr = service.CompleteIndependentSub2APILogin(ctx, sharedFlow.Flow, "123456", wrongSession)
				require.Error(t, flowErr)
				sharedLogin, flowErr := service.CompleteIndependentSub2APILogin(ctx, sharedFlow.Flow, "123456", identity)
				require.NoError(t, flowErr)
				assert.Equal(t, upgraded.AccountID, sharedLogin.AccountID)
				_, flowErr = service.CompleteIndependentSub2APILogin(ctx, sharedFlow.Flow, "123456", identity)
				require.Error(t, flowErr)
				expiredFlow, flowErr := service.LoginIndependentSub2API(ctx, loginOperation, "", loginInput, authorizeShared(loginOperation))
				require.NoError(t, flowErr)
				require.NoError(t, db.Model(&model.AuthFlow{}).Where("purpose = ? AND consumed_at IS NULL", "sub2api_account_login").Update("expires_at", time.Now().Add(-time.Minute)).Error)
				_, flowErr = service.CompleteIndependentSub2APILogin(ctx, expiredFlow.Flow, "123456", identity)
				assert.Equal(t, "SUB2API_FLOW_EXPIRED", service.Sub2APIErrorCode(flowErr))
				// Manual refresh callers (revision=0) also share one rotation. Observe the
				// second caller's DB snapshot before releasing the upstream response.
				beforeRefresh := refreshes.Load()
				blockRefresh.Store(true)
				refreshResults := make(chan error, 2)
				go func() { refreshResults <- service.RefreshSub2APIAccount(ctx, upgraded.AccountID, 0) }()
				select {
				case <-refreshEntered:
				case <-time.After(10 * time.Second):
					t.Fatal("refresh did not reach upstream barrier")
				}
				secondSnapshot := make(chan struct{}, 1)
				var observed atomic.Bool
				require.NoError(t, db.Callback().Query().After("gorm:query").Register("sub2api_shared_refresh_snapshot", func(tx *gorm.DB) {
					if tx.Statement.Table == "sub2_api_accounts" && observed.CompareAndSwap(false, true) {
						secondSnapshot <- struct{}{}
					}
				}))
				defer db.Callback().Query().Remove("sub2api_shared_refresh_snapshot")
				go func() { refreshResults <- service.RefreshSub2APIAccount(ctx, upgraded.AccountID, 0) }()
				select {
				case <-secondSnapshot:
				case <-time.After(10 * time.Second):
					t.Fatal("second refresh did not capture its revision")
				}
				refreshReleaseOnce.Do(func() { close(refreshRelease) })
				require.NoError(t, <-refreshResults)
				require.NoError(t, <-refreshResults)
				assert.Equal(t, beforeRefresh+1, refreshes.Load())
				// Concurrent first logins converge on one verified identity.
				concurrentOp := sharedOperation("import", 0, server.URL)
				concurrentAuth := []*model.AuthFlowAuthorization{authorizeShared(concurrentOp), authorizeShared(concurrentOp)}
				type connectedResult struct {
					result service.Sub2APILoginResult
					err    error
				}
				connectedResults := make(chan connectedResult, 2)
				var concurrent sync.WaitGroup
				for _, auth := range concurrentAuth {
					concurrent.Go(func() {
						value, err := service.ImportIndependentSub2API(ctx, concurrentOp, "", service.Sub2APICredential{AccessToken: "parallel-access"}, auth)
						connectedResults <- connectedResult{value, err}
					})
				}
				concurrent.Wait()
				close(connectedResults)
				var parallelID int64
				for value := range connectedResults {
					require.NoError(t, value.err)
					if parallelID == 0 {
						parallelID = value.result.AccountID
					}
					assert.Equal(t, parallelID, value.result.AccountID)
				}
				foreignOp := sharedOperation("import", 0, server.URL)
				foreign, foreignErr := service.ImportIndependentSub2API(ctx, foreignOp, "", service.Sub2APICredential{AccessToken: "foreign-access"}, authorizeShared(foreignOp))
				require.NoError(t, foreignErr)
				foreignAccount, loadErr := model.GetSub2APIAccount(foreign.AccountID)
				require.NoError(t, loadErr)
				assert.Zero(t, foreignAccount.ChannelID)
				require.Error(t, service.AttachIndependentSub2API(ctx, channel.Id, foreign.AccountID, authorizeChannel(channel.Id, "attach", foreign.AccountID)))
				second := model.Channel{Type: constant.ChannelTypeSub2API, Name: "Second channel", BaseURL: common.GetPointer(server.URL), Key: sub2APITestKey, Group: "default", Models: "fixture-model"}
				require.NoError(t, db.Create(&second).Error)
				require.NoError(t, service.AttachIndependentSub2API(ctx, second.Id, upgraded.AccountID, authorizeChannel(second.Id, "attach", upgraded.AccountID)))
				firstBinding, bindErr := model.GetSub2APIKeyBinding(channel.Id, keyIdentity)
				require.NoError(t, bindErr)
				secondBinding, bindErr := model.GetSub2APIKeyBinding(second.Id, second.KeyIdentity(sub2APITestKey))
				require.NoError(t, bindErr)
				assert.Equal(t, upgraded.AccountID, firstBinding.AccountID)
				assert.Equal(t, upgraded.AccountID, secondBinding.AccountID)
				assert.False(t, secondBinding.SyncBills)
				assert.False(t, secondBinding.SyncRates)
				manager := sub2APITestRequest(t, identity, 0, "", nil, "")
				assert.Contains(t, manager.Body.String(), "Second channel")
				assert.Contains(t, manager.Body.String(), `"legacy":false`)
				assert.Contains(t, manager.Body.String(), `"has_proxy":false`)
				assert.NotContains(t, manager.Body.String(), "fixture-access")
				assert.NotContains(t, manager.Body.String(), "proxy_credential")
				assert.NotContains(t, manager.Body.String(), upgradedAccount.ProxyCredential)
				require.NoError(t, model.SetSub2APIKeySync(channel.Id, keyIdentity, true, true, authorize("keys", 0, keyIdentity)))
				secondBinding, bindErr = model.GetSub2APIKeyBinding(second.Id, second.KeyIdentity(sub2APITestKey))
				require.NoError(t, bindErr)
				assert.False(t, secondBinding.SyncBills)
				require.NoError(t, service.SyncSub2APIChannel(ctx, second.Id, upgraded.AccountID))
				firstBinding, bindErr = model.GetSub2APIKeyBinding(channel.Id, keyIdentity)
				require.NoError(t, bindErr)
				assert.Equal(t, upgraded.AccountID, firstBinding.AccountID, "syncing one channel cannot clear another channel")

				t.Run("complete shared inventory and bounded retries", func(t *testing.T) {
					reset := func() {
						require.NoError(t, model.UpdateSub2APIInventory(ctx, upgraded.AccountID, nil, func(state *model.Sub2APISyncState) error {
							state.NextAttemptAt = 0
							state.BlockedUntil = 0
							state.Requests = 0
							state.UsageRequests = 0
							state.WindowAt = 0
							return nil
						}))
					}
					reset()
					beforeKeys, beforeRates := poolReads.Load(), rateReads.Load()
					syncErrors := make(chan error, 2)
					var syncWorkers sync.WaitGroup
					for _, channelID := range []int{channel.Id, second.Id} {
						syncWorkers.Go(func() { syncErrors <- service.SyncSub2APIChannel(ctx, channelID, upgraded.AccountID) })
					}
					syncWorkers.Wait()
					close(syncErrors)
					for syncErr := range syncErrors {
						require.NoError(t, syncErr)
					}
					assert.EqualValues(t, 2, poolReads.Load()-beforeKeys, "two linked channels share both clamped pages")
					assert.EqualValues(t, 0, rateReads.Load()-beforeRates, "non-peak rates are authoritative in the account endpoints without key-auth requests")
					require.NoError(t, model.DetachSub2APIAccount(second.Id, upgraded.AccountID, authorizeChannel(second.Id, "detach", upgraded.AccountID)))
					require.NoError(t, service.AttachIndependentSub2API(ctx, second.Id, upgraded.AccountID, authorizeChannel(second.Id, "attach", upgraded.AccountID)))
					assert.EqualValues(t, beforeKeys+2, poolReads.Load(), "attaching another channel reuses complete verified ownership")
					view, viewErr := service.GetSub2APIInventory(upgraded.AccountID)
					require.NoError(t, viewErr)
					require.NotNil(t, view.Snapshot)
					require.Len(t, view.Snapshot.Keys, 2)
					assert.Equal(t, 0.5, view.Snapshot.Groups[0].ResolvedRateMultiplier)
					saved, saveErr := model.GetSub2APIAccount(upgraded.AccountID)
					require.NoError(t, saveErr)
					cached, cooldownErr := service.SyncSub2APIInventory(ctx, upgraded.AccountID, true)
					require.NoError(t, cooldownErr)
					assert.True(t, cached.Cached)
					assert.EqualValues(t, beforeKeys+2, poolReads.Load(), "cooldown sends no upstream requests")
					for _, test := range []struct {
						name string
						mode int64
						code string
					}{
						{"second page fails", 1, "SUB2API_UPSTREAM_ERROR"},
						{"duplicate key", 2, "SUB2API_RESPONSE_INVALID"},
						{"wrong owner", 3, "SUB2API_RESPONSE_INVALID"},
						{"total changes", 4, "SUB2API_RESPONSE_INVALID"},
						{"retry after seconds", 5, "SUB2API_RATE_LIMITED"},
						{"retry after date", 6, "SUB2API_RATE_LIMITED"},
						{"total overflow", 8, "SUB2API_RESPONSE_INVALID"},
						{"page size changes", 9, "SUB2API_RESPONSE_INVALID"},
						{"missing group rate", 11, "SUB2API_RESPONSE_INVALID"},
						{"missing peak flag", 12, "SUB2API_RESPONSE_INVALID"},
					} {
						t.Run(test.name, func(t *testing.T) {
							reset()
							poolMode.Store(test.mode)
							if test.mode == 9 {
								_, importErr := service.ImportSub2API(ctx, channel.Id, service.Sub2APICredential{AccessToken: "fixture-access"}, authorizeChannel(channel.Id, "import", 0))
								assert.Equal(t, "SUB2API_RESPONSE_INVALID", service.Sub2APIErrorCode(importErr), "legacy channel import must reject individually valid pages with a changed page size")
							}
							_, syncErr := service.SyncSub2APIInventory(ctx, upgraded.AccountID, true)
							require.Error(t, syncErr)
							assert.Equal(t, test.code, service.Sub2APIErrorCode(syncErr))
							after, err := model.GetSub2APIAccount(upgraded.AccountID)
							require.NoError(t, err)
							assert.Equal(t, saved.InventoryJSON, after.InventoryJSON, "a failed or inconsistent page cannot publish partial ownership/rates")
							assert.Equal(t, saved.LastSyncAt, after.LastSyncAt)
							if test.mode == 5 || test.mode == 6 {
								state, err := model.ReadSub2APISyncState(after)
								require.NoError(t, err)
								assert.GreaterOrEqual(t, state.BlockedUntil, time.Now().Unix()+110)
								reads := poolReads.Load()
								var result any
								assert.Error(t, service.Sub2APIAccountRequest(ctx, upgraded.AccountID, http.MethodGet, "/api/v1/keys", nil, nil, &result))
								assert.Equal(t, reads, poolReads.Load())
							}
						})
					}
					reset()
					poolMode.Store(13)
					zero, zeroErr := service.SyncSub2APIInventory(ctx, upgraded.AccountID, true)
					require.NoError(t, zeroErr)
					assert.Zero(t, zero.Snapshot.Groups[0].ResolvedRateMultiplier)
					assert.Equal(t, 0.0, *channel.UpstreamCostRatioForKey(sub2APITestKey), "an explicit zero override is preserved")
					reset()
					poolMode.Store(7)
					large, largeErr := service.SyncSub2APIInventory(ctx, upgraded.AccountID, true)
					require.NoError(t, largeErr)
					require.Len(t, large.Snapshot.Keys, 200)
					stored, storeErr := model.GetSub2APIAccount(upgraded.AccountID)
					require.NoError(t, storeErr)
					assert.Greater(t, len(stored.InventoryJSON), 65535, "MySQL inventory must use longtext, not 64KiB TEXT")
					public, marshalErr := common.Marshal(large)
					require.NoError(t, marshalErr)
					assert.NotContains(t, string(public), sub2APITestKey)
					assert.NotContains(t, string(public), "key_identities")
					assert.NotContains(t, string(public), "fixture-access")
					// A retained peak snapshot must display the current schedule's
					// multiplier without rewriting its successful synchronization time.
					var peakInventory model.Sub2APIInventory
					require.NoError(t, common.UnmarshalJsonStr(stored.InventoryJSON, &peakInventory))
					start, end := "13:00", "23:00"
					if time.Now().UTC().Hour() >= 12 {
						start, end = "01:00", "11:00"
					}
					peakInventory.Snapshot.Groups[0].Rate = &model.Sub2APIRate{ResolvedRateMultiplier: 0.5, EffectiveRateMultiplier: 0.75, PeakRateEnabled: true, PeakStart: start, PeakEnd: end, PeakRateMultiplier: 1.5, Timezone: "UTC"}
					peakJSON, peakErr := common.Marshal(peakInventory)
					require.NoError(t, peakErr)
					require.NoError(t, db.Model(&model.Sub2APIAccount{}).Where("id = ?", upgraded.AccountID).Update("inventory_json", string(peakJSON)).Error)
					currentView, currentErr := service.GetSub2APIInventory(upgraded.AccountID)
					require.NoError(t, currentErr)
					assert.Equal(t, 0.5, currentView.Snapshot.Groups[0].Rate.EffectiveRateMultiplier)
					assert.Equal(t, stored.LastSyncAt, currentView.Snapshot.SyncedAt)
					unchanged, unchangedErr := model.GetSub2APIAccount(upgraded.AccountID)
					require.NoError(t, unchangedErr)
					assert.Equal(t, string(peakJSON), unchanged.InventoryJSON)
					require.NoError(t, db.Model(&model.Sub2APIAccount{}).Where("id = ?", upgraded.AccountID).Update("inventory_json", stored.InventoryJSON).Error)
					reset()
					poolMode.Store(10)
					poolAccountID.Store(upgraded.AccountID)
					_, revisionErr := service.SyncSub2APIInventory(ctx, upgraded.AccountID, true)
					require.Error(t, revisionErr)
					after, afterErr := model.GetSub2APIAccount(upgraded.AccountID)
					require.NoError(t, afterErr)
					assert.Equal(t, stored.InventoryJSON, after.InventoryJSON, "reimported credentials fence the previous in-flight inventory")
					reset()
					poolMode.Store(0)
					_, restoreErr := service.SyncSub2APIInventory(ctx, upgraded.AccountID, true)
					require.NoError(t, restoreErr)
				})
				disconnect := sharedOperation("disconnect", upgraded.AccountID, "")
				assert.Equal(t, "SUB2API_ACCOUNT_LINKED", service.Sub2APIErrorCode(service.DisconnectIndependentSub2API(ctx, upgraded.AccountID, authorizeShared(disconnect))))
				pendingJSON, marshalErr := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": model.Sub2APIBillingRef{AccountID: upgraded.AccountID, APIKeyID: 71, KeyIdentity: keyIdentity, RequestID: "client:detached"}}})
				require.NoError(t, marshalErr)
				detachedLog := model.Log{UserId: user.Id, ChannelId: channel.Id, RequestId: "gateway-detached", CreatedAt: time.Now().Unix(), Type: model.LogTypeConsume, Quota: 17, Other: string(pendingJSON)}
				require.NoError(t, db.Create(&detachedLog).Error)
				require.NoError(t, model.QueueSub2APIBill(ctx, &detachedLog))
				// An in-flight metadata sync cannot undo an explicit detach.
				require.NoError(t, model.UpdateSub2APIInventory(ctx, upgraded.AccountID, nil, func(state *model.Sub2APISyncState) error { state.NextAttemptAt = 0; state.BlockedUntil = 0; return nil }))
				blockKeys.Store(true)
				syncResult := make(chan error, 1)
				go func() { syncResult <- service.SyncSub2APIChannel(ctx, channel.Id, upgraded.AccountID) }()
				select {
				case <-keysEntered:
				case <-time.After(10 * time.Second):
					t.Fatal("sync did not reach key ownership barrier")
				}
				require.NoError(t, model.DetachSub2APIAccount(channel.Id, upgraded.AccountID, authorizeChannel(channel.Id, "detach", upgraded.AccountID)))
				keysReleaseOnce.Do(func() { close(keysRelease) })
				require.NoError(t, <-syncResult)
				firstBinding, bindErr = model.GetSub2APIKeyBinding(channel.Id, keyIdentity)
				require.NoError(t, bindErr)
				assert.Zero(t, firstBinding.AccountID)
				secondBinding, bindErr = model.GetSub2APIKeyBinding(second.Id, second.KeyIdentity(sub2APITestKey))
				require.NoError(t, bindErr)
				assert.Equal(t, upgraded.AccountID, secondBinding.AccountID)
				require.NoError(t, db.Model(&model.Sub2APIBill{}).Where("account_id = ?", upgraded.AccountID).Update("next_sync_at", 0).Error)
				require.NoError(t, model.UpdateSub2APIInventory(ctx, upgraded.AccountID, nil, func(state *model.Sub2APISyncState) error { state.LedgerCheckedAt = nil; return nil }))
				reconciled, reconcileErr := service.SyncSub2APIBills(ctx, 0)
				require.NoError(t, reconcileErr)
				assert.Equal(t, 1, reconciled)
				var detachedBill model.Sub2APIBill
				require.NoError(t, db.Where("log_id = ? AND log_request_id = ?", detachedLog.Id, detachedLog.RequestId).First(&detachedBill).Error)
				require.NotNil(t, detachedBill.CostQuota)
				assert.Equal(t, "0.000003", detachedBill.ActualCostUSD)
				assert.Equal(t, upgraded.AccountID, detachedBill.AccountID)
				// Routing changes never move a management login, nor tag new requests with its old supplier.
				require.NoError(t, db.Model(&second).Update("base_url", server.URL+"/other-site").Error)
				require.Error(t, service.AttachIndependentSub2API(ctx, second.Id, upgraded.AccountID, authorizeChannel(second.Id, "attach", upgraded.AccountID)))
				var verified map[string]any
				require.NoError(t, service.Sub2APIAccountRequest(ctx, upgraded.AccountID, http.MethodGet, "/api/v1/auth/me", nil, nil, &verified))
				require.NoError(t, model.SetSub2APIKeySync(second.Id, second.KeyIdentity(sub2APITestKey), false, true, func() *model.AuthFlowAuthorization {
					target := sub2APITestOperation(t, second.Id, "keys", 0, second.KeyIdentity(sub2APITestKey))
					proof := issueSecurityEnrollmentProof(t, identity, target, service.VerificationMethodPassword)
					auth, err := service.ConsumeOperationProof(proof, identity, target)
					require.NoError(t, err)
					return auth
				}()))
				traceContext, _ := gin.CreateTestContext(httptest.NewRecorder())
				changedInfo := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSub2API, ChannelId: second.Id, ChannelBaseUrl: server.URL + "/other-site", ApiKey: sub2APITestKey}}
				service.BeginSub2APIBillingAttempt(traceContext, changedInfo)
				service.CaptureSub2APIResponseID(traceContext, "wrong_supplier", true)
				assert.Zero(t, sub2APIBillingRefFromLog(t, traceContext, changedInfo).AccountID)
				require.NoError(t, model.DetachSub2APIAccount(second.Id, upgraded.AccountID, authorizeChannel(second.Id, "detach", upgraded.AccountID)))
				// Historical links to deleted/retyped channels cannot trap a
				// shared account in management. A disabled live channel still can.
				orphanChannels := []model.Channel{
					{Type: constant.ChannelTypeSub2API, Name: "Deleted orphan channel", Key: sub2APITestKey},
					{Type: constant.ChannelTypeSub2API, Name: "Retyped orphan channel", Key: sub2APITestKey},
					{Type: constant.ChannelTypeSub2API, Name: "Disabled linked channel", Key: sub2APITestKey},
				}
				for i := range orphanChannels {
					require.NoError(t, db.Create(&orphanChannels[i]).Error)
					require.NoError(t, db.Create(&model.Sub2APIKeyBinding{ChannelID: orphanChannels[i].Id, AccountID: upgraded.AccountID, KeyIdentity: orphanChannels[i].KeyIdentity(sub2APITestKey), UpstreamAPIKeyID: 71, SyncBills: true}).Error)
				}
				require.NoError(t, db.Delete(&orphanChannels[0]).Error)
				require.NoError(t, db.Model(&orphanChannels[1]).Update("type", constant.ChannelTypeOpenAI).Error)
				require.NoError(t, db.Model(&orphanChannels[2]).Update("status", common.ChannelStatusManuallyDisabled).Error)
				manager = sub2APITestRequest(t, identity, 0, "", nil, "")
				assert.NotContains(t, manager.Body.String(), "Deleted orphan channel")
				assert.NotContains(t, manager.Body.String(), "Retyped orphan channel")
				assert.Contains(t, manager.Body.String(), "Disabled linked channel")
				assert.Equal(t, "SUB2API_ACCOUNT_LINKED", service.Sub2APIErrorCode(service.DisconnectIndependentSub2API(ctx, upgraded.AccountID, authorizeShared(disconnect))))
				var linkedCount int64
				require.NoError(t, db.Model(&model.Sub2APIKeyBinding{}).Where("account_id = ?", upgraded.AccountID).Count(&linkedCount).Error)
				assert.EqualValues(t, 3, linkedCount, "a refused global disconnect must not remove any links")
				require.NoError(t, model.DetachSub2APIAccount(orphanChannels[2].Id, upgraded.AccountID, authorizeChannel(orphanChannels[2].Id, "detach", upgraded.AccountID)))
				manager = sub2APITestRequest(t, identity, 0, "", nil, "")
				var management struct {
					Success bool `json:"success"`
					Data    struct {
						Accounts []struct {
							ID         int64 `json:"id"`
							ChannelIDs []int `json:"channel_ids"`
						} `json:"accounts"`
					} `json:"data"`
				}
				require.NoError(t, common.Unmarshal(manager.Body.Bytes(), &management))
				require.True(t, management.Success)
				foundAccount := false
				for _, item := range management.Data.Accounts {
					if item.ID == upgraded.AccountID {
						foundAccount = true
						assert.Empty(t, item.ChannelIDs)
					}
				}
				require.True(t, foundAccount)
				stoppedJSON, marshalErr := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": model.Sub2APIBillingRef{AccountID: upgraded.AccountID, APIKeyID: 71, KeyIdentity: keyIdentity, RequestID: "client:disconnected"}}})
				require.NoError(t, marshalErr)
				stoppedLog := model.Log{UserId: user.Id, ChannelId: channel.Id, RequestId: "gateway-disconnected", CreatedAt: time.Now().Unix(), Type: model.LogTypeConsume, Quota: 17, Other: string(stoppedJSON)}
				require.NoError(t, db.Create(&stoppedLog).Error)
				require.NoError(t, model.QueueSub2APIBill(ctx, &stoppedLog))
				require.NoError(t, service.DisconnectIndependentSub2API(ctx, upgraded.AccountID, authorizeShared(disconnect)))
				require.NoError(t, db.Model(&model.Sub2APIKeyBinding{}).Where("account_id = ?", upgraded.AccountID).Count(&linkedCount).Error)
				assert.Zero(t, linkedCount)
				require.NoError(t, db.Where("id = ?", detachedBill.ID).First(&detachedBill).Error)
				assert.Equal(t, "0.000003", detachedBill.ActualCostUSD, "orphan cleanup preserves historical invoices")
				require.NoError(t, db.Model(&model.Sub2APIBill{}).Where("log_id = ? AND log_request_id = ?", stoppedLog.Id, stoppedLog.RequestId).Update("next_sync_at", 0).Error)
				reconciled, reconcileErr = service.SyncSub2APIBills(ctx, 0)
				assert.Equal(t, "SUB2API_LOGIN_REQUIRED", service.Sub2APIErrorCode(reconcileErr))
				assert.Zero(t, reconciled)
				var stoppedBill model.Sub2APIBill
				require.NoError(t, db.Where("log_id = ? AND log_request_id = ?", stoppedLog.Id, stoppedLog.RequestId).First(&stoppedBill).Error)
				assert.Nil(t, stoppedBill.CostQuota)
				tombstone, loadErr := model.GetSub2APIAccount(upgraded.AccountID)
				require.NoError(t, loadErr)
				assert.Empty(t, tombstone.Credential)
				assert.Empty(t, tombstone.ProxyCredential)
				assert.Equal(t, server.URL, tombstone.Origin)
				remaining, listErr := model.ListSub2APIAccounts(0)
				require.NoError(t, listErr)
				for _, current := range remaining {
					assert.NotEqual(t, upgraded.AccountID, current.ID)
				}
			})
		})
	}
}

func sub2APITestOperation(t *testing.T, channelID int, action string, accountID int64, key string) service.VerificationOperation {
	t.Helper()
	encoded, err := common.Marshal(service.Sub2APIManageContext{ChannelID: channelID, Action: action, AccountID: accountID, KeyIdentity: key})
	require.NoError(t, err)
	return service.VerificationOperation{Scope: service.VerificationScopeSub2APIManage, Context: encoded}
}

func sub2APIBillingRefFromLog(t *testing.T, c *gin.Context, info *relaycommon.RelayInfo) model.Sub2APIBillingRef {
	t.Helper()
	other := model.NewLogOther()
	service.AppendRelayLogAdminInfo(c, info, other)
	var metadata struct {
		AdminInfo struct {
			Ref model.Sub2APIBillingRef `json:"sub2api_billing"`
		} `json:"admin_info"`
	}
	require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &metadata))
	return metadata.AdminInfo.Ref
}

func sub2APITestRequest(t *testing.T, identity service.AuthIdentity, channelID int, action string, payload any, proof string, secureEntry ...bool) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", identity.UserID)
		c.Set("role", common.RoleRootUser)
		c.Set("session_id", identity.SessionID)
		c.Set("auth_version", identity.UserAuthVersion)
		c.Set("session_version", identity.SessionVersion)
	})
	method, route := http.MethodPost, "/channel/:id/sub2api/"+action
	if action == "keys" {
		method = http.MethodPut
	}
	if channelID == 0 {
		route = "/sub2api/accounts"
		if action == "" {
			method = http.MethodGet
			router.GET(route, GetSub2APIAccounts)
		} else {
			route += "/" + action
			router.Handle(method, route, ManageSub2APIAccounts)
		}
	} else if action == "" {
		method, route = http.MethodGet, "/channel/:id/sub2api"
		router.GET(route, GetSub2APIChannel)
	} else {
		router.Handle(method, route, ManageSub2APIChannel)
	}
	encoded, err := common.Marshal(payload)
	require.NoError(t, err)
	request := httptest.NewRequest(method, strings.ReplaceAll(route, ":id", strconv.Itoa(channelID)), bytes.NewReader(encoded))
	if len(secureEntry) == 0 || secureEntry[0] {
		request.TLS = &tls.ConnectionState{}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Security-Proof", proof)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func sub2APITestEncrypt(t *testing.T, plain string) (string, string) {
	t.Helper()
	keyID, publicPEM := common.PasswordEncryptionPublicKey()
	if keyID == "" {
		privatePEM, err := common.GeneratePasswordEncryptionPrivateKey()
		require.NoError(t, err)
		require.NoError(t, common.LoadPasswordEncryptionPrivateKey(privatePEM))
		keyID, publicPEM = common.PasswordEncryptionPublicKey()
	}
	block, _ := pem.Decode([]byte(publicPEM))
	require.NotNil(t, block)
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	public, ok := parsed.(*rsa.PublicKey)
	require.True(t, ok)
	key, nonce := make([]byte, 32), make([]byte, 12)
	_, err = rand.Read(key)
	require.NoError(t, err)
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, public, key, []byte("password-v2"))
	require.NoError(t, err)
	aesBlock, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(aesBlock)
	require.NoError(t, err)
	encrypted := gcm.Seal(nil, nonce, []byte(plain), []byte("password-v2:"+keyID))
	return fmt.Sprintf("v2.%s.%s.%s", base64.StdEncoding.EncodeToString(wrapped), base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(encrypted)), keyID
}
