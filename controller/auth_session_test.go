package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func authLogoutTestRequest(router http.Handler, path, access, refresh, sid string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://panel.example.test"+path, nil)
	request.Header.Set("Origin", "https://panel.example.test")
	if access != "" {
		request.Header.Set("Authorization", "Bearer "+access)
	}
	if refresh != "" {
		request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: refresh})
	}
	request.Header.Set("X-Auth-Session", sid)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// This fixture retains the previously released users schema. Removing the
// provider must not delete an existing identity column or its historical values.
type userWithLegacyGatewayBinding struct {
	model.User
	GatewayID string `gorm:"column:gateway_id;index:idx_users_gateway_id"`
}

func (userWithLegacyGatewayBinding) TableName() string { return "users" }

func TestAuthLogoutDatabaseMatrix(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret, previousSecure := common.RedisEnabled, common.SessionSecret, common.SessionCookieSecure
	common.RedisEnabled, common.SessionCookieSecure = false, true
	common.SessionSecret = "auth-logout-isolated-test-secret"
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret, common.SessionCookieSecure = previousRedis, previousSecret, previousSecure
	})
	gin.SetMode(gin.TestMode)
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("legacy_schema=%t", legacy), func(t *testing.T) {
					db, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
					model.DB, model.LOG_DB = db, db
					common.SetDatabaseTypes(common.DatabaseType(dialect.kind), common.DatabaseType(dialect.kind))
					var version string
					versionQuery := "SELECT version()"
					if dialect.kind == "sqlite" {
						versionQuery = "SELECT sqlite_version()"
					}
					require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
					t.Logf("database version: %s", version)
					seed := model.User{Username: "existing-user", Password: "existing-password-hash", DisplayName: "Existing User", Email: "existing@example.test", OidcId: "oidc-user", AffCode: "existing", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
					if legacy {
						require.NoError(t, db.AutoMigrate(&userWithLegacyGatewayBinding{}))
						previous := userWithLegacyGatewayBinding{User: seed, GatewayID: "retired-identity"}
						require.NoError(t, db.Create(&previous).Error)
						seed = previous.User
					}
					for attempt := range 2 {
						require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
						if !legacy && attempt == 0 {
							require.NoError(t, db.Create(&seed).Error)
						}
						var stored model.User
						require.NoError(t, db.First(&stored, seed.Id).Error)
						assert.Equal(t, seed, stored)
						assert.Equal(t, legacy, db.Migrator().HasColumn(&model.User{}, "gateway_id"))
						assert.Equal(t, legacy, db.Migrator().HasIndex(&model.User{}, "idx_users_gateway_id"))
						assert.True(t, db.Migrator().HasIndex(&model.User{}, "idx_users_username"))
						if legacy {
							var binding string
							require.NoError(t, db.Table("users").Select("gateway_id").Where("id = ?", seed.Id).Scan(&binding).Error)
							assert.Equal(t, "retired-identity", binding)
						}
					}
					duplicate := model.User{Username: seed.Username, Password: "unused", AffCode: "duplicate"}
					assert.Error(t, db.Create(&duplicate).Error, "existing username uniqueness must survive migration")
					encoded, err := common.Marshal(seed)
					require.NoError(t, err)
					assert.NotContains(t, string(encoded), `"gateway_id"`)

					router := gin.New()
					router.POST("/protected", middleware.UserAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })
					router.POST("/logout", middleware.SessionCookieOriginGuard(), AuthLogout)
					router.POST("/refresh", middleware.SessionCookieOriginGuard(), RefreshAuth)
					cases := []struct {
						name, method, mode string
						wantRevoked        bool
						status             int
					}{
						{"password bearer and cookie", "password", "bearer", true, 200},
						{"oidc cookie only", "oauth:oidc", "cookie", true, 200},
						{"previous cookie within grace", "password", "previous", true, 200},
						{"previous cookie outside grace", "password", "old", false, 200},
						{"forged cookie", "password", "forged", false, 200},
						{"expired cookie", "password", "expired", false, 200},
						{"session mismatch", "password", "mismatch", false, 409},
						{"database failure", "password", "failure", false, 500},
						{"retired provider session", "oauth:gateway", "bearer", true, 200},
					}
					for index, test := range cases {
						t.Run(test.name, func(t *testing.T) {
							user := &model.User{Username: fmt.Sprintf("logout-%d", index), AffCode: fmt.Sprintf("logout%d", index), Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
							require.NoError(t, db.Create(user).Error)
							bundle, err := service.CreateLoginSession(user.Id, test.method, "127.0.0.1", "test-agent")
							require.NoError(t, err)
							assert.Equal(t, http.StatusOK, authLogoutTestRequest(router, "/protected", bundle.AccessToken, "", "").Code)
							access, refresh, sid := "", bundle.RefreshToken, bundle.Session.SID
							newest := bundle
							switch test.mode {
							case "bearer":
								access = bundle.AccessToken
							case "previous", "old":
								newest, _, err = service.RefreshLoginSession(refresh, sid, "127.0.0.1", "test-agent")
								require.NoError(t, err)
								if test.mode == "old" {
									require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", sid).Update("previous_valid_until", time.Now().Unix()-1).Error)
								}
							case "forged":
								refresh = sid + ".wrong-secret"
							case "expired":
								require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", sid).Update("expires_at", time.Now().Unix()-1).Error)
							case "mismatch":
								sid = "different-session"
							case "failure":
								require.NoError(t, db.Callback().Update().Before("gorm:update").Register("logout_test_failure", func(tx *gorm.DB) { tx.AddError(errors.New("isolated logout write failure")) }))
								defer func() { require.NoError(t, db.Callback().Update().Remove("logout_test_failure")) }()
							}
							response := authLogoutTestRequest(router, "/logout", access, refresh, sid)
							assert.Equal(t, test.status, response.Code)
							assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
							assert.NotContains(t, response.Body.String(), `"logout_url"`)
							var result struct {
								Success bool           `json:"success"`
								Data    authLogoutData `json:"data"`
							}
							if test.mode == "failure" {
								assert.Empty(t, response.Result().Cookies(), "a failed revoke must not report a cleared login")
							}
							require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
							assert.Equal(t, test.status == 200, result.Success)
							stored, err := model.GetUserSessionBySID(bundle.Session.SID)
							require.NoError(t, err)
							assert.Equal(t, test.wantRevoked, stored.Status == model.UserSessionStatusRevoked)
							if !test.wantRevoked {
								return
							}
							assert.Equal(t, "logout", stored.RevokedReason)
							assert.True(t, result.Data.CookieCleared)
							require.NotEmpty(t, response.Result().Cookies())
							assert.Negative(t, response.Result().Cookies()[0].MaxAge)
							for _, token := range []string{bundle.AccessToken, newest.AccessToken} {
								assert.Equal(t, http.StatusUnauthorized, authLogoutTestRequest(router, "/protected", token, "", "").Code)
							}
							for _, token := range []string{bundle.RefreshToken, newest.RefreshToken} {
								assert.Equal(t, http.StatusUnauthorized, authLogoutTestRequest(router, "/refresh", "", token, sid).Code)
							}
							// Retrying a lost logout response must remain safe and idempotent.
							retry := authLogoutTestRequest(router, "/logout", access, refresh, sid)
							var retried struct {
								Success bool           `json:"success"`
								Data    authLogoutData `json:"data"`
							}
							require.NoError(t, common.Unmarshal(retry.Body.Bytes(), &retried))
							assert.True(t, retried.Success)
							assert.Equal(t, result.Data, retried.Data)
						})
					}
				})
			}
		})
	}
}

func TestRetiredGatewayCannotAuthenticate(t *testing.T) {
	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{
		"gateway_sso.enabled": "true", "gateway_sso.secret": "old-proxy-secret",
		"gateway_sso.logout_url": "https://retired.example.test/logout",
	}
	t.Cleanup(func() { common.OptionMap = previousOptions })
	// Persisted settings from the old version cannot restore the removed provider.
	require.NoError(t, config.GlobalConfig.LoadFromDB(common.OptionMap))
	require.Nil(t, oauth.GetProvider("gateway"))
	require.Nil(t, config.GlobalConfig.Get("gateway_sso"))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	router.POST("/api/oauth/state", GenerateOAuthCode)
	router.GET("/api/oauth/:provider", HandleOAuth)
	for _, secret := range []string{"", "old-proxy-secret", "forged-secret"} {
		t.Run("secret="+secret, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/oauth/gateway?state=old-flow", nil)
			request.Header.Set("X-Forwarded-User", "admin")
			request.Header.Set("X-Gateway-Secret", secret)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Empty(t, response.Result().Cookies())
			assert.NotContains(t, response.Body.String(), `"access_token"`)
		})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"gateway","intent":"login"}`)))
	var payload struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.NotContains(t, payload.Data, "gateway_sso_enabled")
	assert.NotContains(t, payload.Data, "gateway_sso_display_name")
	assert.Contains(t, payload.Data, "oidc_enabled")
}

func TestAuthLogoutRejectsRefreshCookieSessionMismatch(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "auth-logout-mismatch-test-secret"
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})

	user := &model.User{
		Username: "logout-mismatch-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	sessionA, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-a")
	require.NoError(t, err)
	sessionB, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-b")
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	c.Request.Header.Set("Authorization", "Bearer "+sessionA.AccessToken)
	c.Request.Header.Set("X-Auth-Session", sessionA.Session.SID)
	c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: sessionB.RefreshToken})

	AuthLogout(c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, "AUTH_SESSION_MISMATCH", response.Code)
	for _, sid := range []string{sessionA.Session.SID, sessionB.Session.SID} {
		stored, err := model.GetUserSessionBySID(sid)
		require.NoError(t, err)
		assert.Equal(t, model.UserSessionStatusActive, stored.Status)
	}
}

func TestWriteAuthSessionErrorMapsSessionGrowthLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "active session limit",
			err:            model.ErrUserSessionLimit,
			expectedStatus: http.StatusConflict,
			expectedCode:   "AUTH_SESSION_LIMIT",
		},
		{
			name:           "issuance limit",
			err:            model.ErrUserSessionIssuanceLimit,
			expectedStatus: http.StatusTooManyRequests,
			expectedCode:   "AUTH_SESSION_ISSUANCE_LIMIT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			writeAuthSessionError(c, test.err)

			assert.Equal(t, test.expectedStatus, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, test.expectedCode, response.Code)
		})
	}
}

func TestSessionLimitDoesNotRecordRejectedLoginAsSuccessful(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.PasskeyCredential{}))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 100
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
	})

	const previousLastLoginAt = int64(123)
	user := &model.User{
		Username: "rejected-login-audit-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, LastLoginAt: previousLastLoginAt,
	}
	require.NoError(t, db.Create(user).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{
		SID: "existing-active-session", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
		Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	setupLogin(user, nil, c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var stored model.User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.Equal(t, previousLastLoginAt, stored.LastLoginAt)
}
