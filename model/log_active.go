package model

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/tidwall/gjson"
)

// In-flight requests are a leased, bounded display projection, never log rows.
// Each worker owns one Redis hash field. Replacing its whole snapshot makes
// completion and reconnect independent of journal retention and retry events.
type activeLogPeer struct {
	Expires int64 `json:"expires"`
	Items   []Log `json:"items"`
}

type activeLogFeed struct {
	mu       sync.Mutex
	local    map[string]Log
	peers    map[string]activeLogPeer
	node     string
	revision uint64
}

var activeUsageLogs = activeLogFeed{local: make(map[string]Log), peers: make(map[string]activeLogPeer), node: rand.Text()}

func BeginActiveLog(row Log) string {
	if row.UserId <= 0 || row.RequestId == "" || !row.IsStream {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(common.SessionSecret))
	fmt.Fprintf(mac, "active/%d/%s", row.UserId, row.RequestId)
	key := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
	// Only the explicitly supplied display metadata enters this registry. No
	// usage estimates, cost, raw request, or credential are recorded here.
	row = Log{LiveKey: key, InProgress: true, UserId: row.UserId, CreatedAt: row.CreatedAt, Username: row.Username, TokenName: row.TokenName, TokenId: row.TokenId, ModelName: row.ModelName, IsStream: true, ChannelId: row.ChannelId, ChannelName: row.ChannelName, Group: row.Group, Ip: row.Ip, RequestId: row.RequestId, Other: row.Other}
	f := &activeUsageLogs
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.local) >= maxLiveRefs {
		return ""
	}
	f.local[key] = row
	f.revision++
	return key
}

func UpdateActiveLogChannel(key string, channelID int, channelName, group string) {
	f := &activeUsageLogs
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.local[key]
	if !ok || row.ChannelId == channelID && row.ChannelName == channelName && row.Group == group {
		return
	}
	row.ChannelId, row.ChannelName, row.Group = channelID, channelName, group
	f.local[key] = row
	f.revision++
}

func FinishActiveLog(key string) {
	f := &activeUsageLogs
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.local[key]; ok {
		delete(f.local, key)
		f.revision++
	}
}

func ActiveLogRevision() uint64 {
	f := &activeUsageLogs
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().Unix()
	for node, peer := range f.peers {
		if peer.Expires <= now {
			delete(f.peers, node)
			f.revision++
		}
	}
	return f.revision
}

func (f *activeLogFeed) sync(ctx context.Context, client *redis.Client, key string) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	f.mu.Lock()
	local := activeLogPeer{Expires: time.Now().Add(30 * time.Second).Unix(), Items: make([]Log, 0, len(f.local))}
	for _, row := range f.local {
		local.Items = append(local.Items, row)
	}
	f.mu.Unlock()
	slices.SortFunc(local.Items, func(a, b Log) int { return strings.Compare(a.LiveKey, b.LiveKey) })
	pipe := client.Pipeline()
	if len(local.Items) == 0 {
		pipe.HDel(ctx, key, f.node)
	} else {
		data, err := common.Marshal(local)
		if err != nil {
			return
		}
		pipe.HSet(ctx, key, f.node, string(data))
		pipe.Expire(ctx, key, time.Minute)
	}
	values := pipe.HGetAll(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}
	now := time.Now().Unix()
	peers := make(map[string]activeLogPeer)
	expired := make(map[string]string)
	count := 0
	for node, raw := range values.Val() {
		if node == f.node {
			continue
		}
		var peer activeLogPeer
		if len(raw) > 16<<20 || common.UnmarshalJsonStr(raw, &peer) != nil {
			continue
		}
		if peer.Expires <= now {
			expired[node] = raw
			continue
		}
		if peer.Expires > now+60 || len(peer.Items) > maxLiveRefs || count+len(peer.Items) > maxLiveRefs {
			continue
		}
		count += len(peer.Items)
		peers[node] = peer
	}
	// Compare the exact expired snapshot before deleting: its owner may have
	// renewed the field after HGETALL.
	for node, raw := range expired {
		client.Eval(ctx, `if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0`, []string{key}, node, raw)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := len(peers) != len(f.peers)
	for node, peer := range peers {
		previous, ok := f.peers[node]
		if !ok || !slices.Equal(peer.Items, previous.Items) {
			changed = true
		}
	}
	f.peers = peers
	if changed {
		f.revision++
	}
}

// ActiveLogSnapshot uses the same explicit filters and role projection as the
// persisted list. Pending requests deliberately match neither result category.
func ActiveLogSnapshot(ctx context.Context, filter LogFilter, ownUserID, role int) ([]*Log, error) {
	rows := make([]*Log, 0)
	if filter.Validate() != nil || filter.Source == LogSourceTest || filter.Result != "" || filter.Type != LogTypeUnknown {
		return rows, nil
	}
	f := &activeUsageLogs
	f.mu.Lock()
	seen := make(map[string]bool)
	for _, row := range f.local {
		if ownUserID == 0 || row.UserId == ownUserID {
			copy := row
			rows = append(rows, &copy)
			seen[row.LiveKey] = true
		}
	}
	for _, peer := range f.peers {
		if peer.Expires <= time.Now().Unix() {
			continue
		}
		for _, row := range peer.Items {
			if !row.InProgress || row.UserId <= 0 || row.RequestId == "" || seen[row.LiveKey] || ownUserID != 0 && row.UserId != ownUserID {
				continue
			}
			copy := row
			rows = append(rows, &copy)
			seen[row.LiveKey] = true
		}
	}
	f.mu.Unlock()
	if len(rows) == 0 {
		return rows, nil
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserId)
	}
	users, err := currentLogUsers(ctx, ids, ownUserID == 0 && role >= common.RoleAdminUser)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row.useCurrentUser(users)
	}
	channels := logFilterChannels(filter.Channel, filter.Channels)
	rows = slices.DeleteFunc(rows, func(row *Log) bool {
		if !activeLogFilterMatches(row.ModelName, filter.ModelName, filter.ModelNames, true) ||
			!activeLogFilterMatches(row.Username, filter.Username, filter.Usernames, true) {
			return true
		}
		if !activeLogFilterMatches(row.Ip, filter.IP, filter.IPs, false) ||
			!activeLogFilterMatches(row.TokenName, filter.TokenName, filter.TokenNames, false) ||
			!activeLogFilterMatches(row.Group, filter.Group, filter.Groups, false) ||
			len(channels) > 0 && !slices.Contains(channels, row.ChannelId) {
			return true
		}
		if filter.RequestId != "" && filter.RequestId != row.RequestId || filter.UpstreamRequestId != "" {
			return true
		}
		if filter.StartTimestamp != 0 && row.CreatedAt < filter.StartTimestamp || filter.EndTimestamp != 0 && row.CreatedAt > filter.EndTimestamp {
			return true
		}
		metadata := gjson.Parse(row.Other)
		path, _, _ := strings.Cut(strings.TrimSpace(metadata.Get("request_path").String()), "?")
		return !activeLogFilterMatches(strings.TrimSpace(metadata.Get("reasoning_effort").String()), filter.ReasoningEffort, filter.ReasoningEfforts, false) ||
			!activeLogFilterMatches(strings.TrimSpace(metadata.Get("client_tool").String()), filter.ClientTool, filter.ClientTools, false) ||
			!activeLogFilterMatches(path, filter.RequestPath, filter.RequestPaths, false) ||
			!activeLogFilterMatches(metadata.Get("response_transport").String(), filter.ResponseTransport, filter.ResponseTransports, false)
	})
	slices.SortFunc(rows, func(a, b *Log) int {
		return cmp.Or(cmp.Compare(b.CreatedAt, a.CreatedAt), strings.Compare(a.LiveKey, b.LiveKey))
	})
	if len(rows) > maxLiveRefs {
		rows = rows[:maxLiveRefs]
	}
	if ownUserID != 0 {
		formatUserLogs(rows, 0)
	} else {
		FormatLogList(rows, role, false)
	}
	return rows, nil
}

func activeLogTextMatches(value, filter string) bool {
	if filter == "" {
		return true
	}
	if !strings.Contains(filter, "%") {
		return value == filter
	}
	return regexp.MustCompile("(?s)^" + strings.ReplaceAll(regexp.QuoteMeta(filter), "%", ".*") + "$").MatchString(value)
}

func activeLogFilterMatches(value, scalar string, plural []string, wildcards bool) bool {
	values := logFilterValues(scalar, plural)
	if len(values) == 0 {
		return true
	}
	for _, filter := range values {
		if wildcards && activeLogTextMatches(value, filter) || !wildcards && value == filter {
			return true
		}
	}
	return false
}
