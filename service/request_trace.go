package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// requestTraceContextKey holds the collector for the request being served.
const requestTraceContextKey = "request_trace_collector"

// requestTraceIdContextKey holds the trace id on its own. The log write can run
// on a copied context, or after the collector has been torn down, and losing
// the id there would leave the stored payloads unreachable.
const requestTraceIdContextKey = "request_trace_id"

// redactedHeaderNames are headers whose entire value is a credential. They are
// replaced wholesale rather than scrubbed, because a partial match would leave
// the surrounding scheme and any unrecognized credential format intact.
var redactedHeaderNames = map[string]struct{}{
	"authorization":          {},
	"proxy-authorization":    {},
	"x-api-key":              {},
	"api-key":                {},
	"x-goog-api-key":         {},
	"x-amz-security-token":   {},
	"mj-api-secret":          {},
	"cookie":                 {},
	"set-cookie":             {},
	"x-gateway-secret":       {},
	"sec-websocket-protocol": {},
}

const redactedPlaceholder = "[REDACTED]"

// minRedactableSecretLength keeps the value scan from matching short strings
// that happen to appear in ordinary payload text.
const minRedactableSecretLength = 8

// traceBuffer keeps a payload's head and tail within a byte budget, eliding the
// middle. Head-only truncation is wrong here: a streamed response carries its
// usage block and finish_reason at the very end, which is usually the part an
// administrator opened the trace to read.
type traceBuffer struct {
	head    []byte
	tail    []byte
	headCap int
	tailCap int
	total   int64
}

func newTraceBuffer(limit int) *traceBuffer {
	if limit < 1024 {
		limit = 1024
	}
	headCap := limit / 2
	return &traceBuffer{headCap: headCap, tailCap: limit - headCap}
}

func (b *traceBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	b.total += int64(len(p))
	if room := b.headCap - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
		if len(p) == 0 {
			return
		}
	}
	b.tail = append(b.tail, p...)
	// Trim lazily. Dropping the excess on every call would memmove the whole
	// tail budget per write, and a stream arrives as thousands of small writes
	// — one SSE frame is several — so the cost would be O(stream x cap) rather
	// than O(stream). Letting the tail grow to twice the budget before
	// compacting amortizes the copy to O(1) per byte and still bounds memory.
	if len(b.tail) >= 2*b.tailCap {
		b.tail = append(b.tail[:0], b.tail[len(b.tail)-b.tailCap:]...)
	}
}

// Payload reports the retained bytes and whether anything was dropped. When the
// whole payload fit, head and tail concatenate back to it exactly.
func (b *traceBuffer) Payload() (string, int64, bool) {
	head := b.head
	tail := b.tail
	if len(tail) > b.tailCap {
		tail = tail[len(tail)-b.tailCap:]
	}
	truncated := b.total > int64(len(head)+len(tail))
	var payload string
	if !truncated {
		// Join before decoding: the head/tail split can be inside a valid rune
		// even when the complete payload fits and no bytes were discarded.
		payload = string(head) + string(tail)
	} else {
		// Trim only incomplete characters at the elision boundaries. Otherwise
		// PostgreSQL rejects the partial UTF-8 sequence and loses the trace leg.
		if len(head) > 0 {
			start := len(head) - 1
			for start > 0 && !utf8.RuneStart(head[start]) {
				start--
			}
			if !utf8.FullRune(head[start:]) {
				head = head[:start]
			}
		}
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
		elided := b.total - int64(len(head)+len(tail))
		var builder strings.Builder
		builder.Write(head)
		builder.WriteString("\n\n... [")
		builder.WriteString(strconv.FormatInt(elided, 10))
		builder.WriteString(" bytes elided] ...\n\n")
		builder.Write(tail)
		payload = builder.String()
	}
	// Upstreams can mislabel arbitrary bytes as text. Normalize only the saved
	// diagnostic copy; UTF-8 and NUL restrictions must not affect the relay.
	payload = strings.ToValidUTF8(payload, "\uFFFD")
	payload = strings.ReplaceAll(payload, "\x00", "\uFFFD")
	return payload, b.total, truncated
}

// traceSink accepts captured bytes. Text payloads go to a traceBuffer that
// keeps head and tail; binary payloads go to a traceObjectBuffer that keeps the
// whole thing or nothing, because half an audio file will not play.
type traceSink interface {
	Write(p []byte)
}

// traceObjectBuffer holds a binary payload destined for object storage. It
// stops collecting once the budget is exceeded and drops what it had: a
// truncated media file is not something an administrator can open, so keeping
// part of it only costs memory and storage.
type traceObjectBuffer struct {
	data     []byte
	limit    int
	total    int64
	overflow bool
}

func newTraceObjectBuffer(limit int) *traceObjectBuffer {
	return &traceObjectBuffer{limit: limit}
}

func (b *traceObjectBuffer) Write(p []byte) {
	b.total += int64(len(p))
	if b.overflow {
		return
	}
	if len(b.data)+len(p) > b.limit {
		b.overflow = true
		b.data = nil
		return
	}
	b.data = append(b.data, p...)
}

// traceLeg is one captured direction. Streaming legs keep a live buffer that is
// only turned into a payload once the exchange is over.
type traceLeg struct {
	record *model.RequestTrace
	buffer *traceBuffer
	object *traceObjectBuffer
	// objectContentType is the media type the payload was served with, kept so
	// the viewer can play or display it rather than offering a blind download.
	objectContentType string
}

type requestTraceCollector struct {
	mu       sync.Mutex
	traceId  string
	format   string
	legs     []*traceLeg
	clientTo *traceBuffer
	// secrets are scrubbed out of every header value, URL and body before
	// anything is stored. Collected as the request progresses because the
	// channel key is only known once a channel has been picked.
	secrets []string
}

// addSecret registers a credential and the escaped spellings it takes on once
// it is interpolated into a URL. A key containing "+", "/" or "=" reaches the
// wire percent-encoded, and matching only the raw form would miss it — the same
// reason sanitizeFetchModelsError expands both escapes.
func (t *requestTraceCollector) addSecret(secret string) {
	if len(secret) < minRedactableSecretLength {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
		if slices.Contains(t.secrets, form) {
			continue
		}
		t.secrets = append(t.secrets, form)
	}
}

func (t *requestTraceCollector) scrub(value string) string {
	for _, secret := range t.secrets {
		if strings.Contains(value, secret) {
			value = strings.ReplaceAll(value, secret, redactedPlaceholder)
		}
	}
	return value
}

// encodeHeaders renders a header map as JSON with every credential removed.
// Multi-value headers are preserved as arrays; the lossy flattened copy on
// RelayInfo is deliberately not used.
//
// extraRedacted names headers the operator set through a channel's header
// override. Their values cannot be enumerated — an override commonly carries a
// provider key under a vendor-specific name such as ocp-apim-subscription-key —
// so the whole value is dropped. An override that carries something harmless
// like User-Agent loses it from the trace, which is the right way to be wrong.
func (t *requestTraceCollector) encodeHeaders(header http.Header, extraRedacted map[string]struct{}) string {
	if len(header) == 0 {
		return ""
	}
	encoded := make(map[string][]string, len(header))
	for name, values := range header {
		lowered := strings.ToLower(name)
		_, redacted := redactedHeaderNames[lowered]
		if !redacted {
			_, redacted = extraRedacted[lowered]
		}
		if redacted {
			encoded[name] = []string{redactedPlaceholder}
			continue
		}
		scrubbed := make([]string, 0, len(values))
		for _, value := range values {
			scrubbed = append(scrubbed, t.scrub(value))
		}
		encoded[name] = scrubbed
	}
	rendered, err := common.Marshal(encoded)
	if err != nil {
		return ""
	}
	return string(rendered)
}

// bodyCapturable rejects payloads that would put binary in a text column.
// Uploads and audio/image responses carry no readable content and would blow
// the cap on their own.
func bodyCapturable(contentType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(contentType))
	if normalized == "" {
		// A missing Content-Type on a relay body is almost always JSON that an
		// adaptor built itself; capturing it is what makes the trace useful.
		return true
	}
	for _, prefix := range []string{"application/json", "text/", "application/x-ndjson", "application/xml"} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// headerOverrideNames lists the header names a channel's override sets, lowered
// for comparison. Passthrough directives are skipped: they are wildcards rather
// than header names, and the headers they copy are covered by the standard list.
func headerOverrideNames(info *relaycommon.RelayInfo) map[string]struct{} {
	if info == nil {
		return nil
	}
	override := relaycommon.GetEffectiveHeaderOverride(info)
	if len(override) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(override))
	for name := range override {
		lowered := strings.ToLower(strings.TrimSpace(name))
		if lowered == "" || strings.Contains(lowered, "*") || strings.Contains(lowered, ":") {
			continue
		}
		names[lowered] = struct{}{}
	}
	return names
}

func traceCollectorFrom(c *gin.Context) *requestTraceCollector {
	if c == nil {
		return nil
	}
	cached, exists := c.Get(requestTraceContextKey)
	if !exists {
		return nil
	}
	collector, _ := cached.(*requestTraceCollector)
	return collector
}

// BeginRequestTrace starts capturing the exchange and returns the trace id that
// the usage log must carry to make it findable. It returns "" when tracing is
// off, which every caller treats as "do nothing".
//
// The client's response is teed here rather than in a middleware because
// router.Use on the relay router applies to the whole engine, which would wrap
// the admin API and the frontend bundle as well.
func BeginRequestTrace(c *gin.Context, relayFormat string) string {
	if !common.RequestTraceEnabled || c == nil || c.Request == nil {
		return ""
	}
	collector := &requestTraceCollector{
		traceId:  common.NewRequestId(),
		format:   relayFormat,
		clientTo: newTraceBuffer(common.RequestTraceMaxBytes),
	}
	if tokenKey := common.GetContextKeyString(c, constant.ContextKeyTokenKey); tokenKey != "" {
		collector.addSecret(tokenKey)
	}
	c.Set(requestTraceContextKey, collector)
	c.Set(requestTraceIdContextKey, collector.traceId)
	c.Writer = &traceResponseWriter{ResponseWriter: c.Writer, collector: collector}
	return collector.traceId
}

// RequestTraceId reports the trace recorded for this request, or "".
func RequestTraceId(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(requestTraceIdContextKey)
}

// CaptureUpstreamRequest records what new-api actually sent to the provider:
// the final URL, the final headers after channel setup and header overrides,
// and the serialized body after model mapping, format conversion, field
// stripping and parameter overrides.
func CaptureUpstreamRequest(c *gin.Context, info *relaycommon.RelayInfo, req *http.Request) {
	collector := traceCollectorFrom(c)
	if collector == nil || req == nil {
		return
	}
	if info != nil && info.ApiKey != "" {
		collector.addSecret(info.ApiKey)
	}

	record := &model.RequestTrace{
		TraceId:   collector.traceId,
		Direction: model.TraceDirectionUpstreamRequest,
		Method:    req.Method,
		Format:    collector.format,
	}
	if req.URL != nil {
		record.Url = collector.scrub(relaycommon.SanitizeURLForLog(req.URL.String()))
	}
	if info != nil {
		record.Attempt = info.RetryIndex
		record.ChannelId = info.ChannelId
		record.Format = string(info.GetFinalRequestRelayFormat())
	}
	record.Headers = collector.encodeHeaders(req.Header, headerOverrideNames(info))

	leg := &traceLeg{record: record}
	contentType := req.Header.Get("Content-Type")
	if req.GetBody != nil {
		// GetBody hands out an independent reader, so this does not consume the
		// body the transport is about to send.
		if body, err := req.GetBody(); err == nil && body != nil {
			if bodyCapturable(contentType) {
				buffer := newTraceBuffer(common.RequestTraceMaxBytes)
				copyIntoTraceSink(buffer, body)
				record.Body, record.BodySize, record.Truncated = buffer.Payload()
				record.Body = collector.scrub(record.Body)
			} else if objectCapturable(contentType) {
				leg.object = newTraceObjectBuffer(common.RequestTraceMaxObjectBytes)
				leg.objectContentType = contentType
				copyIntoTraceSink(leg.object, body)
			}
			_ = body.Close()
		}
	}

	collector.mu.Lock()
	collector.legs = append(collector.legs, leg)
	collector.mu.Unlock()
}

// objectCapturable reports whether a payload that cannot go in a text column
// should be offloaded to object storage instead of dropped.
func objectCapturable(contentType string) bool {
	return ObjectStoreEnabled() && common.RequestTraceMaxObjectBytes > 0 && !bodyCapturable(contentType)
}

// CaptureUpstreamResponse records the upstream's status and headers and tees
// its body. Tee-ing here is the only way to see the true bytes: the stream
// scanner drops comments, event lines and blank lines before any handler runs.
//
// The bytes are likewise post-decompression: net/http negotiates gzip
// transparently and strips Content-Encoding and Content-Length from resp.Header
// once it has decoded the stream.
func CaptureUpstreamResponse(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) {
	collector := traceCollectorFrom(c)
	if collector == nil || resp == nil {
		return
	}
	record := &model.RequestTrace{
		TraceId:   collector.traceId,
		Direction: model.TraceDirectionUpstreamResponse,
		Status:    resp.StatusCode,
		Headers:   collector.encodeHeaders(resp.Header, nil),
		Format:    collector.format,
	}
	if info != nil {
		record.Attempt = info.RetryIndex
		record.ChannelId = info.ChannelId
		record.Format = string(info.GetFinalRequestRelayFormat())
	}

	leg := &traceLeg{record: record}
	contentType := resp.Header.Get("Content-Type")
	if resp.Body != nil {
		if bodyCapturable(contentType) {
			leg.buffer = newTraceBuffer(common.RequestTraceMaxBytes)
			resp.Body = &traceBodyTee{ReadCloser: resp.Body, collector: collector, sink: leg.buffer}
		} else if objectCapturable(contentType) {
			leg.object = newTraceObjectBuffer(common.RequestTraceMaxObjectBytes)
			leg.objectContentType = contentType
			resp.Body = &traceBodyTee{ReadCloser: resp.Body, collector: collector, sink: leg.object}
		}
	}

	collector.mu.Lock()
	collector.legs = append(collector.legs, leg)
	collector.mu.Unlock()
}

// FinishRequestTrace closes out the client-side legs and persists everything.
// It runs after the relay handler has returned, so the response buffer is
// complete and the retry loop has produced all of its upstream attempts.
func FinishRequestTrace(c *gin.Context) {
	collector := traceCollectorFrom(c)
	if collector == nil {
		return
	}
	// Later legs cannot arrive: the handler has returned and the pinger
	// goroutines are joined, so this is the only reader left. The trace id is
	// left on the context so a log written afterwards can still point at it.
	c.Set(requestTraceContextKey, nil)
	if wrapper, ok := c.Writer.(*traceResponseWriter); ok {
		c.Writer = wrapper.ResponseWriter
	}

	now := time.Now().Unix()
	requestId := c.GetString(common.RequestIdKey)

	collector.mu.Lock()
	legs := collector.legs
	collector.legs = nil
	collector.mu.Unlock()

	ordered := make([]*traceLeg, 0, len(legs)+2)
	ordered = append(ordered, collector.clientRequestLeg(c))
	ordered = append(ordered, legs...)
	ordered = append(ordered, collector.clientResponseLeg(c))

	stored := make([]*traceLeg, 0, len(ordered))
	for _, leg := range ordered {
		if leg == nil || leg.record == nil {
			continue
		}
		if leg.buffer != nil {
			leg.record.Body, leg.record.BodySize, leg.record.Truncated = leg.buffer.Payload()
			leg.record.Body = collector.scrub(leg.record.Body)
		}
		if leg.object != nil {
			leg.record.BodySize = leg.object.total
			leg.record.Truncated = leg.object.overflow
			leg.record.ContentType = leg.objectContentType
		}
		leg.record.Seq = len(stored)
		leg.record.CreatedAt = now
		leg.record.RequestId = requestId
		stored = append(stored, leg)
	}
	if len(stored) == 0 {
		return
	}

	// Best effort and off the response path: a slow log database or object
	// store must never hold a relay open, and a failed trace write must never
	// fail the request. context.Background rather than the request's: this
	// outlives the handler that produced it.
	gopool.Go(func() {
		ctx := context.Background()
		records := make([]*model.RequestTrace, 0, len(stored))
		for _, leg := range stored {
			uploadTraceObject(ctx, leg)
			records = append(records, leg.record)
		}
		if err := model.SaveRequestTraces(records); err != nil {
			common.SysError("failed to store request trace: " + err.Error())
		}
	})
}

// uploadTraceObject offloads a retained binary payload. A failure leaves the
// row without an object reference rather than losing the leg: the headers,
// status and size are still worth having.
func uploadTraceObject(ctx context.Context, leg *traceLeg) {
	if leg.object == nil || len(leg.object.data) == 0 {
		return
	}
	key := fmt.Sprintf("traces/%s/%d-%s", leg.record.TraceId, leg.record.Seq, leg.record.Direction)
	err := PutObject(ctx, key, leg.objectContentType,
		int64(len(leg.object.data)), bytes.NewReader(leg.object.data))
	if err != nil {
		common.SysError("failed to store request trace payload: " + err.Error())
		return
	}
	leg.record.ObjectKey = key
}

// clientRequestLeg reads the payload the client sent from the storage the relay
// already filled. It never calls GetRequestBody, which would try to drain a
// request body that was consumed long before.
//
// The bytes are post-decompression: DecompressRequestMiddleware replaces the
// request body before the storage is ever populated and drops Content-Encoding,
// so a gzipped upload is recorded as the JSON it decoded to.
func (t *requestTraceCollector) clientRequestLeg(c *gin.Context) *traceLeg {
	record := &model.RequestTrace{
		TraceId:   t.traceId,
		Direction: model.TraceDirectionClientRequest,
		Format:    t.format,
		Method:    c.Request.Method,
	}
	if c.Request.URL != nil {
		record.Url = t.scrub(relaycommon.SanitizeURLForLog(c.Request.URL.String()))
	}
	record.Headers = t.encodeHeaders(c.Request.Header, nil)
	leg := &traceLeg{record: record}

	contentType := c.GetHeader("Content-Type")
	asText := bodyCapturable(contentType)
	if !asText && !objectCapturable(contentType) {
		return leg
	}
	// Read the storage the relay already populated rather than calling
	// GetBodyStorage, which would try to drain a request body that has been
	// consumed since.
	cached, exists := c.Get(common.KeyBodyStorage)
	if !exists || cached == nil {
		return leg
	}
	storage, ok := cached.(common.BodyStorage)
	if !ok || storage.Size() <= 0 {
		return leg
	}
	reader, err := storage.NewReader()
	if err != nil {
		return leg
	}
	defer reader.Close()

	if !asText {
		leg.object = newTraceObjectBuffer(common.RequestTraceMaxObjectBytes)
		leg.objectContentType = contentType
		copyIntoTraceSink(leg.object, reader)
		return leg
	}
	buffer := newTraceBuffer(common.RequestTraceMaxBytes)
	copyIntoTraceSink(buffer, reader)
	record.Body, record.BodySize, record.Truncated = buffer.Payload()
	record.Body = t.scrub(record.Body)
	return leg
}

func (t *requestTraceCollector) clientResponseLeg(c *gin.Context) *traceLeg {
	record := &model.RequestTrace{
		TraceId:   t.traceId,
		Direction: model.TraceDirectionClientResponse,
		Format:    t.format,
		Status:    c.Writer.Status(),
		Headers:   t.encodeHeaders(c.Writer.Header(), nil),
	}
	record.Body, record.BodySize, record.Truncated = t.clientTo.Payload()
	record.Body = t.scrub(record.Body)
	return &traceLeg{record: record}
}

// copyIntoTraceSink drains a reader into the sink without materializing a
// disk-backed payload in one allocation.
func copyIntoTraceSink(sink traceSink, reader io.Reader) {
	chunk := make([]byte, 32*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			sink.Write(chunk[:n])
		}
		if err != nil {
			return
		}
	}
}

// traceBodyTee mirrors the upstream body into the trace while leaving the
// stream's read semantics untouched, so the scanner and every io.ReadAll site
// downstream see exactly the counts, errors and EOF they saw before.
type traceBodyTee struct {
	io.ReadCloser
	collector *requestTraceCollector
	sink      traceSink
}

func (t *traceBodyTee) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		t.collector.mu.Lock()
		t.sink.Write(p[:n])
		t.collector.mu.Unlock()
	}
	return n, err
}

// traceResponseWriter mirrors everything written back to the client.
//
// gin.ResponseWriter is embedded as an interface so Flush, Hijack, Status and
// the rest promote unchanged and streaming keeps working. Unwrap has to be
// declared explicitly: it is not part of that interface, and without it
// http.NewResponseController cannot reach the real writer, which would silently
// disable the stream write deadline and let a slow client hang the handler.
type traceResponseWriter struct {
	gin.ResponseWriter
	collector *requestTraceCollector
}

func (w *traceResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *traceResponseWriter) Write(data []byte) (int, error) {
	w.collector.mu.Lock()
	w.collector.clientTo.Write(data)
	w.collector.mu.Unlock()
	return w.ResponseWriter.Write(data)
}

func (w *traceResponseWriter) WriteString(data string) (int, error) {
	w.collector.mu.Lock()
	w.collector.clientTo.Write([]byte(data))
	w.collector.mu.Unlock()
	return w.ResponseWriter.WriteString(data)
}

const requestTraceCleanupInterval = time.Hour

// StartRequestTraceCleanup expires stored traces on their own schedule.
//
// It runs on every node rather than only the master, because a delete-by-cutoff
// is idempotent and traces must not depend on a master being healthy: on SQL
// engines usage-log cleanup only runs when an administrator triggers it, and a
// trace is orders of magnitude larger than the log row it belongs to.
func StartRequestTraceCleanup() {
	go func() {
		cleanupExpiredRequestTraces()
		ticker := time.NewTicker(requestTraceCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			cleanupExpiredRequestTraces()
		}
	}()
}

func cleanupExpiredRequestTraces() {
	days := common.RequestTraceRetentionDays
	if days <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	// Objects first: a row deleted before its payload would leave the payload
	// with nothing pointing at it, and object storage has no other index.
	if ObjectStoreEnabled() {
		keys, err := model.RequestTraceObjectKeysBefore(cutoff)
		if err != nil {
			common.SysError("failed to list expiring request trace payloads: " + err.Error())
			return
		}
		if err := DeleteObjects(context.Background(), keys); err != nil {
			common.SysError("failed to delete expiring request trace payloads: " + err.Error())
			return
		}
	}
	if err := model.DeleteRequestTracesBefore(cutoff); err != nil {
		common.SysError("failed to expire request traces: " + err.Error())
	}
}
