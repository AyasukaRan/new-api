package controller

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The released schema before provider/account bindings and expiry were added.
type releasedRelayFile struct {
	Id        int
	FileId    string `gorm:"type:varchar(191);uniqueIndex:idx_relay_files_user_file,priority:2"`
	UserId    int    `gorm:"uniqueIndex:idx_relay_files_user_file,priority:1"`
	ChannelId int    `gorm:"index"`
	Model     string `gorm:"type:varchar(191)"`
	Purpose   string `gorm:"type:varchar(32)"`
	Filename  string `gorm:"type:varchar(255)"`
	Bytes     int64
	CreatedAt int64 `gorm:"index"`
}

func (releasedRelayFile) TableName() string { return "relay_files" }

func TestDeepSeekFilesOwnershipAndMigration(t *testing.T) {
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			for _, upgrade := range []bool{false, true} {
				t.Run("upgrade="+strconv.FormatBool(upgrade), func(t *testing.T) {
					require.NoError(t, db.Migrator().DropTable(&model.RelayFile{}))
					if upgrade {
						require.NoError(t, db.AutoMigrate(&releasedRelayFile{}))
						require.NoError(t, db.Create(&releasedRelayFile{FileId: "legacy-batch", UserId: 1, ChannelId: 1, Model: "legacy-model", Purpose: "batch", Filename: "input.jsonl", Bytes: 12, CreatedAt: 100}).Error)
					}
					for range 2 {
						require.NoError(t, db.AutoMigrate(&model.RelayFile{}))
					}
					assert.True(t, db.Migrator().HasIndex(&model.RelayFile{}, "idx_relay_files_user_file"))
					if upgrade {
						legacy, err := model.GetRelayFile(1, "legacy-batch")
						require.NoError(t, err)
						assert.Equal(t, "input.jsonl", legacy.Filename)
						assert.EqualValues(t, 12, legacy.Bytes)
						assert.EqualValues(t, 100, legacy.CreatedAt)
						assert.Empty(t, legacy.Provider)
						require.Error(t, db.Create(&model.RelayFile{UserId: 1, FileId: "legacy-batch"}).Error)
					}
					channel := &model.Channel{Type: constant.ChannelTypeDeepSeek, Name: "files", Key: "fixture-first\nfixture-second", Group: "default", Models: "deepseek-flash", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
					require.NoError(t, db.Create(channel).Error)
					identity := channel.KeyIdentity("fixture-second")
					file := &model.RelayFile{UserId: 1, FileId: "file-ds-owned", Provider: "deepseek", UpstreamFileId: "provider-file", ChannelId: channel.Id, KeyIdentity: identity, Model: "deepseek-flash", Purpose: "user_data", Filename: "report.txt", Bytes: 3, ExpiresAt: common.GetTimestamp() + 3600}
					require.NoError(t, file.Insert())
					for _, extra := range []model.RelayFile{
						{UserId: 2, FileId: file.FileId, ChannelId: channel.Id, Provider: "deepseek", UpstreamFileId: "other-owner-file", KeyIdentity: identity},
						{UserId: 1, FileId: "file-ds-expired", ChannelId: channel.Id, Provider: "deepseek", UpstreamFileId: "expired", KeyIdentity: identity, ExpiresAt: common.GetTimestamp() - 1},
						{UserId: 1, FileId: "file-ds-other-key", ChannelId: channel.Id, Provider: "deepseek", UpstreamFileId: "other-key", KeyIdentity: channel.KeyIdentity("fixture-first")},
						{UserId: 1, FileId: "file-ds-other-channel", ChannelId: channel.Id + 1, Provider: "deepseek", UpstreamFileId: "other-channel", KeyIdentity: identity},
						{UserId: 1, FileId: "file-ds-unbound", ChannelId: channel.Id, Provider: "deepseek", UpstreamFileId: "unbound"},
						{UserId: 1, FileId: "file-other-provider", ChannelId: channel.Id, Provider: "openai", KeyIdentity: identity},
					} {
						require.NoError(t, extra.Insert())
					}
					_, err := model.GetRelayFile(3, file.FileId)
					assert.ErrorIs(t, err, model.ErrRelayFileNotFound)
					_, err = model.GetRelayFile(1, "file-ds-expired")
					assert.ErrorIs(t, err, model.ErrRelayFileNotFound)
					listed, err := model.ListRelayFiles(2, "", 100)
					require.NoError(t, err)
					require.Len(t, listed, 1)
					assert.Equal(t, "other-owner-file", listed[0].UpstreamFileId)
					listed, err = model.ListRelayFiles(1, "user_data", 100)
					require.NoError(t, err)
					require.Len(t, listed, 1)
					assert.Equal(t, file.FileId, listed[0].FileId)
					assert.Equal(t, identity, listed[0].KeyIdentity)
					for i := range 3 {
						require.NoError(t, (&model.RelayFile{UserId: 1, FileId: fmt.Sprintf("paged-%d", i), Purpose: "pagination", Filename: "page.pdf"}).Insert())
					}
					page, more, err := model.ListRelayFilesPage(1, "pagination", "", "", "asc", 2)
					require.NoError(t, err)
					require.Len(t, page, 2)
					assert.True(t, more)
					assert.Equal(t, "paged-0", page[0].FileId)
					assert.Equal(t, "paged-1", page[1].FileId)
					page, more, err = model.ListRelayFilesPage(1, "pagination", page[1].FileId, "", "asc", 2)
					require.NoError(t, err)
					require.Len(t, page, 1)
					assert.False(t, more)
					assert.Equal(t, "paged-2", page[0].FileId)
					_, _, err = model.ListRelayFilesPage(3, "pagination", "paged-1", "", "asc", 2)
					assert.ErrorIs(t, err, model.ErrRelayFileNotFound)
					for _, test := range []struct {
						name, query, first, last string
						owner, status            int
						anthropic, more          bool
					}{
						{"first page", "purpose=pagination&order=asc&limit=2", "paged-0", "paged-1", 1, 200, false, true},
						{"next page", "purpose=pagination&order=asc&limit=2&after=paged-1", "paged-2", "paged-2", 1, 200, false, false},
						{"Anthropic first page", "purpose=pagination&limit=2", "paged-2", "paged-1", 1, 200, true, true},
						{"Anthropic next page", "purpose=pagination&limit=2&after_id=paged-1", "paged-0", "paged-0", 1, 200, true, false},
						{"Anthropic previous page", "purpose=pagination&limit=2&before_id=paged-0", "paged-2", "paged-1", 1, 200, true, false},
						{"foreign cursor", "purpose=pagination&after_id=paged-1", "", "", 3, 400, true, false},
						{"ambiguous cursors", "after_id=paged-0&before_id=paged-2", "", "", 1, 400, true, false},
						{"oversized page", "limit=1001", "", "", 1, 400, false, false},
					} {
						t.Run(test.name, func(t *testing.T) {
							c, recorder := fileTestContext(t, http.MethodGet, "/v1/files?"+test.query, "", test.owner)
							if test.anthropic {
								c.Request.Header.Set("anthropic-beta", "files-api-2025-04-14")
							}
							RelayFileList(c)
							require.Equal(t, test.status, recorder.Code, recorder.Body.String())
							if test.status != 200 {
								return
							}
							body := ojson.ParseBytes(recorder.Body.Bytes())
							assert.Equal(t, test.first, body.Get("first_id").String())
							assert.Equal(t, test.last, body.Get("last_id").String())
							assert.Equal(t, test.more, body.Get("has_more").Bool())
							assert.NotContains(t, recorder.Body.String(), "provider-file")
							assert.NotContains(t, recorder.Body.String(), "key_identity")
							if test.anthropic {
								assert.False(t, body.Get("object").Exists())
								assert.False(t, body.Get("data.0.bytes").Exists())
								assert.Equal(t, "file", body.Get("data.0.type").String())
								assert.Equal(t, "application/pdf", body.Get("data.0.mime_type").String())
								assert.Equal(t, ojson.String, body.Get("data.0.created_at").Type)
							} else {
								assert.Equal(t, "list", body.Get("object").String())
								assert.Equal(t, ojson.Number, body.Get("data.0.created_at").Type)
							}
						})
					}

					imageBlock := `{"type":"input_image","file_id":"file-ds-owned"}`
					for _, test := range []struct {
						name, path, body string
						owner            int
						allowed, pinned  bool
					}{
						{"Responses file", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-ds-owned"}]}]}`, 1, true, true},
						{"Chat file", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-ds-owned"}}]}]}`, 1, true, true},
						{"Messages document", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file-ds-owned"}}]}]}`, 1, true, true},
						{"Responses image", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-ds-owned"}]}]}`, 1, true, true},
						{"600 image references", "/v1/responses", `{"input":[` + strings.Repeat(imageBlock+",", 599) + imageBlock + `]}`, 1, true, true},
						{"600 images and 20 documents", "/v1/responses", `{"input":[` + strings.Repeat(imageBlock+",", 600) + strings.Repeat(`{"type":"input_file","file_id":"file-ds-owned"},`, 19) + `{"type":"input_file","file_id":"file-ds-owned"}]}`, 1, true, true},
						{"over total reference bound", "/v1/responses", `{"input":[` + strings.Repeat(imageBlock+",", 620) + imageBlock + `]}`, 1, false, false},
						{"large image batch foreign owner", "/v1/responses", `{"input":[` + strings.Repeat(imageBlock+",", 20) + imageBlock + `]}`, 3, false, false},
						{"large image batch mixed accounts", "/v1/responses", `{"input":[` + strings.Repeat(imageBlock+",", 20) + `{"type":"input_image","file_id":"file-ds-other-key"}]}`, 1, false, false},
						{"other provider reference bound", "/v1/responses", `{"input":[` + strings.Repeat(`{"type":"input_file","file_id":"file-other-provider"},`, 20) + `{"type":"input_file","file_id":"file-other-provider"}]}`, 1, false, false},
						{"Responses function output image", "/v1/responses", `{"input":[{"type":"function_call_output","call_id":"tool-1","output":[{"type":"input_image","file_id":"file-ds-owned"}]}]}`, 1, true, true},
						{"Responses custom output foreign image", "/v1/responses", `{"input":[{"type":"custom_tool_call_output","call_id":"tool-1","output":[{"type":"input_image","file_id":"provider-foreign-file"}]}]}`, 1, false, false},
						{"Messages tool result image", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"image","source":{"type":"file","file_id":"file-ds-owned"}}]}]}]}`, 1, true, true},
						{"Messages tool result foreign image", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"image","source":{"type":"file","file_id":"provider-foreign-file"}}]}]}]}`, 1, false, false},
						{"Responses foreign image", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"provider-foreign-file"}]}]}`, 1, false, false},
						{"escaped property", "/v1/responses", `{"input":[{"type":"input_file","file\u005fid":"file-ds-owned"}]}`, 1, true, true},
						{"foreign owner", "/v1/responses", `{"input":[{"type":"input_file","file_id":"file-ds-owned"}]}`, 3, false, false},
						{"escaped foreign owner", "/v1/responses", `{"input":[{"type":"input_file","file\u005fid":"file-ds-owned"}]}`, 3, false, false},
						{"expired", "/v1/responses", `{"input":[{"type":"input_file","file_id":"file-ds-expired"}]}`, 1, false, false},
						{"missing binding", "/v1/responses", `{"input":[{"type":"input_file","file_id":"file-ds-unbound"}]}`, 1, false, false},
						{"mixed accounts", "/v1/responses", `{"input":[{"type":"input_file","file_id":"file-ds-owned"},{"type":"input_file","file_id":"file-ds-other-key"}]}`, 1, false, false},
						{"mixed channels", "/v1/responses", `{"input":[{"type":"input_file","file_id":"file-ds-owned"},{"type":"input_file","file_id":"file-ds-other-channel"}]}`, 1, false, false},
						{"duplicate sensitive member", "/v1/responses", `{"input":[{"type":"input_file","file_id":"file-ds-owned","file_id":"foreign-upstream-id"}]}`, 1, false, false},
						{"tool arguments are opaque", "/v1/responses", `{"input":[{"type":"function_call","arguments":"{\"file_id\":\"not-a-resource\"}"}]}`, 1, true, false},
					} {
						t.Run(test.name, func(t *testing.T) {
							c, recorder := fileTestContext(t, http.MethodPost, test.path, test.body, test.owner)
							middleware.PrepareDeepSeekFileReferences()(c)
							assert.Equal(t, !test.allowed, c.IsAborted(), recorder.Body.String())
							if !test.allowed || !test.pinned {
								return
							}
							assert.Equal(t, channel.Id, c.GetInt("relay_file_channel_id"))
							assert.Equal(t, identity, c.GetString("relay_file_key_identity"))
							storage, err := common.GetBodyStorage(c)
							require.NoError(t, err)
							raw, err := storage.Bytes()
							require.NoError(t, err)
							assert.Contains(t, string(raw), "provider-file")
							assert.NotContains(t, string(raw), file.FileId)
							common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
							selected, apiErr := service.SelectChannelKeyForRequest(c, channel, "deepseek-flash", false)
							require.Nil(t, apiErr)
							assert.Equal(t, "fixture-second", selected.Key, "files must remain on their owning key")
						})
					}
					for _, restricted := range []bool{false, true} {
						channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
						if restricted {
							channel.ChannelInfo.MultiKeyStatusList = nil
							channel.ChannelInfo.KeyGroups = map[string][]string{identity: {"other-group"}}
						}
						require.NoError(t, db.Model(channel).Update("channel_info", channel.ChannelInfo).Error)
						c, _ := fileTestContext(t, http.MethodPost, "/v1/responses", "{}", 1)
						c.Set("relay_file_channel_id", channel.Id)
						c.Set("relay_file_key_identity", identity)
						common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
						_, apiErr := service.SelectChannelKeyForRequest(c, channel, "deepseek-flash", false)
						require.NotNil(t, apiErr, "disabled or unauthorized file credentials cannot fall back to another key")
					}
					channel.ChannelInfo.MultiKeyStatusList, channel.ChannelInfo.KeyGroups = nil, nil
					require.NoError(t, db.Model(channel).Update("channel_info", channel.ChannelInfo).Error)
					verifyDeepSeekFileCRUD(t, file, channel)
				})
			}
		})
	}
}

func fileTestContext(t *testing.T, method, path, body string, owner int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", owner)
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	return c, recorder
}

func verifyDeepSeekFileCRUD(t *testing.T, file *model.RelayFile, channel *model.Channel) {
	t.Helper()
	requests := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if err := r.ParseMultipartForm(1 << 20); !assert.NoError(t, err) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer r.MultipartForm.RemoveAll()
			assert.Equal(t, "user_data", r.FormValue("purpose"))
			assert.Empty(t, r.FormValue("model"), "local routing model must not reach the provider")
			_, _ = io.WriteString(w, `{"id":"provider-upload","object":"file","purpose":"user_data","bytes":3}`)
			return
		}
		if r.Method == http.MethodDelete {
			_, _ = io.WriteString(w, `{"id":"provider-file","object":"file","deleted":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"provider-file","object":"file","purpose":"user_data","bytes":3}`)
	}))
	t.Cleanup(upstream.Close)
	var uploadBody bytes.Buffer
	form := multipart.NewWriter(&uploadBody)
	require.NoError(t, form.WriteField("purpose", "user_data"))
	require.NoError(t, form.WriteField("model", "deepseek-flash"))
	part, err := form.CreateFormFile("file", "report.txt")
	require.NoError(t, err)
	_, err = io.WriteString(part, "abc")
	require.NoError(t, err)
	require.NoError(t, form.Close())
	c, recorder := fileTestContext(t, http.MethodPost, "/v1/files", uploadBody.String(), 1)
	c.Request.Header.Set("Content-Type", form.FormDataContentType())
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "fixture-second")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
	common.SetContextKey(c, constant.ContextKeyChannelId, channel.Id)
	middleware.PrepareRelayFileUpload()(c)
	require.False(t, c.IsAborted(), recorder.Body.String())
	RelayFileUpload(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Len(t, requests, 1)
	assert.Equal(t, "POST /files Bearer fixture-second", <-requests)
	localID := ojson.GetBytes(recorder.Body.Bytes(), "id").String()
	require.True(t, strings.HasPrefix(localID, "file-ds-"), recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "provider-upload")
	uploaded, err := model.GetRelayFile(1, localID)
	require.NoError(t, err)
	assert.Equal(t, "provider-upload", uploaded.UpstreamFileId)
	assert.Equal(t, channel.Id, uploaded.ChannelId)
	assert.Equal(t, channel.KeyIdentity("fixture-second"), uploaded.KeyIdentity)
	assert.Equal(t, "deepseek-flash", uploaded.Model)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		c, recorder := fileTestContext(t, method, "/v1/files/"+file.FileId, "", 3)
		c.Params = gin.Params{{Key: "id", Value: file.FileId}}
		middleware.PrepareRelayFile()(c)
		require.True(t, c.IsAborted())
		assert.Equal(t, http.StatusNotFound, recorder.Code)
		assert.Empty(t, requests, "foreign owners must not reach the upstream")
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		c, recorder := fileTestContext(t, method, "/v1/files/"+file.FileId, "", file.UserId)
		c.Params = gin.Params{{Key: "id", Value: file.FileId}}
		middleware.PrepareRelayFile()(c)
		require.False(t, c.IsAborted())
		common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
		common.SetContextKey(c, constant.ContextKeyChannelKey, "fixture-second")
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
		common.SetContextKey(c, constant.ContextKeyChannelId, channel.Id)
		if method == http.MethodGet {
			RelayFileRetrieve(c)
		} else {
			RelayFileDelete(c)
		}
		assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Len(t, requests, 1, "the owner request must reach the provider once")
		assert.Equal(t, method+" /files/provider-file Bearer fixture-second", <-requests)
		assert.Equal(t, file.FileId, ojson.GetBytes(recorder.Body.Bytes(), "id").String())
		assert.NotContains(t, recorder.Body.String(), "provider-file")
	}
	_, err = model.GetRelayFile(file.UserId, file.FileId)
	assert.ErrorIs(t, err, model.ErrRelayFileNotFound)
	other, err := model.GetRelayFile(2, file.FileId)
	require.NoError(t, err)
	assert.Equal(t, "other-owner-file", other.UpstreamFileId, "delete must remain owner scoped")
}

func TestDeepSeekFileUploadValidation(t *testing.T) {
	for _, test := range []struct {
		name, purpose, anchor, seconds string
		wantError                      bool
		anthropic                      bool
	}{
		{name: "user data", purpose: "user_data"},
		{name: "OCR parsing", purpose: "text_recognition"},
		{name: "layout parsing", purpose: "rich_layout_recognition"},
		{name: "bounded expiry", purpose: "user_data", anchor: "created_at", seconds: "3600"},
		{name: "invalid purpose", purpose: "other", wantError: true},
		{name: "negative expiry", purpose: "user_data", anchor: "created_at", seconds: "-1", wantError: true},
		{name: "excessive expiry", purpose: "user_data", anchor: "created_at", seconds: "2592001", wantError: true},
		{name: "expiry missing anchor", purpose: "user_data", seconds: "3600", wantError: true},
		{name: "Anthropic optional purpose", anthropic: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			if test.purpose != "" {
				require.NoError(t, form.WriteField("purpose", test.purpose))
			}
			if test.anchor != "" {
				require.NoError(t, form.WriteField("expires_after[anchor]", test.anchor))
			}
			if test.seconds != "" {
				require.NoError(t, form.WriteField("expires_after[seconds]", test.seconds))
			}
			part, err := form.CreateFormFile("file", "report.txt")
			require.NoError(t, err)
			_, err = io.WriteString(part, "report")
			require.NoError(t, err)
			require.NoError(t, form.Close())
			c, _ := fileTestContext(t, http.MethodPost, "/v1/files", body.String(), 1)
			c.Request.Header.Set("Content-Type", form.FormDataContentType())
			if test.anthropic {
				c.Request.Header.Set("anthropic-beta", "files-api-2025-04-14")
			}
			file, err := service.InspectDeepSeekFileUpload(c)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, file)
			purpose := test.purpose
			if test.anthropic && purpose == "" {
				purpose = "user_data"
			}
			assert.Equal(t, purpose, file.Purpose)
			assert.EqualValues(t, 6, file.Bytes)
			assert.Equal(t, "deepseek-flash", file.Model)
		})
	}
}

func TestDeepSeekFilesAnthropicResponseShapes(t *testing.T) {
	c, _ := fileTestContext(t, http.MethodGet, "/v1/files/local-file", "", 1)
	c.Request.Header.Set("anthropic-beta", "files-api-2025-04-14")
	metadata, err := deepSeekFilePublicJSON(c, []byte(`{"id":"provider-id","object":"file","bytes":12,"created_at":1700000000,"filename":"image.png","purpose":"user_data","request_id":"provider-request","internal_key":"secret"}`), "local-file")
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"local-file","type":"file","size_bytes":12,"created_at":"2023-11-14T22:13:20Z","filename":"image.png","purpose":"user_data","mime_type":"image/png"}`, string(metadata))
	deleted, err := deepSeekFilePublicJSON(c, []byte(`{"id":"provider-id","object":"file","deleted":true,"request_id":"provider-request"}`), "local-file")
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"local-file","type":"file_deleted"}`, string(deleted))
}

func TestDeepSeekFileImageMeteringContract(t *testing.T) {
	for _, test := range []struct {
		name, response string
		wantPages      int
		status         int
	}{
		{"single image", `{"pages":1,"basis":"local_image_pages","engine":"Pillow test"}`, 1, 200},
		{"multi-frame image", `{"pages":3,"basis":"local_image_pages","engine":"Pillow test"}`, 3, 200},
		{"page boundary", `{"pages":1000,"basis":"local_image_pages","engine":"Pillow test"}`, 1000, 200},
		{"zero pages", `{"pages":0,"basis":"local_image_pages","engine":"Pillow test"}`, 0, 200},
		{"negative pages", `{"pages":-1,"basis":"local_image_pages","engine":"Pillow test"}`, 0, 200},
		{"too many pages", `{"pages":1001,"basis":"local_image_pages","engine":"Pillow test"}`, 0, 200},
		{"wrong basis", `{"pages":1,"basis":"local_pagination","engine":"Pillow test"}`, 0, 200},
		{"missing engine", `{"pages":1,"basis":"local_image_pages"}`, 0, 200},
		{"invalid decoder input", "", 0, 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Keep the Unix socket name below the macOS/Linux path limit.
			directory, err := os.MkdirTemp("", "ds-pages-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(directory) })
			socket := filepath.Join(directory, "pages.sock")
			listener, err := net.Listen("unix", socket)
			require.NoError(t, err)
			payload := []byte("unchanged synthetic image bytes")
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/pages?format=image", r.URL.RequestURI())
				assert.EqualValues(t, len(payload), r.ContentLength)
				actual, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Equal(t, payload, actual)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			t.Setenv("DEEPSEEK_DOCUMENT_PAGES_SOCKET", socket)
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			require.NoError(t, form.WriteField("purpose", "text_recognition"))
			part, err := form.CreateFormFile("file", "original-without-extension")
			require.NoError(t, err)
			_, err = part.Write(payload)
			require.NoError(t, err)
			require.NoError(t, form.Close())
			original := bytes.Clone(body.Bytes())
			c, _ := fileTestContext(t, http.MethodPost, "/v1/files", body.String(), 1)
			c.Request.Header.Set("Content-Type", form.FormDataContentType())
			upload, err := service.InspectDeepSeekFileUpload(c)
			require.NoError(t, err)
			basis, err := service.MeterDeepSeekFileUpload(c, upload)
			if test.wantPages == 0 {
				require.Error(t, err)
				assert.Zero(t, upload.Pages, "invalid counts must not become a bill")
				assert.Empty(t, basis)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.wantPages, upload.Pages)
				assert.Equal(t, "local_image_pages", basis)
				assert.Equal(t, "Pillow test", c.GetString("deepseek_file_pagination_engine"))
			}
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			reread, err := storage.NewReader()
			require.NoError(t, err)
			defer reread.Close()
			unchanged, err := io.ReadAll(reread)
			require.NoError(t, err)
			assert.Equal(t, original, unchanged, "pagination must leave the original upstream upload intact")
		})
	}
}

func TestDeepSeekFilesResponsesWebSocketAuthorization(t *testing.T) {
	received := make(chan string, 4)
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.0002))`, func(ws *websocket.Conn, _ *http.Request) {
		for index := 0; ; index++ {
			_, body, err := ws.ReadMessage()
			if err != nil {
				return
			}
			received <- string(body)
			terminal := fmt.Sprintf(`{"type":"response.completed","response":{"id":"file-test-%d","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}}`, index)
			if err := ws.WriteMessage(websocket.TextMessage, []byte(terminal)); err != nil {
				return
			}
		}
	})
	require.NoError(t, model.DB.AutoMigrate(&model.RelayFile{}))
	for _, file := range []model.RelayFile{
		{UserId: fixture.user.Id, FileId: "file-ds-ws", ChannelId: fixture.channel.Id, KeyIdentity: fixture.channel.KeyIdentity("upstream-first"), Provider: "deepseek", UpstreamFileId: "provider-ws", Model: "ws-billing"},
		{UserId: fixture.user.Id, FileId: "file-ds-ws-other-key", ChannelId: fixture.channel.Id, KeyIdentity: fixture.channel.KeyIdentity("upstream-second"), Provider: "deepseek", UpstreamFileId: "provider-ws-second", Model: "ws-billing"},
	} {
		require.NoError(t, file.Insert())
	}
	for _, wrapped := range []bool{false, true} {
		fields := `"model":"ws-billing","input":[{"type":"input_file","file_id":"file-ds-ws"}]`
		payload := `{"type":"response.create",` + fields + `}`
		if wrapped {
			payload = `{"type":"response.create","response":{` + fields + `}}`
		}
		require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(payload)))
		terminal := readResponsesWSTestEvent(t, fixture.client)
		require.Equal(t, "response.completed", terminal["type"], terminal)
		require.Len(t, received, 1)
		forwarded := <-received
		assert.Equal(t, "provider-ws", ojson.Get(forwarded, "input.0.file_id").String())
		assert.NotContains(t, forwarded, "file-ds-ws", "the typed request must use the middleware-authorized body")
	}
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","response":{"model":"ws-billing","input":[{"type":"input_file","file_id":"foreign-upstream"}]}}`)))
	terminal := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", terminal["type"])
	assert.EqualValues(t, http.StatusNotFound, terminal["status"])
	assert.Empty(t, received)
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":[{"type":"input_file","file_id":"file-ds-ws-other-key"}]}`)))
	terminal = readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", terminal["type"])
	assert.EqualValues(t, http.StatusForbidden, terminal["status"])
	assert.Empty(t, received, "the existing socket cannot consume another account's file")
	fixture.closeAndWait(t)
}
