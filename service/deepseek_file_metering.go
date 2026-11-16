package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// MeterDeepSeekFileUpload counts the original file without changing its bytes.
// Office documents are paginated in the isolated local office worker.
func MeterDeepSeekFileUpload(c *gin.Context, upload *DeepSeekFileUploadInfo) (string, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", err
	}
	reader, err := storage.NewReader()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	_, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	form := multipart.NewReader(reader, params["boundary"])
	for {
		part, err := form.NextPart()
		if errors.Is(err, io.EOF) {
			return "", errors.New("file is required")
		}
		if err != nil {
			return "", err
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		buffered := bufio.NewReader(part)
		prefix, _ := buffered.Peek(8)
		if strings.HasPrefix(string(prefix), "%PDF-") {
			upload.Pages, err = CountDeepSeekFilePages(c.Request.Context(), buffered, upload.Bytes)
			if errors.Is(err, ErrDeepSeekFilePagesUnavailable) {
				return "", types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponse, 503, types.ErrOptionWithSkipRetry())
			}
			return "document_pages", err
		}
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(part.FileName())), ".")
		switch format {
		case "doc", "docx", "ppt", "pptx":
		default:
			return "", errors.New("page counting currently requires PDF, Word or PowerPoint; use user_data for image input")
		}
		socket := strings.TrimSpace(os.Getenv("DEEPSEEK_DOCUMENT_PAGES_SOCKET"))
		if socket == "" {
			return "", types.NewErrorWithStatusCode(errors.New("local document pagination is not configured"), types.ErrorCodeBadResponse, 503, types.ErrOptionWithSkipRetry())
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
		defer cancel()
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		}, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://document-pages/pages?format="+format, buffered)
		if err != nil {
			return "", err
		}
		req.ContentLength = upload.Bytes
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := client.Do(req)
		if err != nil {
			return "", types.NewErrorWithStatusCode(errors.New("local document pagination could not complete"), types.ErrorCodeBadResponse, 503, types.ErrOptionWithSkipRetry())
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			status := http.StatusBadRequest
			if resp.StatusCode == 429 {
				status = 429
			} else if resp.StatusCode >= 500 {
				status = 503
			}
			return "", types.NewErrorWithStatusCode(fmt.Errorf("local document pagination failed (status %d)", resp.StatusCode), types.ErrorCodeBadResponse, status, types.ErrOptionWithSkipRetry())
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
		if err != nil || len(data) > 4096 {
			return "", errors.New("invalid pagination response")
		}
		var result struct {
			Pages  int    `json:"pages"`
			Engine string `json:"engine"`
			Basis  string `json:"basis"`
		}
		if common.Unmarshal(data, &result) != nil || result.Basis != "local_pagination" || result.Engine == "" || len(result.Engine) > 100 {
			return "", errors.New("invalid pagination response")
		}
		usage := billingexpr.MeteredUsage{Unit: billingexpr.BillingUnitPage, Count: result.Pages}
		if err = usage.Validate(); err != nil || result.Pages == 0 {
			return "", errors.New("document must contain between 1 and 1000 pages")
		}
		upload.Pages = result.Pages
		c.Set("deepseek_file_pagination_engine", result.Engine)
		return "local_pagination", nil
	}
}
