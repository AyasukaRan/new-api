package model

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

// Only committed row references travel through the journal. Authorization,
// filtering and projection always run against the same stores as the log list.
type LiveLogRef struct {
	ID        int    `json:"i,omitempty"`
	UserID    int    `json:"u,omitempty"`
	RequestID string `json:"r,omitempty"`
	CreatedAt int64  `json:"t,omitempty"`
}

type liveLogBatch struct {
	Cursor string       `json:"cursor"`
	Refs   []LiveLogRef `json:"refs,omitempty"`
	Reset  bool         `json:"reset,omitempty"`
}

const maxLiveRefs = 4096
const liveLogRetention = 120

type liveLogFeed struct {
	mu       sync.Mutex
	pending  []LiveLogRef
	overflow bool
	history  []liveLogBatch
}

var usageLiveFeed = liveLogFeed{history: []liveLogBatch{{Cursor: rand.Text()}}}

func (f *liveLogFeed) committed(log *Log) {
	ref := LiveLogRef{ID: log.Id, UserID: log.UserId}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		ref.RequestID = log.RequestId
		ref.CreatedAt = log.CreatedAt
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == maxLiveRefs {
		f.overflow = true
		return
	}
	f.pending = append(f.pending, ref)
}

func (f *liveLogFeed) publish(batch liveLogBatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if batch.Reset {
		f.history = nil
	}
	if len(f.history) == liveLogRetention {
		f.history = slices.Delete(f.history, 0, 1)
	}
	f.history = append(f.history, batch)
}

func LiveLogCursor() string {
	usageLiveFeed.mu.Lock()
	defer usageLiveFeed.mu.Unlock()
	return usageLiveFeed.history[len(usageLiveFeed.history)-1].Cursor
}

// Missing or excessively old cursors require a fresh first page. Replaying
// commits, rather than IDs above a high-water mark, includes delayed writers.
func LiveLogChanges(cursor string, ownUserID int) (refs []LiveLogRef, next string, reset bool) {
	f := &usageLiveFeed
	f.mu.Lock()
	defer f.mu.Unlock()
	index := len(f.history) - 1
	next = f.history[index].Cursor
	for index >= 0 && f.history[index].Cursor != cursor {
		index--
	}
	if index < 0 {
		return nil, next, true
	}
	for _, batch := range f.history[index+1:] {
		for _, ref := range batch.Refs {
			if ownUserID != 0 && ref.UserID != ownUserID {
				continue
			}
			if len(refs) == maxLiveRefs {
				return nil, next, true
			}
			refs = append(refs, ref)
		}
	}
	return refs, next, false
}

// Every instance consumes the shared journal once per tick, independently of
// the number of open dashboards. The local journal remains usable if Redis
// goes offline; the recovery marker covers writes on unreachable peers.
func StartLiveLogFeed(ctx context.Context) {
	shared := redisLogFeed{client: common.RDB, key: fmt.Sprintf("usage_log_events:%x", sha256.Sum256([]byte(common.SessionSecret)))}
	if !common.RedisEnabled {
		shared.client = nil
	}
	f := &usageLiveFeed
	if shared.client != nil {
		if err := shared.sync(ctx, f, liveLogBatch{Cursor: rand.Text(), Reset: true}); err != nil {
			shared.offline = true
		}
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			f.mu.Lock()
			batch := liveLogBatch{Cursor: rand.Text(), Refs: f.pending, Reset: f.overflow}
			f.pending = nil
			f.overflow = false
			f.mu.Unlock()
			if batch.Reset {
				batch.Refs = nil
			}
			if shared.client != nil {
				if err := shared.sync(ctx, f, batch); err == nil {
					shared.offline = false
					continue
				}
				if !shared.offline {
					f.publish(liveLogBatch{Cursor: rand.Text(), Reset: true})
				}
				shared.offline = true
			}
			if len(batch.Refs) > 0 || batch.Reset {
				f.publish(batch)
			}
		}
	}()
}

type redisLogFeed struct {
	client  *redis.Client
	key     string
	lastID  string
	offline bool
}

func (r *redisLogFeed) sync(ctx context.Context, f *liveLogFeed, batch liveLogBatch) error {
	if r.offline {
		batch.Reset = true
		batch.Refs = nil
	}
	if batch.Reset || len(batch.Refs) > 0 {
		data, err := common.Marshal(batch)
		if err != nil {
			return err
		}
		id, err := r.client.XAdd(ctx, &redis.XAddArgs{Stream: r.key, MaxLenApprox: liveLogRetention, Values: map[string]any{"batch": string(data)}}).Result()
		if err != nil {
			return err
		}
		if r.lastID == "" || r.offline {
			// The marker supersedes all older events. Use its actual shared
			// token so snapshots can move between healthy gateway workers.
			r.lastID = id
			f.publish(batch)
			return nil
		}
	}
	result, err := r.client.XRead(ctx, &redis.XReadArgs{Streams: []string{r.key, r.lastID}, Count: 64, Block: -1}).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	// Redis may have trimmed our previous position during a process pause.
	previous, err := r.client.XRangeN(ctx, r.key, r.lastID, r.lastID, 1).Result()
	if err != nil {
		return err
	}
	if len(previous) == 0 {
		f.publish(liveLogBatch{Cursor: rand.Text(), Reset: true})
	}
	for _, stream := range result {
		for _, message := range stream.Messages {
			var received liveLogBatch
			raw, ok := message.Values["batch"].(string)
			if !ok || common.UnmarshalJsonStr(raw, &received) != nil || received.Cursor == "" {
				received = liveLogBatch{Cursor: rand.Text(), Reset: true}
			}
			f.publish(received)
			r.lastID = message.ID
		}
	}
	return nil
}

func keyLiveLogs(logs []*Log) {
	for _, row := range logs {
		// Self lists use page-relative IDs. A keyed row identity permits
		// overlap/replay deduplication without disclosing the global row ID.
		mac := hmac.New(sha256.New, []byte(common.SessionSecret))
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) && row.Id == 0 {
			fmt.Fprintf(mac, "ch/%d/%s/%d/%d/%d/%d/%s", row.CreatedAt, row.RequestId, row.UserId, row.ChannelId, row.Type, row.TokenId, row.Content)
			fmt.Fprintf(mac, "/%s/%s", row.ModelName, row.Other)
		} else {
			fmt.Fprintf(mac, "log/%d", row.Id)
		}
		row.LiveKey = base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
	}
}

func GetChangedLogs(ctx context.Context, filter LogFilter, ownUserID, role int, compact bool, pageSize int, refs []LiveLogRef) ([]*Log, int, error) {
	rows := make([]*Log, 0)
	if len(refs) == 0 {
		return rows, 0, nil
	}
	tx := LOG_DB.WithContext(ctx).Model(&Log{})
	var err error
	if ownUserID != 0 {
		tx, err = filter.forUser(tx, ownUserID)
	} else {
		tx, err = filter.apply(tx)
	}
	if err != nil {
		return nil, 0, err
	}
	clickhouse := common.UsingLogDatabase(common.DatabaseTypeClickHouse)
	order := "logs.created_at desc, logs.id desc"
	if clickhouse {
		order = clickHouseLogOrder("logs.")
	}
	pageSize = min(100, max(1, pageSize))
	total := 0
	// Keep bind counts portable and never materialize the bodies of a large
	// backlog. Only overflowing chunks need the extra count.
	seen := make(map[LiveLogRef]bool, len(refs))
	unique := make([]LiveLogRef, 0, len(refs))
	for _, ref := range refs {
		if !seen[ref] {
			unique = append(unique, ref)
			seen[ref] = true
		}
	}
	for chunk := range slices.Chunk(unique, 400) {
		ids := make([]int, 0, len(chunk))
		for _, ref := range chunk {
			ids = append(ids, ref.ID)
		}
		q := tx.Session(&gorm.Session{}).Where("logs.id IN ?", ids)
		if clickhouse {
			// Match the MergeTree sorting key as well as the unique attempt ID.
			times := make([]int64, 0, len(chunk))
			requests := make([]string, 0, len(chunk))
			for _, ref := range chunk {
				times = append(times, ref.CreatedAt)
				requests = append(requests, ref.RequestID)
			}
			q = q.Where("logs.created_at IN ? AND logs.request_id IN ?", times, requests)
		}
		var part []*Log
		if err := q.Session(&gorm.Session{}).Order(order).Limit(pageSize + 1).Find(&part).Error; err != nil {
			return nil, 0, err
		}
		if len(part) > pageSize {
			var count int64
			if err := q.Count(&count).Error; err != nil {
				return nil, 0, err
			}
			total += int(count)
			part = part[:pageSize]
		} else {
			total += len(part)
		}
		rows = append(rows, part...)
	}
	slices.SortStableFunc(rows, func(a, b *Log) int {
		if d := cmp.Compare(b.CreatedAt, a.CreatedAt); d != 0 {
			return d
		}
		if clickhouse {
			return cmp.Compare(b.RequestId, a.RequestId)
		}
		return cmp.Compare(b.Id, a.Id)
	})
	rows = rows[:min(len(rows), pageSize)]
	keyLiveLogs(rows)
	if ownUserID != 0 {
		formatUserLogs(rows, 0)
	} else {
		if err := resolveLogChannels(rows); err != nil {
			return nil, 0, err
		}
		if clickhouse {
			assignDisplayLogIds(rows, 0)
		}
		FormatLogList(rows, role, compact)
	}
	return rows, total - len(rows), resolveLogUsernames(rows)
}
