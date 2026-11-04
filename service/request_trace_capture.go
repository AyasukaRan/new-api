package service

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Keep a small diagnostic preview in memory. Complete large payloads are
// spooled until the configured capture limit and consumed by the async writer.
const tracePreviewBytes = 1 << 20

// Include captures still streaming and captures queued for persistence.
var requestTraceSpoolSlots = make(chan struct{}, 64)

type traceCaptureBuffer struct {
	preview  *traceBuffer
	limit    int64
	file     *os.File
	filePath string
	fileSize int64
	failed   bool
}

func newTraceCaptureBuffer(limit int) *traceCaptureBuffer {
	limit = max(1024, min(limit, 32<<20))
	return &traceCaptureBuffer{preview: newTraceBuffer(min(limit, tracePreviewBytes)), limit: int64(limit)}
}

func (b *traceCaptureBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	defer b.preview.Write(p)
	if b.failed {
		return
	}
	if b.preview.total+int64(len(p)) > b.limit {
		b.failed = true
		b.Close()
		return
	}
	if b.file == nil && b.preview.total+int64(len(p)) > tracePreviewBytes {
		select {
		case requestTraceSpoolSlots <- struct{}{}:
		default:
			b.failed = true
			return
		}
		path, file, err := common.CreateDiskCacheFile(common.DiskCacheTypeBody)
		if err != nil {
			<-requestTraceSpoolSlots
			b.failed = true
			return
		}
		b.filePath, b.file = path, file
		common.SetDiskCacheFileActive(path, true)
		common.IncrementDiskFiles(0)
		// Before spilling, the preview still contains every original byte.
		for _, part := range [][]byte{b.preview.head, b.preview.tail} {
			n, err := file.Write(part)
			b.fileSize += int64(n)
			common.AddDiskCacheBytes(int64(n))
			if err != nil || n != len(part) {
				b.failed = true
				b.Close()
				return
			}
		}
	}
	if b.file != nil {
		n, err := b.file.Write(p)
		b.fileSize += int64(n)
		common.AddDiskCacheBytes(int64(n))
		if err != nil || n != len(p) {
			b.failed = true
			b.Close()
		}
	}
}

func (b *traceCaptureBuffer) Payload() (string, int64, bool) {
	body, size, truncated := b.preview.Payload()
	return body, size, truncated || b.failed
}

// CompletePayload must only run after capture finishes. A disk error or an
// over-limit body leaves the head/tail preview available, never a false claim
// that a partial payload is complete.
func (b *traceCaptureBuffer) CompletePayload() (string, bool) {
	if b.failed {
		return "", false
	}
	if b.file == nil {
		body, _, truncated := b.preview.Payload()
		return body, !truncated
	}
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(b.file, b.limit+1))
	if err != nil || int64(len(data)) != b.preview.total {
		return "", false
	}
	return normalizeTraceText(string(data)), true
}

func (b *traceCaptureBuffer) Close() {
	if b.file == nil {
		return
	}
	_ = b.file.Close()
	if err := os.Remove(b.filePath); err == nil || os.IsNotExist(err) {
		common.DecrementDiskFiles(b.fileSize)
	} else {
		common.SysError(fmt.Sprintf("failed to remove request trace temporary file: %v", err))
	}
	common.SetDiskCacheFileActive(b.filePath, false)
	<-requestTraceSpoolSlots
	b.file, b.filePath = nil, ""
}

func normalizeTraceText(payload string) string {
	payload = strings.ToValidUTF8(payload, "\uFFFD")
	return strings.ReplaceAll(payload, "\x00", "\uFFFD")
}
