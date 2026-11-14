package service

import (
	"context"
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
)

const channelKeyAffinityContext = "channel_key_affinity"

var channelKeyAffinityCacheOnce sync.Once
var channelKeyAffinityCache *cachex.HybridCache[string]
var channelKeyAffinityLocks [256]sync.Mutex

type channelKeyAffinityState struct {
	CacheKey        string
	Expected        string
	Selected        string
	InitialSelected string
	ChannelID       int
	TTL             time.Duration
}

func getChannelKeyAffinityCache() *cachex.HybridCache[string] {
	channelKeyAffinityCacheOnce.Do(func() {
		capacity := operation_setting.GetChannelAffinitySetting().MaxEntries
		if capacity <= 0 {
			capacity = 100_000
		}
		channelKeyAffinityCache = cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace:    "new-api:channel_key_affinity:v1",
			Redis:        common.RDB,
			RedisEnabled: func() bool { return common.RedisEnabled && common.RDB != nil },
			RedisCodec:   cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] {
				return hot.NewHotCache[string, string](hot.LRU, capacity).WithTTL(time.Hour).WithJanitor().Build()
			},
		})
	})
	return channelKeyAffinityCache
}

func prepareChannelKeyAffinity(c *gin.Context, channelID int, group string) *channelKeyAffinityState {
	meta, matched := getChannelAffinityMeta(c)
	setting := operation_setting.GetChannelAffinitySetting()
	if !matched || meta.KeyAffinityScope == "" || !setting.Enabled || RequestPolicy(c).SessionMode == "off" {
		return nil
	}
	cacheKey := meta.KeyAffinityScope + ":" + common.GenerateHMAC(group) + ":" + strconv.Itoa(channelID)
	if value, ok := c.Get(channelKeyAffinityContext); ok {
		if state, ok := value.(*channelKeyAffinityState); ok && state.CacheKey == cacheKey {
			return state
		}
	}
	expected, _, err := getChannelKeyAffinityCache().Get(cacheKey)
	if err != nil {
		return nil
	}
	ttl := meta.TTLSeconds
	if ttl <= 0 {
		ttl = setting.DefaultTTLSeconds
	}
	if ttl <= 0 {
		ttl = 3600
	}
	state := &channelKeyAffinityState{CacheKey: cacheKey, Expected: expected, ChannelID: channelID, TTL: time.Duration(ttl) * time.Second}
	c.Set(channelKeyAffinityContext, state)
	return state
}

// SelectChannelKeyForRequest keeps a successful session on its healthy key.
// The model selector remains the authority for current permissions and health.
func SelectChannelKeyForRequest(c *gin.Context, channel *model.Channel, modelName string, batch bool) (model.RelayKeySelection, *types.NewAPIError) {
	group := GetSelectedRequestGroup(c)
	var state *channelKeyAffinityState
	if !batch && channel.ChannelInfo.IsMultiKey && channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeFailover && RequestPolicy(c).Attempts == 0 && channel.RelayKeyIdentity == "" {
		state = prepareChannelKeyAffinity(c, channel.Id, group)
		if state != nil && state.Expected != "" {
			candidate := *channel
			candidate.RelayKeyIdentity = state.Expected
			if selected, err := candidate.SelectRelayKeyForModel(modelName, false, group); err == nil && selected.Channel.ChannelInfo.IsMultiKey && selected.Channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeFailover {
				state.Selected = selected.Channel.KeyIdentity(selected.Key)
				if state.InitialSelected == "" {
					state.InitialSelected = state.Selected
				}
				return selected, nil
			}
		}
	}
	selected, apiErr := channel.SelectRelayKeyForModel(modelName, batch, group)
	if apiErr != nil || batch || selected.Channel == nil || !selected.Channel.ChannelInfo.IsMultiKey || selected.Channel.ChannelInfo.MultiKeyMode != constant.MultiKeyModeFailover {
		c.Set(channelKeyAffinityContext, nil)
		return selected, apiErr
	}
	if state == nil {
		state = prepareChannelKeyAffinity(c, channel.Id, group)
		if state != nil && RequestPolicy(c).Attempts == 0 && channel.RelayKeyIdentity == "" && state.Expected != "" {
			candidate := *selected.Channel
			candidate.RelayKeyIdentity = state.Expected
			if preferred, err := candidate.SelectRelayKeyForModel(modelName, false, group); err == nil && preferred.Channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeFailover {
				selected = preferred
			}
		}
	}
	if state != nil {
		state.Selected = selected.Channel.KeyIdentity(selected.Key)
		if state.InitialSelected == "" {
			state.InitialSelected = state.Selected
		}
	}
	return selected, nil
}

// RecordChannelKeyAffinity is called only after the relay records its outcome.
// Comparing the original binding prevents a late success from stealing a session
// back after another request successfully failed over to a different key.
func RecordChannelKeyAffinity(c *gin.Context) {
	if c == nil || c.Request == nil || c.Request.Context().Err() != nil {
		return
	}
	policy := RequestPolicy(c)
	if !policy.OutcomeRecorded || !policy.Successful {
		return
	}
	value, _ := c.Get(channelKeyAffinityContext)
	state, _ := value.(*channelKeyAffinityState)
	if state == nil || state.Selected == "" || state.ChannelID != c.GetInt("channel_id") {
		return
	}
	cache := getChannelKeyAffinityCache()
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		_, err := common.RDB.Eval(ctx, `local current = redis.call('GET', KEYS[1]) or ''; if current == ARGV[1] or (current == ARGV[4] and ARGV[2] ~= ARGV[4]) then redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3]); return 1 end; return 0`, []string{cache.FullKey(state.CacheKey)}, state.Expected, state.Selected, state.TTL.Milliseconds(), state.InitialSelected).Result()
		if err != nil {
			common.SysError(common.LogText("channel key affinity cache update failed: %v", err))
		}
		return
	}
	digest := sha256.Sum256([]byte(state.CacheKey))
	lock := &channelKeyAffinityLocks[digest[0]]
	lock.Lock()
	defer lock.Unlock()
	current, _, err := cache.Get(state.CacheKey)
	if err == nil && (current == state.Expected || current == state.InitialSelected && state.Selected != state.InitialSelected) && c.Request.Context().Err() == nil {
		_ = cache.SetWithTTL(state.CacheKey, state.Selected, state.TTL)
	}
}
