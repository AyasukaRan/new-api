package service

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"golang.org/x/sync/semaphore"
)

// Keep a small diagnostic preview in memory. Complete large payloads are
// spooled until the configured capture limit and consumed by the async writer.
const tracePreviewBytes = 1 << 20

// Include captures still streaming and captures queued for persistence.
var requestTraceSpoolSlots = make(chan struct{}, 64)

// Account for growing buffers while requests are still streaming as well as
// while waiting for a writer. A large body never draws on the small-body pool.
var (
	traceSmallBuffers      = semaphore.NewWeighted(96 << 20)
	traceLargeBuffers      = semaphore.NewWeighted(128 << 20)
	traceEmergencyPreviews = make(chan struct{}, 4096) // at most 8 MiB of overloaded previews
)

type traceBufferReservation int

func traceBufferPool(size int) *semaphore.Weighted {
	if size <= traceSmallJobBytes {
		return traceSmallBuffers
	}
	return traceLargeBuffers
}

func (r *traceBufferReservation) grow(want, limit int) bool {
	old := int(*r)
	if want <= old {
		return true
	}
	next := min(limit, max(want, 2*old, 256))
	if want <= traceSmallJobBytes {
		next = min(next, traceSmallJobBytes)
	}
	pool := traceBufferPool(next)
	if pool == traceBufferPool(old) {
		if !pool.TryAcquire(int64(next - old)) {
			return false
		}
	} else {
		if !pool.TryAcquire(int64(next)) {
			return false
		}
		r.release()
	}
	*r = traceBufferReservation(next)
	return true
}

func (r *traceBufferReservation) release() {
	if *r != 0 {
		traceBufferPool(int(*r)).Release(int64(*r))
		*r = 0
	}
}

type traceCaptureBuffer struct {
	preview   *traceBuffer
	memory    traceBufferReservation
	emergency bool
	limit     int64
	file      *os.File
	filePath  string
	fileSize  int64
	failed    bool
}

func newTraceCaptureBuffer(limit int) *traceCaptureBuffer {
	limit = max(1024, min(limit, 32<<20))
	return &traceCaptureBuffer{preview: newTraceBuffer(min(limit, tracePreviewBytes)), limit: int64(limit)}
}

func (b *traceCaptureBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	limit := b.preview.headCap + b.preview.tailCap
	want := int(min(int64(limit), b.preview.total+int64(len(p))))
	if !b.emergency && !b.memory.grow(want, limit) {
		// A rejected large buffer releases its small-body reservation as it
		// degrades. Keep at most 2 KiB from a separate emergency budget.
		old := b.preview
		b.memory.release()
		size := 0
		select {
		case traceEmergencyPreviews <- struct{}{}:
			b.emergency = true
			size = min(2<<10, limit)
		default:
		}
		b.preview = &traceBuffer{headCap: size / 2, tailCap: size - size/2, allocationLimit: size}
		b.preview.Write(old.head)
		b.preview.Write(old.tail)
		b.preview.total = old.total
		b.failed = true
		b.closeSpool()
	}
	if !b.emergency {
		b.preview.allocationLimit = int(b.memory)
	}
	defer b.preview.Write(p)
	if b.failed {
		return
	}
	if b.preview.total+int64(len(p)) > b.limit {
		b.failed = true
		b.closeSpool()
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
				b.closeSpool()
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
			b.closeSpool()
		}
	}
}

func (b *traceCaptureBuffer) Payload() (string, int64, bool) {
	return b.payload(nil)
}

func (b *traceCaptureBuffer) payload(redactor *requestTraceCollector) (string, int64, bool) {
	body, size, truncated := b.preview.payload(redactor)
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
	b.closeSpool()
	b.preview.head, b.preview.tail = nil, nil
	b.memory.release()
	if b.emergency {
		<-traceEmergencyPreviews
		b.emergency = false
	}
}

func (b *traceCaptureBuffer) closeSpool() {
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
