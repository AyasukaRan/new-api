package model

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

const logRetryWindow = int64(5 * 60)
const logRetryColumns = "id, user_id, created_at, type, request_id, upstream_request_id, token_id, model_name, quota, prompt_tokens, completion_tokens, use_time"

type logRetryRequest struct {
	user    int
	request string
}
type logRetryProjection struct {
	hidden         []int
	groups         map[int][]*Log
	requestWinners map[logRetryRequest]string
}

// Only failed, unbilled attempts are hidden. Stored logs and accounting are never rewritten.
func (f LogFilter) withRetries(ctx context.Context, ownUserID int) (LogFilter, error) {
	if f.retries != nil || (f.Category != "model" && f.Result == "") || f.Source == LogSourceTest {
		return f, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	projection := &logRetryProjection{groups: make(map[int][]*Log), requestWinners: make(map[logRetryRequest]string)}
	f.retries = projection
	end := f.EndTimestamp
	if end == 0 {
		end = common.GetTimestamp()
	}
	start := f.StartTimestamp
	if start == 0 {
		start = end - operation_setting.GetLogSetting().MaxRangeSeconds()
	}
	// The overlap joins a failure immediately before the selected window to its final result.
	base := LOG_DB.WithContext(ctx).Model(&Log{}).Where("type = ? AND quota = 0 AND prompt_tokens = 0 AND completion_tokens = 0 AND request_id <> '' AND created_at >= ? AND created_at <= ?", LogTypeError, max(int64(0), start-logRetryWindow), end)
	if ownUserID != 0 {
		base = base.Where("user_id = ?", ownUserID)
	}
	var err error
	if base, err = filterLogsByUsername(base, f.Username); err != nil {
		return f, err
	}
	// Other filters apply to the final row, so they must not split a retry chain.
	var failures []*Log
	if err := base.Select(logRetryColumns).Order("created_at, id").Find(&failures).Error; err != nil {
		return f, err
	}
	if len(failures) == 0 {
		return f, nil
	}
	failureIDs := make([]int, 0, len(failures))
	for _, failure := range failures {
		failureIDs = append(failureIDs, failure.Id)
	}
	paid, err := sub2APIPaidFailureIDs(ctx, failureIDs)
	if err != nil {
		return f, err
	}
	failures = slices.DeleteFunc(failures, func(log *Log) bool { return paid[log.Id] })
	if len(failures) == 0 {
		return f, nil
	}
	requests := make(map[logRetryRequest][]*Log)
	requestIDs := make([]string, 0, len(failures))
	for _, row := range failures {
		requestIDs = append(requestIDs, row.RequestId)
	}
	slices.Sort(requestIDs)
	requestIDs = slices.Compact(requestIDs)
	for batch := range slices.Chunk(requestIDs, 300) {
		var rows []*Log
		q := LOG_DB.WithContext(ctx).Model(&Log{}).Select(logRetryColumns).Where("request_id IN ? AND type IN ? AND created_at <= ?", batch, []int{LogTypeConsume, LogTypeError}, end)
		if ownUserID != 0 {
			q = q.Where("user_id = ?", ownUserID)
		}
		if err := q.Find(&rows).Error; err != nil {
			return f, err
		}
		for _, row := range rows {
			key := logRetryRequest{row.UserId, row.RequestId}
			requests[key] = append(requests[key], row)
		}
	}
	// Request traces are fetched only for failed requests and their short explicit-session windows.
	traceByRequest := make(map[logRetryRequest]*RequestTrace)
	for batch := range slices.Chunk(requestIDs, 300) {
		var traces []*RequestTrace
		q := logRetryTraceQuery(ctx).Where("request_id IN ?", batch)
		if ownUserID != 0 {
			q = q.Where("conversation_user_id = ?", ownUserID)
		}
		if err := q.Find(&traces).Error; err != nil {
			return f, err
		}
		for _, trace := range traces {
			key := logRetryRequest{trace.ConversationUserId, trace.RequestId}
			if _, exists := traceByRequest[key]; !exists {
				traceByRequest[key] = trace
			} else {
				traceByRequest[key] = nil
			}
		}
	}
	type sessionWindow struct {
		start int64
	}
	type retrySession struct {
		owner int
		key   string
	}
	sessions := make(map[retrySession]sessionWindow)
	for _, failure := range failures {
		key := logRetryRequest{failure.UserId, failure.RequestId}
		trace := traceByRequest[key]
		if trace == nil || !strings.HasPrefix(trace.ConversationKey, "c_") || logRetryBodyIdentity(trace) == "" {
			continue
		}
		session := retrySession{failure.UserId, trace.ConversationKey}
		window, exists := sessions[session]
		if !exists {
			window = sessionWindow{start: failure.CreatedAt}
		} else {
			window.start = min(window.start, failure.CreatedAt)
		}
		sessions[session] = window
	}
	sessionTraces := make(map[retrySession][]*RequestTrace)
	traceCounts := make(map[logRetryRequest]int)
	for session, window := range sessions {
		var traces []*RequestTrace
		// Trace timestamps are completion times: a retry started within five minutes can finish later.
		// Only metadata is read here; complete bodies are fetched for a qualified successor below.
		if err := LOG_DB.WithContext(ctx).Model(&RequestTrace{}).Select("trace_id, request_id, created_at, conversation_key, conversation_user_id, method, url, truncated, body_encoding, body_size").Where("direction = ? AND conversation_key = ? AND conversation_user_id = ? AND created_at >= ? AND created_at <= ?", TraceDirectionClientRequest, session.key, session.owner, window.start, end).Order("created_at, request_id").Find(&traces).Error; err != nil {
			return f, err
		}
		sessionTraces[session] = traces
		for _, trace := range traces {
			traceCounts[logRetryRequest{trace.ConversationUserId, trace.RequestId}]++
			if _, exists := requests[logRetryRequest{trace.ConversationUserId, trace.RequestId}]; !exists {
				requestIDs = append(requestIDs, trace.RequestId)
			}
		}
	}
	// Only request metadata for potential successors, never all successful payloads.
	slices.Sort(requestIDs)
	requestIDs = slices.Compact(requestIDs)
	for batch := range slices.Chunk(requestIDs, 300) {
		var rows []*Log
		q := LOG_DB.WithContext(ctx).Model(&Log{}).Select(logRetryColumns).Where("request_id IN ? AND type IN ? AND created_at <= ?", batch, []int{LogTypeConsume, LogTypeError}, end)
		if ownUserID != 0 {
			q = q.Where("user_id = ?", ownUserID)
		}
		if err := q.Find(&rows).Error; err != nil {
			return f, err
		}
		for _, row := range rows {
			key := logRetryRequest{row.UserId, row.RequestId}
			if !slices.ContainsFunc(requests[key], func(existing *Log) bool { return existing.Id == row.Id }) {
				requests[key] = append(requests[key], row)
			}
		}
	}
	// Same-second ClickHouse IDs are random, so recover the recorded attempt clock only for ties.
	orderValues := make(map[int]int64)
	ambiguous := make(map[logRetryRequest]bool)
	for key, rows := range requests {
		if slices.ContainsFunc(rows, func(row *Log) bool { return row.Id == 0 }) {
			ambiguous[key] = true
			continue
		}
		consumes := 0
		for _, row := range rows {
			if row.Type == LogTypeConsume {
				consumes++
			}
		}
		if consumes > 1 {
			ambiguous[key] = true
			continue
		}
		if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) || len(rows) < 2 {
			continue
		}
		counts := make(map[int64]int)
		for _, row := range rows {
			counts[row.CreatedAt]++
		}
		var tiedIDs []int
		for _, row := range rows {
			if counts[row.CreatedAt] > 1 {
				tiedIDs = append(tiedIDs, row.Id)
			}
		}
		if len(tiedIDs) == 0 {
			continue
		}
		var tied []*Log
		if err := LOG_DB.WithContext(ctx).Select("id, created_at, other").Where("id IN ? AND user_id = ? AND request_id = ?", tiedIDs, key.user, key.request).Find(&tied).Error; err != nil {
			return f, err
		}
		seenOrder := make(map[[2]int64]bool)
		for _, row := range tied {
			var meta struct {
				Duration int64 `json:"duration_ms"`
				Admin    struct {
					Policy []struct {
						Attempt int
						Elapsed int64 `json:"elapsed_ms"`
					} `json:"request_policy"`
				} `json:"admin_info"`
			}
			if common.UnmarshalJsonStr(row.Other, &meta) != nil {
				ambiguous[key] = true
				break
			}
			order := meta.Duration * 1024
			for _, event := range meta.Admin.Policy {
				order = max(order, event.Elapsed*1024+int64(event.Attempt))
			}
			clock := [2]int64{row.CreatedAt, order}
			if order <= 0 || seenOrder[clock] {
				ambiguous[key] = true
				break
			}
			seenOrder[clock] = true
			orderValues[row.Id] = order
		}
	}
	winners := make(map[logRetryRequest]*Log)
	members := make(map[int][]*Log)
	for key, rows := range requests {
		if ambiguous[key] {
			continue
		}
		slices.SortFunc(rows, func(a, b *Log) int {
			if a.CreatedAt == b.CreatedAt && (orderValues[a.Id] != 0 || orderValues[b.Id] != 0) {
				return cmp.Compare(orderValues[a.Id], orderValues[b.Id])
			}
			return compareRetryLogs(a, b)
		})
		winner := rows[len(rows)-1]
		winners[key] = winner
		group := []*Log{winner}
		for _, row := range rows[:len(rows)-1] {
			if row.Type == LogTypeError && row.Quota == 0 && row.PromptTokens == 0 && row.CompletionTokens == 0 && row.Id != 0 && !paid[row.Id] {
				group = append(group, row)
			}
		}
		members[winner.Id] = group
	}
	successors := make(map[int]int)
	for _, failure := range failures {
		key := logRetryRequest{failure.UserId, failure.RequestId}
		if winners[key] == nil || winners[key].Id != failure.Id {
			continue
		}
		original := traceByRequest[key]
		if original == nil {
			continue
		}
		identity := logRetryBodyIdentity(original)
		if identity == "" || !strings.HasPrefix(original.ConversationKey, "c_") {
			continue
		}
		var candidate *RequestTrace
		var next *Log
		var firstCompleted int64
		uncertain := false
		for _, sibling := range sessionTraces[retrySession{failure.UserId, original.ConversationKey}] {
			if sibling.RequestId == failure.RequestId || sibling.CreatedAt <= failure.CreatedAt {
				continue
			}
			siblingKey := logRetryRequest{failure.UserId, sibling.RequestId}
			row := winners[siblingKey]
			if row == nil || row.Id == 0 || row.UseTime <= 0 || traceCounts[siblingKey] != 1 {
				if firstCompleted == 0 || sibling.CreatedAt <= firstCompleted {
					uncertain = true
					break
				}
				continue
			}
			if firstCompleted == 0 {
				firstCompleted = sibling.CreatedAt
			}
			// Completion order is not request order: an overlapping long request can finish last.
			started := row.CreatedAt - int64(row.UseTime)
			if next == nil || started < next.CreatedAt-int64(next.UseTime) {
				uncertain = next != nil && next.CreatedAt-int64(next.UseTime)-started <= 2
				candidate, next = sibling, row
			} else if started-(next.CreatedAt-int64(next.UseTime)) <= 2 {
				uncertain = true
			}
		}
		if uncertain || next == nil {
			continue
		}
		// The first subsequent request is a barrier, including a changed prompt or parallel request.
		if next.CreatedAt-int64(next.UseTime)-2 < failure.CreatedAt || next.CreatedAt-int64(next.UseTime)-failure.CreatedAt > logRetryWindow || next.TokenId != failure.TokenId || next.ModelName != failure.ModelName {
			continue
		}
		if candidate.Truncated || candidate.Method != original.Method || strings.SplitN(candidate.Url, "?", 2)[0] != strings.SplitN(original.Url, "?", 2)[0] {
			continue
		}
		var complete RequestTrace
		if err := logRetryTraceQuery(ctx).Where("trace_id = ? AND request_id = ? AND conversation_user_id = ?", candidate.TraceId, candidate.RequestId, failure.UserId).Take(&complete).Error; err != nil {
			return f, err
		}
		if logRetryBodyIdentity(&complete) == identity {
			successors[failure.Id] = next.Id
		}
	}
	// Process in chronological order so a failure chain is folded into its final result once.
	ordered := make([]*Log, 0, len(winners))
	for _, winner := range winners {
		ordered = append(ordered, winner)
	}
	slices.SortFunc(ordered, compareRetryLogs)
	for _, winner := range ordered {
		if next, ok := successors[winner.Id]; ok {
			members[next] = append(members[next], members[winner.Id]...)
			delete(members, winner.Id)
		}
	}
	for winnerID, group := range members {
		if len(group) <= 1 {
			continue
		}
		slices.SortFunc(group, func(a, b *Log) int {
			if a.CreatedAt == b.CreatedAt && a.RequestId == b.RequestId && (orderValues[a.Id] != 0 || orderValues[b.Id] != 0) {
				return cmp.Compare(orderValues[a.Id], orderValues[b.Id])
			}
			return compareRetryLogs(a, b)
		})
		for _, member := range group {
			projection.groups[member.Id] = group
			projection.requestWinners[logRetryRequest{member.UserId, member.RequestId}] = group[len(group)-1].RequestId
			if member.Id != winnerID {
				projection.hidden = append(projection.hidden, member.Id)
			}
		}
	}
	if f.UpstreamRequestId != "" {
		for _, group := range projection.groups {
			for _, member := range group {
				if member.UpstreamRequestId == f.UpstreamRequestId && (f.RequestId == "" || f.RequestId == member.RequestId) {
					f.RequestId, f.UpstreamRequestId = group[len(group)-1].RequestId, ""
					break
				}
			}
			if f.UpstreamRequestId == "" {
				break
			}
		}
	}
	if f.RequestId != "" {
		for key, winner := range projection.requestWinners {
			if key.request == f.RequestId && (ownUserID == 0 || key.user == ownUserID) {
				f.RequestId = winner
				break
			}
		}
	}
	return f, nil
}

func compareRetryLogs(a, b *Log) int {
	if order := cmp.Compare(a.CreatedAt, b.CreatedAt); order != 0 {
		return order
	}
	return cmp.Compare(a.Id, b.Id)
}

func logRetryTraceQuery(ctx context.Context) *gorm.DB {
	// A complete inline request is at most 4KiB; large payloads use a compact manifest.
	return LOG_DB.WithContext(ctx).Model(&RequestTrace{}).Select("trace_id, request_id, created_at, conversation_key, conversation_user_id, method, url, truncated, body_encoding, body_size, CASE WHEN body_encoding = 'chunks_v1' OR length(body) <= 4096 THEN body ELSE '' END AS body").Where("direction = ?", TraceDirectionClientRequest)
}

func logRetryBodyIdentity(trace *RequestTrace) string {
	if trace == nil || trace.Truncated || trace.Body == "" {
		return ""
	}
	if trace.BodyEncoding == "chunks_v1" {
		var manifest requestTraceBodyManifest
		if common.UnmarshalJsonStr(trace.Body, &manifest) != nil || manifest.Size <= 0 || int64(manifest.Size) != trace.BodySize || manifest.Owner == "" || len(manifest.Chunks) == 0 || len(manifest.Digest) != 64 {
			return ""
		}
		if digest, err := hex.DecodeString(manifest.Digest); err != nil || len(digest) != 32 {
			return ""
		}
		return manifest.Digest
	}
	if trace.BodyEncoding != "" || trace.BodySize != int64(len(trace.Body)) {
		return ""
	}
	digest := sha256.Sum256([]byte(trace.Body))
	return hex.EncodeToString(digest[:])
}

func (p *logRetryProjection) apply(tx *gorm.DB) *gorm.DB {
	if p == nil {
		return tx
	}
	for batch := range slices.Chunk(p.hidden, 300) {
		tx = tx.Where("logs.id NOT IN ?", batch)
	}
	return tx
}

func (p *logRetryProjection) annotate(rows []*Log) {
	if p == nil {
		return
	}
	for _, row := range rows {
		if group := p.groups[row.Id]; len(group) > 1 {
			row.RetryCount, row.RetryGroupRequestID, row.RetryStartedAt = len(group)-1, group[0].RequestId, group[0].CreatedAt
		}
	}
}

// GetLogRetryAttempts is read-only and re-derives membership instead of trusting client-supplied IDs.
func GetLogRetryAttempts(ctx context.Context, requestID string, start, end int64, ownUserID, role int) ([]*Log, error) {
	if requestID == "" || start <= 0 || end < start || end-start > operation_setting.GetLogSetting().MaxRangeSeconds() || end > time.Now().Unix()+60 {
		return nil, common.NewMessage("Invalid parameters")
	}
	filter, err := (LogFilter{Category: "model", StartTimestamp: start, EndTimestamp: end, RequestId: requestID}).withRetries(ctx, ownUserID)
	if err != nil {
		return nil, err
	}
	var anchor Log
	query := LOG_DB.WithContext(ctx).Where("request_id = ? AND type IN ? AND created_at >= ? AND created_at <= ?", filter.RequestId, []int{LogTypeConsume, LogTypeError}, start, end)
	query = filter.retries.apply(query)
	if ownUserID != 0 {
		query = query.Where("user_id = ?", ownUserID)
	}
	if err := query.Order("created_at desc, id desc").Take(&anchor).Error; err != nil {
		return nil, err
	}
	members := filter.retries.groups[anchor.Id]
	ids := make([]int, 0, len(members))
	for _, member := range members {
		if member.UserId == anchor.UserId {
			ids = append(ids, member.Id)
		}
	}
	if len(ids) == 0 {
		ids = append(ids, anchor.Id)
	}
	var rows []*Log
	if err := LOG_DB.WithContext(ctx).Where("id IN ? AND user_id = ?", ids, anchor.UserId).Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	// Keep the proven attempt order, including ClickHouse's random IDs within one second.
	memberOrder := make(map[int]int, len(ids))
	for index, id := range ids {
		memberOrder[id] = index
	}
	slices.SortFunc(rows, func(a, b *Log) int { return cmp.Compare(memberOrder[a.Id], memberOrder[b.Id]) })
	keyLiveLogs(rows)
	if ownUserID != 0 {
		formatUserLogs(rows, 0)
	} else {
		if err := resolveLogChannels(rows); err != nil {
			return nil, err
		}
		FormatLogList(rows, role, true)
	}
	return rows, resolveLogUsers(rows, ownUserID == 0 && role >= common.RoleAdminUser)
}
