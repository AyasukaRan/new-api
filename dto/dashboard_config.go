package dto

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxDashboardConfigBytes = 64 << 10

var ErrDashboardConfigInvalid = errors.New("Invalid dashboard configuration")

type DashboardTime struct {
	Range       string `json:"range"`
	Granularity string `json:"granularity"`
	Refresh     int    `json:"refresh"`
}

type DashboardPanel struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Metric    string `json:"metric"`
	Chart     string `json:"chart"`
	Dimension string `json:"dimension"`
	Filters   struct {
		Model    string `json:"model,omitempty"`
		Username string `json:"username,omitempty"`
	} `json:"filters"`
	Time   *DashboardTime `json:"time,omitempty"`
	Layout struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	} `json:"layout"`
}

type PersonalDashboard struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Time   DashboardTime    `json:"time"`
	Panels []DashboardPanel `json:"panels"`
}

type DashboardConfig struct {
	Version int                 `json:"version"`
	Active  string              `json:"active"`
	Boards  []PersonalDashboard `json:"boards"`
}

type DashboardConfigResponse struct {
	Revision int64            `json:"revision"`
	Config   *DashboardConfig `json:"config"`
}

func (t DashboardTime) Valid(source string) bool {
	ranges := []string{"15m", "1h", "6h", "24h", "7d", "30d", "today", "yesterday"}
	steps := []string{"auto", "minute", "5m", "hour", "day"}
	refresh := []int{0, 5, 15, 30, 60}
	if source == "live" {
		ranges, steps = []string{"15m", "1h", "6h", "24h"}, []string{"auto", "minute", "5m", "hour"}
	} else if source != "" {
		ranges, steps, refresh = []string{"6h", "24h", "7d", "30d", "today", "yesterday"}, []string{"auto", "hour", "day"}, []int{0, 30, 60}
	}
	return slices.Contains(ranges, t.Range) && slices.Contains(steps, t.Granularity) && slices.Contains(refresh, t.Refresh)
}

func (c *DashboardConfig) Validate(admin bool) error {
	if c == nil {
		return nil
	}
	if c.Version != 1 || len(c.Boards) < 1 || len(c.Boards) > 10 {
		return ErrDashboardConfigInvalid
	}
	boards := make(map[string]bool)
	for _, board := range c.Boards {
		if strings.TrimSpace(board.ID) == "" || len(board.ID) > 64 || boards[board.ID] || strings.TrimSpace(board.Name) == "" || utf8.RuneCountInString(board.Name) > 80 || len(board.Panels) > 24 || !board.Time.Valid("") {
			return ErrDashboardConfigInvalid
		}
		boards[board.ID] = true
		panels := make(map[string]bool)
		for _, panel := range board.Panels {
			if strings.TrimSpace(panel.ID) == "" || len(panel.ID) > 64 || panels[panel.ID] || utf8.RuneCountInString(panel.Title) > 128 || len(panel.Filters.Model) > 192 || len(panel.Filters.Username) > 64 || !slices.Contains([]string{"line", "area", "bar", "pie", "stat", "table"}, panel.Chart) {
				return ErrDashboardConfigInvalid
			}
			panels[panel.ID] = true
			if panel.Layout.X < 0 || panel.Layout.X > 11 || panel.Layout.Y < 0 || panel.Layout.Y > 4096 || panel.Layout.W < 3 || panel.Layout.W > 12 || panel.Layout.H < 3 || panel.Layout.H > 12 || panel.Layout.X+panel.Layout.W > 12 {
				return ErrDashboardConfigInvalid
			}
			if !admin && (panel.Source == "live" || panel.Metric == "upstream_cost" || panel.Metric == "profit" || panel.Filters.Username != "") {
				return ErrDashboardConfigInvalid
			}
			metrics := []string{"requests", "tokens", "cache_read", "cache_write", "quota", "upstream_cost", "profit"}
			dimensions := []string{"model", "total"}
			switch panel.Source {
			case "usage":
			case "sources":
				metrics, dimensions = []string{"requests", "tokens", "quota", "upstream_cost", "profit"}, []string{"client", "total"}
				if panel.Filters.Model != "" || !admin && panel.Dimension == "client" {
					return ErrDashboardConfigInvalid
				}
			case "live":
				metrics = []string{"requests", "success_rate", "latency", "ttft", "tps", "p95", "concurrency"}
				if panel.Filters.Username != "" || utf8.RuneCountInString(panel.Filters.Model) > 128 || panel.Metric == "concurrency" && (panel.Dimension != "total" || panel.Filters.Model != "") || panel.Chart == "pie" {
					return ErrDashboardConfigInvalid
				}
			default:
				return ErrDashboardConfigInvalid
			}
			if !slices.Contains(metrics, panel.Metric) || !slices.Contains(dimensions, panel.Dimension) || panel.Metric == "profit" && panel.Chart == "pie" || panel.Time != nil && !panel.Time.Valid(panel.Source) {
				return ErrDashboardConfigInvalid
			}
		}
	}
	if !boards[c.Active] {
		return ErrDashboardConfigInvalid
	}
	return nil
}

// Restricted panels are removed only from the response after a role downgrade.
func (c *DashboardConfig) ForUser(admin bool) {
	if c == nil || admin {
		return
	}
	for i := range c.Boards {
		panels := make([]DashboardPanel, 0, len(c.Boards[i].Panels))
		for _, panel := range c.Boards[i].Panels {
			if panel.Source == "live" || panel.Metric == "upstream_cost" || panel.Metric == "profit" || panel.Source == "sources" && panel.Dimension == "client" {
				continue
			}
			panel.Filters.Username = ""
			panels = append(panels, panel)
		}
		c.Boards[i].Panels = panels
	}
}
