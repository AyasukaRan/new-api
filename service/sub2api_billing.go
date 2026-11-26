package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const sub2APIBillingContextKey = "sub2api_billing_attempt"

func BeginSub2APIBillingAttempt(c *gin.Context, info *relaycommon.RelayInfo) {
	// Clear a previous attempt even when this retry selects a different provider.
	c.Set(sub2APIBillingContextKey, nil)
	if info == nil || info.ChannelType != constant.ChannelTypeSub2API || info.IsChannelTest || !model.Sub2APIStorageReady() {
		return
	}
	channel := model.Channel{Id: info.ChannelId}
	identity := channel.KeyIdentity(info.ApiKey)
	binding, err := model.GetSub2APIKeyBinding(info.ChannelId, identity)
	if err != nil || binding == nil || !binding.SyncBills || binding.AccountID <= 0 || binding.UpstreamAPIKeyID <= 0 {
		return
	}
	origin, originErr := NormalizeSub2APIOrigin(info.ChannelBaseUrl)
	if originErr != nil || !sameSub2APIOrigin(origin, binding.AccountOrigin) {
		return
	}
	c.Set(sub2APIBillingContextKey, &model.Sub2APIBillingRef{AccountID: binding.AccountID, APIKeyID: binding.UpstreamAPIKeyID, KeyIdentity: identity})
}

func CaptureSub2APIBillingResponse(c *gin.Context, header http.Header) {
	value, _ := c.Get(sub2APIBillingContextKey)
	ref, ok := value.(*model.Sub2APIBillingRef)
	if !ok || ref == nil {
		return
	}
	id := strings.TrimSpace(header.Get("X-Client-Request-ID"))
	if id == "" || len(id) > 153 || strings.ContainsAny(id, "\r\n\x00") {
		return
	}
	ref.RequestID = "client:" + id
}

// Sub2API records a Responses WS generation under its original response.id,
// including when the downstream request used HTTP. Preserve the HTTP header as
// the primary candidate; neither ID is inferred from local request metadata.
func CaptureSub2APIResponseID(c *gin.Context, id string, websocket bool) {
	if id == "" || len(id) > 160 || !utf8.ValidString(id) || strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return
	}
	value, _ := c.Get(sub2APIBillingContextKey)
	ref, ok := value.(*model.Sub2APIBillingRef)
	if !ok || ref == nil || (ref.ResponseID != "" && ref.ResponseID != id) {
		return
	}
	ref.ResponseID = id
	if websocket {
		ref.RequestID = id
	}
}

type sub2APIUsageBill struct {
	ID             int64        `json:"id"`
	APIKeyID       int64        `json:"api_key_id"`
	RequestID      string       `json:"request_id"`
	ActualCost     *json.Number `json:"actual_cost"`
	RateMultiplier *float64     `json:"rate_multiplier"`
	BillingType    *int         `json:"billing_type"`
}

type sub2APIBillWindow struct {
	account, key int64
	day          string
}

// SyncSub2APIBills reads the account's own usage ledger. It does not guess from
// time/model/token counts, and never substitutes total_cost for actual_cost.
func SyncSub2APIBills(ctx context.Context, channelID int) (int, error) {
	if !model.Sub2APIStorageReady() {
		return 0, nil
	}
	pending, err := model.PendingSub2APIBills(ctx, channelID, 500)
	if err != nil {
		return 0, err
	}
	// Pending rows were authorized and frozen at request capture. Detaching a
	// channel only stops future captures; it must not orphan an existing bill.
	windows := make(map[sub2APIBillWindow][]model.Sub2APIBill)
	for _, bill := range pending {
		if channelID > 0 && bill.ChannelID != channelID {
			continue
		}
		window := sub2APIBillWindow{bill.AccountID, bill.APIKeyID, time.Unix(bill.CreatedAt, 0).UTC().Format("2006-01-02")}
		windows[window] = append(windows[window], bill)
	}
	matched := 0
	var firstErr error
	for window, bills := range windows {
		if ctx.Err() != nil {
			return matched, ctx.Err()
		}
		ids := make([]int64, 0, len(bills))
		wanted := make(map[string][]model.Sub2APIBill, len(bills))
		remaining := make(map[int64]bool, len(bills))
		for _, bill := range bills {
			ids = append(ids, bill.ID)
			remaining[bill.ID] = true
			wanted[bill.RequestID] = append(wanted[bill.RequestID], bill)
			if bill.ResponseID != "" && bill.ResponseID != bill.RequestID {
				wanted[bill.ResponseID] = append(wanted[bill.ResponseID], bill)
			}
		}
		if err := model.DeferSub2APIBills(ctx, ids); err != nil {
			return matched, err
		}
		day, _ := time.Parse("2006-01-02", window.day)
		query := url.Values{"api_key_id": {strconv.FormatInt(window.key, 10)}, "page_size": {"1000"}, "timezone": {"UTC"},
			"start_date": {day.AddDate(0, 0, -1).Format("2006-01-02")}, "end_date": {day.AddDate(0, 0, 1).Format("2006-01-02")}, "sort_order": {"asc"}, "sort_by": {"created_at"}}
		for page := 1; len(remaining) > 0; page++ {
			if page > 1000 {
				if firstErr == nil {
					firstErr = errors.New("Sub2API ledger page limit reached")
				}
				break
			}
			query.Set("page", strconv.Itoa(page))
			var response struct {
				Items []sub2APIUsageBill `json:"items"`
				Pages int                `json:"pages"`
			}
			if err := Sub2APIAccountRequest(ctx, window.account, http.MethodGet, "/api/v1/usage", query, nil, &response); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			for _, usage := range response.Items {
				if usage.APIKeyID != window.key || usage.ActualCost == nil || usage.BillingType == nil {
					continue
				}
				for _, bill := range wanted[usage.RequestID] {
					if !remaining[bill.ID] {
						continue
					}
					if err := model.SettleSub2APIBill(ctx, bill, usage.ID, usage.APIKeyID, usage.RequestID, usage.ActualCost.String(), *usage.BillingType, usage.RateMultiplier); err != nil {
						if firstErr == nil {
							firstErr = err
						}
						continue
					}
					delete(remaining, bill.ID)
					matched++
				}
			}
			if len(response.Items) == 0 || page >= response.Pages {
				break
			}
		}
	}
	return matched, firstErr
}

var sub2APIBillingOnce sync.Once

func StartSub2APIBillingSync() {
	sub2APIBillingOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			lastMetadata := int64(0)
			repairSince := time.Now().Add(-24 * time.Hour).Unix()
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
				if model.Sub2APIStorageReady() {
					bindings, err := model.ListSub2APIKeyBindings(0)
					if err == nil {
						channels := make([]int, 0, len(bindings))
						for _, binding := range bindings {
							if binding.SyncBills {
								channels = append(channels, binding.ChannelID)
							}
						}
						slices.Sort(channels)
						if model.RepairSub2APIBillQueue(ctx, repairSince, slices.Compact(channels)) == nil {
							repairSince = time.Now().Add(-10 * time.Minute).Unix()
						}
					}
					if time.Now().Unix()-lastMetadata >= 300 {
						channels := make([]int, 0, len(bindings))
						for _, binding := range bindings {
							if binding.SyncRates || binding.SyncBills || binding.AccountID > 0 {
								channels = append(channels, binding.ChannelID)
							}
						}
						accounts, err := model.ListSub2APIAccounts(0)
						if err == nil {
							for _, account := range accounts {
								if account.NeedsLogin {
									continue
								}
								if account.HasRefreshToken && account.ExpiresAt > 0 && account.ExpiresAt <= time.Now().Unix()+300 {
									_ = RefreshSub2APIAccount(ctx, account.ID, account.Revision)
								}
							}
						}
						slices.Sort(channels)
						for _, channel := range slices.Compact(channels) {
							_ = SyncSub2APIChannel(ctx, channel, 0)
						}
						lastMetadata = time.Now().Unix()
					}
					_, err = SyncSub2APIBills(ctx, 0)
					if err != nil {
						common.SysError("Sub2API bill synchronization deferred; recorded charges unchanged")
					}
				}
				cancel()
				<-ticker.C
			}
		})
	})
}
