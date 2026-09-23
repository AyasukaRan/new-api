package service

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A batch input file is JSON Lines, one chat request per line, and the provider
// requires every line to name the same model. That model is also the only thing
// in the upload that can pick a channel, so it is read here rather than guessed.
const (
	// batchLineScanLimit bounds one line. The provider's own per-request body
	// limit is far below this; anything larger is a malformed upload, and
	// without a bound a file with no newline would be read entirely.
	batchLineScanLimit = 1 << 20
	// batchModelScanLines caps how far the scan looks for the model. Lines are
	// only read until one carries a model, so a well-formed file costs one line.
	batchModelScanLines = 16
)

var errBatchMultipartBoundary = errors.New("multipart boundary not found")

// BatchUploadModel reports the model named inside a multipart file upload.
//
// An explicit `model` form field wins so a client can address a channel
// directly; otherwise the model is read from the first line of the uploaded
// JSONL. Only the leading lines are read, so a 100MB upload is never
// materialized to answer this question.
func BatchUploadModel(c *gin.Context) (string, error) {
	mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return "", errBatchMultipartBoundary
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", errBatchMultipartBoundary
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", err
	}
	if _, err = storage.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	defer func() { _, _ = storage.Seek(0, io.SeekStart) }()

	reader := multipart.NewReader(storage, boundary)
	fileModel := ""
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			return "", fmt.Errorf("read upload: %w", partErr)
		}
		if part.FileName() == "" {
			if part.FormName() == "model" {
				value, readErr := io.ReadAll(io.LimitReader(part, batchLineScanLimit))
				_ = part.Close()
				if readErr != nil {
					return "", fmt.Errorf("read upload: %w", readErr)
				}
				// An explicit field is authoritative; stop reading the upload.
				if model := strings.TrimSpace(string(value)); model != "" {
					return model, nil
				}
				continue
			}
			_ = part.Close()
			continue
		}
		if fileModel == "" {
			fileModel = batchFirstLineModel(part)
		}
		_ = part.Close()
	}
	if fileModel == "" {
		return "", errors.New("the uploaded file names no model; every line of a batch input file must carry the same \"model\"")
	}
	return fileModel, nil
}

// batchFirstLineModel returns the model of the first line that declares one.
// A parse failure is not an error here: the upload is still relayed, and the
// provider owns the verdict on whether the file is well formed.
func batchFirstLineModel(part io.Reader) string {
	scanner := bufio.NewScanner(part)
	scanner.Buffer(make([]byte, 0, 64<<10), batchLineScanLimit)
	for line := 0; line < batchModelScanLines && scanner.Scan(); line++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if model := gjson.Get(raw, "body.model").String(); model != "" {
			return model
		}
		if model := gjson.Get(raw, "model").String(); model != "" {
			return model
		}
	}
	return ""
}

// BatchUploadForUpstream returns the body to relay and its content type.
//
// A batch file names its model on every line, and the provider only knows the
// upstream name — the one a channel's model_mapping renames to. Nothing else
// applies that rename on this path: a chat request gets it from the request
// body, but an uploaded file is otherwise copied byte for byte, so the client's
// own name would reach the provider and be rejected hours later, per line.
//
// When no rename applies the original body is streamed untouched, which is the
// ordinary case and costs nothing.
func BatchUploadForUpstream(c *gin.Context, upstreamModel string) (io.Reader, string, int64, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, "", 0, err
	}
	if _, err = storage.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, err
	}
	contentType := c.GetHeader("Content-Type")
	if strings.TrimSpace(upstreamModel) == "" {
		return storage, contentType, c.Request.ContentLength, nil
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return nil, "", 0, errBatchMultipartBoundary
	}

	reader := multipart.NewReader(storage, params["boundary"])
	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	go func() {
		// CloseWithError(nil) behaves as a plain Close, so one deferred call
		// covers both the success and failure paths.
		var walkErr error
		defer func() { _ = pipeWriter.CloseWithError(walkErr) }()
		walkErr = rewriteBatchParts(reader, writer, upstreamModel)
		if walkErr == nil {
			walkErr = writer.Close()
		}
	}()
	// The rewritten length is not known until the whole file has been read, so
	// the request goes out chunked rather than buffering it to measure.
	return pipeReader, writer.FormDataContentType(), -1, nil
}

func rewriteBatchParts(reader *multipart.Reader, writer *multipart.Writer, upstreamModel string) error {
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if part.FileName() == "" {
			field, createErr := writer.CreateFormField(part.FormName())
			if createErr != nil {
				return createErr
			}
			if _, err = io.Copy(field, part); err != nil {
				return err
			}
			_ = part.Close()
			continue
		}
		file, createErr := writer.CreateFormFile(part.FormName(), part.FileName())
		if createErr != nil {
			return createErr
		}
		scanner := bufio.NewScanner(part)
		scanner.Buffer(make([]byte, 0, 64<<10), batchLineScanLimit)
		for scanner.Scan() {
			if _, err = io.WriteString(file, rewriteBatchLine(scanner.Text(), upstreamModel)+"\n"); err != nil {
				return err
			}
		}
		if err = scanner.Err(); err != nil {
			return err
		}
		_ = part.Close()
	}
}

// rewriteBatchLine renames the model on one line. A line that does not parse is
// passed through untouched: the provider owns the verdict on a malformed file,
// and silently dropping a line would be worse than relaying it.
func rewriteBatchLine(line string, upstreamModel string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	path := ""
	switch {
	case gjson.Get(line, "body.model").Exists():
		path = "body.model"
	case gjson.Get(line, "model").Exists():
		path = "model"
	default:
		return line
	}
	rewritten, err := sjson.Set(line, path, upstreamModel)
	if err != nil {
		return line
	}
	return rewritten
}
