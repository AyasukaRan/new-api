package dto

import (
	"net/http"
	"unicode"

	"github.com/QuantumNous/new-api/relaykit/types"
)

const MaxDeepSeekSpeechCharacters = 10000

type DeepSeekTTSRequest struct {
	Model          string `json:"-"`
	Text           string `json:"text,omitempty"`
	VoiceID        string `json:"voice_id,omitempty"`
	Format         string `json:"format,omitempty"`
	FilterMarkdown *bool  `json:"filter_markdown,omitempty"`
	WebSocket      bool   `json:"-"`
}

func (r *DeepSeekTTSRequest) GetTokenCountMeta() *types.TokenCountMeta {
	return &types.TokenCountMeta{TokenType: types.TokenTypeTokenizer}
}

// Binary audio uses its own writer, not the gateway's SSE transport.
func (r *DeepSeekTTSRequest) IsStream(_ *http.Request) bool { return false }
func (r *DeepSeekTTSRequest) SetModelName(model string)     { r.Model = model }

func (r *DeepSeekTTSRequest) GetMeteredUsage() (string, int) {
	if r.WebSocket {
		return "character", MaxDeepSeekSpeechCharacters
	}
	return "character", DeepSeekSpeechCharacters(r.Text)
}

// DeepSeekSpeechCharacters counts submitted text, not provider-reported usage.
func DeepSeekSpeechCharacters(text string) int {
	count := 0
	for _, char := range text {
		count++
		if unicode.Is(unicode.Han, char) {
			count++
		}
	}
	return count
}
