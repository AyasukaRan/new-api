package model

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm/clause"
)

const (
	requestTraceChunksEncoding = "chunks_v1"
	requestTraceInlineBytes    = 4 << 10
	requestTraceChunkMinBytes  = 2 << 10
	requestTraceChunkMaxBytes  = 32 << 10
	requestTraceBodyMaxBytes   = 32 << 20
	requestTraceBucketSeconds  = int64(24 * 60 * 60)
	requestTraceChunkBatch     = 128
)

// Chunks are immutable and scoped to one user and UTC day. Every trace keeps
// its own complete manifest, so deleting an earlier conversation cannot break it.
type RequestTracePayloadChunk struct {
	Owner  string `gorm:"type:varchar(72);primaryKey"`
	Bucket int64  `gorm:"type:bigint;primaryKey;autoIncrement:false;index"`
	Digest string `gorm:"type:varchar(64);primaryKey"`
	Size   int
	Data   string `gorm:"type:text"`
}

type requestTraceBodyManifest struct {
	Owner  string   `json:"owner"`
	Bucket int64    `json:"bucket"`
	Size   int      `json:"size"`
	Digest string   `json:"digest"`
	Chunks []string `json:"chunks"`
}

// Gear hashing finds boundaries from content, letting an inserted message
// resynchronize with unchanged history instead of shifting every later chunk.
var requestTraceGear = func() [256]uint64 {
	var table [256]uint64
	for i := range table {
		value := uint64(i) + 0x9e3779b97f4a7c15
		value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
		value = (value ^ (value >> 27)) * 0x94d049bb133111eb
		table[i] = value ^ (value >> 31)
	}
	return table
}()

// StoreRequestTraceBody persists sanitized text before publishing its manifest.
// A failed insert leaves the caller's fallback body unchanged; orphaned chunks
// are reclaimed with their day bucket, without cross-node reference counting.
func StoreRequestTraceBody(trace *RequestTrace, body string, userID int) error {
	if trace == nil {
		return errors.New("request trace is nil")
	}
	if len(body) > requestTraceBodyMaxBytes {
		return errors.New("request trace body exceeds the storage limit")
	}
	if len(body) <= requestTraceInlineBytes {
		trace.Body, trace.BodyEncoding = body, ""
		return nil
	}
	owner := "u:" + strconv.Itoa(userID)
	if userID <= 0 {
		if trace.TraceId == "" {
			return errors.New("request trace without a user requires a trace ID")
		}
		owner = "t:" + requestTraceDigest([]byte(trace.TraceId))
	}
	timestamp := max(trace.CreatedAt, time.Now().Unix())
	manifest := requestTraceBodyManifest{
		Owner: owner, Bucket: timestamp / requestTraceBucketSeconds * requestTraceBucketSeconds,
		Size: len(body), Digest: requestTraceDigest([]byte(body)),
	}
	chunks := make(map[string]string)
	var rolling uint64
	start := 0
	for i := range len(body) {
		rolling = (rolling << 1) + requestTraceGear[body[i]]
		size := i + 1 - start
		if i+1 != len(body) && size < requestTraceChunkMaxBytes && (size < requestTraceChunkMinBytes || rolling&8191 != 0) {
			continue
		}
		chunk := body[start : i+1]
		digest := requestTraceDigest([]byte(chunk))
		manifest.Chunks = append(manifest.Chunks, digest)
		chunks[digest] = chunk
		start, rolling = i+1, 0
	}
	digests := make([]string, 0, len(chunks))
	for digest := range chunks {
		digests = append(digests, digest)
	}
	// Concurrent writers acquire unique-key locks in the same order.
	slices.Sort(digests)
	for start := 0; start < len(digests); start += requestTraceChunkBatch {
		batch := digests[start:min(start+requestTraceChunkBatch, len(digests))]
		var existing []string
		if err := LOG_DB.Model(&RequestTracePayloadChunk{}).
			Where("owner = ? AND bucket = ? AND digest IN ?", owner, manifest.Bucket, batch).
			Pluck("digest", &existing).Error; err != nil {
			return err
		}
		for _, digest := range existing {
			delete(chunks, digest)
		}
	}
	// Each compressed/base64 chunk stays below MySQL's 64 KiB TEXT limit.
	missing := make([]RequestTracePayloadChunk, 0, len(chunks))
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return err
	}
	for _, digest := range digests {
		body, missingChunk := chunks[digest]
		if !missingChunk {
			continue
		}
		compressed.Reset()
		writer.Reset(&compressed)
		if _, err := io.WriteString(writer, body); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		missing = append(missing, RequestTracePayloadChunk{
			Owner: owner, Bucket: manifest.Bucket, Digest: digest, Size: len(body),
			Data: base64.StdEncoding.EncodeToString(compressed.Bytes()),
		})
	}
	if len(missing) > 0 {
		query := LOG_DB
		if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			query = query.Clauses(clause.OnConflict{DoNothing: true})
		}
		// Keep each batch below MySQL's default 4 MiB packet limit. ClickHouse
		// duplicates are harmless immutable rows until ReplacingMergeTree merges.
		for start := 0; start < len(missing); start += 32 {
			batch := missing[start:min(start+32, len(missing))]
			if err := query.Create(&batch).Error; err != nil {
				return err
			}
		}
	}
	encoded, err := common.Marshal(manifest)
	if err != nil {
		return err
	}
	trace.Body, trace.BodyEncoding = string(encoded), requestTraceChunksEncoding
	return nil
}

func requestTraceDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func materializeRequestTraceBody(ctx context.Context, trace *RequestTrace) error {
	if trace.BodyEncoding == "" {
		return nil
	}
	if trace.BodyEncoding != requestTraceChunksEncoding {
		return fmt.Errorf("unsupported request trace body encoding %q", trace.BodyEncoding)
	}
	var manifest requestTraceBodyManifest
	if err := common.UnmarshalJsonStr(trace.Body, &manifest); err != nil {
		return fmt.Errorf("invalid request trace body manifest: %w", err)
	}
	if manifest.Owner == "" || len(manifest.Owner) > 72 || manifest.Bucket < 0 || manifest.Bucket%requestTraceBucketSeconds != 0 ||
		manifest.Size <= 0 || manifest.Size > requestTraceBodyMaxBytes || len(manifest.Digest) != 64 ||
		len(manifest.Chunks) == 0 || len(manifest.Chunks) > requestTraceBodyMaxBytes/requestTraceChunkMinBytes+1 {
		return errors.New("invalid request trace body manifest")
	}
	chunks := make(map[string]string)
	digests := make([]string, 0, len(manifest.Chunks))
	for _, digest := range manifest.Chunks {
		if len(digest) != 64 {
			return errors.New("invalid request trace chunk digest")
		}
		if _, seen := chunks[digest]; !seen {
			digests = append(digests, digest)
			chunks[digest] = ""
		}
	}
	for start := 0; start < len(digests); start += requestTraceChunkBatch {
		batch := digests[start:min(start+requestTraceChunkBatch, len(digests))]
		var stored []RequestTracePayloadChunk
		if err := LOG_DB.WithContext(ctx).Where("owner = ? AND bucket = ? AND digest IN ?", manifest.Owner, manifest.Bucket, batch).Find(&stored).Error; err != nil {
			return err
		}
		for _, chunk := range stored {
			if err := ctx.Err(); err != nil {
				return err
			}
			if chunk.Size <= 0 || chunk.Size > requestTraceChunkMaxBytes || len(chunk.Data) > 64<<10 {
				return errors.New("invalid request trace chunk size")
			}
			compressed, err := base64.StdEncoding.DecodeString(chunk.Data)
			if err != nil {
				return fmt.Errorf("invalid request trace chunk encoding: %w", err)
			}
			reader, err := gzip.NewReader(bytes.NewReader(compressed))
			if err != nil {
				return fmt.Errorf("invalid request trace chunk compression: %w", err)
			}
			body, readErr := io.ReadAll(io.LimitReader(reader, requestTraceChunkMaxBytes+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || len(body) != chunk.Size || requestTraceDigest(body) != chunk.Digest {
				return errors.New("request trace chunk integrity check failed")
			}
			chunks[chunk.Digest] = string(body)
		}
	}
	var body strings.Builder
	body.Grow(manifest.Size)
	for _, digest := range manifest.Chunks {
		chunk := chunks[digest]
		if chunk == "" {
			return errors.New("request trace chunk is missing")
		}
		if len(chunk) > manifest.Size-body.Len() {
			return errors.New("request trace body length check failed")
		}
		body.WriteString(chunk)
	}
	if body.Len() != manifest.Size || requestTraceDigest([]byte(body.String())) != manifest.Digest {
		return errors.New("request trace body integrity check failed")
	}
	trace.Body = body.String()
	return nil
}
