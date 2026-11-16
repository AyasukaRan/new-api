package controller

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func relayDeepSeekFileUpload(c *gin.Context, upload *service.DeepSeekFileUploadInfo) {
	if common.GetContextKeyInt(c, constant.ContextKeyChannelType) != constant.ChannelTypeDeepSeek {
		relayFileError(c, http.StatusBadRequest, "unsupported_channel", "file parsing requires a DeepSeek channel")
		return
	}
	info := relaycommon.GenRelayInfoOpenAI(c, &dto.GeneralOpenAIRequest{Model: "deepseek-files"})
	info.OriginModelName = "deepseek-files"
	info.InitChannelMeta(c)
	pricingBody, err := common.Marshal(map[string]any{"purpose": upload.Purpose})
	if err != nil {
		relayFileError(c, 500, "internal_error", "failed to prepare file pricing")
		return
	}
	info.BillingRequestInput = &billingexpr.RequestInput{Body: pricingBody}
	// Parse once at upload; later conversations pay tokens but no parsing fee.
	if upload.Purpose != "user_data" {
		basis, err := service.MeterDeepSeekFileUpload(c, upload)
		if err != nil {
			status := 400
			var apiErr *types.NewAPIError
			if errors.As(err, &apiErr) {
				status = apiErr.StatusCode
			}
			relayFileError(c, status, "page_count_failed", err.Error())
			return
		}
		c.Set("deepseek_file_usage_basis", basis)
		usage := &billingexpr.MeteredUsage{Unit: billingexpr.BillingUnitPage, Count: upload.Pages}
		info.BillingRequestInput.MeteredUsage = usage
		info.BillingMeteredUsage = usage
		price, err := helper.ModelPriceHelper(c, info, 0, &types.TokenCountMeta{})
		if err != nil {
			relayFileError(c, 400, "model_price_error", err.Error())
			return
		}
		if apiErr := service.PreConsumeBilling(c, price.QuotaToPreConsume, info); apiErr != nil {
			relayFileError(c, apiErr.StatusCode, "billing_failed", apiErr.Error())
			return
		}
	}
	settled := false
	submitted := false
	defer func() {
		if !settled && info.Billing != nil {
			info.Billing.Refund(c)
		}
		if submitted && !settled {
			status := c.Writer.Status()
			if status < 400 {
				status = 502
			}
			c.Set("deepseek_file_cost_unconfirmed", upload.Purpose != "user_data")
			c.Set("original_model", "deepseek-files")
			failure := types.NewErrorWithStatusCode(errors.New("file upload did not complete"), types.ErrorCodeBadResponse, status, types.ErrOptionWithSkipRetry())
			service.ProcessChannelError(c, types.ChannelError{ChannelId: info.ChannelId}, failure, info)
		}
	}()
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		relayFileError(c, 400, "invalid_request_error", "invalid upload")
		return
	}
	reader, err := storage.NewReader()
	if err != nil {
		relayFileError(c, 500, "internal_error", "failed to read upload")
		return
	}
	defer reader.Close()
	_, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	source := multipart.NewReader(reader, params["boundary"])
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	writer := multipart.NewWriter(pipeWriter)
	contentType := writer.FormDataContentType()
	go func() {
		var writeErr error
		defer func() { _ = pipeWriter.CloseWithError(writeErr) }()
		if writeErr = writer.WriteField("purpose", upload.Purpose); writeErr != nil {
			return
		}
		for {
			part, err := source.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeErr = err
				return
			}
			if part.FormName() == "purpose" || part.FormName() == "model" {
				continue
			}
			target, err := writer.CreatePart(part.Header)
			if err != nil {
				writeErr = err
				return
			}
			if _, writeErr = io.Copy(target, part); writeErr != nil {
				return
			}
		}
		writeErr = writer.Close()
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 260*time.Second)
	defer cancel()
	original := c.Request
	c.Request = original.WithContext(ctx)
	defer func() { c.Request = original }()
	submitted = true
	resp, err := relayFileUpstream(c, http.MethodPost, "/files", pipeReader, contentType, -1)
	if err != nil {
		relayFileError(c, 502, "upstream_request_failed", "file upload could not complete")
		return
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, relayFileMaxResponseBytes+1))
	if err != nil || len(payload) > relayFileMaxResponseBytes || !ojson.ValidBytes(payload) {
		relayFileError(c, 502, "upstream_read_failed", "invalid file response")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || ojson.GetBytes(payload, "error").Exists() {
		status := resp.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		relayFileError(c, status, "upstream_error", "provider file upload failed")
		return
	}
	id := ojson.GetBytes(payload, "id").String()
	if id == "" || len(id) > 191 {
		relayFileError(c, 502, "upstream_error", "provider returned no valid file id")
		return
	}
	channel := &model.Channel{Id: info.ChannelId}
	record := &model.RelayFile{FileId: "file-ds-" + common.GetUUID(), UpstreamFileId: id, Provider: "deepseek", UserId: info.UserId, ChannelId: info.ChannelId,
		KeyIdentity: channel.KeyIdentity(common.GetContextKeyString(c, constant.ContextKeyChannelKey)), Model: upload.Model, Purpose: upload.Purpose, Filename: upload.Filename, Bytes: upload.Bytes, ExpiresAt: ojson.GetBytes(payload, "expires_at").Int()}
	if err = record.Insert(); err != nil {
		if deleted, deleteErr := relayFileUpstream(c, http.MethodDelete, "/files/"+url.PathEscape(id), nil, "", 0); deleteErr == nil {
			deleted.Body.Close()
		}
		relayFileError(c, 500, "internal_error", "failed to record file ownership")
		return
	}
	if upload.Purpose != "user_data" {
		service.PostTextConsumeQuota(c, info, &dto.Usage{}, nil)
	}
	settled = true
	payload, err = deepSeekFilePublicJSON(c, payload, record.FileId)
	if err != nil {
		relayFileError(c, 500, "internal_error", "failed to encode file response")
		return
	}
	c.Data(resp.StatusCode, "application/json", payload)
}

// File IDs are resource identifiers. Only their owner-scoped aliases and the
// documented metadata leave the gateway; provider diagnostic IDs never do.
func deepSeekFilePublicJSON(c *gin.Context, payload []byte, alias string) ([]byte, error) {
	var upstream map[string]common.RawMessage
	if err := common.Unmarshal(payload, &upstream); err != nil {
		return nil, err
	}
	result := map[string]any{"id": alias}
	for _, field := range []string{"object", "bytes", "created_at", "filename", "purpose", "expires_at", "deleted"} {
		if value, ok := upstream[field]; ok {
			result[field] = value
		}
	}
	if strings.Contains(c.GetHeader("anthropic-beta"), "files-api-2025-04-14") {
		if ojson.GetBytes(payload, "deleted").Bool() {
			return common.Marshal(map[string]any{"id": alias, "type": "file_deleted"})
		}
		delete(result, "object")
		result["type"] = "file"
		result["size_bytes"] = result["bytes"]
		delete(result, "bytes")
		result["created_at"] = time.Unix(ojson.GetBytes(payload, "created_at").Int(), 0).UTC().Format(time.RFC3339)
		result["mime_type"] = mime.TypeByExtension(filepath.Ext(ojson.GetBytes(payload, "filename").String()))
	}
	return common.Marshal(result)
}

func relayDeepSeekFileMetadata(c *gin.Context, file *model.RelayFile, method string) {
	if file.UpstreamFileId == "" || file.KeyIdentity == "" {
		relayFileError(c, 404, "not_found", "file not found")
		return
	}
	resp, err := relayFileUpstream(c, method, "/files/"+url.PathEscape(file.UpstreamFileId), nil, "", 0)
	if err != nil {
		relayFileError(c, 502, "upstream_request_failed", "file request could not complete")
		return
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, relayFileMaxResponseBytes+1))
	if err != nil || len(payload) > relayFileMaxResponseBytes || !ojson.ValidBytes(payload) {
		relayFileError(c, 502, "upstream_read_failed", "invalid file response")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || ojson.GetBytes(payload, "error").Exists() {
		status := resp.StatusCode
		if status < 400 {
			status = 502
		}
		relayFileError(c, status, "upstream_error", "provider file request failed")
		return
	}
	if method == http.MethodDelete {
		if !ojson.GetBytes(payload, "deleted").Bool() {
			relayFileError(c, 502, "upstream_error", "provider did not confirm file deletion")
			return
		}
		if err = model.DeleteRelayFile(file.UserId, file.FileId); err != nil {
			relayFileError(c, 500, "internal_error", "failed to remove file record")
			return
		}
	}
	payload, err = deepSeekFilePublicJSON(c, payload, file.FileId)
	if err != nil {
		relayFileError(c, 502, "upstream_read_failed", "invalid file response")
		return
	}
	c.Data(resp.StatusCode, "application/json", payload)
}
