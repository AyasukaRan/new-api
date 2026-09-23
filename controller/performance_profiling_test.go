package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	profile "github.com/google/pprof/profile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func profilingTestRequest(t *testing.T, handler gin.HandlerFunc, payload string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/performance/profiling/query", strings.NewReader(payload))
	handler(c)
	return response
}

func TestProfilingRequiresRoot(t *testing.T) {
	_, _ = setupAccessTokenAudit(t)
	t.Setenv("PYROSCOPE_URL", "")
	t.Setenv("ENABLE_PPROF", "")
	router := gin.New()
	group := router.Group("/profiling", middleware.RootAuth(), middleware.DisableCache())
	group.GET("/status", GetProfilingStatus)
	group.POST("/query", QueryProfiling)
	group.POST("/capture", CaptureProfiling)
	group.GET("/profiles/:id", DownloadProfiling)
	for _, tc := range []struct {
		name     string
		role     int
		expected int
	}{
		{"anonymous", 0, http.StatusUnauthorized},
		{"user", common.RoleCommonUser, http.StatusForbidden},
		{"admin", common.RoleAdminUser, http.StatusForbidden},
		{"root", common.RoleRootUser, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := "profiling-test-" + tc.name
			if tc.role != 0 {
				user := model.User{Username: tc.name, Role: tc.role, Status: common.UserStatusEnabled, AccessToken: &token, AffCode: tc.name, AuthVersion: 1}
				require.NoError(t, model.DB.Create(&user).Error)
			}
			for _, endpoint := range []struct {
				method, path string
				rootStatus   int
			}{
				{"GET", "/profiling/status", http.StatusOK}, {"POST", "/profiling/query", http.StatusBadRequest},
				{"POST", "/profiling/capture", http.StatusServiceUnavailable}, {"GET", "/profiling/profiles/missing", http.StatusNotFound},
			} {
				request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`))
				if tc.role != 0 {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				expected := tc.expected
				if tc.role == common.RoleRootUser {
					expected = endpoint.rootStatus
				}
				assert.Equal(t, expected, response.Code, response.Body.String())
			}
		})
	}
}

func TestProfilingQueryUsesFixedApplicationAndDecodesFlamegraph(t *testing.T) {
	now := time.Now().UnixMilli()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		assert.Equal(t, `{service_name="gateway"}`, body["labelSelector"])
		assert.Equal(t, "process_cpu:cpu:nanoseconds:cpu:nanoseconds", body["profileTypeID"])
		user, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "collector", user)
		assert.Equal(t, "private-password", password)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/querier.v1.QuerierService/SelectMergeStacktraces":
			_, _ = io.WriteString(w, `{"flamegraph":{"names":["total","main","left","right"],"levels":[{"values":["0","100","0","0"]},{"values":["0","100","10","1"]},{"values":["0","30","30","2","0","60","60","3"]}],"total":"100"}}`)
		case "/querier.v1.QuerierService/SelectSeries":
			data, err := common.Marshal(map[string]any{"series": []any{map[string]any{"points": []any{map[string]any{"timestamp": now - 1000, "value": 100}}}}})
			require.NoError(t, err)
			_, _ = w.Write(data)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("PYROSCOPE_URL", server.URL)
	t.Setenv("PYROSCOPE_APP_NAME", "gateway")
	t.Setenv("PYROSCOPE_BASIC_AUTH_USER", "collector")
	t.Setenv("PYROSCOPE_BASIC_AUTH_PASSWORD", "private-password")
	payload, err := common.Marshal(map[string]any{"profile_type": "process_cpu:cpu:nanoseconds:cpu:nanoseconds", "start": now - 60000, "end": now, "labelSelector": `{service_name="other"}`})
	require.NoError(t, err)
	response := profilingTestRequest(t, QueryProfiling, string(payload))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result struct {
		Data profilingResult `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.Equal(t, 2, calls)
	assert.Equal(t, int64(100), result.Data.Total)
	require.Len(t, result.Data.Flamegraph, 4)
	assert.Equal(t, int64(30), result.Data.Flamegraph[3].Start)
	assert.Equal(t, "right", result.Data.Hotspots[0].Name)
	require.Len(t, result.Data.Timeline, 1)
	assert.Equal(t, now-1000, result.Data.Timeline[0].Timestamp)
	assert.NotContains(t, response.Body.String(), "private-password")
	assert.NotContains(t, response.Body.String(), server.URL)

	for _, bad := range []string{
		`{"profile_type":"cpu{service_name=other}","start":1,"end":2}`,
		`{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","start":1,"end":90000000}`,
		`{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","start":2,"end":1}`,
	} {
		assert.Equal(t, http.StatusBadRequest, profilingTestRequest(t, QueryProfiling, bad).Code)
	}
	assert.Equal(t, 2, calls, "invalid requests must not reach the profiling service")
}

func TestProfilingServiceLimitsAndSanitizesFailures(t *testing.T) {
	for _, mode := range []string{"failure", "redirect", "oversize", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "failure":
					w.WriteHeader(500)
					_, _ = io.WriteString(w, "private-service-error")
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.1:1/private", http.StatusFound)
				case "oversize":
					_, _ = io.CopyN(w, strings.NewReader(strings.Repeat("x", profilingByteLimit+1)), profilingByteLimit+1)
				case "malformed":
					_, _ = io.WriteString(w, `{"broken"`)
				}
			}))
			defer server.Close()
			t.Setenv("PYROSCOPE_URL", server.URL)
			err := profilingRPC(context.Background(), "ProfileTypes", map[string]any{}, &map[string]any{})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private")
			assert.NotContains(t, err.Error(), server.URL)
		})
	}
}

func TestProfilingPprofTreeCaptureCancellationAndDownload(t *testing.T) {
	outer := &profile.Function{ID: 1, Name: "outer"}
	inner := &profile.Function{ID: 2, Name: "inner"}
	location1 := &profile.Location{ID: 1, Line: []profile.Line{{Function: outer}}}
	location2 := &profile.Location{ID: 2, Line: []profile.Line{{Function: inner}}}
	parsed := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "inuse_space", Unit: "bytes"}},
		Sample:     []*profile.Sample{{Location: []*profile.Location{location2, location1}, Value: []int64{20}}, {Location: []*profile.Location{location1}, Value: []int64{10}}},
	}
	result := profilingFromPprof(parsed, "heap")
	assert.Equal(t, int64(30), result.Total)
	require.Len(t, result.Flamegraph, 3)
	assert.Equal(t, "outer", result.Flamegraph[1].Name)
	assert.Equal(t, int64(10), result.Flamegraph[1].Self)
	assert.Equal(t, int64(20), result.Flamegraph[2].Self)
	recursive := profilingHotspots([]profilingFrame{
		{Name: "recursive", Depth: 1, Start: 0, Total: 30, Self: 10},
		{Name: "recursive", Depth: 2, Start: 0, Total: 20, Self: 20},
	})
	require.Len(t, recursive, 1)
	assert.Equal(t, int64(30), recursive[0].Total, "recursive calls must not double-count cumulative cost")

	t.Setenv("ENABLE_PPROF", "true")
	profilingCaptureLock.Lock()
	response := profilingTestRequest(t, CaptureProfiling, `{"profile_type":"heap"}`)
	profilingCaptureLock.Unlock()
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.Equal(t, http.StatusBadRequest, profilingTestRequest(t, CaptureProfiling, `{"profile_type":"cmdline"}`).Code)
	assert.Equal(t, http.StatusBadRequest, profilingTestRequest(t, CaptureProfiling, `{"profile_type":"cpu","seconds":16}`).Code)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest("POST", "/capture", strings.NewReader(`{"profile_type":"cpu","seconds":1}`)).WithContext(ctx)
	CaptureProfiling(c)
	require.True(t, profilingCaptureLock.TryLock(), "cancelled CPU capture must release the capture lock")
	profilingCaptureLock.Unlock()
	var otherCPU bytes.Buffer
	require.NoError(t, pprof.StartCPUProfile(&otherCPU), "cancelled capture must stop the runtime CPU profiler")
	response = profilingTestRequest(t, CaptureProfiling, `{"profile_type":"cpu","seconds":1}`)
	pprof.StopCPUProfile()
	assert.Equal(t, http.StatusConflict, response.Code, "a running profiler must never be interrupted by instant capture")

	response = profilingTestRequest(t, CaptureProfiling, `{"profile_type":"goroutine"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var captured struct {
		Data profilingResult `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &captured))
	require.NotEmpty(t, captured.Data.DownloadID)
	response = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(response)
	c.Params = gin.Params{{Key: "id", Value: captured.Data.DownloadID}}
	DownloadProfiling(c)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.Contains(t, response.Header().Get("Content-Disposition"), "attachment;")
	_, err := profile.Parse(bytes.NewReader(response.Body.Bytes()))
	require.NoError(t, err)

	profilingDownloads.Lock()
	entry := profilingDownloads.items[captured.Data.DownloadID]
	entry.expires = time.Now().Add(-time.Minute)
	profilingDownloads.items[captured.Data.DownloadID] = entry
	profilingDownloads.Unlock()
	response = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(response)
	c.Params = gin.Params{{Key: "id", Value: captured.Data.DownloadID}}
	DownloadProfiling(c)
	assert.Equal(t, http.StatusNotFound, response.Code)
	for range 5 {
		response = profilingTestRequest(t, CaptureProfiling, `{"profile_type":"goroutine"}`)
		require.Equal(t, http.StatusOK, response.Code)
	}
	profilingDownloads.Lock()
	assert.Len(t, profilingDownloads.items, 4, "download storage must evict the oldest capture")
	clear(profilingDownloads.items)
	profilingDownloads.Unlock()
}
