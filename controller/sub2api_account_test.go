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
	"net/http"
	"net/http/httptest"
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
	"github.com/QuantumNous/new-api/model"
	relayopenai "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sub2APITestKey = "sk-sub2api-fixture-one"
const sub2APITestOtherKey = "sk-sub2api-fixture-two"

// The same public contracts run against all supported main databases. The
// upgrade fixture creates existing users/channels before adding the new tables.
func TestSub2APIAccountDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ name, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.name, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.name, os.Getenv(dialect.env))
			t.Setenv("SUB2API_CREDENTIAL_SECRET", strings.Repeat("fixture-secret-", 4))
			require.NoError(t, i18n.Init())
			gin.SetMode(gin.TestMode)
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

			var refreshes, completions, redirectHits atomic.Int32
			var redirectMode, incompleteRate, ledgerFailure, refreshFailure atomic.Bool
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
					if redirectMode.Load() {
						http.Redirect(w, r, server.URL+"/redirect-target", http.StatusFound)
						return
					}
					if r.Header.Get("Authorization") == "Bearer foreign-access" {
						respond(map[string]any{"id": 99, "email": "foreign@example.com"})
						return
					}
					respond(map[string]any{"id": 7, "email": "owner@example.com"})
				case "/api/v1/keys":
					if r.Header.Get("Authorization") == "Bearer foreign-access" {
						respond(map[string]any{"pages": 1, "items": []any{map[string]any{"id": 99, "user_id": 99, "key": "sk-unrelated"}}})
						return
					}
					// Matching keys may be beyond the first page.
					if r.URL.Query().Get("page") == "1" {
						respond(map[string]any{"pages": 2, "items": []any{map[string]any{"id": 9, "user_id": 7, "key": "sk-unrelated"}}})
					} else {
						respond(map[string]any{"pages": 2, "items": []any{map[string]any{"id": 71, "user_id": 7, "group_id": 3, "key": sub2APITestKey}}})
					}
				case "/api/v1/auth/refresh":
					refreshes.Add(1)
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
					if r.URL.Query().Get("page") == "1" {
						respond(map[string]any{"pages": 2, "items": []any{
							map[string]any{"id": 110, "api_key_id": 72, "request_id": "client:wrong-key", "actual_cost": 1, "billing_type": 0},
							map[string]any{"id": 111, "api_key_id": 71, "request_id": "client:missing-amount", "billing_type": 0},
						}})
					} else {
						respond(map[string]any{"pages": 2, "items": []any{
							map[string]any{"id": 112, "api_key_id": 71, "request_id": "client:exact", "actual_cost": 0, "billing_type": 1, "rate_multiplier": 0.5},
							map[string]any{"id": 113, "api_key_id": 71, "request_id": "resp_http", "actual_cost": 0.000001, "billing_type": 0},
							map[string]any{"id": 114, "api_key_id": 71, "request_id": "resp_ws_second", "actual_cost": 0.000002, "billing_type": 0},
						}})
					}
				case "/v1/sub2api/billing":
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
			require.NoError(t, model.SetSub2APIKeySync(channel.Id, keyIdentity, true, true, authorize("keys", 0, keyIdentity)))
			assert.Equal(t, 0.5, *channel.UpstreamCostRatioForKey(sub2APITestKey))
			assert.Equal(t, 0.8, channel.ChannelInfo.KeyCostRatios[keyIdentity])
			// The ledger sync matches both the original upstream request ID and
			// verified key ID, follows pages, and never interprets missing data as free.
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			recorder := httptest.NewRecorder()
			billingContext, _ := gin.CreateTestContext(recorder)
			billingContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			relayInfo := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSub2API, ChannelId: channel.Id, ApiKey: sub2APITestKey}}
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
			require.NoError(t, db.Model(&model.Sub2APIBill{}).Where("channel_id = ?", channel.Id).Update("next_sync_at", 0).Error)
			settled, syncErr = service.SyncSub2APIBills(ctx, channel.Id)
			require.NoError(t, syncErr)
			assert.Equal(t, 3, settled)
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
			incompleteRate.Store(true)
			err = service.SyncSub2APIChannel(ctx, channel.Id, account.ID)
			assert.Equal(t, "SUB2API_RESPONSE_INVALID", service.Sub2APIErrorCode(err))
			assert.Equal(t, 0.5, *channel.UpstreamCostRatioForKey(sub2APITestKey), "missing amounts must not turn the supplier cost into zero")
			incompleteRate.Store(false)
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

func sub2APITestRequest(t *testing.T, identity service.AuthIdentity, channelID int, action string, payload any, proof string) *httptest.ResponseRecorder {
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
	if action == "" {
		method, route = http.MethodGet, "/channel/:id/sub2api"
		router.GET(route, GetSub2APIChannel)
	} else {
		router.Handle(method, route, ManageSub2APIChannel)
	}
	encoded, err := common.Marshal(payload)
	require.NoError(t, err)
	request := httptest.NewRequest(method, strings.ReplaceAll(route, ":id", strconv.Itoa(channelID)), bytes.NewReader(encoded))
	request.TLS = &tls.ConnectionState{}
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
