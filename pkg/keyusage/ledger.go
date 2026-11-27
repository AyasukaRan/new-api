package keyusage

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

const (
	maxCandidates  = 10000
	maxQueryEvents = 5000000
	pageSize       = 512
)

var ledgerScript = redis.NewScript(ledgerLua)

type ledgerEvent struct {
	ID              string   `json:"id"`
	KeyIdentity     string   `json:"key_identity"`
	StartedAt       int64    `json:"started_at"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CostUSD         *float64 `json:"cost_usd"`
	CostActual      bool     `json:"cost_actual"`
	TokensEstimated bool     `json:"tokens_estimated"`
	Status          string   `json:"status"`
}

type ledgerReply struct {
	Again         bool         `json:"again"`
	Error         string       `json:"error"`
	Event         *ledgerEvent `json:"event"`
	WindowStart   int64        `json:"window_start"`
	WindowEnd     int64        `json:"window_end"`
	ObservedSince int64        `json:"observed_since"`
	Items         []string     `json:"items"`
}

// Reserve selects and reserves one attempt atomically across gateway nodes.
// A nonempty preferred identity restricts selection to that eligible credential.
// Replaying an ID returns its original reservation without charging it twice.
func Reserve(ctx context.Context, channelID int, id string, candidates []Candidate, preferred string) (*Reservation, error) {
	if !validID(id) || len(candidates) == 0 || len(candidates) > maxCandidates {
		return nil, errors.New("invalid key usage reservation")
	}
	candidates = slices.Clone(candidates)
	slices.SortFunc(candidates, func(a, b Candidate) int { return cmp.Compare(a.Identity, b.Identity) })
	foundPreferred := preferred == ""
	for index, candidate := range candidates {
		if !validIdentity(candidate.Identity) || candidate.Weight <= 0 || candidate.Weight > 1000000 ||
			!validUsage(candidate.InputTokens, candidate.OutputTokens, candidate.CostUSD) ||
			index > 0 && candidates[index-1].Identity == candidate.Identity {
			return nil, errors.New("invalid key usage candidate")
		}
		foundPreferred = foundPreferred || candidate.Identity == preferred
	}
	if !foundPreferred {
		return nil, ErrConflict
	}
	reply, err := run(ctx, channelID, "reserve", map[string]any{"id": id, "candidates": candidates, "preferred": preferred})
	if err != nil {
		return nil, err
	}
	if reply.Event == nil {
		return nil, fmt.Errorf("%w: invalid reservation response", ErrUnavailable)
	}
	return &Reservation{ChannelID: channelID, ID: reply.Event.ID, KeyIdentity: reply.Event.KeyIdentity, StartedAt: reply.Event.StartedAt / 1000}, nil
}

// Finish replaces an estimate with observed usage once. Nil releases in-flight
// usage while retaining the attempt for a later supplier invoice. Actual cost
// already supplied by CorrectCost is never downgraded to an estimate.
func Finish(ctx context.Context, res *Reservation, observation *Observation) error {
	if !validReservation(res) || observation != nil && (!validUsage(observation.InputTokens, observation.OutputTokens, observation.CostUSD) || observation.CostActual && observation.CostUSD == nil) {
		return errors.New("invalid key usage observation")
	}
	_, err := run(ctx, res.ChannelID, "finish", map[string]any{"id": res.ID, "identity": res.KeyIdentity, "started_at": res.StartedAt, "observation": observation})
	return err
}

// CorrectCost replaces this attempt's cost with a supplier-confirmed amount.
// The original event time is retained, including after the 24-hour window ends.
func CorrectCost(ctx context.Context, channelID int, id string, costUSD float64) error {
	if !math.IsNaN(costUSD) && !math.IsInf(costUSD, 0) && costUSD > MaxCostUSD {
		return ErrCostOutOfRange
	}
	if !validID(id) || !validUsage(0, 0, &costUSD) {
		return errors.New("invalid key usage cost")
	}
	_, err := run(ctx, channelID, "correct", map[string]any{"id": id, "cost_usd": costUSD})
	return err
}

// Renew extends an active reservation's 15-minute lease. Expired reservations
// cannot be resurrected; Finish can still reconcile their eventual outcome.
func Renew(ctx context.Context, res *Reservation) error {
	if !validReservation(res) {
		return errors.New("invalid key usage reservation")
	}
	_, err := run(ctx, res.ChannelID, "renew", map[string]any{"id": res.ID, "identity": res.KeyIdentity, "started_at": res.StartedAt})
	return err
}

// Query reads retained events in bounded pages. It is a reporting view: costs
// reconciled during pagination can appear in the same report. Admission uses
// the atomic rolling totals, never this eventually consistent reporting view.
func Query(ctx context.Context, channelID, hours int) (Report, error) {
	report := Report{Keys: []KeyStats{}, Series: []SeriesPoint{}}
	if hours < 1 || hours > 30*24 {
		return report, errors.New("key usage window must be between 1 and 720 hours")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	keys := make(map[string]*KeyStats)
	buckets := make(map[int64]map[string]*KeyStats)
	bucketSeconds := int64(3600)
	if hours <= 24 {
		bucketSeconds = 300
	}
	var start, end, cursor int64
	offset, count := 0, 0
	for {
		reply, err := run(ctx, channelID, "query", map[string]any{"hours": hours, "start": start, "end": end, "cursor": cursor, "offset": offset})
		if err != nil {
			return report, err
		}
		start, end = reply.WindowStart, reply.WindowEnd
		report.WindowStart, report.WindowEnd = start/1000, end/1000
		report.ObservedSince = reply.ObservedSince / 1000
		for _, raw := range reply.Items {
			var event ledgerEvent
			if err := common.UnmarshalJsonStr(raw, &event); err != nil || !validIdentity(event.KeyIdentity) || !validUsage(event.InputTokens, event.OutputTokens, event.CostUSD) {
				return report, fmt.Errorf("%w: invalid retained usage", ErrUnavailable)
			}
			if event.StartedAt != cursor {
				cursor, offset = event.StartedAt, 0
			}
			offset++
			count++
			if count > maxQueryEvents {
				return report, ErrLimit
			}
			if keys[event.KeyIdentity] == nil {
				if len(keys) >= maxCandidates {
					return report, ErrLimit
				}
				keys[event.KeyIdentity] = &KeyStats{KeyIdentity: event.KeyIdentity}
			}
			addEventStats(keys[event.KeyIdentity], event)
			bucket := event.StartedAt / (bucketSeconds * 1000) * bucketSeconds
			if buckets[bucket] == nil {
				buckets[bucket] = make(map[string]*KeyStats)
			}
			if buckets[bucket][event.KeyIdentity] == nil {
				buckets[bucket][event.KeyIdentity] = &KeyStats{KeyIdentity: event.KeyIdentity}
			}
			addEventStats(buckets[bucket][event.KeyIdentity], event)
		}
		if len(reply.Items) < pageSize {
			break
		}
	}
	for _, stats := range keys {
		report.Keys = append(report.Keys, *stats)
	}
	slices.SortFunc(report.Keys, func(a, b KeyStats) int { return cmp.Compare(a.KeyIdentity, b.KeyIdentity) })
	for timestamp, values := range buckets {
		point := SeriesPoint{Timestamp: timestamp, Keys: make([]KeyStats, 0, len(values))}
		for _, stats := range values {
			point.Keys = append(point.Keys, *stats)
		}
		slices.SortFunc(point.Keys, func(a, b KeyStats) int { return cmp.Compare(a.KeyIdentity, b.KeyIdentity) })
		report.Series = append(report.Series, point)
	}
	slices.SortFunc(report.Series, func(a, b SeriesPoint) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	return report, nil
}

func addEventStats(stats *KeyStats, event ledgerEvent) {
	stats.RequestCount++
	if event.Status == "pending" {
		stats.PendingRequests++
		return
	}
	stats.InputTokens += event.InputTokens
	stats.OutputTokens += event.OutputTokens
	if event.TokensEstimated {
		stats.TokensEstimatedRequests++
	}
	if event.CostUSD == nil {
		stats.UnknownCostRequests++
		return
	}
	if stats.CostUSD == nil {
		stats.CostUSD = new(float64)
	}
	*stats.CostUSD += *event.CostUSD
	if event.CostActual {
		stats.ActualCostRequests++
	} else {
		stats.EstimatedCostRequests++
	}
}

func validUsage(input, output int64, cost *float64) bool {
	return input >= 0 && input <= common.MaxQuota && output >= 0 && output <= common.MaxQuota &&
		(cost == nil || !math.IsNaN(*cost) && !math.IsInf(*cost, 0) && *cost >= 0 && *cost <= MaxCostUSD)
}

func validIdentity(identity string) bool {
	if len(identity) != 64 {
		return false
	}
	_, err := hex.DecodeString(identity)
	return err == nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 160 {
		return false
	}
	for _, character := range id {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == ':') {
			return false
		}
	}
	return true
}

func validReservation(res *Reservation) bool {
	return res != nil && res.ChannelID > 0 && validID(res.ID) && validIdentity(res.KeyIdentity) && res.StartedAt > 0
}

func redisKeys(channelID int) []string {
	prefix := "new-api:key_usage:v1:{" + strconv.Itoa(channelID) + "}:"
	return []string{prefix + "events", prefix + "records", prefix + "window", prefix + "leases", prefix + "totals"}
}

func run(ctx context.Context, channelID int, operation string, arguments any) (ledgerReply, error) {
	var reply ledgerReply
	if channelID <= 0 {
		return reply, errors.New("invalid key usage channel")
	}
	if !common.RedisEnabled || common.RDB == nil {
		return reply, ErrUnavailable
	}
	encoded, err := common.Marshal(arguments)
	if err != nil {
		return reply, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		result, err := ledgerScript.Run(ctx, common.RDB, redisKeys(channelID), operation, string(encoded)).Text()
		if err != nil {
			return reply, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		reply = ledgerReply{}
		if err := common.UnmarshalJsonStr(result, &reply); err != nil {
			return reply, fmt.Errorf("%w: invalid ledger response: %v", ErrUnavailable, err)
		}
		if reply.Again {
			if err := ctx.Err(); err != nil {
				return reply, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			continue
		}
		switch reply.Error {
		case "":
			return reply, nil
		case "not_found":
			return reply, ErrNotFound
		case "conflict":
			return reply, ErrConflict
		case "limit":
			return reply, ErrLimit
		default:
			return reply, fmt.Errorf("%w: invalid ledger state", ErrUnavailable)
		}
	}
}
