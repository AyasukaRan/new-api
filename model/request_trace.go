package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
)

// The four legs of a relayed exchange. A retry produces a fresh upstream pair
// per attempt while the client pair stays single, so a trace is keyed by
// (trace_id, direction, attempt) rather than by direction alone.
const (
	TraceDirectionClientRequest    = "client_request"
	TraceDirectionUpstreamRequest  = "upstream_request"
	TraceDirectionUpstreamResponse = "upstream_response"
	TraceDirectionClientResponse   = "client_response"
)

// RequestTrace stores one leg of one relay attempt.
//
// It lives beside the usage log rather than inside `logs.other` because
// GetAllLogs returns `other` for every row of every page: a multi-megabyte
// payload there would make the admin log list unloadable. The log row carries
// only the trace id, under admin_info, and this table is fetched on demand.
type RequestTrace struct {
	Id      int    `json:"id"`
	TraceId string `json:"trace_id" gorm:"type:varchar(64);index:idx_request_traces_trace,priority:1"`
	// Seq orders the legs chronologically. The primary key cannot: ClickHouse
	// inserts every row with id 0.
	Seq       int    `json:"seq" gorm:"index:idx_request_traces_trace,priority:2"`
	Attempt   int    `json:"attempt"`
	Direction string `json:"direction" gorm:"type:varchar(24)"`
	CreatedAt int64  `json:"created_at" gorm:"type:bigint;index"`
	RequestId string `json:"request_id" gorm:"type:varchar(64);index"`
	ChannelId int    `json:"channel_id"`
	// Format names the wire dialect of this leg (openai, claude, gemini,
	// openai_responses …) so the reader knows which parser to run. The client
	// legs carry the format the client spoke; the upstream legs carry the
	// format the request was converted into.
	Format  string `json:"format" gorm:"type:varchar(32)"`
	Method  string `json:"method" gorm:"type:varchar(16)"`
	Url     string `json:"url" gorm:"type:text"`
	Status  int    `json:"status"`
	Headers string `json:"headers" gorm:"type:text"`
	Body    string `json:"body" gorm:"type:text"`
	// Empty for historical inline bodies; chunk manifests are materialized on read.
	BodyEncoding string `json:"-" gorm:"type:varchar(24)"`
	HasBody      bool   `json:"-" gorm:"->;-:migration"`
	// BodySize is the payload's size before truncation, so the reader can say
	// how much was dropped.
	BodySize  int64 `json:"body_size"`
	Truncated bool  `json:"truncated"`
	// ObjectKey points at object storage for the payloads that cannot live in
	// a text column — an uploaded audio file, a synthesized speech response, a
	// generated image. Empty when the body is inline or was not retained.
	ObjectKey   string `json:"object_key" gorm:"type:varchar(255)"`
	ContentType string `json:"content_type" gorm:"type:varchar(128)"`
}

// MySQL TEXT holds only 64 KiB, below even the default 1 MiB payload cap.
// Override only its migration schema; other databases retain their native TEXT,
// and repeated migrations cannot shrink MySQL's widened body column again.
type requestTraceMySQLSchema struct {
	RequestTrace `gorm:"embedded"`
	Body         string `gorm:"type:longtext"`
}

func (requestTraceMySQLSchema) TableName() string { return "request_traces" }

// MigrateRequestTraces runs from both log-database entry points, because
// InitLogDB returns early — without reaching migrateLOGDB — whenever
// LOG_SQL_DSN is unset, which is the default deployment.
func MigrateRequestTraces() error {
	if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
		return LOG_DB.AutoMigrate(&requestTraceMySQLSchema{}, &RequestTracePayloadChunk{})
	}
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return LOG_DB.AutoMigrate(&RequestTrace{}, &RequestTracePayloadChunk{})
	}
	if err := LOG_DB.Exec(`CREATE TABLE IF NOT EXISTS request_traces (
		id Int64 DEFAULT 0, trace_id String, seq Int32, attempt Int32, direction String,
		created_at Int64, request_id String, channel_id Int32, format String,
		method String, url String, status Int32,
		headers String, body String, body_size Int64, truncated UInt8,
		object_key String, content_type String, body_encoding String
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMM(toDateTime(created_at))
	ORDER BY (created_at, trace_id)`).Error; err != nil {
		return err
	}
	// CREATE TABLE IF NOT EXISTS is a no-op on an install that already has the
	// table, so a column added after the first release needs its own statement
	// or every insert naming it would fail. ClickHouse has no AutoMigrate.
	for _, column := range []string{"object_key String", "content_type String", "body_encoding String"} {
		if err := LOG_DB.Exec("ALTER TABLE request_traces ADD COLUMN IF NOT EXISTS " + column).Error; err != nil {
			return err
		}
	}
	return LOG_DB.Exec(`CREATE TABLE IF NOT EXISTS request_trace_payload_chunks (
		owner String, bucket Int64, digest String, size Int32, data String
	) ENGINE = ReplacingMergeTree()
	PARTITION BY toYYYYMM(toDateTime(bucket))
	ORDER BY (owner, bucket, digest)`).Error
}

// RequestTraceObjectKeysBefore lists the stored objects belonging to traces
// that are about to expire, so the purge can drop them from object storage too.
func RequestTraceObjectKeysBefore(cutoff int64) ([]string, error) {
	var keys []string
	err := LOG_DB.Model(&RequestTrace{}).
		Where("created_at < ? AND object_key <> ?", cutoff, "").
		Pluck("object_key", &keys).Error
	return keys, err
}

func SaveRequestTraces(traces []*RequestTrace) error {
	// Keep normal exchanges in one INSERT. PostgreSQL can inline escaped SQL
	// parameters and MySQL limits packet size, so send oversized legs alone.
	const maxRows, maxBytes = 16, 256 << 10
	for len(traces) > 0 {
		rows, size := 0, 0
		for _, leg := range traces[:min(len(traces), maxRows)] {
			if leg == nil {
				return errors.New("request trace is nil")
			}
			rowSize := 256 + len(leg.TraceId) + len(leg.Direction) + len(leg.RequestId) +
				len(leg.Format) + len(leg.Method) + len(leg.Url) + len(leg.Headers) +
				len(leg.Body) + len(leg.BodyEncoding) + len(leg.ObjectKey) + len(leg.ContentType)
			if rows > 0 && rowSize > maxBytes-size {
				break
			}
			rows++
			size += rowSize
		}
		batch := traces[:rows]
		if err := LOG_DB.Create(&batch).Error; err != nil {
			return err
		}
		traces = traces[rows:]
	}
	return nil
}

func GetRequestTraces(traceId string) ([]*RequestTrace, error) {
	if traceId == "" {
		return nil, nil
	}
	var traces []*RequestTrace
	if err := LOG_DB.Where("trace_id = ?", traceId).Order("seq asc").Find(&traces).Error; err != nil {
		return nil, err
	}
	for _, trace := range traces {
		if err := materializeRequestTraceBody(context.Background(), trace); err != nil {
			return nil, err
		}
	}
	return traces, nil
}

// Metadata reads never transfer stored bodies or manifests from the database.
// The explicit presence flag also covers historical inline rows without sizes.
func GetRequestTraceMetadata(traceId string) ([]*RequestTrace, error) {
	if traceId == "" {
		return nil, nil
	}
	var traces []*RequestTrace
	err := LOG_DB.Model(&RequestTrace{}).
		Select(`seq, attempt, direction, channel_id, format, method, url, status,
			headers, body_size, truncated, object_key, content_type,
			CASE WHEN body_size > 0 AND content_type = '' THEN 1
			WHEN body_encoding <> '' THEN 1 WHEN body <> '' THEN 1 ELSE 0 END AS has_body`).
		Where("trace_id = ?", traceId).Order("seq asc").Find(&traces).Error
	return traces, err
}

// GetRequestTraceLeg fetches one leg, which is how a stored payload is served:
// the key is never taken from the caller, only the trace and leg it belongs to.
func GetRequestTraceLeg(traceId string, seq int) (*RequestTrace, error) {
	return GetRequestTraceLegContext(context.Background(), traceId, seq)
}

func GetRequestTraceLegContext(ctx context.Context, traceId string, seq int) (*RequestTrace, error) {
	if traceId == "" {
		return nil, nil
	}
	var trace RequestTrace
	err := LOG_DB.WithContext(ctx).Where("trace_id = ? AND seq = ?", traceId, seq).First(&trace).Error
	if err != nil {
		return nil, err
	}
	if err := materializeRequestTraceBody(ctx, &trace); err != nil {
		return nil, err
	}
	return &trace, nil
}

func DeleteRequestTracesBefore(cutoff int64) error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// ClickHouse has no DELETE; a mutation is the supported equivalent and
		// mutations_sync makes it observable to the caller.
		if err := LOG_DB.Exec("ALTER TABLE request_traces DELETE WHERE created_at < ? SETTINGS mutations_sync = 1", cutoff).Error; err != nil {
			return err
		}
		return LOG_DB.Exec("ALTER TABLE request_trace_payload_chunks DELETE WHERE bucket < ? SETTINGS mutations_sync = 1", cutoff-requestTraceBucketSeconds).Error
	}
	if err := LOG_DB.Where("created_at < ?", cutoff).Delete(&RequestTrace{}).Error; err != nil {
		return err
	}
	// A whole UTC bucket expires only after every trace that could reference it.
	return LOG_DB.Where("bucket < ?", cutoff-requestTraceBucketSeconds).Delete(&RequestTracePayloadChunk{}).Error
}
