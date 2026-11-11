package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Codex catalog bearer token",
			path:          "/v1/codex/models",
			headerName:    "Authorization",
			expectedField: "models",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
				assert.NotContains(t, payload, "models", "OpenAI response must stay unchanged")
			}
			if test.path == "/v1/codex/models" {
				assert.Len(t, payload, 1)
				assert.Equal(t, []any{}, payload["models"])
			}
		})
	}
}

func TestCodexModelCatalogRejectsInvalidCredentials(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "codex-models-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&[]model.Token{
		{UserId: user.Id, Key: "expiredcatalogkey", Status: common.TokenStatusEnabled, ExpiredTime: 1, UnlimitedQuota: true},
		{UserId: user.Id, Key: "disabledcatalogkey", Status: common.TokenStatusDisabled, ExpiredTime: -1, UnlimitedQuota: true},
	}).Error)
	engine := gin.New()
	SetRelayRouter(engine)
	for _, key := range []string{"", "unknowncatalogkey", "expiredcatalogkey", "disabledcatalogkey"} {
		t.Run(key, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/codex/models", nil)
			if key != "" {
				request.Header.Set("Authorization", "Bearer "+key)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, "error")
			assert.NotContains(t, payload, "models")
			if key != "" {
				assert.NotContains(t, recorder.Body.String(), key)
			}
		})
	}
}

// One provider account backs every user of a channel, so a file id alone is
// not an authorization: the gateway has to scope it to whoever uploaded it.
func TestFilesAreScopedToTheUserThatUploadedThem(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.RelayFile{}))

	tokenFor := func(username string, key string) int {
		user := model.User{Username: username, Status: common.UserStatusEnabled, Group: "default", Quota: 100, AffCode: username}
		require.NoError(t, model.DB.Create(&user).Error)
		require.NoError(t, model.DB.Create(&model.Token{
			UserId: user.Id, Key: key, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
		}).Error)
		return user.Id
	}
	ownerId := tokenFor("files-owner", "filesownerkey")
	tokenFor("files-stranger", "filesstrangerkey")
	require.NoError(t, (&model.RelayFile{
		FileId: "file-owned", UserId: ownerId, ChannelId: 1, Model: "4.0Ultra",
		Purpose: "batch", Filename: "batch.jsonl", Bytes: 42,
	}).Insert())

	engine := gin.New()
	SetRelayRouter(engine)
	call := func(method string, path string, key string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Authorization", "Bearer "+key)
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	t.Run("a stranger cannot read a file they do not own", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/files/file-owned", "filesstrangerkey").Code)
	})

	t.Run("a stranger cannot delete a file they do not own", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, call(http.MethodDelete, "/v1/files/file-owned", "filesstrangerkey").Code)
		_, err := model.GetRelayFile(ownerId, "file-owned")
		assert.NoError(t, err, "the owner's record must survive a stranger's delete")
	})

	t.Run("a stranger cannot download a file they do not own", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/files/file-owned/content", "filesstrangerkey").Code)
	})

	t.Run("an unknown id is refused the same way as one owned by someone else", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/files/file-imagined", "filesownerkey").Code)
	})

	t.Run("the listing shows only the caller's own files", func(t *testing.T) {
		recorder := call(http.MethodGet, "/v1/files", "filesstrangerkey")
		require.Equal(t, http.StatusOK, recorder.Code)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		assert.Empty(t, payload["data"])

		recorder = call(http.MethodGet, "/v1/files", "filesownerkey")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		listed, ok := payload["data"].([]any)
		require.True(t, ok)
		require.Len(t, listed, 1)
		assert.Equal(t, "file-owned", listed[0].(map[string]any)["id"])
	})
}

func TestRelayHandsVendorEndpointsToPluginRoutes(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "veo-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "veotestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	require.NoError(t, model.DB.AutoMigrate(&model.RelayFile{}))
	registry := jsplugin.NewRegistry()
	for _, key := range []string{"google", "hailuo"} {
		source, err := plugins.Source(key)
		require.NoError(t, err)
		_, err = registry.RegisterFactory(source, jsplugin.Options{Key: key})
		require.NoError(t, err)
	}
	// The plugin route authenticates as in production, then names itself.
	handlers := func(generation *jsplugin.RoutingGeneration, group jsplugin.RouteGroup) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, group)
		return []gin.HandlerFunc{production[0], production[1], production[2], func(c *gin.Context) {
			c.String(http.StatusOK, "plugin %s for user %d", group.Bindings[0].Plugin.Meta.Key, c.GetInt("id"))
		}}
	}
	require.NoError(t, registry.SetGenerationPreparer(newPluginGenerationBuilder(engine.Routes(), nil, handlers).prepare))
	require.Empty(t, registry.RoutingErrors())
	previousRegistry := defaultPluginDispatcher.registry
	defaultPluginDispatcher.registry = registry
	t.Cleanup(func() { defaultPluginDispatcher.registry = previousRegistry })
	engine.NoRoute(defaultPluginDispatcher.dispatch)

	for _, testCase := range []struct {
		method string
		path   string
		plugin string
	}{
		{method: http.MethodPost, path: "/v1beta/models/veo-3.1-generate-preview:predictLongRunning", plugin: "google"},
		{method: http.MethodGet, path: "/v1beta/models/veo-3.1-generate-preview/operations/task_sample", plugin: "google"},
		{method: http.MethodGet, path: "/v1/files/retrieve?file_id=task_sample", plugin: "hailuo"},
		{method: http.MethodGet, path: "/v1/files/retrieve"},
		{method: http.MethodGet, path: "/v1/files/file-other?file_id=task_sample"},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.5-flash:generateContent"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer veotestkey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if testCase.plugin != "" {
				assert.Equal(t, fmt.Sprintf("plugin %s for user %d", testCase.plugin, user.Id), recorder.Body.String())
				return
			}
			if strings.HasPrefix(testCase.path, "/v1/files/") {
				assert.Equal(t, http.StatusNotFound, recorder.Code)
				return
			}
			// The relay looks for a Gemini channel serving the model; there is none.
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"model_not_found"`)
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}
