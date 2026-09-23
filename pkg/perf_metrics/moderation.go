package perfmetrics

import (
	"errors"
	"net/http"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// IsContentModerationError identifies a rejected request, which provides no
// availability outcome. Keep transport, credential, quota and service failures
// as failures even if their diagnostics mention a content filter.
func IsContentModerationError(apiErr *types.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusOK, http.StatusBadRequest, http.StatusForbidden, http.StatusUnprocessableEntity:
	default:
		return false
	}
	upstream := apiErr.ToOpenAIError()
	for _, code := range []string{string(apiErr.GetErrorCode()), upstream.Type} {
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "content_filter", "content_filter_error", "content_policy_violation", "responsibleaipolicyviolation", "sensitive_words_detected", "prompt_blocked":
			return true
		}
	}
	// iFlytek's generic 400 does not carry a dedicated moderation code. Match
	// its explicit rejection wording rather than broad terms such as "safety".
	return apiErr.StatusCode != http.StatusOK &&
		strings.Contains(apiErr.Error(), "根据相关法律法规") &&
		strings.Contains(apiErr.Error(), "有关信息不予显示")
}

// Stream handlers can both stop with an error and return that same error. Stop
// records one stream error itself; additional errors indicate a mixed failure.
func isContentModerationStreamStop(status *relaycommon.StreamStatus) bool {
	if status == nil || status.EndReason != relaycommon.StreamEndReasonHandlerStop || status.TotalErrorCount() > 1 {
		return false
	}
	var apiErr *types.NewAPIError
	return errors.As(status.EndError, &apiErr) && IsContentModerationError(apiErr)
}
