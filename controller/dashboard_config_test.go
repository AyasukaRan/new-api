package controller

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dashboardFixture() *dto.DashboardConfig {
	panel := dto.DashboardPanel{ID: "panel-one", Title: "Requests", Source: "usage", Metric: "requests", Chart: "line", Dimension: "model"}
	panel.Layout.W, panel.Layout.H = 6, 4
	return &dto.DashboardConfig{Version: 1, Active: "board-one", Boards: []dto.PersonalDashboard{{ID: "board-one", Name: "My board", Time: dto.DashboardTime{Range: "24h", Granularity: "minute", Refresh: 5}, Panels: []dto.DashboardPanel{panel}}}}
}

func TestPersonalDashboardDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ name, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.name, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			db := modelManagementDB(t, dialect.name, os.Getenv(dialect.env))
			user := model.User{Username: "board-user", AffCode: "bd01", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 13579, Setting: `{"language":"en","future_setting":{"keep":true}}`}
			other := model.User{Username: "board-other", AffCode: "bd02", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Setting: `{"language":"fr"}`}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Create(&other).Error)
			route := gin.New()
			route.Use(func(c *gin.Context) { c.Set("id", user.Id); c.Set("role", common.RoleRootUser) })
			route.GET("/self/dashboards", GetPersonalDashboard)
			route.PUT("/self/dashboards", SavePersonalDashboard)
			route.PUT("/self", UpdateSelf)
			route.PUT("/preferences", UpdateUserSetting)
			request := func(method, path string, body []byte) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				route.ServeHTTP(w, r)
				return w
			}
			decode := func(w *httptest.ResponseRecorder) dto.DashboardConfigResponse {
				var result struct {
					Success bool                        `json:"success"`
					Data    dto.DashboardConfigResponse `json:"data"`
				}
				require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
				require.True(t, result.Success, w.Body.String())
				return result.Data
			}
			initial := decode(request(http.MethodGet, "/self/dashboards?user_id="+fmt.Sprint(other.Id), nil))
			assert.Zero(t, initial.Revision)
			assert.Nil(t, initial.Config)
			config := dashboardFixture()
			payload, err := common.Marshal(dto.DashboardConfigResponse{Config: config})
			require.NoError(t, err)
			t.Run("bridge refuses writes while legacy master exists", func(t *testing.T) {
				t.Setenv("DASHBOARD_CONFIG_READ_ONLY", "true")
				assert.Equal(t, http.StatusServiceUnavailable, request(http.MethodPut, "/self/dashboards", payload).Code)
				assert.Equal(t, initial, decode(request(http.MethodGet, "/self/dashboards", nil)))
			})
			saved := decode(request(http.MethodPut, "/self/dashboards", payload))
			assert.EqualValues(t, 1, saved.Revision)
			assert.Equal(t, config, saved.Config)
			assert.Equal(t, saved, decode(request(http.MethodGet, "/self/dashboards", nil)))
			assert.Equal(t, http.StatusConflict, request(http.MethodPut, "/self/dashboards", payload).Code)
			var current model.User
			require.NoError(t, db.First(&current, user.Id).Error)
			assert.Contains(t, current.Setting, `"future_setting":{"keep":true}`)
			assert.Contains(t, current.Setting, `"language":"en"`)
			assert.Equal(t, 13579, current.Quota)
			require.NoError(t, db.First(&other, other.Id).Error)
			assert.Equal(t, `{"language":"fr"}`, other.Setting)
			// An older preference form must preserve the independent dashboard revision.
			for _, preference := range []struct{ path, body string }{{"/self", `{"language":"zh"}`}, {"/preferences", `{"notify_type":"email","notification_email":"notice@example.test","quota_warning_threshold":20}`}} {
				response := request(http.MethodPut, preference.path, []byte(preference.body))
				var result struct {
					Success bool `json:"success"`
				}
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				require.True(t, result.Success, response.Body.String())
			}
			afterPreferences, err := model.GetPersonalDashboard(user.Id)
			require.NoError(t, err)
			assert.Equal(t, saved, afterPreferences)
			require.NoError(t, db.First(&current, user.Id).Error)
			assert.Equal(t, "zh", current.GetSetting().Language)
			assert.Equal(t, "notice@example.test", current.GetSetting().NotificationEmail)
			// A pre-dashboard User snapshot used for a profile update cannot own settings.
			stale := model.User{Id: user.Id, DisplayName: "Renamed", Setting: user.Setting}
			require.NoError(t, stale.Update(false))
			afterProfile, err := model.GetPersonalDashboard(user.Id)
			require.NoError(t, err)
			assert.Equal(t, saved, afterProfile)
			require.NoError(t, db.First(&current, user.Id).Error)
			assert.Equal(t, "zh", current.GetSetting().Language)
			assert.Equal(t, "notice@example.test", current.GetSetting().NotificationEmail)

			start := make(chan struct{})
			results := make(chan error, 2)
			for range 2 {
				go func() { <-start; _, e := model.SavePersonalDashboard(user.Id, 1, config); results <- e }()
			}
			close(start)
			successes, conflicts := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					successes++
				} else if errors.Is(err, model.ErrDashboardRevisionConflict) {
					conflicts++
				} else {
					require.NoError(t, err)
				}
			}
			assert.Equal(t, 1, successes)
			assert.Equal(t, 1, conflicts)
			concurrent, err := model.GetPersonalDashboard(user.Id)
			require.NoError(t, err)
			assert.EqualValues(t, 2, concurrent.Revision)

			for _, invalid := range []string{`{"revision":2}`, `{"revision":null,"config":null}`, `{"revision":2.5,"config":null}`, `{"revision":2,"config":null,"user_id":` + fmt.Sprint(other.Id) + `}`, strings.Repeat(" ", dto.MaxDashboardConfigBytes) + `{}`} {
				assert.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/self/dashboards", []byte(invalid)).Code)
			}
			for _, mutate := range []func(*dto.DashboardConfig){
				func(c *dto.DashboardConfig) { c.Boards[0].Panels[0].Source = "live" },
				func(c *dto.DashboardConfig) { c.Boards[0].Panels[0].Metric = "profit" },
				func(c *dto.DashboardConfig) {
					c.Boards[0].Panels[0].Source = "sources"
					c.Boards[0].Panels[0].Dimension = "client"
				},
				func(c *dto.DashboardConfig) { c.Boards[0].Panels[0].Filters.Username = "other" },
				func(c *dto.DashboardConfig) { c.Boards[0].Panels[0].Layout.X = 11 },
				func(c *dto.DashboardConfig) { c.Boards[0].Panels[0].Layout.Y = 4097 },
				func(c *dto.DashboardConfig) { c.Boards[0].Panels = append(c.Boards[0].Panels, c.Boards[0].Panels[0]) },
				func(c *dto.DashboardConfig) {
					c.Boards[0].Panels[0].Time = &dto.DashboardTime{Range: "15m", Granularity: "minute", Refresh: 5}
				},
			} {
				invalid := dashboardFixture()
				mutate(invalid)
				b, err := common.Marshal(dto.DashboardConfigResponse{Revision: 2, Config: invalid})
				require.NoError(t, err)
				assert.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/self/dashboards", b).Code)
			}
			// A stale privileged context cannot bypass the authoritative stored role.
			afterInvalid, err := model.GetPersonalDashboard(user.Id)
			require.NoError(t, err)
			assert.Equal(t, concurrent, afterInvalid)
			require.NoError(t, db.Model(&user).Update("role", common.RoleAdminUser).Error)
			admin := dashboardFixture()
			admin.Boards[0].Panels[0].Filters.Username = "selected-user"
			privileged := admin.Boards[0].Panels[0]
			privileged.ID = "admin-profit"
			privileged.Metric = "profit"
			admin.Boards[0].Panels = append(admin.Boards[0].Panels, privileged)
			adminSaved, err := model.SavePersonalDashboard(user.Id, 2, admin)
			require.NoError(t, err)
			require.Len(t, adminSaved.Config.Boards[0].Panels, 2)
			require.NoError(t, db.First(&current, user.Id).Error)
			raw := current.Setting
			require.NoError(t, db.Model(&user).Update("role", common.RoleCommonUser).Error)
			filtered := decode(request(http.MethodGet, "/self/dashboards", nil))
			require.Len(t, filtered.Config.Boards[0].Panels, 1)
			assert.Empty(t, filtered.Config.Boards[0].Panels[0].Filters.Username)
			require.NoError(t, db.First(&current, user.Id).Error)
			assert.Equal(t, raw, current.Setting, "GET must not rewrite saved admin configuration")
			reset, err := model.SavePersonalDashboard(user.Id, 3, nil)
			require.NoError(t, err)
			assert.Nil(t, reset.Config)
			assert.EqualValues(t, 4, reset.Revision)
		})
	}
}

func TestDashboardConfigCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, source, metric, dimension, chart string
		valid                                  bool
	}{
		{"usage cache", "usage", "cache_read", "model", "area", true},
		{"client requests", "sources", "requests", "client", "bar", true},
		{"client cache unsupported", "sources", "cache_read", "client", "stat", false},
		{"live concurrency total", "live", "concurrency", "total", "stat", true},
		{"live concurrency model unsupported", "live", "concurrency", "model", "line", false},
		{"live latency pie unsupported", "live", "latency", "total", "pie", false},
		{"live requests pie unsupported", "live", "requests", "model", "pie", false},
		{"profit pie unsupported", "usage", "profit", "model", "pie", false},
		{"unknown metric", "usage", "unknown", "model", "line", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dashboardFixture()
			p := &c.Boards[0].Panels[0]
			p.Source, p.Metric, p.Dimension, p.Chart = tc.source, tc.metric, tc.dimension, tc.chart
			assert.Equal(t, tc.valid, c.Validate(true) == nil)
		})
	}
	c := dashboardFixture()
	p := &c.Boards[0].Panels[0]
	p.Source, p.Metric, p.Dimension = "live", "concurrency", "total"
	p.Filters.Model = "specific-model"
	assert.ErrorIs(t, c.Validate(true), dto.ErrDashboardConfigInvalid)
	p.Metric = "requests"
	p.Filters.Model = strings.Repeat("x", 129)
	assert.ErrorIs(t, c.Validate(true), dto.ErrDashboardConfigInvalid)
}

func TestLiveDashboardRequestAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path   string
		role, status int
	}{
		{"user denied", "/live", common.RoleCommonUser, http.StatusForbidden},
		{"admin allowed", "/live", common.RoleAdminUser, http.StatusOK},
		{"seconds rejected", "/live?seconds=123", common.RoleAdminUser, http.StatusBadRequest},
		{"step rejected", "/live?step=2", common.RoleAdminUser, http.StatusBadRequest},
		{"fraction rejected", "/live?seconds=900.5", common.RoleAdminUser, http.StatusBadRequest},
		{"long model rejected", "/live?model=" + strings.Repeat("x", 129), common.RoleAdminUser, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/live", func(c *gin.Context) { c.Set("role", tc.role); GetLiveDashboardMetrics(c) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			assert.Equal(t, tc.status, w.Code)
		})
	}
}
