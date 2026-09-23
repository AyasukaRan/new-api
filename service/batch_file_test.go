package service

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchUploadContext builds the multipart upload a client would send.
func batchUploadContext(t *testing.T, fields map[string]string, filename string, content string) *gin.Context {
	t.Helper()
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	for name, value := range fields {
		require.NoError(t, writer.WriteField(name, value))
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = part.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	request := httptest.NewRequest("POST", "/v1/files", bytes.NewReader(payload.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	return context
}

func TestBatchUploadModelReadsTheModelWithoutConsumingTheUpload(t *testing.T) {
	t.Run("the model comes from the first line of the input file", func(t *testing.T) {
		context := batchUploadContext(t, map[string]string{"purpose": "batch"}, "batch.jsonl",
			`{"custom_id":"1","method":"POST","url":"/v1/chat/completions","body":{"model":"4.0Ultra","messages":[]}}`+"\n"+
				`{"custom_id":"2","method":"POST","url":"/v1/chat/completions","body":{"model":"4.0Ultra","messages":[]}}`)
		model, err := BatchUploadModel(context)
		require.NoError(t, err)
		assert.Equal(t, "4.0Ultra", model)
	})

	t.Run("an explicit form field addresses a channel directly", func(t *testing.T) {
		context := batchUploadContext(t, map[string]string{"purpose": "batch", "model": "generalv3.5"}, "batch.jsonl",
			`{"body":{"model":"4.0Ultra"}}`)
		model, err := BatchUploadModel(context)
		require.NoError(t, err)
		assert.Equal(t, "generalv3.5", model)
	})

	t.Run("blank leading lines are skipped", func(t *testing.T) {
		context := batchUploadContext(t, nil, "batch.jsonl", "\n\n"+`{"body":{"model":"lite"}}`)
		model, err := BatchUploadModel(context)
		require.NoError(t, err)
		assert.Equal(t, "lite", model)
	})

	t.Run("a file that names no model is refused", func(t *testing.T) {
		// Relaying this would pick a channel at random, and the provider would
		// only reject it once the whole upload had crossed the wire.
		context := batchUploadContext(t, nil, "batch.jsonl", `{"custom_id":"1"}`)
		_, err := BatchUploadModel(context)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model")
	})

	t.Run("a request that is not multipart is refused", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/v1/files", strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		_, err := BatchUploadModel(context)
		require.ErrorIs(t, err, errBatchMultipartBoundary)
	})

	t.Run("the upload is still readable in full afterwards", func(t *testing.T) {
		// The handler relays the same bytes upstream, so reading the model must
		// not consume them.
		body := `{"body":{"model":"max-32k"}}`
		context := batchUploadContext(t, map[string]string{"purpose": "batch"}, "batch.jsonl", body)
		_, err := BatchUploadModel(context)
		require.NoError(t, err)

		storage, err := common.GetBodyStorage(context)
		require.NoError(t, err)
		replayed, err := storage.Bytes()
		require.NoError(t, err)
		assert.Contains(t, string(replayed), body)
		assert.Contains(t, string(replayed), `name="purpose"`)
	})
}

// A channel may serve a model under another name. A chat request gets that
// rename from its body; an uploaded batch file is otherwise copied byte for
// byte, so without this the client's own name reaches the provider and every
// line is rejected hours later.
func TestBatchUploadRenamesTheModelForTheChannelThatServesIt(t *testing.T) {
	readUpload := func(t *testing.T, body io.Reader, contentType string) (map[string][]string, string) {
		t.Helper()
		_, params, err := mime.ParseMediaType(contentType)
		require.NoError(t, err)
		reader := multipart.NewReader(body, params["boundary"])
		fields := map[string][]string{}
		file := ""
		for {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				break
			}
			require.NoError(t, partErr)
			content, readErr := io.ReadAll(part)
			require.NoError(t, readErr)
			if part.FileName() == "" {
				fields[part.FormName()] = append(fields[part.FormName()], string(content))
				continue
			}
			file = string(content)
		}
		return fields, file
	}

	lines := `{"custom_id":"1","method":"POST","url":"/v1/chat/completions","body":{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}}
{"custom_id":"2","method":"POST","url":"/v1/chat/completions","body":{"model":"deepseek-v4-pro","messages":[]}}`

	t.Run("every line is renamed and the rest of the line is kept", func(t *testing.T) {
		context := batchUploadContext(t, map[string]string{"purpose": "batch"}, "batch.jsonl", lines)
		body, contentType, contentLength, err := BatchUploadForUpstream(context, "xopdeepseekv4pro")
		require.NoError(t, err)
		assert.EqualValues(t, -1, contentLength, "a rewritten body has no known length")

		fields, file := readUpload(t, body, contentType)
		assert.Equal(t, []string{"batch"}, fields["purpose"], "other form fields must survive the rewrite")
		assert.Equal(t, 2, strings.Count(file, `"model":"xopdeepseekv4pro"`))
		assert.NotContains(t, file, "deepseek-v4-pro\"")
		assert.Contains(t, file, `"custom_id":"1"`)
		assert.Contains(t, file, `"role":"user"`)
	})

	t.Run("an unmapped channel streams the upload untouched", func(t *testing.T) {
		// The ordinary case must not pay for a rewrite it does not need.
		context := batchUploadContext(t, map[string]string{"purpose": "batch"}, "batch.jsonl", lines)
		body, contentType, contentLength, err := BatchUploadForUpstream(context, "")
		require.NoError(t, err)
		assert.Equal(t, context.GetHeader("Content-Type"), contentType)
		assert.Equal(t, context.Request.ContentLength, contentLength)
		_, file := readUpload(t, body, contentType)
		assert.Contains(t, file, `"model":"deepseek-v4-pro"`)
	})

	t.Run("a line that is not JSON is relayed rather than dropped", func(t *testing.T) {
		// The provider owns the verdict on a malformed file; silently losing a
		// line would be worse than forwarding it.
		context := batchUploadContext(t, nil, "batch.jsonl", "not json\n"+lines)
		body, contentType, _, err := BatchUploadForUpstream(context, "xopdeepseekv4pro")
		require.NoError(t, err)
		_, file := readUpload(t, body, contentType)
		assert.Contains(t, file, "not json")
		assert.Equal(t, 2, strings.Count(file, `"model":"xopdeepseekv4pro"`))
	})
}
