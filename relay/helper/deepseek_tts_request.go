package helper

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func GetAndValidateDeepSeekTTSRequest(c *gin.Context) (*dto.DeepSeekTTSRequest, error) {
	request := &dto.DeepSeekTTSRequest{Model: constant.DeepSeekTTSModel}
	if c.Request.Method == http.MethodGet {
		if !websocket.IsWebSocketUpgrade(c.Request) {
			return nil, errors.New("a WebSocket upgrade is required")
		}
		request.WebSocket = true
		for key, values := range c.Request.URL.Query() {
			if len(values) != 1 {
				return nil, errors.New("repeated TTS query parameters are not supported")
			}
			switch key {
			case "voice_id":
				request.VoiceID = values[0]
			case "format":
				request.Format = values[0]
			case "filter_markdown":
				if values[0] != "true" && values[0] != "false" {
					return nil, errors.New("filter_markdown must be true or false")
				}
				value, _ := strconv.ParseBool(values[0])
				request.FilterMarkdown = &value
			default:
				return nil, errors.New("unsupported TTS query parameter")
			}
		}
	} else {
		if c.ContentType() != "application/json" {
			return nil, errors.New("application/json is required")
		}
		if c.Request.ContentLength > 64<<10 {
			return nil, common.ErrRequestBodyTooLarge
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return nil, err
		}
		if storage.Size() > 64<<10 {
			return nil, common.ErrRequestBodyTooLarge
		}
		data, err := storage.Bytes()
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(data) {
			return nil, errors.New("TTS text must be valid UTF-8")
		}
		var fields map[string]common.RawMessage
		if err := common.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
		for field := range fields {
			switch field {
			case "text", "voice_id", "format", "filter_markdown":
			default:
				return nil, errors.New("unsupported TTS request field")
			}
		}
		if err := common.Unmarshal(data, request); err != nil {
			return nil, err
		}
		if strings.TrimSpace(request.Text) == "" || !utf8.ValidString(request.Text) || dto.DeepSeekSpeechCharacters(request.Text) > dto.MaxDeepSeekSpeechCharacters {
			return nil, errors.New("TTS text must contain between 1 and 10000 billed characters")
		}
	}
	if request.VoiceID != "" && request.VoiceID != "stella" && request.VoiceID != "tide" {
		return nil, errors.New("voice_id must be stella or tide")
	}
	validFormat := request.Format == "" || request.Format == "pcm"
	validFormat = validFormat || (!request.WebSocket && request.Format == "wav") || (request.WebSocket && request.Format == "opus")
	if !validFormat {
		return nil, errors.New("unsupported TTS audio format")
	}
	return request, nil
}
