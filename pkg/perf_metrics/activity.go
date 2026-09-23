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
	ioMu          sync.Mutex
}

// One worker renews leases for all active pairs in this process. SQL remains
// authoritative across instances; the map only coalesces in-flight heartbeats.
var channelRequestActivity = struct {
	sync.Mutex
	once    sync.Once
	wake    chan struct{}
	entries map[channelActivityPair]*channelActiveRequests
}{wake: make(chan struct{}, 1), entries: make(map[channelActivityPair]*channelActiveRequests)}

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
	recordChannelRequestActivity(pair, now.UnixMilli(), now.Add(lease).UnixMilli(), nil)
	channelRequestActivity.Lock()
	entry := channelRequestActivity.entries[pair]
	if entry == nil {
		entry = &channelActiveRequests{}
		channelRequestActivity.entries[pair] = entry
	}
	entry.count++
	next := time.Now().Add(lease / 3)
	if entry.nextHeartbeat.IsZero() || next.Before(entry.nextHeartbeat) {
		entry.nextHeartbeat = next
	}
	channelRequestActivity.Unlock()
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
			entry.ioMu.Lock()
			recordChannelRequestActivity(pair, completedAt, 0, success)
			entry.ioMu.Unlock()
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
				entry.ioMu.Lock()
				recordChannelRequestActivity(pair, time.Now().UnixMilli(), 0, success)
				entry.ioMu.Unlock()
			}
		})
	}
}

func recordChannelRequestActivity(pair channelActivityPair, at, activeUntil int64, success *bool) {
	if err := model.RecordChannelModelRequest(pair.channelID, pair.modelName, at, activeUntil, success); err != nil {
		common.SysError(fmt.Sprintf("channel activity persistence failed: channel_id=%d model=%q error=%v", pair.channelID, pair.modelName, err))
	}
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
