package channel

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

var deepSeekErrorIDPattern = regexp.MustCompile(`(?i)\b(?:request|trace|response)[ _-]?id["']?[ \t]*[:=][ \t]*["']?([a-z0-9][a-z0-9_-]*)`)

func deepSeekResponseError(c *gin.Context, reason string) error {
	logger.LogError(c, common.LogText("DeepSeek response privacy rejected: %s", reason))
	return errors.New(i18n.T(c, i18n.MsgRelayInvalidUpstreamResponse))
}

// protectDeepSeekResponse runs after trace capture so operators retain the
// provider's evidence while clients receive only this gateway's request IDs.
func protectDeepSeekResponse(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) error {
	if info == nil || info.ChannelMeta == nil || info.ChannelType != appconstant.ChannelTypeDeepSeek || resp == nil {
		return nil
	}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = common.NewRequestId()
		c.Set(common.RequestIdKey, requestID)
	}
	responseID := "chatcmpl-" + requestID
	switch {
	case info.GetFinalRequestRelayFormat() == types.RelayFormatClaude:
		responseID = "msg_" + requestID
	case info.RelayMode == relayconstant.RelayModeResponses:
		responseID = "resp_" + requestID
	case info.RelayMode == relayconstant.RelayModeCompletions:
		responseID = "cmpl-" + requestID
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	body := &deepSeekPrivateResponse{
		source: resp.Body, context: c, requestID: requestID, responseID: responseID,
		stream: strings.HasPrefix(contentType, "text/event-stream") ||
			(contentType == "" && info.IsStream && resp.StatusCode >= 200 && resp.StatusCode < 300),
		compressed: encoding == "gzip", errorResponse: resp.StatusCode >= 300, limit: helper.DefaultMaxScannerBufferSize,
	}
	if appconstant.StreamScannerMaxBufferMB > 0 {
		body.limit = appconstant.StreamScannerMaxBufferMB << 20
	}
	c.Set(common.UpstreamRequestIdKey, "")
	for _, name := range []string{"X-Ds-Request-Id", "X-Request-Id", "Request-Id", common.RequestIdKey, "X-Ds-Trace-Id", "X-Trace-Id", "Trace-Id", "Traceparent", "X-B3-Traceid"} {
		for key, values := range resp.Header {
			if strings.EqualFold(key, name) {
				for _, value := range values {
					if err := body.rememberID(value); err != nil {
						return err
					}
				}
			}
		}
	}
	// Provider diagnostic headers (including trace headers and validators) do
	// not belong to the public response. Keep only protocol/cache/rate data.
	for name := range resp.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "content-type", "cache-control", "retry-after", "expires", "date", "content-language",
			"ratelimit", "ratelimit-policy", "ratelimit-limit", "ratelimit-remaining", "ratelimit-reset",
			"x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests",
			"x-ratelimit-limit-tokens", "x-ratelimit-remaining-tokens", "x-ratelimit-reset-tokens":
		default:
			delete(resp.Header, name)
		}
	}
	resp.Header.Set("X-Request-Id", requestID)
	resp.ContentLength = -1
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		return deepSeekResponseError(c, "unsupported DeepSeek response content encoding")
	}
	if resp.Body != nil {
		resp.Body = body
	}
	return nil
}

type deepSeekPrivateResponse struct {
	source        io.ReadCloser
	context       *gin.Context
	reader        *bufio.Reader
	gzipReader    *gzip.Reader
	requestID     string
	responseID    string
	originalIDs   []string
	stream        bool
	compressed    bool
	errorResponse bool
	limit         int
	pending       []byte
	terminal      error
	closed        atomic.Bool
}

func (r *deepSeekPrivateResponse) rememberID(id string) error {
	if id == "" || slices.Contains(r.originalIDs, id) {
		return nil
	}
	if len(id) > 1024 || len(r.originalIDs) >= 64 {
		return deepSeekResponseError(r.context, "DeepSeek response contains excessive diagnostic identifiers")
	}
	r.originalIDs = append(r.originalIDs, id)
	if r.context.GetString(common.UpstreamRequestIdKey) == "" {
		logID := []rune(id)
		r.context.Set(common.UpstreamRequestIdKey, string(logID[:min(len(logID), 128)]))
	}
	return nil
}

func (r *deepSeekPrivateResponse) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	// Closing the source interrupts an in-progress read. The gzip decoder owns
	// no external resource and must not be mutated concurrently with that read.
	return r.source.Close()
}

func (r *deepSeekPrivateResponse) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	if r.terminal != nil {
		return 0, r.terminal
	}
	if r.reader == nil {
		var source io.Reader = r.source
		if r.compressed {
			var err error
			r.gzipReader, err = gzip.NewReader(r.source)
			if err != nil {
				r.terminal = deepSeekResponseError(r.context, "invalid compressed DeepSeek response")
				return 0, r.terminal
			}
			source = r.gzipReader
		}
		r.reader = bufio.NewReader(source)
	}
	for len(r.pending) == 0 && r.terminal == nil {
		if r.stream {
			r.pending, r.terminal = r.readEvent()
		} else {
			body, err := io.ReadAll(io.LimitReader(r.reader, int64(r.limit)+1))
			if err != nil {
				r.terminal = deepSeekResponseError(r.context, "failed to read DeepSeek response")
			} else if len(body) > r.limit {
				r.terminal = deepSeekResponseError(r.context, "DeepSeek response exceeds the response size limit")
			} else if len(bytes.TrimSpace(body)) == 0 {
				r.terminal = io.EOF
			} else {
				r.pending, r.terminal = r.rewriteJSON(body)
				if r.terminal == nil {
					r.terminal = io.EOF
				}
			}
		}
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.terminal
}

func (r *deepSeekPrivateResponse) readEvent() ([]byte, error) {
	var prefix, data bytes.Buffer
	size := 0
	var readErr error
	for {
		var line []byte
		for {
			part, err := r.reader.ReadSlice('\n')
			size += len(part)
			if size > r.limit {
				return nil, deepSeekResponseError(r.context, "DeepSeek stream event exceeds the response size limit")
			}
			line = append(line, part...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			readErr = err
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, deepSeekResponseError(r.context, "failed to read DeepSeek stream event")
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			break
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		case "id":
			// SSE event IDs need not be stable across a stream and are not the
			// provider request ID used for operator lookup.
			fmt.Fprintf(&prefix, "id: %s\n", r.requestID)
		case "event", "retry":
			prefix.Write(line)
			prefix.WriteByte('\n')
		case "":
			// Preserve the heartbeat without forwarding provider diagnostics.
			prefix.WriteString(":\n")
		}
		if readErr != nil {
			break
		}
	}
	if data.Len() > 0 {
		payload := bytes.TrimSpace(data.Bytes())
		if !bytes.Equal(payload, []byte("[DONE]")) {
			var err error
			payload, err = r.rewriteJSON(payload)
			if err != nil {
				return nil, err
			}
		}
		prefix.WriteString("data: ")
		prefix.Write(payload)
		prefix.WriteByte('\n')
	}
	if prefix.Len() == 0 {
		return nil, readErr
	}
	prefix.WriteByte('\n')
	return prefix.Bytes(), readErr
}

func (r *deepSeekPrivateResponse) rewriteJSON(payload []byte) ([]byte, error) {
	var root map[string]common.RawMessage
	if err := common.Unmarshal(payload, &root); err != nil || root == nil {
		return nil, deepSeekResponseError(r.context, "invalid DeepSeek response JSON")
	}
	objects := []struct {
		path  string
		value map[string]common.RawMessage
		id    string
	}{{value: root, id: r.responseID}}
	for _, nested := range []struct{ key, id string }{{"response", "resp_" + r.requestID}, {"message", "msg_" + r.requestID}} {
		if raw, ok := root[nested.key]; ok && common.GetJsonType(raw) == "object" {
			var value map[string]common.RawMessage
			if err := common.Unmarshal(raw, &value); err != nil {
				return nil, deepSeekResponseError(r.context, "invalid DeepSeek response JSON")
			}
			objects = append(objects, struct {
				path  string
				value map[string]common.RawMessage
				id    string
			}{path: nested.key, value: value, id: nested.id})
		}
	}
	for _, object := range objects {
		for _, key := range []string{"id", "request_id", "requestId", "trace_id", "traceId", "response_id", "responseId"} {
			if raw, exists := object.value[key]; exists && string(raw) != "null" {
				var id string
				if common.Unmarshal(raw, &id) != nil {
					return nil, deepSeekResponseError(r.context, "invalid DeepSeek response identifier")
				}
				if err := r.rememberID(id); err != nil {
					return nil, err
				}
				local := r.requestID
				if key == "id" {
					local = object.id
				}
				object.value[key], _ = common.Marshal(local)
			}
		}
	}
	for i := range objects {
		object := &objects[i]
		if raw, exists := object.value["error"]; exists && string(raw) != "null" {
			if _, err := r.rewriteError(raw, true, 0); err != nil {
				return nil, err
			}
			clean, err := r.rewriteError(raw, false, 0)
			if err != nil {
				return nil, err
			}
			object.value["error"] = clean
		}
		_, hasError := object.value["error"]
		if r.errorResponse || hasError || string(object.value["type"]) == `"error"` {
			for _, key := range []string{"message", "msg", "err", "error_msg", "detail"} {
				if raw, exists := object.value[key]; exists && common.GetJsonType(raw) == "string" {
					if _, err := r.rewriteError(raw, true, 0); err != nil {
						return nil, err
					}
					object.value[key] = r.rewriteErrorText(raw)
				}
			}
		}
		if object.path != "" {
			root[object.path], _ = common.Marshal(object.value)
		}
	}
	return common.Marshal(root)
}

// The error's diagnostic tree is distinct from assistant content. Collect all
// identifiers first so a message can refer to an ID held in a later sibling.
func (r *deepSeekPrivateResponse) rewriteError(raw common.RawMessage, collect bool, depth int) (common.RawMessage, error) {
	if depth > 64 {
		return nil, deepSeekResponseError(r.context, "DeepSeek error diagnostics exceed the nesting limit")
	}
	switch common.GetJsonType(raw) {
	case "string":
		if collect {
			var message string
			_ = common.Unmarshal(raw, &message)
			for {
				match := deepSeekErrorIDPattern.FindStringSubmatchIndex(message)
				if match == nil {
					break
				}
				if err := r.rememberID(message[match[2]:match[3]]); err != nil {
					return nil, err
				}
				message = message[match[1]:]
			}
			return raw, nil
		}
		return r.rewriteErrorText(raw), nil
	case "array":
		var items []common.RawMessage
		if common.Unmarshal(raw, &items) != nil {
			return nil, deepSeekResponseError(r.context, "invalid DeepSeek error response")
		}
		for i, item := range items {
			value, err := r.rewriteError(item, collect, depth+1)
			if err != nil {
				return nil, err
			}
			items[i] = value
		}
		if collect {
			return raw, nil
		}
		return common.Marshal(items)
	case "object":
	default:
		return raw, nil
	}
	var fields map[string]common.RawMessage
	if common.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, deepSeekResponseError(r.context, "invalid DeepSeek error response")
	}
	for key, value := range fields {
		switch key {
		case "code", "type", "param":
			continue
		case "request_id", "requestId", "trace_id", "traceId", "response_id", "responseId":
			if string(value) == "null" {
				continue
			}
			var id string
			if common.Unmarshal(value, &id) != nil {
				return nil, deepSeekResponseError(r.context, "invalid DeepSeek error identifier")
			}
			if collect {
				if err := r.rememberID(id); err != nil {
					return nil, err
				}
			} else {
				fields[key], _ = common.Marshal(r.requestID)
			}
		default:
			clean, err := r.rewriteError(value, collect, depth+1)
			if err != nil {
				return nil, err
			}
			fields[key] = clean
		}
	}
	if collect {
		return raw, nil
	}
	return common.Marshal(fields)
}

func (r *deepSeekPrivateResponse) rewriteErrorText(raw common.RawMessage) common.RawMessage {
	var message string
	_ = common.Unmarshal(raw, &message)
	// Longer IDs first also covers IDs containing another recorded identifier.
	slices.SortFunc(r.originalIDs, func(a, b string) int { return len(b) - len(a) })
	replacements := make([]string, 0, len(r.originalIDs)*2)
	for _, id := range r.originalIDs {
		replacements = append(replacements, id, r.requestID)
	}
	if len(replacements) > 0 {
		message = strings.NewReplacer(replacements...).Replace(message)
	}
	encoded, _ := common.Marshal(message)
	return encoded
}
