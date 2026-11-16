package helper

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

func GetAndValidateDeepSeekUploadRequest(c *gin.Context, mode int) (*dto.DeepSeekUploadRequest, error) {
	request := &dto.DeepSeekUploadRequest{Model: constant.DeepSeekOCRModel, Unit: "page", EstimatedCount: 1000}
	maxFileSize := int64(100 << 20)
	if mode == relayconstant.RelayModeToolsASR {
		request.Model, request.Unit, request.EstimatedCount = constant.DeepSeekASRModel, "second", 120
		maxFileSize = 8 << 20
	} else if mode != relayconstant.RelayModeToolsOCR {
		return nil, errors.New("unsupported DeepSeek upload operation")
	}
	mediaType, params, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, errors.New("multipart/form-data with a boundary is required")
	}
	if c.Request.ContentLength > maxFileSize+(1<<20) {
		return nil, common.ErrRequestBodyTooLarge
	}
	if _, cached := c.Get(common.KeyBodyStorage); !cached {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxFileSize+(1<<20))
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	// Bound multipart overhead too, without loading a 100 MiB document into RAM.
	if storage.Size() > maxFileSize+(1<<20) {
		return nil, common.ErrRequestBodyTooLarge
	}
	reader, err := storage.NewReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	parts := multipart.NewReader(reader, params["boundary"])
	fileSeen, purposeSeen := false, false
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid multipart upload: %w", err)
		}
		switch part.FormName() {
		case "file":
			if fileSeen || part.FileName() == "" {
				return nil, errors.New("exactly one named file is required")
			}
			fileSeen = true
			// DeepSeek detects supported formats from content, not the filename suffix.
			size, err := io.Copy(io.Discard, io.LimitReader(part, maxFileSize+1))
			if err != nil {
				return nil, fmt.Errorf("failed to read uploaded file: %w", err)
			}
			if size > maxFileSize {
				return nil, common.ErrRequestBodyTooLarge
			}
			if size == 0 {
				return nil, errors.New("file must not be empty")
			}
		case "purpose":
			if mode != relayconstant.RelayModeToolsOCR || purposeSeen || part.FileName() != "" {
				return nil, errors.New("unexpected or repeated purpose field")
			}
			purposeSeen = true
			purpose, err := io.ReadAll(io.LimitReader(part, 128))
			if err != nil {
				return nil, err
			}
			if string(purpose) != "text_recognition" && string(purpose) != "rich_layout_recognition" {
				return nil, errors.New("purpose must be text_recognition or rich_layout_recognition")
			}
		default:
			return nil, errors.New("unsupported multipart field for this DeepSeek operation")
		}
		if err := part.Close(); err != nil {
			return nil, err
		}
	}
	if !fileSeen || (mode == relayconstant.RelayModeToolsOCR && !purposeSeen) {
		return nil, errors.New("file and OCR purpose are required")
	}
	return request, nil
}
