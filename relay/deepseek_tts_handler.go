package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/wsmanager"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// DeepSeekTTSHelper bills submitted text only after a complete successful audio
// response. DeepSeek provides no usage receipt; this is explicitly a local basis.
func DeepSeekTTSHelper(c *gin.Context, info *relaycommon.RelayInfo) (apiErr *types.NewAPIError) {
	info.InitChannelMeta(c)
	if info.ChannelType != constant.ChannelTypeDeepSeek {
		return types.NewError(errors.New("channel does not support DeepSeek TTS"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	request, ok := info.Request.(*dto.DeepSeekTTSRequest)
	if !ok || len(info.ParamOverride) > 0 {
		return types.NewErrorWithStatusCode(errors.New("invalid TTS request or unsupported parameter overrides"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ModelMappedHelper(c, info, request); err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(errors.New("invalid API type"), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	info.StreamStatus = relaycommon.NewStreamStatus()
	c.Set("deepseek_tts_usage_basis", "request_text")
	defer func() {
		if apiErr != nil {
			info.StreamStatus.MarkFailed("tts_failed", "upstream_error", apiErr.StatusCode)
		}
	}()
	characters := dto.DeepSeekSpeechCharacters(request.Text)
	if request.WebSocket {
		if info.ClientWs == nil {
			return types.NewError(errors.New("missing TTS WebSocket"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		upstream, err := channel.DoWssRequest(adaptor, c, info, nil)
		if err != nil {
			return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer upstream.Close()
		info.IsStream = true
		c.Set("deepseek_tts_cost_unconfirmed", true)
		characters, err = relayDeepSeekTTSSocket(c, info, upstream)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry(), types.ErrOptionWithHideErrMsg("TTS session did not complete"))
		}
	} else {
		body, err := common.Marshal(request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		upstreamURL, err := adaptor.GetRequestURL(info)
		if err != nil {
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
		defer cancel()
		// An opaque body deliberately omits GetBody, preventing transport replay.
		upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, io.NopCloser(bytes.NewReader(body)))
		if err != nil {
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		upstream.ContentLength = int64(len(body))
		if err := adaptor.SetupRequestHeader(c, &upstream.Header, info); err != nil {
			return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		if err := channel.ApplyHeaderOverride(info, c, upstream); err != nil {
			return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		upstream.Header.Set("Content-Type", "application/json")
		c.Set("deepseek_tts_cost_unconfirmed", true)
		response, err := channel.DoRequest(c, upstream, info)
		if err != nil {
			return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return types.NewError(service.RelayErrorHandler(c.Request.Context(), response, false), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
		}
		contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]))
		expectedType := "audio/wav"
		if request.Format == "pcm" {
			expectedType = "audio/pcm"
		}
		if contentType != expectedType {
			return types.NewError(errors.New("invalid TTS audio response"), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
		}
		info.IsStream = true
		c.Header("Content-Type", contentType)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Accel-Buffering", "no")
		buffer := make([]byte, 32<<10)
		written := int64(0)
		controller := http.NewResponseController(c.Writer)
		defer controller.SetWriteDeadline(time.Time{})
		for {
			count, readErr := response.Body.Read(buffer)
			if count > 0 {
				written += int64(count)
				// The documented 30-minute mono PCM limit plus WAV framing.
				if written > 24_000*2*1800+65536 {
					return types.NewError(errors.New("TTS audio exceeds duration limit"), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
				}
				info.SetFirstResponseTime()
				_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if _, err := c.Writer.Write(buffer[:count]); err != nil {
					return types.NewError(err, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
				}
				if err := helper.FlushWriter(c); err != nil {
					return types.NewError(err, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
				}
			}
			if readErr != nil {
				if readErr != io.EOF || written == 0 {
					return types.NewError(errors.New("TTS audio ended before completion"), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
				}
				break
			}
		}
	}
	info.BillingMeteredUsage = &billingexpr.MeteredUsage{Unit: billingexpr.BillingUnitCharacter, Count: characters}
	c.Set("deepseek_tts_cost_unconfirmed", false)
	info.StreamStatus.MarkCompleted()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	service.PostTextConsumeQuota(c, info, &dto.Usage{}, nil)
	if request.WebSocket {
		_ = info.ClientWs.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = info.ClientWs.WriteMessage(websocket.TextMessage, []byte(`{"event":"finish","code":"success","message":""}`))
	}
	return nil
}

// relayDeepSeekTTSSocket enforces bounded text/audio and requires the provider's
// success terminal. An interrupted session has unknown cost and is never replayed.
func relayDeepSeekTTSSocket(c *gin.Context, info *relaycommon.RelayInfo, upstream *websocket.Conn) (int, error) {
	client := info.ClientWs
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()
	unregister := wsmanager.Register(info.ChannelId, "deepseek_tts", func(string) {
		cancel()
		_ = upstream.Close()
	})
	defer unregister()
	// JSON-escaped supplementary Unicode can use twelve bytes per character.
	client.SetReadLimit(128 << 10)
	upstream.SetReadLimit(1 << 20)
	_ = upstream.SetReadDeadline(time.Now().Add(30 * time.Minute))
	type inputResult struct {
		characters int
		err        error
	}
	inputDone := make(chan inputResult, 1)
	var readDeadlineMu sync.Mutex
	var textFinished atomic.Bool
	var workers sync.WaitGroup
	workers.Go(func() {
		result := inputResult{}
		reported, finished := false, false
		defer func() {
			if !reported {
				inputDone <- result
			}
			if result.err != nil {
				_ = upstream.Close()
			}
		}()
		textBytes := 0
		for {
			readDeadlineMu.Lock()
			if ctx.Err() != nil {
				readDeadlineMu.Unlock()
				result.err = ctx.Err()
				return
			}
			deadline := time.Now().Add(60 * time.Second)
			if finished {
				deadline, _ = ctx.Deadline()
			}
			_ = client.SetReadDeadline(deadline)
			readDeadlineMu.Unlock()
			kind, body, err := client.ReadMessage()
			if err != nil {
				result.err = err
				return
			}
			if finished {
				result.err = errors.New("TTS input is already complete")
				return
			}
			if kind != websocket.TextMessage || !utf8.Valid(body) {
				result.err = errors.New("TTS input must be UTF-8 text events")
				return
			}
			var event struct {
				Event string `json:"event"`
				Text  string `json:"text,omitempty"`
			}
			if err := common.Unmarshal(body, &event); err != nil {
				result.err = err
				return
			}
			switch event.Event {
			case "text":
				textBytes += len(event.Text)
				result.characters += dto.DeepSeekSpeechCharacters(event.Text)
				if event.Text == "" || result.characters > dto.MaxDeepSeekSpeechCharacters || textBytes > 64<<10 {
					result.err = errors.New("TTS text exceeds the session limit")
					return
				}
			case "finish_text":
				if result.characters == 0 || event.Text != "" {
					result.err = errors.New("invalid TTS text completion")
					return
				}
			default:
				result.err = errors.New("unsupported TTS event")
				return
			}
			// Re-marshal only known fields; request extras cannot change synthesis.
			payload, err := common.Marshal(event)
			if err != nil {
				result.err = err
				return
			}
			if event.Event == "finish_text" {
				textFinished.Store(true)
			}
			_ = upstream.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if err := upstream.WriteMessage(websocket.TextMessage, payload); err != nil {
				result.err = err
				return
			}
			if event.Event == "finish_text" {
				// Keep reading control frames while synthesis continues, so pings,
				// disconnects and policy shutdowns remain observable.
				inputDone <- result
				reported, finished = true, true
			}
		}
	})
	workers.Go(func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := client.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					cancel()
					return
				}
			}
		}
	})
	defer func() {
		cancel()
		_ = upstream.Close()
		readDeadlineMu.Lock()
		// Interrupt only the reader; the controller can still write its error terminal.
		_ = client.UnderlyingConn().SetReadDeadline(time.Now())
		readDeadlineMu.Unlock()
		workers.Wait()
	}()
	audioBytes := int64(0)
	for {
		kind, body, err := upstream.ReadMessage()
		if err != nil {
			return 0, err
		}
		switch kind {
		case websocket.BinaryMessage:
			audioBytes += int64(len(body))
			if audioBytes > 24_000*2*1800+65536 {
				return 0, errors.New("TTS audio exceeds duration limit")
			}
			info.SetFirstResponseTime()
			_ = client.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if err := client.WriteMessage(websocket.BinaryMessage, body); err != nil {
				return 0, err
			}
		case websocket.TextMessage:
			var event struct {
				Event string `json:"event"`
				Code  string `json:"code"`
			}
			if len(body) > 64<<10 || common.Unmarshal(body, &event) != nil || event.Event != "finish" || event.Code != "success" || audioBytes == 0 || !textFinished.Load() {
				return 0, errors.New("TTS upstream did not finish successfully")
			}
			select {
			case input := <-inputDone:
				if input.err != nil {
					return 0, input.err
				}
				return input.characters, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		default:
			return 0, errors.New("unsupported TTS upstream event")
		}
	}
}
