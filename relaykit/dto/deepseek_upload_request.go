package dto

import (
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// DeepSeekUploadRequest names a model-less multipart OCR/ASR operation for
// routing. Reserve the bounded provider maximum until measured usage arrives.
type DeepSeekUploadRequest struct {
	Model          string `json:"model"`
	Unit           string `json:"-"`
	EstimatedCount int    `json:"-"`
}

func (r *DeepSeekUploadRequest) GetTokenCountMeta() *types.TokenCountMeta {
	return &types.TokenCountMeta{TokenType: types.TokenTypeTokenizer}
}

func (r *DeepSeekUploadRequest) IsStream(_ *http.Request) bool { return false }

func (r *DeepSeekUploadRequest) SetModelName(model string) {
	if model != "" {
		r.Model = model
	}
}

func (r *DeepSeekUploadRequest) GetMeteredUsage() (string, int) {
	return r.Unit, r.EstimatedCount
}
