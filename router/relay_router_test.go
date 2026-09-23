package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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
