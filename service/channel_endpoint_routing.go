package service

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
)

const channelEndpointContext = "channel_endpoint_candidates"

var unavailableChannelEndpoints = hot.NewHotCache[string, bool](hot.LRU, 4096).WithTTL(30 * time.Second).Build()

type channelEndpointCandidates struct {
	urls      []string
	failed    map[string]bool
	selected  string
	attempted bool
}

// SelectChannelEndpoint keeps a request on its selected mirror until that
// mirror fails. Each failed address is excluded for the rest of this request.
func SelectChannelEndpoint(c *gin.Context, channel *model.Channel) (string, error) {
	settings := channel.GetSetting()
	urls, err := settings.EndpointURLs(channel.GetBaseURL())
	if err != nil || len(urls) < 2 {
		return channel.GetBaseURL(), err
	}
	states, _ := c.Get(channelEndpointContext)
	byChannel, _ := states.(map[int]*channelEndpointCandidates)
	if byChannel == nil {
		byChannel = make(map[int]*channelEndpointCandidates)
		c.Set(channelEndpointContext, byChannel)
	}
	state := byChannel[channel.Id]
	if state == nil {
		state = &channelEndpointCandidates{urls: urls, failed: make(map[string]bool)}
		byChannel[channel.Id] = state
	}
	state.attempted = false
	if state.selected != "" && !state.failed[state.selected] {
		return state.selected, nil
	}
	var available, fallback []string
	for _, endpoint := range state.urls {
		if state.failed[endpoint] {
			continue
		}
		fallback = append(fallback, endpoint)
		_, cooling, _ := unavailableChannelEndpoints.Get(strconv.Itoa(channel.Id) + ":" + endpoint)
		if !cooling {
			available = append(available, endpoint)
		}
	}
	if len(available) == 0 {
		available = fallback // A cold process or an outage must still allow recovery.
	}
	if len(available) == 0 {
		return "", errors.New(i18n.T(c, i18n.MsgRelayChannelEndpointsExhausted))
	}
	state.selected = available[0]
	if settings.EndpointMode == "load_balance" {
		state.selected = available[rand.IntN(len(available))]
	}
	return state.selected, nil
}

// ContinueChannelEndpointSelection retains the finite candidate set when a
// channel probe creates a fresh request context for its next mirror attempt.
func ContinueChannelEndpointSelection(c, previous *gin.Context) {
	if previous == nil {
		return
	}
	if states, ok := previous.Get(channelEndpointContext); ok {
		c.Set(channelEndpointContext, states)
	}
}

// RecordChannelEndpointRequest attributes failures only after the transport
// actually targets the selected base URL, never an independent absolute route.
func RecordChannelEndpointRequest(c *gin.Context, target *url.URL) {
	states, _ := c.Get(channelEndpointContext)
	byChannel, _ := states.(map[int]*channelEndpointCandidates)
	state := byChannel[common.GetContextKeyInt(c, constant.ContextKeyChannelId)]
	if state == nil || target == nil {
		return
	}
	state.attempted = false
	base, err := url.Parse(state.selected)
	if err != nil {
		return
	}
	path := strings.TrimRight(base.Path, "/")
	state.attempted = strings.EqualFold(base.Scheme, target.Scheme) && strings.EqualFold(base.Host, target.Host) &&
		(target.Path == path || strings.HasPrefix(target.Path, path+"/"))
}

// FailChannelEndpoint reports an attributed mirror failure and whether another
// mirror can be tried. Mirror outages must not disable the shared credentials.
func FailChannelEndpoint(c *gin.Context, channel *model.Channel, apiErr *types.NewAPIError) (failed, retry bool) {
	if c == nil || apiErr == nil || c.Writer.Written() || c.Request.Context().Err() != nil {
		return false, false
	}
	if apiErr.GetErrorCode() != types.ErrorCodeDoRequestFailed && apiErr.StatusCode != http.StatusBadGateway && apiErr.StatusCode != http.StatusServiceUnavailable && apiErr.StatusCode != http.StatusGatewayTimeout {
		return false, false
	}
	custom := channel.GetOtherSettings().AdvancedCustom
	if custom != nil {
		route, found := custom.MatchPathForModel(c.Request.URL.Path, common.GetContextKeyString(c, constant.ContextKeyOriginalModel))
		if found && !strings.HasPrefix(strings.TrimSpace(route.UpstreamPath), "/") {
			return false, false
		}
	}
	states, _ := c.Get(channelEndpointContext)
	byChannel, _ := states.(map[int]*channelEndpointCandidates)
	state := byChannel[channel.Id]
	if state == nil || !state.attempted {
		return false, false
	}
	state.failed[state.selected] = true
	unavailableChannelEndpoints.Set(strconv.Itoa(channel.Id)+":"+state.selected, true)
	return true, len(state.failed) < len(state.urls) && !GetChannelConstraints(c).SuppressesRetry()
}
