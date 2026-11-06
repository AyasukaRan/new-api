package perfmetrics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type channelActivityPair struct {
	channelID int
	modelName string
}

type channelActiveRequests struct {
	count         int
	nextHeartbeat time.Time
	started       chan struct{}
	ioMu          sync.RWMutex
}

type channelActivityBatch struct {
	row     model.ChannelModelActivity
	writing bool
	done    chan struct{}
	err     error
}

// One worker renews leases for all active pairs in this process. SQL remains
// authoritative across instances.
var channelRequestActivity = struct {
	sync.Mutex
	once    sync.Once
	wake    chan struct{}
	entries map[channelActivityPair]*channelActiveRequests
	writes  map[channelActivityPair]*channelActivityBatch
}{wake: make(chan struct{}, 1), entries: make(map[channelActivityPair]*channelActiveRequests), writes: make(map[channelActivityPair]*channelActivityBatch)}

// BeginChannelRequest captures the attempt's pair before retries can change
// RelayInfo. Cancellation and the returned completion callback are idempotent.
// Activity must still be recorded when optional performance metrics are off.
func BeginChannelRequest(ctx context.Context, info *relaycommon.RelayInfo, channelID int) func(success *bool) {
	if info == nil || info.IsChannelTest || channelID <= 0 || strings.TrimSpace(info.OriginModelName) == "" {
		return func(*bool) {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return func(*bool) {}
	}
	pair := channelActivityPair{channelID: channelID, modelName: info.OriginModelName}
	now := time.Now()
	lease := min(30*time.Second, operation_setting.ChannelTestInterval())
	channelRequestActivity.Lock()
	entry := channelRequestActivity.entries[pair]
	if entry == nil {
		entry = &channelActiveRequests{}
		channelRequestActivity.entries[pair] = entry
	}
	entry.count++
	first := entry.started == nil
	if first {
		entry.started = make(chan struct{})
	}
	started := entry.started
	next := time.Now().Add(lease / 3)
	if entry.nextHeartbeat.IsZero() || next.Before(entry.nextHeartbeat) {
		entry.nextHeartbeat = next
	}
	channelRequestActivity.Unlock()
	// All overlapping requests share the same renewable lease. Publish it
	// before returning from Begin, including on nodes other than the scheduler.
	if first {
		if err := recordChannelRequestActivity(pair, now.UnixMilli(), now.Add(lease).UnixMilli(), nil); err != nil {
			channelRequestActivity.Lock()
			entry.started = nil // The next arrival retries the initial lease.
			channelRequestActivity.Unlock()
		}
		close(started)
	} else {
		<-started
	}
	channelRequestActivity.once.Do(func() { go renewChannelRequestActivity() })
	select {
	case channelRequestActivity.wake <- struct{}{}:
	default:
	}

	var cleanupOnce, resultOnce sync.Once
	finish := func(success *bool) bool {
		cleaned := false
		cleanupOnce.Do(func() {
			cleaned = true
			completedAt := time.Now().UnixMilli()
			channelRequestActivity.Lock()
			entry.count--
			if entry.count == 0 {
				delete(channelRequestActivity.entries, pair)
			}
			channelRequestActivity.Unlock()
			// Wait for a previously selected heartbeat before returning. A
			// finished pair can never be written later by a queued heartbeat.
			entry.ioMu.RLock()
			recordChannelRequestActivity(pair, completedAt, 0, success)
			entry.ioMu.RUnlock()
			select {
			case channelRequestActivity.wake <- struct{}{}:
			default:
			}
		})
		return cleaned
	}
	// A disconnect releases the lease but says nothing about upstream health.
	// The relay may already have sent its final event and still be settling usage.
	stopCancellation := context.AfterFunc(ctx, func() { finish(nil) })
	return func(success *bool) {
		resultOnce.Do(func() {
			stopCancellation()
			if !finish(success) && success != nil {
				entry.ioMu.RLock()
				recordChannelRequestActivity(pair, time.Now().UnixMilli(), 0, success)
				entry.ioMu.RUnlock()
			}
		})
	}
}

// A busy route has one in-flight upsert and at most one pending batch.
// The first caller of each batch performs the write; its peers only wait for
// that batch. No flush timer or goroutine outlives a completed observation.
func recordChannelRequestActivity(pair channelActivityPair, at, activeUntil int64, success *bool) error {
	channelRequestActivity.Lock()
	previous := channelRequestActivity.writes[pair]
	batch := previous
	leader := previous == nil || previous.writing
	if leader {
		batch = &channelActivityBatch{
			row:     model.ChannelModelActivity{ChannelID: pair.channelID, ModelName: pair.modelName},
			writing: previous == nil, done: make(chan struct{}),
		}
		channelRequestActivity.writes[pair] = batch
	}
	row := &batch.row
	row.LastRequestAt = max(row.LastRequestAt, at)
	row.RequestActiveUntil = max(row.RequestActiveUntil, activeUntil)
	if success != nil && at >= row.LastResultAt {
		row.LastResultAt, row.LastResultSuccess = at, *success
	}
	channelRequestActivity.Unlock()

	if !leader {
		<-batch.done
		return batch.err
	}
	if previous != nil {
		<-previous.done
		channelRequestActivity.Lock()
		batch.writing = true
		if previous.err != nil {
			// Carry a failed write into the waiting batch. Newer outcomes win.
			old := &previous.row
			row.LastRequestAt = max(row.LastRequestAt, old.LastRequestAt)
			row.RequestActiveUntil = max(row.RequestActiveUntil, old.RequestActiveUntil)
			if old.LastResultAt > row.LastResultAt {
				row.LastResultAt, row.LastResultSuccess = old.LastResultAt, old.LastResultSuccess
			}
		}
		channelRequestActivity.Unlock()
	}
	batch.err = model.RecordChannelModelRequestBatch(*row)
	if batch.err != nil {
		common.SysError(fmt.Sprintf("channel activity persistence failed: channel_id=%d model=%q error=%v", pair.channelID, pair.modelName, batch.err))
	}
	channelRequestActivity.Lock()
	if channelRequestActivity.writes[pair] == batch {
		delete(channelRequestActivity.writes, pair)
	}
	close(batch.done)
	channelRequestActivity.Unlock()
	return batch.err
}

func renewChannelRequestActivity() {
	for {
		now := time.Now()
		channelRequestActivity.Lock()
		due := make(map[channelActivityPair]*channelActiveRequests)
		var next time.Time
		for pair, entry := range channelRequestActivity.entries {
			if !entry.nextHeartbeat.After(now) {
				due[pair] = entry
			} else if next.IsZero() || entry.nextHeartbeat.Before(next) {
				next = entry.nextHeartbeat
			}
		}
		channelRequestActivity.Unlock()
		if len(due) > 0 {
			for pair, entry := range due {
				entry.ioMu.Lock()
				channelRequestActivity.Lock()
				active := channelRequestActivity.entries[pair] == entry && entry.count > 0
				now := time.Now()
				var lease time.Duration
				if active {
					lease = min(30*time.Second, operation_setting.ChannelTestInterval())
					entry.nextHeartbeat = now.Add(lease / 3)
				}
				channelRequestActivity.Unlock()
				if active {
					recordChannelRequestActivity(pair, now.UnixMilli(), now.Add(lease).UnixMilli(), nil)
				}
				entry.ioMu.Unlock()
			}
			continue
		}
		if next.IsZero() {
			<-channelRequestActivity.wake
			continue
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-channelRequestActivity.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}
