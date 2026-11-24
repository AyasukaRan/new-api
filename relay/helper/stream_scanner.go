package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a write blocked on a client that stopped
	// reading so the unconditional wg.Wait() in cleanup can always finish.
	// Without it, a connected client with a full TCP buffer (no server
	// WriteTimeout) could hang the handler forever. StreamWriteBudget adds the
	// time a client reading at streamMinClientRate needs for the data.
	streamWriteTimeout  = 30 * time.Second
	streamMinClientRate = 8 << 10 // bytes per second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// NewStreamScanner shares relay scanner configuration. Callers buffering bounded
// task state may additionally cap a line without increasing the configured limit.
func NewStreamScanner(reader io.Reader, maxBytes ...int) *bufio.Scanner {
	limit := getScannerBufferSize()
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = min(limit, maxBytes[0])
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(InitialScannerBufferSize, limit)), limit)
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// StreamWriteBudget gives the writes of one stream a deadline of
// streamWriteTimeout past the time a client reading at streamMinClientRate
// needs for everything written so far. A fixed deadline per write cut slow
// clients off in the middle of a multi-megabyte image event, and the timeout
// looked like a client disconnect. The budget carries over between writes
// because the kernel can still hold megabytes of earlier writes and wakes a
// blocked writer only after a large part of them drains. A client that stops
// reading is still cut off once the budget runs out. Callers serialize writes.
type StreamWriteBudget struct {
	drainBy time.Time
}

// Deadline returns the write deadline for n more bytes written at now.
func (b *StreamWriteBudget) Deadline(now time.Time, n int) time.Time {
	if b.drainBy.Before(now) {
		b.drainBy = now
	}
	b.drainBy = b.drainBy.Add(time.Duration(n) * time.Second / streamMinClientRate)
	return b.drainBy.Add(streamWriteTimeout)
}

// streamResponseWriter sets the connection write deadline from its budget
// before every write. Writers that don't support deadlines (e.g. httptest
// recorders) are silently ignored.
type streamResponseWriter struct {
	gin.ResponseWriter
	budget           StreamWriteBudget
	protocolStarted  atomic.Bool
	keepaliveWritten atomic.Bool
}

func (w *streamResponseWriter) Write(p []byte) (int, error) {
	if string(p) == ": PING\n\n" {
		w.keepaliveWritten.Store(true)
	} else if len(p) > 0 {
		w.protocolStarted.Store(true)
	}
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.budget.Deadline(time.Now(), len(p)))
	return w.ResponseWriter.Write(p)
}

func (w *streamResponseWriter) WriteString(s string) (int, error) {
	if s == ": PING\n\n" {
		w.keepaliveWritten.Store(true)
	} else if s != "" {
		w.protocolStarted.Store(true)
	}
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.budget.Deadline(time.Now(), len(s)))
	return w.ResponseWriter.WriteString(s)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *streamResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// RelayResponseReplayable permits only our own SSE keepalive comments after
// headers have been sent. Even protocol metadata cannot be replayed safely.
func RelayResponseReplayable(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Context().Err() != nil {
		return false
	}
	if !c.Writer.Written() {
		return true
	}
	var writer http.ResponseWriter = c.Writer
	for writer != nil {
		if stream, ok := writer.(*streamResponseWriter); ok {
			return stream.keepaliveWritten.Load() && !stream.protocolStarted.Load()
		}
		wrapped, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		writer = wrapped.Unwrap()
	}
	return false
}

// Meaningful stream output excludes errors, usage, keepalives and protocol
// setup. Inspect only until the first output, so long streams pay this cost once.
func streamDataHasContent(data string) bool {
	value := ojson.Parse(data)
	if value.Get("error").Exists() || value.Get("error_code").Int() != 0 || value.Get("event").String() == "error" {
		return false
	}
	switch value.Get("type").String() {
	case "content_block_delta":
		for _, field := range []string{"delta.text", "delta.thinking", "delta.partial_json"} {
			if value.Get(field).String() != "" {
				return true
			}
		}
	case "content_block_start":
		kind := value.Get("content_block.type").String()
		return kind == "tool_use" || kind == "server_tool_use" || value.Get("content_block.text").String() != "" || value.Get("content_block.thinking").String() != ""
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta", "response.audio.delta", "response.audio_transcript.delta", "transcript.text.delta":
		return value.Get("delta").String() != ""
	case "speech.audio.delta":
		return value.Get("audio").String() != ""
	case "image_generation.partial_image", "image_generation.completed", "image_edit.partial_image", "image_edit.completed":
		return value.Get("b64_json").String() != "" || value.Get("partial_image_b64").String() != "" || value.Get("url").String() != ""
	case "response.output_item.added":
		kind := value.Get("item.type").String()
		return kind == "function_call" || kind == "web_search_call" || kind == "file_search_call"
	}
	// The other native SSE users of this scanner are Baidu and Dify.
	if value.Get("result").String() != "" || value.Get("answer").String() != "" {
		return true
	}
	for _, choice := range value.Get("choices").Array() {
		for _, field := range []string{"text", "delta.content", "delta.reasoning_content", "delta.reasoning", "delta.function_call.name", "delta.function_call.arguments"} {
			if choice.Get(field).String() != "" {
				return true
			}
		}
		if len(choice.Get("delta.tool_calls").Array()) > 0 {
			return true
		}
	}
	for _, candidate := range value.Get("candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("text").String() != "" || part.Get("functionCall").Exists() || part.Get("inlineData").Exists() {
				return true
			}
		}
	}
	return false
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {

	if resp == nil || dataHandler == nil {
		return
	}

	// Finish the request keepalive before changing headers. The scanner owns
	// subsequent ping and data writes through its shared write mutex.
	if writer := keepAliveWriter(c); writer != nil {
		writer.stop()
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second

	var (
		stopChan    = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner     = NewStreamScanner(resp.Body)
		ticker      = time.NewTicker(streamingTimeout)
		pingTicker  *time.Ticker
		writeMutex  sync.Mutex     // Mutex to protect concurrent writes
		wg          sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce sync.Once
		stopOnce    sync.Once
	)

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, common.LogText("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, common.LogText("ping data error: %s", err.Error()))
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, common.LogText("ping goroutine max duration reached"))
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)
	sr := newStreamResult(info.StreamStatus)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, common.LogText("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		for data := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				dataHandler(data, sr)
				if !info.HasSendResponse() && !sr.status.ResponseFailed() && streamDataHasContent(data) {
					info.SetFirstResponseTime()
				}
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, common.LogText("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			data := scanner.Text()
			logger.LogDebug(c, "stream scanner data: %s", data)

			data, hasDataPrefix := strings.CutPrefix(data, "data:")
			data = strings.TrimSpace(data)
			if !hasDataPrefix && data != "[DONE]" {
				continue
			}
			if data == "" {
				continue
			}
			if data != "[DONE]" {
				info.ReceivedResponseCount++

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		// Closing the upstream body is part of stopping the scanner. Its EOF or
		// read error must not replace the actual stop reason or add a false error.
		if ctx.Err() != nil {
			return
		}
		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, common.LogText("scanner error: %s", err.Error()))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-c.Request.Context().Done():
		// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
		// 避免为已放弃的请求继续消费上游 token。
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}

	cleanup()
	// All stream goroutines have joined. Done from a successfully delivered
	// protocol completion can now correct a cancellation that won while its
	// callback was still running. Bare EOF and failed writes never qualify.
	if sr.done && info.StreamStatus.ResponseOutcome() == string(relaycommon.ResponseOutcomeCompleted) &&
		info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone &&
		errors.Is(info.StreamStatus.EndError, context.Canceled) && !info.StreamStatus.HasErrors() {
		info.StreamStatus.EndReason = relaycommon.StreamEndReasonDone
		info.StreamStatus.EndError = nil
	}
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, common.LogText("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, common.LogText("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
}
