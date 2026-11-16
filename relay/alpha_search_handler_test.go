package relay

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekUploadValidation(t *testing.T) {
	for _, test := range []struct {
		name, path, purpose, extra string
		files                      int
		declaredSize               int64
		wantError                  bool
	}{
		{name: "OCR", path: "/v1/tools/ocr", purpose: "text_recognition", files: 1},
		{name: "layout OCR", path: "/v1/tools/ocr", purpose: "rich_layout_recognition", files: 1},
		{name: "ASR", path: "/v1/tools/asr", files: 1},
		{name: "missing file", path: "/v1/tools/ocr", purpose: "text_recognition", wantError: true},
		{name: "missing purpose", path: "/v1/tools/ocr", files: 1, wantError: true},
		{name: "invalid purpose", path: "/v1/tools/ocr", purpose: "user_data", files: 1, wantError: true},
		{name: "duplicate file", path: "/v1/tools/asr", files: 2, wantError: true},
		{name: "ASR purpose", path: "/v1/tools/asr", files: 1, purpose: "text_recognition", wantError: true},
		{name: "unknown field", path: "/v1/tools/asr", files: 1, extra: "model", wantError: true},
		{name: "declared oversized ASR", path: "/v1/tools/asr", files: 1, declaredSize: 10 << 20, wantError: true},
		{name: "declared oversized OCR", path: "/v1/tools/ocr", files: 1, purpose: "text_recognition", declaredSize: 102 << 20, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if test.purpose != "" {
				require.NoError(t, writer.WriteField("purpose", test.purpose))
			}
			if test.extra != "" {
				require.NoError(t, writer.WriteField(test.extra, "ignored"))
			}
			for range test.files {
				part, err := writer.CreateFormFile("file", "upload.bin")
				require.NoError(t, err)
				_, err = io.WriteString(part, "provider validates file content")
				require.NoError(t, err)
			}
			require.NoError(t, writer.Close())
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body.Bytes()))
			c.Request.Header.Set("Content-Type", writer.FormDataContentType())
			if test.declaredSize > 0 {
				c.Request.ContentLength = test.declaredSize
			}
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			request, err := helper.GetAndValidateRequest(c, types.RelayFormatDeepSeekTools)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			upload, ok := request.(*dto.DeepSeekUploadRequest)
			require.True(t, ok)
			unit, count := upload.GetMeteredUsage()
			if test.path == "/v1/tools/ocr" {
				assert.Equal(t, "page", unit)
				assert.Equal(t, 1000, count)
			} else {
				assert.Equal(t, "second", unit)
				assert.Equal(t, 120, count)
			}
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			raw, err := storage.Bytes()
			require.NoError(t, err)
			assert.Equal(t, body.Bytes(), raw, "validation must not rewrite the multipart body")
		})
	}
}

func TestDeepSeekUploadHelper(t *testing.T) {
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	batch, logging := common.BatchUpdateEnabled, common.LogConsumeEnabled
	common.BatchUpdateEnabled, common.LogConsumeEnabled = true, false
	t.Cleanup(func() { common.BatchUpdateEnabled, common.LogConsumeEnabled = batch, logging })
	const ocrResponse = `{"id":"upstream-ocr-id","object":"ocr.result","page_count":2,"pages":[{"index":0,"text":"first","finish_reason":"stop"},{"index":1,"text":"second","finish_reason":"length"}],"usage":{"input_tokens":500,"output_tokens":900}}`
	for _, test := range []struct {
		name, path, response string
		status               int
		count                int
		cancelClient         bool
		wantError            bool
	}{
		{name: "OCR measured pages ignores tokens", path: "/v1/tools/ocr", response: "\n\n" + ocrResponse, status: 200, count: 2},
		{name: "OCR downstream cancellation still settles", path: "/v1/tools/ocr", response: ocrResponse, status: 200, count: 2, cancelClient: true},
		{name: "ASR ceil seconds", path: "/v1/tools/asr", response: `{"request_id":"upstream-asr-id","text":"hello","duration_ms":3200}`, status: 200, count: 4},
		{name: "ASR maximum seconds", path: "/v1/tools/asr", response: `{"request_id":"upstream-asr-id","text":"","duration_ms":120000}`, status: 200, count: 120},
		{name: "ASR negative", path: "/v1/tools/asr", response: `{"text":"hello","duration_ms":-1}`, status: 200, wantError: true},
		{name: "ASR fractional", path: "/v1/tools/asr", response: `{"text":"hello","duration_ms":1.5}`, status: 200, wantError: true},
		{name: "ASR excessive", path: "/v1/tools/asr", response: `{"text":"hello","duration_ms":120001}`, status: 200, wantError: true},
		{name: "OCR missing pages", path: "/v1/tools/ocr", response: `{"object":"ocr.result","page_count":2,"pages":[]}`, status: 200, wantError: true},
		{name: "OCR excessive", path: "/v1/tools/ocr", response: `{"object":"ocr.result","page_count":1001}`, status: 200, wantError: true},
		{name: "recognition failed after acceptance", path: "/v1/tools/ocr", response: `{"error":{"type":"gateway_timeout_error","message":"OCR did not finish in time"}}`, status: 200, wantError: true},
		{name: "provider unavailable", path: "/v1/tools/asr", response: `{"error":{"type":"api_error","message":"busy"}}`, status: 503, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			if test.path == "/v1/tools/ocr" {
				require.NoError(t, form.WriteField("purpose", "text_recognition"))
			}
			part, err := form.CreateFormFile("file", "fixture.bin")
			require.NoError(t, err)
			_, err = io.WriteString(part, "fixture")
			require.NoError(t, err)
			require.NoError(t, form.Close())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var gotBody []byte
			var gotPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				if test.cancelClient {
					cancel()
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			}))
			t.Cleanup(upstream.Close)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body.Bytes())).WithContext(ctx)
			c.Request.Header.Set("Content-Type", form.FormDataContentType())
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			model := constant.DeepSeekOCRModel
			if test.path == "/v1/tools/asr" {
				model = constant.DeepSeekASRModel
			}
			common.SetContextKey(c, constant.ContextKeyOriginalModel, model)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelKey, "sk-fixture")
			c.Set(common.RequestIdKey, "local-request")
			request, err := helper.GetAndValidateRequest(c, types.RelayFormatDeepSeekTools)
			require.NoError(t, err)
			info, err := relaycommon.GenRelayInfo(c, types.RelayFormatDeepSeekTools, request, nil)
			require.NoError(t, err)
			_, err = helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
			require.NoError(t, err)
			settlement := &recordedSettlement{}
			info.Billing = settlement
			apiErr := DeepSeekUploadHelper(c, info)
			assert.Equal(t, test.path, gotPath)
			assert.Equal(t, body.Bytes(), gotBody)
			if test.wantError {
				require.NotNil(t, apiErr)
				assert.True(t, types.IsSkipRetryError(apiErr), "accepted uploads cannot replay")
				require.NotNil(t, RefundFailedRequestBilling(c, info, apiErr))
				assert.True(t, settlement.refunded)
				assert.Empty(t, settlement.settled)
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, info.BillingMeteredUsage)
			assert.Equal(t, test.count, info.BillingMeteredUsage.Count)
			require.Len(t, settlement.settled, 1)
			if model == constant.DeepSeekOCRModel {
				assert.Equal(t, 225, settlement.settled[0])
			}
			assert.False(t, settlement.refunded)
			if test.cancelClient {
				assert.Empty(t, recorder.Body.String())
			} else {
				if strings.HasPrefix(test.response, "\n") {
					assert.True(t, strings.HasPrefix(recorder.Body.String(), "\n\n"), "OCR keepalives must reach the downstream")
				}
				assert.NotContains(t, recorder.Body.String(), "upstream-ocr-id")
				assert.NotContains(t, recorder.Body.String(), "upstream-asr-id")
				assert.Contains(t, recorder.Body.String(), "local-request")
			}
		})
	}
}

func TestBuildAlphaSearchRequestBodyPreservesUnknownFields(t *testing.T) {
	raw := []byte(`{
		"id":"req_1",
		"model":"gpt-5.1",
		"input":[{"role":"user","content":"hi"}],
		"commands":{"search_query":[{"q":"weather","recency":1}]},
		"settings":{"locale":"en"},
		"future_field":{"nested":true}
	}`)

	out, err := buildAlphaSearchRequestBody(raw, "gpt-5.1", "gpt-5.1-mapped")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, common.Unmarshal(out, &body))
	assert.Equal(t, "gpt-5.1-mapped", body["model"])
	assert.Equal(t, "req_1", body["id"])
	require.Contains(t, body, "commands")
	require.Contains(t, body, "settings")
	require.Contains(t, body, "future_field")
	require.Contains(t, body, "input")

	commands, ok := body["commands"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, commands, "search_query")

	future, ok := body["future_field"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, future["nested"])
}

func TestBuildAlphaSearchRequestBodyNoMappingKeepsRawBytes(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.1","commands":{"search_query":[{"q":"x"}]},"future_field":1}`)
	out, err := buildAlphaSearchRequestBody(raw, "gpt-5.1", "gpt-5.1")
	require.NoError(t, err)
	assert.Equal(t, raw, out)
}

// A single model is rewritten where it stands. Routing reads the first
// "model" and most upstreams read the last, so a repeated model comes out as
// one member carrying the mapped value.
func TestBuildAlphaSearchRequestBodyRewritesModel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"single model keeps its place", `{"model":"gpt-5.1","input":[]}`, `{"model":"gpt-5.1-mapped","input":[]}`},
		{"repeated model collapses", `{"model":"gpt-5.1","input":[],"model":"gpt-5.5-pro"}`, `{"input":[],"model":"gpt-5.1-mapped"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := buildAlphaSearchRequestBody([]byte(tt.raw), "gpt-5.1", "gpt-5.1-mapped")
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(out))
		})
	}
}

type recordedSettlement struct {
	settled  []int
	refunded bool
}

func (s *recordedSettlement) Settle(quota int) error {
	s.settled = append(s.settled, quota)
	return nil
}
func (s *recordedSettlement) Refund(*gin.Context)    { s.refunded = true }
func (*recordedSettlement) NeedsRefund() bool        { return false }
func (*recordedSettlement) GetPreConsumedQuota() int { return 0 }
func (*recordedSettlement) Reserve(int) error        { return nil }

func (*recordedSettlement) ReserveBeforeSubmit(int) error { return nil }

// /v1/tools/search and /v1/tools/fetch follow /v1/alpha/search: the raw body
// reaches /v1/tools/* unchanged and the response keeps its JSON values. With only
// the built-in prices, a response DeepSeek bills (results, page content)
// settles one call at the routing name's request price and no tokens, an
// empty one releases the reservation, an upstream error keeps its status, and
// a channel that cannot relay the endpoint fails retryably without sending
// anything.
func TestDeepSeekToolsHelper(t *testing.T) {
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)
	batch, logConsume := common.BatchUpdateEnabled, common.LogConsumeEnabled
	common.BatchUpdateEnabled, common.LogConsumeEnabled = true, false
	t.Cleanup(func() { common.BatchUpdateEnabled, common.LogConsumeEnabled = batch, logConsume })

	const search, fetch = "/v1/tools/search", "/v1/tools/fetch"
	const results = `{"results":[{"title":"DeepSeek","url":"https://deepseek.com/","rerank_score":2.1}]}`
	tests := []struct {
		name, path, body, response string
		channelType, status        int
		settled                    []int
		refunded                   bool
		invalidResponse            bool
	}{
		{name: "search with results", path: search, body: `{"query":"deepseek","count":2,"future_field":{"nested":true}}`,
			response: results, channelType: constant.ChannelTypeDeepSeek, status: http.StatusOK, settled: []int{375}},
		{name: "search without results", path: search, body: `{"query":"site:none.invalid deepseek"}`,
			response: `{"results":[]}`, channelType: constant.ChannelTypeDeepSeek, status: http.StatusOK, refunded: true},
		{name: "fetch with page content", path: fetch, body: `{"url":"https://example.com/"}`,
			response: `{"title":"Example Domain","content":"# Example Domain"}`, channelType: constant.ChannelTypeDeepSeek, status: http.StatusOK, settled: []int{375}},
		{name: "queued search is free", path: search, body: `{"query":"deepseek"}`,
			response: results, channelType: constant.ChannelTypeDeepSeek, status: http.StatusAccepted, refunded: true},
		{name: "invalid search response is free", path: search, body: `{"query":"deepseek"}`,
			response: results + "truncated", channelType: constant.ChannelTypeDeepSeek, status: http.StatusOK, refunded: true, invalidResponse: true},
		{name: "fetch without readable content is free", path: fetch, body: `{"url":"https://example.com/"}`,
			response: `{"content":"   "}`, channelType: constant.ChannelTypeDeepSeek, status: http.StatusOK, refunded: true},
		{name: "fetch with non-text content is free", path: fetch, body: `{"url":"https://example.com/"}`,
			response: `{"content":{"error":"no page"}}`, channelType: constant.ChannelTypeDeepSeek, status: http.StatusOK, refunded: true},
		{name: "fetch failure keeps its status", path: fetch, body: `{"url":"https://example.com/missing"}`,
			response:    `{"error":{"message":"failed to fetch URL","type":"not_found_error","param":null,"code":"invalid_request_error"}}`,
			channelType: constant.ChannelTypeDeepSeek, status: http.StatusNotFound},
		{name: "search through a New API channel", path: search, body: `{"query":"deepseek"}`,
			response: results, channelType: constant.ChannelTypeNewAPI, status: http.StatusOK, settled: []int{375}},
		{name: "channel without the endpoint", path: search, body: `{"query":"deepseek"}`, channelType: constant.ChannelTypeOpenAI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
			}))
			t.Cleanup(upstream.Close)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			routingName := map[string]string{search: constant.DeepSeekSearchModel, fetch: constant.DeepSeekFetchModel}[tc.path]
			common.SetContextKey(c, constant.ContextKeyOriginalModel, routingName)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelKey, "sk-test")

			request, err := helper.GetAndValidateRequest(c, types.RelayFormatDeepSeekTools)
			require.NoError(t, err)
			info, err := relaycommon.GenRelayInfo(c, types.RelayFormatDeepSeekTools, request, nil)
			require.NoError(t, err)
			info.BillingRequestInput = &billingexpr.RequestInput{Body: []byte(tc.body)}
			_, err = helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
			require.NoError(t, err, "the routing name is priced out of the box")
			settlement := &recordedSettlement{}
			info.Billing = settlement

			apiErr := DeepSeekToolsHelper(c, info)
			if tc.status == 0 {
				require.NotNil(t, apiErr)
				assert.False(t, types.IsSkipRetryError(apiErr))
				assert.Empty(t, gotPath)
				return
			}
			assert.Equal(t, tc.path, gotPath)
			assert.Equal(t, tc.body, string(gotBody))
			if tc.invalidResponse {
				// Invalid provider JSON must not bypass the identifier privacy
				// boundary. Exercise the controller's final-error refund path.
				require.NotNil(t, apiErr)
				assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
				assert.Equal(t, types.ErrorCodeReadResponseBodyFailed, apiErr.GetErrorCode())
				assert.True(t, types.IsSkipRetryError(apiErr))
				assert.Empty(t, recorder.Body.String())
				require.NotNil(t, RefundFailedRequestBilling(c, info, apiErr))
			}
			assert.Equal(t, tc.settled, settlement.settled)
			assert.Equal(t, tc.refunded, settlement.refunded)
			if tc.invalidResponse {
				return
			}
			if tc.status < 200 || tc.status >= 300 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				return
			}
			require.Nil(t, apiErr)
			assert.Equal(t, tc.status, recorder.Code)
			assert.JSONEq(t, tc.response, recorder.Body.String())
		})
	}
}

func TestDeepSeekTTSValidation(t *testing.T) {
	for _, test := range []struct {
		name, body, query string
		ws, wantError     bool
		count             int
	}{
		{name: "weighted Unicode text", body: `{"text":"你好🙂a","filter_markdown":false}`, count: 6},
		{name: "maximum text", body: `{"text":"` + strings.Repeat("a", 10000) + `"}`, count: 10000},
		{name: "oversized text", body: `{"text":"` + strings.Repeat("中", 5001) + `"}`, wantError: true},
		{name: "empty text", body: `{"text":" "}`, wantError: true},
		{name: "hidden parameter", body: `{"text":"hi","n":2}`, wantError: true},
		{name: "HTTP opus unsupported", body: `{"text":"hi","format":"opus"}`, wantError: true},
		{name: "WS reserves maximum", ws: true, query: "?format=opus&filter_markdown=false", count: 10000},
		{name: "WS WAV unsupported", ws: true, query: "?format=wav", wantError: true},
		{name: "query credentials rejected", ws: true, query: "?key=secret", wantError: true},
		{name: "duplicate parameter rejected", ws: true, query: "?format=pcm&format=opus", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			method := http.MethodPost
			if test.ws {
				method = http.MethodGet
			}
			c.Request = httptest.NewRequest(method, "/v1/tools/tts"+test.query, strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")
			if test.ws {
				c.Request.Header.Set("Connection", "Upgrade")
				c.Request.Header.Set("Upgrade", "websocket")
			}
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			request, err := helper.GetAndValidateRequest(c, types.RelayFormatDeepSeekTools)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tts := request.(*dto.DeepSeekTTSRequest)
			unit, count := tts.GetMeteredUsage()
			assert.Equal(t, "character", unit)
			assert.Equal(t, test.count, count)
			if strings.Contains(test.body+test.query, "false") {
				require.NotNil(t, tts.FilterMarkdown)
				assert.False(t, *tts.FilterMarkdown)
			}
		})
	}
}

func TestDeepSeekTTSHTTPSettlement(t *testing.T) {
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	batch, logging := common.BatchUpdateEnabled, common.LogConsumeEnabled
	common.BatchUpdateEnabled, common.LogConsumeEnabled = true, false
	t.Cleanup(func() { common.BatchUpdateEnabled, common.LogConsumeEnabled = batch, logging })
	for _, test := range []struct {
		name, contentType, response string
		status                      int
		truncated, wantError        bool
	}{
		{name: "success", contentType: "audio/wav", response: "RIFFfixture", status: 200},
		{name: "empty audio", contentType: "audio/wav", status: 200, wantError: true},
		{name: "interrupted audio", contentType: "audio/wav", response: "partial", status: 200, truncated: true, wantError: true},
		{name: "JSON failure", contentType: "application/json", response: `{"error":{"type":"api_error","message":"failed"}}`, status: 503, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var received dto.DeepSeekTTSRequest
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/tools/tts", r.URL.Path)
				assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
				assert.NoError(t, common.DecodeJson(r.Body, &received))
				w.Header().Set("Content-Type", test.contentType)
				w.Header().Set("X-Ds-Request-Id", "private-upstream-id")
				if test.truncated {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			}))
			t.Cleanup(upstream.Close)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/tools/tts", strings.NewReader(`{"text":"**你好**","filter_markdown":false}`))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			common.SetContextKey(c, constant.ContextKeyOriginalModel, constant.DeepSeekTTSModel)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelKey, "sk-upstream")
			c.Set(common.RequestIdKey, "local-tts-id")
			request, err := helper.GetAndValidateRequest(c, types.RelayFormatDeepSeekTools)
			require.NoError(t, err)
			info, err := relaycommon.GenRelayInfo(c, types.RelayFormatDeepSeekTools, request, nil)
			require.NoError(t, err)
			_, err = helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
			require.NoError(t, err)
			settlement := &recordedSettlement{}
			info.Billing = settlement
			apiErr := DeepSeekTTSHelper(c, info)
			assert.Equal(t, "**你好**", received.Text)
			require.NotNil(t, received.FilterMarkdown)
			assert.False(t, *received.FilterMarkdown)
			assert.Empty(t, recorder.Header().Get("X-Ds-Request-Id"))
			if test.wantError {
				require.NotNil(t, apiErr)
				assert.True(t, types.IsSkipRetryError(apiErr))
				assert.Empty(t, settlement.settled)
				assert.True(t, c.GetBool("deepseek_tts_cost_unconfirmed"))
				require.NotNil(t, RefundFailedRequestBilling(c, info, apiErr))
				assert.True(t, settlement.refunded)
				return
			}
			require.Nil(t, apiErr)
			assert.Equal(t, test.response, recorder.Body.String())
			assert.Equal(t, []int{90}, settlement.settled)
			assert.Equal(t, 8, info.BillingMeteredUsage.Count)
			assert.Equal(t, "request_text", c.GetString("deepseek_tts_usage_basis"))
			assert.False(t, c.GetBool("deepseek_tts_cost_unconfirmed"))
		})
	}
}

func TestDeepSeekTTSWebSocketSettlement(t *testing.T) {
	require.NoError(t, i18n.Init())
	batch, logging := common.BatchUpdateEnabled, common.LogConsumeEnabled
	common.BatchUpdateEnabled, common.LogConsumeEnabled = true, false
	t.Cleanup(func() { common.BatchUpdateEnabled, common.LogConsumeEnabled = batch, logging })
	for _, test := range []struct {
		name, text, terminal string
		wantError            bool
		unfinishedInput      bool
	}{
		{name: "success", text: "你好🙂a", terminal: "success"},
		{name: "upstream partial failure", text: "hello", terminal: "service_error", wantError: true},
		{name: "upstream disconnect", text: "hello", wantError: true},
		{name: "premature success", text: "hello", terminal: "success", unfinishedInput: true, wantError: true},
		{name: "text bound", text: strings.Repeat("中", 5001), terminal: "success", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{Subprotocols: []string{"tts"}}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/tools/tts", r.URL.Path)
				assert.Equal(t, "false", r.URL.Query().Get("filter_markdown"))
				assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("Cookie"))
				assert.NotContains(t, r.Header.Get("Sec-WebSocket-Protocol"), "local-secret")
				conn, err := upgrader.Upgrade(w, r, nil)
				if !assert.NoError(t, err) {
					return
				}
				defer conn.Close()
				for {
					_, body, err := conn.ReadMessage()
					if err != nil {
						return
					}
					var event struct {
						Event string `json:"event"`
					}
					if !assert.NoError(t, common.Unmarshal(body, &event)) {
						return
					}
					if event.Event == "finish_text" || test.unfinishedInput {
						break
					}
				}
				_ = conn.WriteMessage(websocket.BinaryMessage, []byte("pcm-fixture"))
				if test.terminal != "" {
					body, _ := common.Marshal(map[string]any{"event": "finish", "code": test.terminal, "message": "private-provider-id"})
					_ = conn.WriteMessage(websocket.TextMessage, body)
				}
			}))
			t.Cleanup(upstream.Close)
			type result struct {
				err     *types.NewAPIError
				counts  []int
				usage   *billingexpr.MeteredUsage
				unknown bool
			}
			done := make(chan result, 1)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, _ := gin.CreateTestContext(w)
				c.Request = r
				defer common.CleanupBodyStorage(c)
				common.SetContextKey(c, constant.ContextKeyOriginalModel, constant.DeepSeekTTSModel)
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
				common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
				common.SetContextKey(c, constant.ContextKeyChannelKey, "sk-upstream")
				request, err := helper.GetAndValidateRequest(c, types.RelayFormatDeepSeekTools)
				if !assert.NoError(t, err) {
					return
				}
				info, err := relaycommon.GenRelayInfo(c, types.RelayFormatDeepSeekTools, request, nil)
				if !assert.NoError(t, err) {
					return
				}
				_, err = helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
				if !assert.NoError(t, err) {
					return
				}
				assert.Equal(t, 10000, info.TieredBillingSnapshot.EstimatedMeteredUsage.Count)
				conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
				if !assert.NoError(t, err) {
					return
				}
				defer conn.Close()
				info.ClientWs = conn
				settlement := &recordedSettlement{}
				info.Billing = settlement
				apiErr := DeepSeekTTSHelper(c, info)
				done <- result{apiErr, settlement.settled, info.BillingMeteredUsage, c.GetBool("deepseek_tts_cost_unconfirmed")}
			}))
			t.Cleanup(gateway.Close)
			dialer := websocket.Dialer{Subprotocols: []string{"tts", "bearer.local-secret"}}
			conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/tools/tts?format=pcm&filter_markdown=false", http.Header{"Cookie": []string{"secret-session"}})
			require.NoError(t, err)
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			payload, err := common.Marshal(map[string]any{"event": "text", "text": test.text})
			require.NoError(t, err)
			require.NoError(t, conn.WriteMessage(websocket.TextMessage, payload))
			if !test.unfinishedInput {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"finish_text"}`))
			}
			var terminal string
			for {
				kind, body, err := conn.ReadMessage()
				if err != nil {
					break
				}
				if kind == websocket.TextMessage {
					terminal = string(body)
					break
				}
			}
			select {
			case got := <-done:
				if test.wantError {
					require.NotNil(t, got.err)
					assert.True(t, types.IsSkipRetryError(got.err))
					assert.Empty(t, got.counts)
					assert.True(t, got.unknown)
					return
				}
				require.Nil(t, got.err)
				require.NotNil(t, got.usage)
				assert.Equal(t, 6, got.usage.Count)
				assert.Equal(t, []int{68}, got.counts)
				assert.False(t, got.unknown)
				assert.Contains(t, terminal, `"code":"success"`)
				assert.NotContains(t, terminal, "private-provider-id")
			case <-time.After(5 * time.Second):
				t.Fatal("TTS session did not terminate")
			}
		})
	}
}
