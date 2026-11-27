package model

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const SatelliteLogEventMaxBytes = 64 << 20

// This queue lives beside its source rows, including when LOG_SQL_DSN is separate.
// A committed source row must never be left without a durable delivery record.
type SatelliteLogOutbox struct {
	ID        int64  `json:"id" gorm:"primaryKey"`
	Kind      string `json:"kind" gorm:"type:varchar(16)"`
	SourceID  string `json:"source_id" gorm:"type:varchar(64)"`
	Payload   string `json:"payload" gorm:"type:text"`
	CreatedAt int64  `json:"created_at" gorm:"type:bigint"`
}

type satelliteLogOutboxMySQL struct {
	SatelliteLogOutbox `gorm:"embedded"`
	Payload            string `gorm:"type:longtext"`
}

func (satelliteLogOutboxMySQL) TableName() string { return "satellite_log_outboxes" }

// Receipts and imported rows commit together. A sender can retry an uncertain
// response without duplicating a log, even after either process restarts.
type SatelliteLogReceipt struct {
	SourceNode  string `gorm:"type:varchar(64);primaryKey"`
	Kind        string `gorm:"type:varchar(16);primaryKey"`
	SourceID    string `gorm:"type:varchar(64);primaryKey"`
	PayloadHash string `gorm:"type:varchar(64)"`
	TargetID    int    `gorm:"type:bigint"`
	CreatedAt   int64  `gorm:"type:bigint"`
}

type SatelliteLogEvent struct {
	ID       int64           `json:"id"`
	Kind     string          `json:"kind"`
	SourceID string          `json:"source_id"`
	Payload  json.RawMessage `json:"payload"`
}

// RequestTrace's public JSON deliberately hides storage and conversation fields.
// The node protocol must include them to preserve replay and conversation trees.
type satelliteTraceRecord struct {
	RequestTrace
	BodyEncoding              string  `json:"body_encoding"`
	ConversationKey           string  `json:"conversation_key"`
	ConversationParentKey     string  `json:"conversation_parent_key"`
	ConversationTurnKey       string  `json:"conversation_turn_key"`
	ConversationParentTurnKey string  `json:"conversation_parent_turn_key"`
	ConversationAgent         string  `json:"conversation_agent"`
	ConversationUserId        int     `json:"conversation_user_id"`
	ConversationClient        *string `json:"conversation_client"`
	ConversationGreeting      bool    `json:"conversation_greeting"`
	ConversationTitle         string  `json:"conversation_title"`
	ConversationModel         string  `json:"conversation_model"`
}

func satelliteLogStore(db *gorm.DB) *gorm.DB {
	// SQL traces must not contain captured request bodies, headers or prompts.
	return db.Session(&gorm.Session{Logger: logger.Discard})
}

func MigrateSatelliteLogSync() error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		if common.IsSatellite() {
			return errors.New("satellite log delivery requires a transactional log database")
		}
		return nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
		if err := LOG_DB.Table(LOG_DB.NamingStrategy.TableName("satellite_log_outboxes")).AutoMigrate(&satelliteLogOutboxMySQL{}); err != nil {
			return err
		}
		return LOG_DB.AutoMigrate(&SatelliteLogReceipt{})
	}
	return LOG_DB.AutoMigrate(&SatelliteLogOutbox{}, &SatelliteLogReceipt{})
}

func queueSatelliteLogEvent(tx *gorm.DB, kind, sourceID string, createdAt int64, value any) error {
	payload, err := common.Marshal(value)
	if err != nil {
		return errors.New("satellite log event encoding failed")
	}
	if len(payload) > SatelliteLogEventMaxBytes {
		return errors.New("satellite log event exceeds the delivery limit")
	}
	return tx.Create(&SatelliteLogOutbox{
		Kind: kind, SourceID: sourceID, Payload: string(payload), CreatedAt: createdAt,
	}).Error
}

func createSatelliteLog(log *Log) error {
	err := satelliteLogStore(LOG_DB).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(log).Error; err != nil {
			return err
		}
		return queueSatelliteLogEvent(tx, "log", strconv.Itoa(log.Id), log.CreatedAt, log)
	})
	if err != nil {
		return errors.New("satellite log persistence failed")
	}
	return nil
}

func createSatelliteTraces(traces []*RequestTrace) error {
	err := satelliteLogStore(LOG_DB).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&traces).Error; err != nil {
			return err
		}
		for _, trace := range traces {
			record := satelliteTraceRecord{
				RequestTrace: *trace, BodyEncoding: trace.BodyEncoding,
				ConversationKey: trace.ConversationKey, ConversationParentKey: trace.ConversationParentKey,
				ConversationTurnKey: trace.ConversationTurnKey, ConversationParentTurnKey: trace.ConversationParentTurnKey,
				ConversationAgent: trace.ConversationAgent, ConversationUserId: trace.ConversationUserId,
				ConversationClient: trace.ConversationClient, ConversationGreeting: trace.ConversationGreeting,
				ConversationTitle: trace.ConversationTitle, ConversationModel: trace.ConversationModel,
			}
			if err := queueSatelliteLogEvent(tx, "trace", strconv.Itoa(trace.Id), trace.CreatedAt, record); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return errors.New("satellite trace persistence failed")
	}
	return nil
}

func createSatelliteTraceChunks(chunks []RequestTracePayloadChunk) error {
	err := satelliteLogStore(LOG_DB).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunks).Error; err != nil {
			return err
		}
		for _, chunk := range chunks {
			identity := requestTraceDigest([]byte(chunk.Owner + "/" + strconv.FormatInt(chunk.Bucket, 10) + "/" + chunk.Digest))
			if err := queueSatelliteLogEvent(tx, "trace_chunk", identity, chunk.Bucket, chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return errors.New("satellite trace chunk persistence failed")
	}
	return nil
}

// A byte budget bounds each delivery, while one larger event remains deliverable.
// Read IDs first so pending large requests do not all enter memory at once.
func PendingSatelliteLogEvents(ctx context.Context, limit, maxBytes int) ([]SatelliteLogEvent, error) {
	limit = min(max(limit, 1), 128)
	maxBytes = min(max(maxBytes, 1), SatelliteLogEventMaxBytes)
	var ids []int64
	if err := satelliteLogStore(LOG_DB).WithContext(ctx).Model(&SatelliteLogOutbox{}).Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	events := make([]SatelliteLogEvent, 0, len(ids))
	size := 0
	for _, id := range ids {
		var row SatelliteLogOutbox
		if err := satelliteLogStore(LOG_DB).WithContext(ctx).First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if len(events) > 0 && len(row.Payload) > maxBytes-size {
			break
		}
		events = append(events, SatelliteLogEvent{ID: row.ID, Kind: row.Kind, SourceID: row.SourceID, Payload: json.RawMessage(row.Payload)})
		size += len(row.Payload)
	}
	return events, nil
}

// Binary payloads are referenced by their object-storage key, not embedded in
// the queue. Keep their source retention window until delivery is acknowledged.
func SatelliteTraceCleanupCutoff(ctx context.Context, cutoff int64) (int64, error) {
	if !common.IsSatellite() {
		return cutoff, nil
	}
	var oldest struct{ CreatedAt *int64 }
	if err := satelliteLogStore(LOG_DB).WithContext(ctx).Model(&SatelliteLogOutbox{}).
		Select("MIN(created_at) AS created_at").Scan(&oldest).Error; err != nil {
		return 0, errors.New("satellite trace retention state is unavailable")
	}
	if oldest.CreatedAt != nil {
		cutoff = min(cutoff, *oldest.CreatedAt)
	}
	return cutoff, nil
}

func AcknowledgeSatelliteLogEvents(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 128 {
		return errors.New("too many satellite log acknowledgements")
	}
	for _, id := range ids {
		if id <= 0 {
			return errors.New("invalid satellite log acknowledgement")
		}
	}
	return satelliteLogStore(LOG_DB).WithContext(ctx).Where("id IN ?", ids).Delete(&SatelliteLogOutbox{}).Error
}

// ImportSatelliteLogEvent never settles a user's quota. Accounting has its own
// idempotent protocol; this path only imports immutable observability records.
func ImportSatelliteLogEvent(ctx context.Context, sourceNode string, event SatelliteLogEvent) error {
	if common.IsSatellite() || common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return errors.New("satellite log receiver requires a primary transactional log database")
	}
	if sourceNode == "" || len(sourceNode) > 64 || strings.ContainsAny(sourceNode, "\x00\r\n") ||
		event.ID <= 0 || event.SourceID == "" || len(event.SourceID) > 64 || len(event.Payload) == 0 || len(event.Payload) > SatelliteLogEventMaxBytes {
		return errors.New("invalid satellite log event")
	}
	if event.Kind != "log" && event.Kind != "trace" && event.Kind != "trace_chunk" {
		return errors.New("unsupported satellite log event kind")
	}
	digest := sha256.Sum256(event.Payload)
	receipt := SatelliteLogReceipt{SourceNode: sourceNode, Kind: event.Kind, SourceID: event.SourceID,
		PayloadHash: hex.EncodeToString(digest[:]), CreatedAt: common.GetTimestamp()}
	var importedLog *Log
	var importedTrace *RequestTrace
	err := satelliteLogStore(LOG_DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			var previous SatelliteLogReceipt
			if err := tx.Where("source_node = ? AND kind = ? AND source_id = ?", sourceNode, event.Kind, event.SourceID).First(&previous).Error; err != nil {
				return err
			}
			if previous.PayloadHash != receipt.PayloadHash {
				return errors.New("satellite log event conflicts with a stored receipt")
			}
			if event.Kind == "log" {
				var row Log
				if err := tx.First(&row, previous.TargetID).Error; err != nil {
					// An acknowledged source may outlive the main node's retention.
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return nil
					}
					return err
				}
				importedLog = &row
			}
			return nil
		}
		switch event.Kind {
		case "log":
			var row Log
			if common.Unmarshal(event.Payload, &row) != nil || row.Id <= 0 || strconv.Itoa(row.Id) != event.SourceID || len(row.RequestId) > 64 || len(row.UpstreamRequestId) > 128 || row.CreatedAt <= 0 || row.UserId < 0 {
				return errors.New("invalid satellite log payload")
			}
			row.Id = 0
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			receipt.TargetID, importedLog = row.Id, &row
		case "trace":
			var record satelliteTraceRecord
			if common.Unmarshal(event.Payload, &record) != nil || record.Id <= 0 || strconv.Itoa(record.Id) != event.SourceID || record.TraceId == "" || len(record.TraceId) > 64 || record.Seq < 0 || record.CreatedAt <= 0 || len(record.Body) > requestTraceBodyMaxBytes || record.BodySize < 0 {
				return errors.New("invalid satellite trace payload")
			}
			row := record.RequestTrace
			row.Id, row.BodyEncoding = 0, record.BodyEncoding
			row.ConversationKey, row.ConversationParentKey = record.ConversationKey, record.ConversationParentKey
			row.ConversationTurnKey, row.ConversationParentTurnKey = record.ConversationTurnKey, record.ConversationParentTurnKey
			row.ConversationAgent, row.ConversationUserId = record.ConversationAgent, record.ConversationUserId
			row.ConversationClient, row.ConversationGreeting = record.ConversationClient, record.ConversationGreeting
			row.ConversationTitle, row.ConversationModel = record.ConversationTitle, record.ConversationModel
			if row.BodyEncoding != "" && row.BodyEncoding != requestTraceChunksEncoding {
				return errors.New("unsupported satellite trace encoding")
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			receipt.TargetID, importedTrace = row.Id, &row
		case "trace_chunk":
			var row RequestTracePayloadChunk
			if common.Unmarshal(event.Payload, &row) != nil || row.Owner == "" || len(row.Owner) > 72 || row.Bucket < 0 || row.Bucket%requestTraceBucketSeconds != 0 || len(row.Digest) != 64 || row.Size <= 0 || row.Size > requestTraceChunkMaxBytes || len(row.Data) > 64<<10 {
				return errors.New("invalid satellite trace chunk payload")
			}
			identity := requestTraceDigest([]byte(row.Owner + "/" + strconv.FormatInt(row.Bucket, 10) + "/" + row.Digest))
			if identity != event.SourceID {
				return errors.New("invalid satellite trace chunk identity")
			}
			compressed, err := base64.StdEncoding.DecodeString(row.Data)
			if err != nil {
				return errors.New("invalid satellite trace chunk encoding")
			}
			reader, err := gzip.NewReader(bytes.NewReader(compressed))
			if err != nil {
				return errors.New("invalid satellite trace chunk compression")
			}
			body, readErr := io.ReadAll(io.LimitReader(reader, requestTraceChunkMaxBytes+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || len(body) != row.Size || requestTraceDigest(body) != row.Digest {
				return errors.New("satellite trace chunk integrity check failed")
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		if importedLog != nil && DB == LOG_DB {
			if err := projectSatelliteLogQuota(tx, sourceNode, event.SourceID, importedLog); err != nil {
				return err
			}
		}
		return tx.Model(&SatelliteLogReceipt{}).Where("source_node = ? AND kind = ? AND source_id = ?", sourceNode, event.Kind, event.SourceID).Update("target_id", receipt.TargetID).Error
	})
	if err != nil {
		return errors.New("satellite log import failed")
	}
	if importedLog != nil {
		if DB != LOG_DB {
			if err := satelliteLogStore(DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return projectSatelliteLogQuota(tx, sourceNode, event.SourceID, importedLog)
			}); err != nil {
				return errors.New("satellite dashboard projection is pending")
			}
		}
		if err := QueueSub2APIBill(ctx, importedLog); err != nil {
			return errors.New("satellite upstream bill reconciliation is pending")
		}
		// Replayed deliveries can be completing a previously failed projection.
		// The live feed references this same row ID, so clients update it in place.
		usageLiveFeed.committed(importedLog)
	}
	if importedTrace != nil {
		NotifyRetryTraceLogs([]*RequestTrace{importedTrace})
	}
	return nil
}

// The receipt is in the primary database with quota_data, so a separate LOG_DB
// cannot turn a retry into either missing statistics or a repeated increment.
func projectSatelliteLogQuota(tx *gorm.DB, sourceNode, sourceID string, log *Log) error {
	if log.Type != LogTypeConsume || !common.DataExportEnabled {
		return nil
	}
	if log.Quota < 0 || log.Quota > common.MaxQuota || log.PromptTokens < 0 || log.PromptTokens > common.MaxQuota || log.CompletionTokens < 0 || log.CompletionTokens > common.MaxQuota {
		return errors.New("invalid satellite usage quantities")
	}
	receipt := SatelliteLogReceipt{SourceNode: sourceNode, Kind: "quota", SourceID: sourceID, TargetID: log.Id, CreatedAt: common.GetTimestamp()}
	claim := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
	if claim.Error != nil || claim.RowsAffected == 0 {
		return claim.Error
	}
	var metadata struct {
		CacheRead  int64  `json:"cache_tokens"`
		CacheWrite int64  `json:"cache_write_tokens"`
		ClientTool string `json:"client_tool"`
		RootInfo   struct {
			NodeName string `json:"node_name"`
		} `json:"root_info"`
	}
	_ = common.UnmarshalJsonStr(log.Other, &metadata)
	if metadata.CacheRead < 0 || metadata.CacheRead > int64(common.MaxQuota) || metadata.CacheWrite < 0 || metadata.CacheWrite > int64(common.MaxQuota) {
		return errors.New("invalid satellite cache quantities")
	}
	other := NewLogOther()
	other.SetPublic("client_tool", metadata.ClientTool)
	nodeName := metadata.RootInfo.NodeName
	if nodeName == "" {
		nodeName = sourceNode
	}
	row := QuotaData{UserID: log.UserId, Username: log.Username, ModelName: log.ModelName, CreatedAt: log.CreatedAt - log.CreatedAt%3600,
		UseGroup: log.Group, TokenID: log.TokenId, ChannelID: log.ChannelId, NodeName: nodeName, ClientTool: other.ClientTool()}
	identity, err := common.Marshal(row)
	if err != nil {
		return errors.New("satellite dashboard identity encoding failed")
	}
	bucket := SatelliteLogReceipt{SourceNode: sourceNode, Kind: "quota_bucket", SourceID: requestTraceDigest(identity), CreatedAt: common.GetTimestamp()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&bucket).Error; err != nil {
		return err
	}
	if err := lockForUpdate(tx).Where("source_node = ? AND kind = ? AND source_id = ?", sourceNode, bucket.Kind, bucket.SourceID).First(&bucket).Error; err != nil {
		return err
	}
	row.Count, row.Quota, row.TokenUsed = 1, log.Quota, log.PromptTokens+log.CompletionTokens
	row.CacheReadTokens, row.CacheCreationTokens = metadata.CacheRead, metadata.CacheWrite
	if bucket.TargetID == 0 {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return tx.Model(&SatelliteLogReceipt{}).Where("source_node = ? AND kind = ? AND source_id = ?", sourceNode, bucket.Kind, bucket.SourceID).Update("target_id", row.Id).Error
	}
	result := tx.Model(&QuotaData{}).Where("id = ?", bucket.TargetID).Updates(map[string]any{
		"count": gorm.Expr("count + ?", row.Count), "quota": gorm.Expr("quota + ?", row.Quota),
		"token_used":            gorm.Expr("token_used + ?", row.TokenUsed),
		"cache_read_tokens":     gorm.Expr("cache_read_tokens + ?", row.CacheReadTokens),
		"cache_creation_tokens": gorm.Expr("cache_creation_tokens + ?", row.CacheCreationTokens),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("satellite dashboard bucket is missing")
	}
	return nil
}
