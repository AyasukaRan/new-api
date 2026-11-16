package service

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

type DeepSeekFileUploadInfo struct {
	Model        string
	Purpose      string
	Filename     string
	Bytes        int64
	ExpiresAfter int64
	Pages        int
}

// InspectDeepSeekFileUpload reads metadata without materializing uploaded bytes.
// Batch purpose retains the established JSONL upload path.
func InspectDeepSeekFileUpload(c *gin.Context) (*DeepSeekFileUploadInfo, error) {
	_, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return nil, errors.New("multipart boundary is required")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	reader, err := storage.NewReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	form := multipart.NewReader(reader, params["boundary"])
	info := &DeepSeekFileUploadInfo{Model: "deepseek-flash"}
	fields := map[string]string{}
	var validationErr error
	for count := 0; ; count++ {
		part, readErr := form.NextPart()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if count >= 16 {
			return nil, errors.New("too many upload fields")
		}
		if part.FileName() != "" {

			if part.FormName() != "file" || info.Filename != "" {
				validationErr = errors.New("exactly one file is required")
			}
			info.Filename = part.FileName()
			info.Bytes, err = io.Copy(io.Discard, io.LimitReader(part, (128<<20)+1))
			if err != nil {
				return nil, err
			}
			if info.Bytes == 0 || info.Bytes > 100<<20 {
				validationErr = errors.New("file must be between 1 byte and 100 MiB")
			}
			continue
		}
		name := part.FormName()
		if _, exists := fields[name]; exists {
			if name == "purpose" {
				return nil, errors.New("duplicate upload purpose")
			}
			validationErr = fmt.Errorf("duplicate upload field %q", name)
		}
		value, err := io.ReadAll(io.LimitReader(part, 4097))
		if err != nil || len(value) > 4096 {
			return nil, errors.New("invalid upload field")
		}
		fields[name] = string(value)
		switch name {
		case "purpose":
			info.Purpose = string(value)

			if info.Purpose != "batch" && info.Purpose != "user_data" && info.Purpose != "text_recognition" && info.Purpose != "rich_layout_recognition" {
				validationErr = errors.New("unsupported file purpose")
			}
		case "model":
			info.Model = strings.TrimSpace(string(value))
		case "expires_after[anchor]", "expires_after[seconds]":
		default:
			validationErr = fmt.Errorf("unsupported upload field %q", name)
		}
	}
	if info.Purpose == "batch" {
		return nil, nil
	}
	if info.Purpose == "" {
		if strings.Contains(c.GetHeader("anthropic-beta"), "files-api-2025-04-14") {
			info.Purpose = "user_data"
		} else {
			return nil, nil
		}
	}
	if validationErr != nil {
		return nil, validationErr
	}
	if utf8.RuneCountInString(info.Filename) > 255 {
		return nil, errors.New("file name must not exceed 255 characters")
	}
	if info.Filename == "" || info.Model == "" {
		return nil, errors.New("purpose, model and file are required")
	}
	anchor, hasAnchor := fields["expires_after[anchor]"]
	seconds, hasSeconds := fields["expires_after[seconds]"]
	if hasAnchor || hasSeconds {
		info.ExpiresAfter, err = strconv.ParseInt(seconds, 10, 64)
		if !hasAnchor || !hasSeconds || anchor != "created_at" || err != nil || info.ExpiresAfter < 3600 || info.ExpiresAfter > 2592000 {
			return nil, errors.New("expires_after requires created_at and 3600 to 2592000 seconds")
		}
	}
	return info, nil
}
