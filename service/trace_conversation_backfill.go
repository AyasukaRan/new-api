package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

// New captures already carry these labels. The nullable client marks historical
// rows so an interrupted upgrade resumes without rewriting completed batches.
func BackfillTraceConversationFilters(ctx context.Context) error {
	db := model.LOG_DB.WithContext(ctx)
	last := ""
	for {
		var batch []struct {
			TraceId, RequestId, Headers, ConversationKey, ConversationTitle, InlineBody string
			Seq                                                                         int
			Truncated                                                                   bool
			BodyEncoding                                                                string
			BodySize                                                                    int
		}
		err := model.ConversationTraceQuery(db).Where("conversation_client IS NULL AND trace_id > ?", last).
			Select(`trace_id, request_id, seq, headers, conversation_key, conversation_title, truncated, body_encoding, body_size,
				CASE WHEN COALESCE(body_encoding, '') = '' AND COALESCE(body_size, 0) <= 4096 THEN substr(body, 1, 4097) ELSE '' END AS inline_body`).
			Order("trace_id").Limit(192).Scan(&batch).Error
		if err != nil || len(batch) == 0 {
			return err
		}
		clients := make([]string, len(batch))
		missing := make([]string, 0)
		for i, row := range batch {
			var header http.Header
			if common.UnmarshalJsonStr(row.Headers, &header) == nil && len(header) > 0 {
				clients[i] = IdentifyRequestClient(header)
			}
			if (clients[i] == "" || clients[i] == "Normal HTTP") && row.RequestId != "" {
				missing = append(missing, row.RequestId)
			}
		}
		// Historical inference may only exist in the usage log. Read fallback rows
		// from the log database one at a time and never expose the stored payload.
		logged := make(map[string]string)
		if len(missing) > 0 {
			rows, err := db.Model(&model.Log{}).Select("request_id, other").Where("request_id IN ?", missing).Order("created_at DESC, id DESC").Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				var requestID string
				var other *string
				if err := rows.Scan(&requestID, &other); err != nil {
					rows.Close()
					return err
				}
				if other != nil && logged[requestID] == "" && gjson.Valid(*other) {
					logged[requestID] = operation_setting.NormalizeRequestSourceName(gjson.Get(*other, "client_tool").Str)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}

		byClient := make(map[string][]string)
		var greetings, ids []string
		for i, row := range batch {
			client := clients[i]
			if prior := logged[row.RequestId]; prior != "" && (client == "" || client == "Normal HTTP") {
				client = prior
			}
			byClient[client] = append(byClient[client], row.TraceId)
			ids = append(ids, row.TraceId)
			if row.Truncated || !(isGreetingText(row.ConversationTitle) || row.ConversationKey == "" && row.ConversationTitle == "") {
				continue
			}
			body := row.InlineBody
			if row.BodyEncoding != "" || row.BodySize > 4096 || len(body) > 4096 {
				leg, err := model.GetRequestTraceLegContext(ctx, row.TraceId, row.Seq)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					// Missing or damaged chunks cannot establish a pure greeting.
					// Preserve the record and still index its client and later rows.
					continue
				}
				body = leg.Body
			}
			if isGreetingOnlyRequest(body) {
				greetings = append(greetings, row.TraceId)
			}
		}
		clientSQL := strings.Builder{}
		clientSQL.WriteString("CASE")
		var args []any
		for client, traceIDs := range byClient {
			clientSQL.WriteString(" WHEN trace_id IN ? THEN ?")
			args = append(args, traceIDs, client)
		}
		clientSQL.WriteString(" ELSE '' END")
		greetingSQL := "0"
		if len(greetings) > 0 {
			greetingSQL = "trace_id IN ?"
		}
		const condition = "direction = ? AND conversation_client IS NULL AND trace_id IN ?"
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			if len(greetings) > 0 {
				args = append(args, greetings)
			}
			args = append(args, model.TraceDirectionClientRequest, ids)
			err = db.Exec("ALTER TABLE request_traces UPDATE conversation_client = "+clientSQL.String()+", conversation_greeting = "+greetingSQL+" WHERE "+condition+" SETTINGS mutations_sync = 1", args...).Error
		} else {
			var greetingValue any = false
			if len(greetings) > 0 {
				greetingValue = gorm.Expr(greetingSQL, greetings)
			}
			err = db.Model(&model.RequestTrace{}).Where(condition, model.TraceDirectionClientRequest, ids).
				Updates(map[string]any{"conversation_client": gorm.Expr(clientSQL.String(), args...), "conversation_greeting": greetingValue}).Error
		}
		if err != nil {
			return err
		}
		last = batch[len(batch)-1].TraceId
	}
}
