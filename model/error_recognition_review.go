package model

import (
	"context"
	"encoding/base64"
	"regexp"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type ErrorInventoryCursor struct {
	Snapshot  int64  `json:"s"`
	Days      int    `json:"d"`
	Time      int64  `json:"t"`
	RequestID string `json:"r,omitempty"`
	ID        int    `json:"i,omitempty"`
	Skipped   int    `json:"n,omitempty"`
}
type ErrorInventoryGroup struct {
	Pattern          string `json:"pattern"`
	SuggestedPattern string `json:"suggested_pattern"`
	SuggestedMatch   string `json:"suggested_match"`
	StatusCode       int    `json:"status_code,omitempty"`
	Count            int    `json:"count"`
	LastSeen         int64  `json:"last_seen"`
	Label            string `json:"label"`
	Matched          bool   `json:"matched"`
	RuleID           string `json:"rule_id,omitempty"`
}
type ErrorInventory struct {
	Groups     []ErrorInventoryGroup `json:"groups"`
	Reviewed   int                   `json:"reviewed"`
	NextCursor string                `json:"next_cursor"`
}

var errorTemplatePlaceholders = regexp.MustCompile(`#|\[(?:request-id|redacted(?: credential)?|url|ip|email|identifier)\]|…`)

var errorReviewRedactions = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)\(?request[_ ]id\s*[:=]\s*[^\s)]+\)?`), "[request-id]"},
	{regexp.MustCompile(`(?i)bearer\s+[^\s,;"<>]+`), "Bearer [redacted]"},
	{regexp.MustCompile(`(?i)(?:sk-|sess-|rk-|ghp_|gho_|glpat-)[A-Za-z0-9_-]+`), "[redacted]"},
	{regexp.MustCompile(`(?i)(?:api[_ ]?key|authorization|password|secret|access[_ ]?token)["']?\s*[:=]\s*["']?[^\s,;"<>]+`), "[redacted credential]"},
	{regexp.MustCompile(`https?://[^\s"<>]+`), "[url]"},
	{regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?::[0-9]+)?\b`), "[ip]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[email]"},
	{regexp.MustCompile(`\b[A-Za-z0-9_-]{40,}\b`), "[identifier]"},
	{regexp.MustCompile(`[0-9]+`), "#"},
}

// This inventory reads error records and failed streams from the existing log
// database. It never reads trace payloads or exports metadata, prompts or headers.
func ReviewRequestErrors(ctx context.Context, cursor ErrorInventoryCursor, limit int) (ErrorInventory, bool, error) {
	report := ErrorInventory{Groups: []ErrorInventoryGroup{}}
	filter := LogFilter{Category: "model", Result: "failure", EndTimestamp: cursor.Snapshot}
	if cursor.Days > 0 {
		filter.StartTimestamp = cursor.Snapshot - int64(cursor.Days)*86400
	}
	query, err := filter.apply(LOG_DB.WithContext(ctx))
	if err != nil {
		return report, false, err
	}
	rid := "COALESCE(logs.request_id, '')"
	switch {
	case common.UsingLogDatabase(common.DatabaseTypeMySQL):
		rid = "BINARY " + rid
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		rid += ` COLLATE "C"`
	}
	if cursor.Time > 0 {
		op := " < ?"
		if cursor.Skipped > 0 {
			op = " <= ?"
		}
		query = query.Where("logs.created_at < ? OR (logs.created_at = ? AND ("+rid+" < ? OR ("+rid+" = ? AND logs.id"+op+")))", cursor.Time, cursor.Time, cursor.RequestID, cursor.RequestID, cursor.ID)
	}
	var rows []Log
	err = query.Select("logs.id, logs.request_id, logs.created_at, logs.type, logs.content, logs.other").Order("logs.created_at DESC, " + rid + " DESC, logs.id DESC").Offset(cursor.Skipped).Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return report, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		skipped := 0
		for _, row := range rows {
			if row.CreatedAt == last.CreatedAt && row.RequestId == last.RequestId && row.Id == last.Id {
				skipped++
			}
		}
		if cursor.Time == last.CreatedAt && cursor.RequestID == last.RequestId && cursor.ID == last.Id {
			skipped += cursor.Skipped
		}
		cursor.Time, cursor.RequestID, cursor.ID, cursor.Skipped = last.CreatedAt, last.RequestId, last.Id, skipped
		raw, e := common.Marshal(cursor)
		if e != nil {
			return report, false, e
		}
		report.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}

	type key struct {
		pattern     string
		status      int
		label, rule string
		matched     bool
	}
	groups := map[key]int{}
	for _, row := range rows {
		summary := operation_setting.ClassifyRequestError(row.Content, row.Other)
		text, _ := operation_setting.RequestErrorText(row.Content, row.Other)
		// Long blobs and HTML are not useful rule templates; never export them.
		if strings.Contains(strings.ToLower(text), "<html") || strings.Contains(strings.ToLower(text), "<!doctype") {
			text = "HTML error response"
		}
		// Do not surface reflected JSON request fragments in the inventory.
		if before, _, ok := strings.Cut(text, "{"); ok {
			text = before
		}
		if before, _, ok := strings.Cut(text, "\n"); ok {
			text = before
		}
		for _, redaction := range errorReviewRedactions {
			text = redaction.pattern.ReplaceAllString(text, redaction.replacement)
		}
		text = strings.Join(strings.Fields(text), " ")
		if len(text) > 512 {
			text = string([]rune(text)[:min(len([]rune(text)), 256)]) + "…"
		}
		if text == "" {
			text = "[empty error]"
		}
		k := key{text, summary.StatusCode, summary.Label, summary.RuleID, summary.Matched}
		index, ok := groups[k]
		if !ok {
			index = len(report.Groups)
			groups[k] = index
			suggestion := ""
			if text != "HTML error response" && text != "[empty error]" {
				for _, candidate := range errorTemplatePlaceholders.Split(text, -1) {
					candidate = strings.Trim(candidate, " ,.;:()[]\"'=")
					if candidate == "status_code" || candidate == "status" || candidate == "HTTP" {
						continue
					}
					if len(candidate) > len(suggestion) && len(candidate) >= 8 {
						suggestion = candidate
					}
				}
			}
			report.Groups = append(report.Groups, ErrorInventoryGroup{Pattern: text, SuggestedPattern: suggestion, SuggestedMatch: "contains", StatusCode: summary.StatusCode, Label: summary.Label, RuleID: summary.RuleID, Matched: summary.Matched})
		}
		report.Groups[index].Count++
		report.Groups[index].LastSeen = max(report.Groups[index].LastSeen, row.CreatedAt)
		report.Reviewed++
	}
	sort.SliceStable(report.Groups, func(i, j int) bool {
		if report.Groups[i].Count != report.Groups[j].Count {
			return report.Groups[i].Count > report.Groups[j].Count
		}
		return report.Groups[i].LastSeen > report.Groups[j].LastSeen
	})
	return report, more, nil
}
