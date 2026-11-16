package controller

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// The Files API is relayed rather than reimplemented: the provider owns the
// storage, the retention window, and the format checks. The gateway adds the
// one thing a shared provider account cannot provide — per-user ownership, so
// one tenant's batch input is not readable by every other tenant of the same
// channel key.

// relayFileMaxResponseBytes bounds a relayed control-plane response. These
// endpoints return small JSON envelopes; the download path streams instead.
const relayFileMaxResponseBytes = 1 << 20

func relayFileError(c *gin.Context, status int, code string, message string) {
	c.JSON(status, gin.H{"error": types.OpenAIError{
		Message: message,
		Type:    "new_api_error",
		Code:    code,
	}})
}

// relayFileUpstream forwards one request to the channel selected for this call
// and returns the upstream response with its body still open.
func relayFileUpstream(c *gin.Context, method string, path string, body io.Reader, contentType string, contentLength int64) (*http.Response, error) {
	baseURL := strings.TrimSuffix(common.GetContextKeyString(c, constant.ContextKeyChannelBaseUrl), "/")
	if baseURL == "" && common.GetContextKeyInt(c, constant.ContextKeyChannelType) == constant.ChannelTypeDeepSeek {
		baseURL = "https://api.deepseek.com"
	}
	if baseURL == "" {
		return nil, fmt.Errorf("the selected channel has no base url")
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+common.GetContextKeyString(c, constant.ContextKeyChannelKey))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// A negative length means the body is being rewritten as it streams and its
	// size is not known yet, so the request goes out chunked.
	if contentLength > 0 && body != nil {
		req.ContentLength = contentLength
	}

	channelSetting, _ := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
	client, err := service.GetHttpClientWithProxySettings(channelSetting.Proxy, channelSetting)
	if err != nil {
		return nil, fmt.Errorf("build http client: %w", err)
	}
	logger.LogDebug(c, "relay file request: %s", relaycommon.SanitizeURLForLog(req.URL.String()))
	return client.Do(req)
}

// relayFileForwardJSON copies a small upstream JSON response to the client and
// returns the decoded body so the caller can record what the provider issued.
func relayFileForwardJSON(c *gin.Context, resp *http.Response) ([]byte, bool) {
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, relayFileMaxResponseBytes))
	if err != nil {
		relayFileError(c, http.StatusBadGateway, "upstream_read_failed", "failed to read the provider response")
		return nil, false
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, payload)
	return payload, resp.StatusCode >= 200 && resp.StatusCode < 300
}

// RelayFileUpload relays a file upload and records who owns the result.
func RelayFileUpload(c *gin.Context) {
	if value, ok := c.Get("deepseek_file_upload"); ok {
		if upload, ok := value.(*service.DeepSeekFileUploadInfo); ok {
			relayDeepSeekFileUpload(c, upload)
			return
		}
	}
	clientModel := c.GetString("original_model")
	// The channel may serve this model under another name. Nothing else on this
	// path applies that rename, because the file is otherwise copied verbatim.
	upstreamModel, mapped, err := relayhelper.ResolveMappedModel(
		common.GetContextKeyString(c, constant.ContextKeyChannelModelMapping), clientModel)
	if err != nil {
		relayFileError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	rewriteTo := ""
	if mapped {
		rewriteTo = upstreamModel
	}
	body, contentType, contentLength, err := service.BatchUploadForUpstream(c, rewriteTo)
	if err != nil {
		relayFileError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	resp, err := relayFileUpstream(c, http.MethodPost, "/v1/files", body, contentType, contentLength)
	if err != nil {
		relayFileError(c, http.StatusBadGateway, "upstream_request_failed", err.Error())
		return
	}
	payload, ok := relayFileForwardJSON(c, resp)
	if !ok {
		return
	}
	fileId := gjson.GetBytes(payload, "id").String()
	if fileId == "" {
		// The upload succeeded upstream but the gateway cannot attribute it, so
		// the id will never resolve again. Say so rather than leave a caller
		// holding an id that every later call rejects.
		logger.LogError(c, "file upload succeeded but the provider returned no id")
		return
	}
	record := &model.RelayFile{
		FileId:    fileId,
		UserId:    c.GetInt("id"),
		ChannelId: common.GetContextKeyInt(c, constant.ContextKeyChannelId),
		Model:     clientModel,
		Purpose:   gjson.GetBytes(payload, "purpose").String(),
		Filename:  gjson.GetBytes(payload, "filename").String(),
		Bytes:     gjson.GetBytes(payload, "bytes").Int(),
	}
	if err = record.Insert(); err != nil {
		logger.LogError(c, "failed to record uploaded file "+fileId+": "+err.Error())
	}
}

// RelayFileList answers from the gateway's own records.
//
// The provider's list endpoint is deliberately not relayed: it enumerates every
// file of the account behind the channel key, which on a shared gateway means
// every other tenant's uploads.
func RelayFileList(c *gin.Context) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			relayFileError(c, 400, "invalid_request_error", "limit must be between 1 and 1000")
			return
		}
		limit = parsed
	} else if raw := c.Query("size"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}
	order := c.DefaultQuery("order", "desc")
	after, before := c.Query("after"), c.Query("before")
	anthropic := strings.Contains(c.GetHeader("anthropic-beta"), "files-api-2025-04-14")
	if anthropic {
		after, before = c.Query("after_id"), c.Query("before_id")
	}
	files, more, err := model.ListRelayFilesPage(c.GetInt("id"), c.Query("purpose"), after, before, order, limit)
	if err != nil {
		relayFileError(c, 400, "invalid_request_error", "invalid file pagination")
		return
	}
	data := make([]gin.H, 0, len(files))
	for _, file := range files {
		item := gin.H{"id": file.FileId, "object": "file", "bytes": file.Bytes, "created_at": file.CreatedAt, "filename": file.Filename, "purpose": file.Purpose}
		if file.ExpiresAt > 0 {
			item["expires_at"] = file.ExpiresAt
		}
		if anthropic {
			delete(item, "object")
			delete(item, "bytes")
			item["type"] = "file"
			item["size_bytes"] = file.Bytes
			item["created_at"] = time.Unix(file.CreatedAt, 0).UTC().Format(time.RFC3339)
			item["mime_type"] = mime.TypeByExtension(filepath.Ext(file.Filename))
		}
		data = append(data, item)
	}
	first, last := "", ""
	if len(files) > 0 {
		first, last = files[0].FileId, files[len(files)-1].FileId
	}
	result := gin.H{"object": "list", "data": data, "first_id": first, "last_id": last, "has_more": more}
	if anthropic {
		delete(result, "object")
	}
	c.JSON(http.StatusOK, result)
}

// RelayFileRetrieve relays a metadata read for a file the caller owns.
func RelayFileRetrieve(c *gin.Context) {
	file := middleware.RelayFileRecord(c)
	if file == nil {
		relayFileError(c, http.StatusNotFound, "not_found", "no such file")
		return
	}
	if file.Provider == "deepseek" {
		relayDeepSeekFileMetadata(c, file, http.MethodGet)
		return
	}
	resp, err := relayFileUpstream(c, http.MethodGet, "/v1/files/"+file.FileId, nil, "", 0)
	if err != nil {
		relayFileError(c, http.StatusBadGateway, "upstream_request_failed", err.Error())
		return
	}
	relayFileForwardJSON(c, resp)
}

// RelayFileDelete relays a delete and drops the local record with it.
func RelayFileDelete(c *gin.Context) {
	file := middleware.RelayFileRecord(c)
	if file == nil {
		relayFileError(c, http.StatusNotFound, "not_found", "no such file")
		return
	}
	if file.Provider == "deepseek" {
		relayDeepSeekFileMetadata(c, file, http.MethodDelete)
		return
	}
	resp, err := relayFileUpstream(c, http.MethodDelete, "/v1/files/"+file.FileId, nil, "", 0)
	if err != nil {
		relayFileError(c, http.StatusBadGateway, "upstream_request_failed", err.Error())
		return
	}
	_, ok := relayFileForwardJSON(c, resp)
	if !ok {
		return
	}
	// The local row only grants access to bytes that no longer exist, so it is
	// dropped even if the provider's own bookkeeping lags.
	if err = model.DeleteRelayFile(file.UserId, file.FileId); err != nil {
		logger.LogError(c, "failed to drop file record "+file.FileId+": "+err.Error())
	}
}

// RelayFileContent streams a file's bytes back to the caller. Batch outputs are
// large, so the body is piped rather than buffered.
func RelayFileContent(c *gin.Context) {
	file := middleware.RelayFileRecord(c)
	if file == nil {
		relayFileError(c, http.StatusNotFound, "not_found", "no such file")
		return
	}
	if file.Provider == "deepseek" {
		relayFileError(c, 404, "not_found", "file downloads are not supported by this provider")
		return
	}
	resp, err := relayFileUpstream(c, http.MethodGet, "/v1/files/"+file.FileId+"/content", nil, "", 0)
	if err != nil {
		relayFileError(c, http.StatusBadGateway, "upstream_request_failed", err.Error())
		return
	}
	defer resp.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
		if value := resp.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	c.Status(resp.StatusCode)
	if _, err = io.Copy(c.Writer, resp.Body); err != nil {
		logger.LogError(c, "failed to stream file "+file.FileId+": "+err.Error())
	}
}
