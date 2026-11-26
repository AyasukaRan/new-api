package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type Sub2APIInventoryView struct {
	AccountID     int64                           `json:"account_id"`
	Cached        bool                            `json:"cached"`
	Snapshot      *model.Sub2APIInventorySnapshot `json:"snapshot"`
	LastAttemptAt int64                           `json:"last_attempt_at"`
	NextAttemptAt int64                           `json:"next_attempt_at"`
	LastError     string                          `json:"last_error"`
	Records       []model.Sub2APISyncRecord       `json:"records"`
}

func GetSub2APIInventory(accountID int64) (Sub2APIInventoryView, error) {
	view := Sub2APIInventoryView{AccountID: accountID, Cached: true, Records: []model.Sub2APISyncRecord{}}
	account, err := model.GetSub2APIAccount(accountID)
	if err != nil || account.Credential == "" {
		return view, sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	state, err := model.ReadSub2APISyncState(account)
	if err != nil {
		return view, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	view.LastAttemptAt, view.NextAttemptAt = state.LastAttemptAt, max(state.NextAttemptAt, state.BlockedUntil)
	if len(state.Records) > 0 && state.Records[len(state.Records)-1].Success {
		view.NextAttemptAt = max(state.LastAttemptAt+30, state.BlockedUntil)
	}
	if state.Records != nil {
		view.Records = state.Records
	}
	if len(state.Records) > 0 {
		view.LastError = state.Records[len(state.Records)-1].ErrorCode
	}
	if account.InventoryJSON != "" {
		var inventory model.Sub2APIInventory
		if common.UnmarshalJsonStr(account.InventoryJSON, &inventory) != nil || inventory.Version != 1 {
			return view, sub2APIError("SUB2API_RESPONSE_INVALID")
		}
		for i := range inventory.Snapshot.Groups {
			group := &inventory.Snapshot.Groups[i]
			if group.Rate != nil {
				if effective, valid := group.Rate.EffectiveAt(time.Now()); valid {
					group.Rate.EffectiveRateMultiplier = effective
				} else {
					group.Rate = nil
				}
			}
		}
		view.Snapshot = &inventory.Snapshot
	}
	return view, nil
}

// Requests share a persisted account budget, including ledger pages. A budget
// exhaustion returns immediately; callers retain their progress for a later run.
func Sub2APIAccountRequest(ctx context.Context, accountID int64, method, path string, query url.Values, payload, result any) error {
	return model.WithSub2APISyncLock(ctx, accountID, "request", func() error {
		if _, _, _, err := sub2APILoadAccount(accountID); err != nil {
			return err
		}
		var denied *Sub2APIError
		err := model.UpdateSub2APIInventory(ctx, accountID, nil, func(state *model.Sub2APISyncState) error {
			now := time.Now().Unix()
			if state.BlockedUntil > now {
				denied = &Sub2APIError{Code: "SUB2API_RATE_LIMITED", RetryAt: state.BlockedUntil}
				return nil
			}
			// Rolling windows begin with this account's first request, not at a
			// wall-clock minute boundary. The headroom accommodates upstream UI.
			if now-state.WindowAt >= 60 {
				state.WindowAt, state.Requests, state.UsageRequests = now, 0, 0
			}
			usage := path == "/api/v1/usage"
			if state.Requests >= 120 || usage && state.UsageRequests >= 40 {
				denied = &Sub2APIError{Code: "SUB2API_RATE_LIMITED", RetryAt: state.WindowAt + 60}
				return nil
			}
			state.Requests++
			if usage {
				state.UsageRequests++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if denied != nil {
			return denied
		}
		err = sub2APIAccountRequest(ctx, accountID, method, path, query, payload, result)
		var upstream *Sub2APIError
		if errors.As(err, &upstream) && upstream.Status == http.StatusTooManyRequests {
			if saveErr := model.UpdateSub2APIInventory(ctx, accountID, nil, func(state *model.Sub2APISyncState) error {
				state.BlockedUntil = max(state.BlockedUntil, upstream.RetryAt)
				return nil
			}); saveErr != nil {
				return saveErr
			}
		}
		return err
	})
}

type sub2APIPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}

func (page sub2APIPage[T]) valid(number int, previousTotal int64) bool {
	if page.Page != number || page.Total < 0 || page.PageSize < 1 || page.PageSize > 1000 || page.Pages < 1 || page.Pages > 1000 || previousTotal >= 0 && page.Total != previousTotal {
		return false
	}
	pages := max(page.Total/int64(page.PageSize), 1)
	if page.Total > 0 && page.Total%int64(page.PageSize) != 0 {
		pages = page.Total/int64(page.PageSize) + 1
	}
	if int64(page.Pages) != pages || number > page.Pages {
		return false
	}
	expected := min(int64(page.PageSize), max(page.Total-int64(number-1)*int64(page.PageSize), 0))
	return int64(len(page.Items)) == expected
}

type sub2APIInventoryKey struct {
	sub2APIKey
	Name      string                 `json:"name"`
	Status    string                 `json:"status"`
	ExpiresAt *time.Time             `json:"expires_at"`
	Group     *sub2APIInventoryGroup `json:"group"`
}

type sub2APIInventoryGroup struct {
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Platform           string   `json:"platform"`
	RateMultiplier     *float64 `json:"rate_multiplier"`
	PeakRateEnabled    *bool    `json:"peak_rate_enabled"`
	PeakStart          string   `json:"peak_start"`
	PeakEnd            string   `json:"peak_end"`
	PeakRateMultiplier *float64 `json:"peak_rate_multiplier"`
}

func sub2APIInventoryKeyIdentity(account *model.Sub2APIAccount, key string) string {
	return common.GenerateHMACWithKey([]byte(key), "new-api:sub2api-inventory:v1:"+account.Identity)
}

func sub2APIRateValid(rate float64) bool {
	return !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate >= 0 && rate <= 10000
}

func loadSub2APIInventory(ctx context.Context, account *model.Sub2APIAccount) (*model.Sub2APIInventory, error) {
	inventory := &model.Sub2APIInventory{Version: 1, KeyIdentities: map[int64]string{}, Snapshot: model.Sub2APIInventorySnapshot{Keys: []model.Sub2APIInventoryKey{}, Groups: []model.Sub2APIInventoryGroup{}}}
	var user sub2APIUser
	if err := Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/auth/me", nil, nil, &user); err != nil {
		return nil, err
	}
	if user.ID != account.UpstreamUserID {
		return nil, sub2APIError("SUB2API_CREDENTIAL_INVALID")
	}
	account, err := model.GetSub2APIAccount(account.ID)
	if err != nil || account.Credential == "" {
		return nil, sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	inventory.AccountRevision = account.Revision
	groups := map[int64]sub2APIInventoryGroup{}
	groupKeys := map[int64]string{}
	seenIdentities := map[string]bool{}
	total := int64(-1)
	pageSize := 0
	for number := 1; ; number++ {
		if number > 100 {
			return nil, sub2APIError("SUB2API_INVENTORY_LIMIT")
		}
		var page sub2APIPage[sub2APIInventoryKey]
		query := url.Values{"page": {strconv.Itoa(number)}, "page_size": {"1000"}, "sort_by": {"id"}, "sort_order": {"asc"}}
		if err := Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/keys", query, nil, &page); err != nil {
			return nil, err
		}
		if page.Pages > 100 {
			return nil, sub2APIError("SUB2API_INVENTORY_LIMIT")
		}
		if !page.valid(number, total) || pageSize > 0 && page.PageSize != pageSize {
			return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
		}
		total, pageSize = page.Total, page.PageSize
		for _, key := range page.Items {
			if key.ID <= 0 || key.UserID != account.UpstreamUserID || len(key.Key) < 8 || len(key.Key) > 16384 || inventory.KeyIdentities[key.ID] != "" {
				return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
			}
			identity := sub2APIInventoryKeyIdentity(account, key.Key)
			if seenIdentities[identity] {
				return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
			}
			seenIdentities[identity] = true
			inventory.KeyIdentities[key.ID] = identity
			item := model.Sub2APIInventoryKey{ID: key.ID, Name: key.Name, MaskedKey: "••••" + key.Key[len(key.Key)-4:], Status: key.Status, ExpiresAt: key.ExpiresAt}
			if key.GroupID > 0 {
				item.GroupID = &key.GroupID
			}
			inventory.Snapshot.Keys = append(inventory.Snapshot.Keys, item)
			if key.Group != nil && key.Group.ID == key.GroupID {
				groups[key.GroupID] = *key.Group
			}
			if key.GroupID > 0 && key.Status == "active" && groupKeys[key.GroupID] == "" {
				groupKeys[key.GroupID] = key.Key
			}
		}
		if number == page.Pages {
			break
		}
	}
	if int64(len(inventory.Snapshot.Keys)) != total {
		return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	var available []sub2APIInventoryGroup
	if err := Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/groups/available", nil, nil, &available); err != nil {
		return nil, err
	}
	for _, group := range available {
		if group.ID <= 0 {
			return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
		}
		groups[group.ID] = group
	}
	var rates map[int64]float64
	if err := Sub2APIAccountRequest(ctx, account.ID, http.MethodGet, "/api/v1/groups/rates", nil, nil, &rates); err != nil {
		return nil, err
	}
	for _, key := range inventory.Snapshot.Keys {
		if key.GroupID != nil {
			if _, exists := groups[*key.GroupID]; !exists {
				return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
			}
		}
	}
	_, proxy, _, err := sub2APILoadAccount(account.ID)
	if err != nil {
		return nil, err
	}
	for id, group := range groups {
		if group.RateMultiplier == nil || group.PeakRateEnabled == nil {
			return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
		}
		peakRate := 0.0
		if group.PeakRateMultiplier != nil {
			peakRate = *group.PeakRateMultiplier
		}
		if *group.PeakRateEnabled {
			schedule := model.Sub2APIRate{ResolvedRateMultiplier: *group.RateMultiplier, EffectiveRateMultiplier: *group.RateMultiplier, PeakRateEnabled: true, PeakStart: group.PeakStart, PeakEnd: group.PeakEnd, PeakRateMultiplier: peakRate, Timezone: "UTC"}
			if _, valid := schedule.EffectiveAt(time.Now()); group.PeakRateMultiplier == nil || !valid {
				return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
			}
		}
		resolved := *group.RateMultiplier
		item := model.Sub2APIInventoryGroup{ID: id, Name: group.Name, Description: group.Description, Platform: group.Platform, GroupRateMultiplier: *group.RateMultiplier, PeakRateEnabled: *group.PeakRateEnabled, PeakStart: group.PeakStart, PeakEnd: group.PeakEnd, PeakRateMultiplier: peakRate}
		if rate, ok := rates[id]; ok {
			resolved, item.UserRateMultiplier = rate, &rate
		}
		if !sub2APIRateValid(*group.RateMultiplier) || !sub2APIRateValid(resolved) || *group.PeakRateEnabled && !sub2APIRateValid(peakRate) {
			return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
		}
		item.ResolvedRateMultiplier = resolved
		// One active key per group is sufficient: upstream resolves token rates
		// by user+group. This also supplies the server timezone for peak rates.
		if key := groupKeys[id]; *group.PeakRateEnabled && key != "" {
			rate, err := readSub2APIKeyRate(ctx, account.Origin, proxy, key)
			if err != nil {
				return nil, err
			}
			if rate.ResolvedRateMultiplier != resolved || !rate.PeakRateEnabled || rate.PeakStart != group.PeakStart || rate.PeakEnd != group.PeakEnd || rate.PeakRateMultiplier != peakRate {
				return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
			}
			item.Rate = rate
		} else if !*group.PeakRateEnabled {
			item.Rate = &model.Sub2APIRate{ResolvedRateMultiplier: resolved, EffectiveRateMultiplier: resolved, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		}
		inventory.Snapshot.Groups = append(inventory.Snapshot.Groups, item)
	}
	slices.SortFunc(inventory.Snapshot.Groups, func(a, b model.Sub2APIInventoryGroup) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	inventory.Snapshot.SyncedAt = time.Now().Unix()
	return inventory, nil
}

func readSub2APIKeyRate(ctx context.Context, origin, proxy, key string) (*model.Sub2APIRate, error) {
	var response struct {
		model.Sub2APIRate
		Object        string   `json:"object"`
		SchemaVersion int      `json:"schema_version"`
		BillingScope  string   `json:"billing_scope"`
		Resolved      *float64 `json:"resolved_rate_multiplier"`
		Effective     *float64 `json:"effective_rate_multiplier"`
		PeakEnabled   *bool    `json:"peak_rate_enabled"`
		PeakRate      *float64 `json:"peak_rate_multiplier"`
	}
	if err := sub2APIRequest(ctx, origin, proxy, key, http.MethodGet, "/v1/sub2api/billing", nil, nil, &response, true); err != nil {
		return nil, err
	}
	_, observedErr := time.Parse(time.RFC3339Nano, response.ObservedAt)
	if response.Resolved == nil || response.Effective == nil || response.PeakEnabled == nil || observedErr != nil || response.Object != "sub2api.key_billing" || response.SchemaVersion != 1 || response.BillingScope != "token" {
		return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	response.ResolvedRateMultiplier, response.EffectiveRateMultiplier, response.PeakRateEnabled = *response.Resolved, *response.Effective, *response.PeakEnabled
	if response.PeakRate != nil {
		response.PeakRateMultiplier = *response.PeakRate
	}
	if _, valid := response.EffectiveAt(time.Now()); !valid || response.PeakRateEnabled && response.PeakRate == nil {
		return nil, sub2APIError("SUB2API_RESPONSE_INVALID")
	}
	return &response.Sub2APIRate, nil
}

func SyncSub2APIInventory(ctx context.Context, accountID int64, manual bool) (Sub2APIInventoryView, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	fresh := false
	err := model.WithSub2APISyncLock(ctx, accountID, "inventory", func() error {
		account, err := model.GetSub2APIAccount(accountID)
		if err != nil || account.Credential == "" {
			return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
		}
		state, err := model.ReadSub2APISyncState(account)
		if err != nil {
			return err
		}
		if state.BlockedUntil > time.Now().Unix() {
			return &Sub2APIError{Code: "SUB2API_RATE_LIMITED", RetryAt: state.BlockedUntil}
		}
		if state.NextAttemptAt > time.Now().Unix() {
			var cached model.Sub2APIInventory
			sameRevision := account.InventoryJSON != "" && common.UnmarshalJsonStr(account.InventoryJSON, &cached) == nil && cached.Version == 1 && cached.AccountRevision == account.Revision
			successful := len(state.Records) > 0 && state.Records[len(state.Records)-1].Success
			if successful && sameRevision && (!manual || time.Now().Unix() < state.LastAttemptAt+30) {
				return nil
			}
			if !successful {
				return &Sub2APIError{Code: "SUB2API_RATE_LIMITED", RetryAt: state.NextAttemptAt}
			}
		}
		inventory, syncErr := loadSub2APIInventory(ctx, account)
		if syncErr == nil {
			syncErr = applySub2APIInventory(ctx, accountID, inventory)
		}
		// Persist the result even if the client cancelled; never publish a partial
		// inventory or erase the last complete one on a failed page.
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer saveCancel()
		if syncErr != nil {
			inventory = nil
		}
		err = model.UpdateSub2APIInventory(saveCtx, accountID, inventory, func(current *model.Sub2APISyncState) error {
			now := time.Now().Unix()
			current.LastAttemptAt = now
			record := model.Sub2APISyncRecord{At: now, Success: syncErr == nil}
			if syncErr == nil {
				current.Failures, current.NextAttemptAt = 0, now+300
				record.Keys, record.Groups = len(inventory.Snapshot.Keys), len(inventory.Snapshot.Groups)
			} else {
				current.Failures = min(current.Failures+1, 6)
				current.NextAttemptAt = now + min(int64(30)<<current.Failures, 1800)
				record.ErrorCode = Sub2APIErrorCode(syncErr)
				var upstream *Sub2APIError
				if errors.As(syncErr, &upstream) {
					record.UpstreamStatus = upstream.Status
					current.NextAttemptAt = max(current.NextAttemptAt, upstream.RetryAt)
					if upstream.Status == 429 {
						current.BlockedUntil = max(current.BlockedUntil, upstream.RetryAt)
					}
				}
			}
			current.Records = append(current.Records, record)
			if len(current.Records) > 10 {
				current.Records = current.Records[len(current.Records)-10:]
			}
			return nil
		})
		if err != nil {
			return err
		}
		fresh = syncErr == nil
		return syncErr
	})
	view, viewErr := GetSub2APIInventory(accountID)
	view.Cached = !fresh
	if err != nil {
		return view, err
	}
	return view, viewErr
}

func applySub2APIInventory(ctx context.Context, accountID int64, inventory *model.Sub2APIInventory) error {
	account, err := model.GetSub2APIAccount(accountID)
	if err != nil || account.Credential == "" {
		return sub2APIError("SUB2API_ACCOUNT_NOT_FOUND")
	}
	if inventory.AccountRevision != account.Revision {
		return sub2APIError("SUB2API_CHANNEL_CHANGED")
	}
	bindings, err := model.ListSub2APIKeyBindings(0)
	if err != nil {
		return err
	}
	channels := map[int]bool{}
	for _, binding := range bindings {
		if binding.AccountID == accountID {
			channels[binding.ChannelID] = true
		}
	}
	keys := map[string]model.Sub2APIInventoryKey{}
	for _, key := range inventory.Snapshot.Keys {
		keys[inventory.KeyIdentities[key.ID]] = key
	}
	rates := map[int64]*model.Sub2APIRate{}
	for _, group := range inventory.Snapshot.Groups {
		rates[group.ID] = group.Rate
	}
	channelIDs := make([]int, 0, len(channels))
	for channelID := range channels {
		channelIDs = append(channelIDs, channelID)
	}
	slices.Sort(channelIDs)
	for _, channelID := range channelIDs {
		channel, origin, err := sub2APIChannel(channelID)
		if err != nil {
			continue
		} // Deleted/retyped channels are not part of this inventory.
		if !sameSub2APIOrigin(account.Origin, origin) {
			continue
		}
		verified := []model.Sub2APIKeyBinding{}
		for _, raw := range channel.GetKeys() {
			key, ok := keys[sub2APIInventoryKeyIdentity(account, raw)]
			if !ok {
				continue
			}
			groupID := int64(0)
			if key.GroupID != nil {
				groupID = *key.GroupID
			}
			verified = append(verified, model.Sub2APIKeyBinding{ChannelID: channelID, KeyIdentity: channel.KeyIdentity(raw), UpstreamAPIKeyID: key.ID, UpstreamGroupID: groupID})
		}
		inventory.Channels = append(inventory.Channels, model.Sub2APIInventoryChannel{Channel: channel, Bindings: verified, Rates: rates})
	}
	return nil
}
