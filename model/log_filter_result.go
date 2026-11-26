package model

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func (f LogFilter) Validate() error {
	totalBytes := 0
	for _, values := range [][]string{
		logFilterValues(f.ModelName, f.ModelNames),
		logFilterValues(f.Username, f.Usernames),
		logFilterValues(f.TokenName, f.TokenNames),
		logFilterValues(f.Group, f.Groups),
		logFilterValues(f.IP, f.IPs),
		logFilterValues(f.ReasoningEffort, f.ReasoningEfforts),
		logFilterValues(f.ClientTool, f.ClientTools),
		logFilterValues(f.RequestPath, f.RequestPaths),
		logFilterValues(f.ResponseTransport, f.ResponseTransports),
	} {
		if len(values) > 50 {
			return common.NewMessage("Invalid parameters")
		}
		for _, value := range values {
			if len(value) > 1024 || strings.ContainsRune(value, '\x00') {
				return common.NewMessage("Invalid parameters")
			}
			totalBytes += len(value)
		}
	}
	if totalBytes > 32768 {
		return common.NewMessage("Invalid parameters")
	}
	channels := logFilterChannels(f.Channel, f.Channels)
	if len(channels) > 50 {
		return common.NewMessage("Invalid parameters")
	}
	for _, channel := range channels {
		if channel <= 0 {
			return common.NewMessage("Invalid parameters")
		}
	}
	for _, value := range logFilterValues(f.ResponseTransport, f.ResponseTransports) {
		if !slices.Contains([]string{"sse", "ws", "http", "sync", "async", "unknown", "non_streamed"}, value) {
			return common.NewMessage("Invalid parameters")
		}
	}
	if f.Category != "" && f.Category != "model" {
		return common.NewMessage("Invalid parameters")
	}
	if f.Result != "" && f.Result != "success" && f.Result != "failure" {
		return common.NewMessage("Invalid parameters")
	}
	if f.Source == LogSourceTest && (f.Category == "model" || f.Result != "") {
		return common.NewMessage("Invalid parameters")
	}
	return nil
}

func logResultFilterSQL() string {
	if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		return "new_api_log_result_v1(logs.other, logs.type)"
	}
	return logResultCaseSQL("logs.type")
}

// Match isFailedStreamResponse: a completed response can contain recoverable
// diagnostics. Error counters or a generic stream status alone do not fail it.
func logResultCaseSQL(logType string) string {
	fact := logTransportFact
	status := fact("string", "stream_status", "status")
	response := fact("string", "stream_status", "response_status")
	return "CASE WHEN " + logType + " = 5 THEN 'failure'" +
		" WHEN " + response + " = 'completed' THEN 'success'" +
		" WHEN " + response + " IN ('failed', 'incomplete', 'cancelled') THEN 'failure'" +
		" WHEN " + status + " = 'ok' THEN 'success'" +
		" WHEN " + fact("string", "stream_status", "end_error") + " <> ''" +
		" OR " + fact("string", "stream_status", "end_reason") + " IN ('timeout', 'client_gone', 'scanner_error', 'panic', 'ping_fail') THEN 'failure'" +
		" ELSE 'success' END"
}

func appendLogErrorSummary(log *Log) {
	if log.Type != LogTypeConsume && log.Type != LogTypeError {
		return
	}
	if log.Type == LogTypeConsume {
		// Successful rows can contain large captured bodies. Inspect only
		// the outcome before allocating a map of the entire metadata payload.
		var outcome struct {
			Status struct {
				Status         string `json:"status"`
				ResponseStatus string `json:"response_status"`
				EndError       string `json:"end_error"`
				EndReason      string `json:"end_reason"`
			} `json:"stream_status"`
		}
		if common.UnmarshalJsonStr(log.Other, &outcome) != nil || outcome.Status.ResponseStatus == "completed" {
			return
		}
		status := outcome.Status
		if !slices.Contains([]string{"failed", "incomplete", "cancelled"}, status.ResponseStatus) {
			if status.Status == "ok" || status.EndError == "" && !slices.Contains([]string{"timeout", "client_gone", "scanner_error", "panic", "ping_fail"}, status.EndReason) {
				return
			}
		}
	}
	var metadata map[string]common.RawMessage
	summaryMetadata := log.Other
	if log.Other != "" && common.UnmarshalJsonStr(log.Other, &metadata) != nil {
		// Type-5 errors still need the current rule result. Discard only the
		// malformed response projection, including any partially decoded keys;
		// the stored record and its original error content remain untouched.
		metadata = nil
		summaryMetadata = ""
	}
	encoded, err := common.Marshal(operation_setting.ClassifyRequestError(log.Content, summaryMetadata))
	if err != nil {
		return
	}
	if metadata == nil {
		metadata = make(map[string]common.RawMessage)
	}
	metadata["error_summary"] = encoded
	if encoded, err = common.Marshal(metadata); err == nil {
		log.Other = string(encoded)
	}
}

func logFilterValues(scalar string, values []string) []string {
	result := make([]string, 0, len(values)+1)
	seen := make(map[string]bool, len(values)+1)
	for _, value := range append([]string{scalar}, values...) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}

func logFilterChannels(scalar int, values []int) []int {
	result := make([]int, 0, len(values)+1)
	seen := make(map[int]bool, len(values)+1)
	if scalar != 0 {
		result = append(result, scalar)
		seen[scalar] = true
	}
	for _, value := range values {
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}
