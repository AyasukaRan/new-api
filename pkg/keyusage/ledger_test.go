package keyusage

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testChannelSequence atomic.Int64

type ledgerFixture struct {
	channel int
	server  *miniredis.Miniredis
	now     time.Time
}

func useLedger(t *testing.T, controlledClock bool) ledgerFixture {
	t.Helper()
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	fixture := ledgerFixture{channel: 1000000000 + os.Getpid()*100 + int(testChannelSequence.Add(1)), now: time.Date(2026, 11, 27, 8, 0, 0, 123000000, time.UTC)}
	address := os.Getenv("TEST_KEY_USAGE_REDIS_ADDR")
	if address == "" || controlledClock {
		fixture.server = miniredis.RunT(t)
		fixture.server.SetTime(fixture.now)
		address = fixture.server.Addr()
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	require.NoError(t, client.Ping(context.Background()).Err())
	common.RedisEnabled, common.RDB = true, client
	t.Cleanup(func() {
		require.NoError(t, client.Del(context.Background(), redisKeys(fixture.channel)...).Err())
		require.NoError(t, client.Close())
		common.RedisEnabled, common.RDB = previousEnabled, previousClient
	})
	return fixture
}

func TestWeightedReservationsShareUsageAndAreIdempotent(t *testing.T) {
	fixture := useLedger(t, false)
	ctx := context.Background()
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	candidates := []Candidate{{Identity: a, Weight: 1, InputTokens: 100}, {Identity: b, Weight: 3, InputTokens: 100}}
	var reservations []*Reservation
	for index, expected := range []string{b, a, b, b} {
		id := fmt.Sprintf("weighted-%d", index)
		reservation, err := Reserve(ctx, fixture.channel, id, candidates, "")
		require.NoError(t, err)
		assert.Equal(t, expected, reservation.KeyIdentity)
		replay, err := Reserve(ctx, fixture.channel, id, candidates, "")
		require.NoError(t, err)
		assert.Equal(t, reservation, replay)
		reservations = append(reservations, reservation)
	}
	report, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, report.Keys, 2)
	assert.Equal(t, int64(1), report.Keys[0].PendingRequests)
	assert.Equal(t, int64(3), report.Keys[1].PendingRequests)
	assert.Zero(t, report.Keys[0].InputTokens)
	assert.Nil(t, report.Keys[0].CostUSD)
	for _, reservation := range reservations {
		observation := &Observation{InputTokens: 100, OutputTokens: 20, CostUSD: common.GetPointer(0.01)}
		require.NoError(t, Finish(ctx, reservation, observation))
		require.NoError(t, Finish(ctx, reservation, observation))
	}
	report, err = Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	assert.Equal(t, int64(100), report.Keys[0].InputTokens)
	assert.Equal(t, int64(300), report.Keys[1].InputTokens)
	assert.Equal(t, int64(60), report.Keys[1].OutputTokens)
	assert.Equal(t, int64(3), report.Keys[1].RequestCount)
	assert.Equal(t, int64(3), report.Keys[1].EstimatedCostRequests)
	assert.Zero(t, report.Keys[1].PendingRequests)
	require.NotNil(t, report.Keys[1].CostUSD)
	assert.InDelta(t, 0.03, *report.Keys[1].CostUSD, 1e-12)
}

func TestJointSelectionUsesCostAndRequestSize(t *testing.T) {
	for _, tc := range []struct {
		name             string
		inputA, inputB   int64
		costA, costB     *float64
		weightA, weightB int
		nextInput        int64
		expected         string
	}{
		{"cost_breaks_equal_token_load", 1000, 1000, common.GetPointer(0.09), common.GetPointer(0.01), 1, 1, 100, "b"},
		{"unknown_cost_is_not_free", 100, 200, common.GetPointer(100.0), nil, 1, 1, 100, "a"},
		{"large_request_preserves_weighted_share", 100, 300, nil, nil, 1, 3, 1000, "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := useLedger(t, false)
			ctx := context.Background()
			a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
			for index, seed := range []Candidate{{Identity: a, Weight: tc.weightA, InputTokens: tc.inputA, CostUSD: tc.costA}, {Identity: b, Weight: tc.weightB, InputTokens: tc.inputB, CostUSD: tc.costB}} {
				reservation, err := Reserve(ctx, fixture.channel, fmt.Sprintf("seed-%d", index), []Candidate{seed}, seed.Identity)
				require.NoError(t, err)
				require.NoError(t, Finish(ctx, reservation, &Observation{InputTokens: seed.InputTokens, CostUSD: seed.CostUSD, CostActual: seed.CostUSD != nil}))
			}
			candidates := []Candidate{{Identity: a, Weight: tc.weightA, InputTokens: tc.nextInput, CostUSD: common.GetPointer(0.01)}, {Identity: b, Weight: tc.weightB, InputTokens: tc.nextInput, CostUSD: common.GetPointer(0.01)}}
			reservation, err := Reserve(ctx, fixture.channel, "joint", candidates, "")
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat(tc.expected, 64), reservation.KeyIdentity)
		})
	}
}

func TestCostCorrectionPreservesCoverageAndCannotBeDowngraded(t *testing.T) {
	fixture := useLedger(t, false)
	ctx := context.Background()
	identity := strings.Repeat("a", 64)
	candidates := []Candidate{{Identity: identity, Weight: 1, InputTokens: 100, CostUSD: common.GetPointer(0.2)}}
	failed, err := Reserve(ctx, fixture.channel, "failed", candidates, "")
	require.NoError(t, err)
	require.NoError(t, Finish(ctx, failed, nil))
	completed, err := Reserve(ctx, fixture.channel, "completed", candidates, "")
	require.NoError(t, err)
	require.NoError(t, Finish(ctx, completed, &Observation{InputTokens: 80, CostUSD: common.GetPointer(0.1)}))
	report, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, report.Keys, 1)
	assert.Equal(t, int64(1), report.Keys[0].UnknownCostRequests)
	assert.Equal(t, int64(1), report.Keys[0].EstimatedCostRequests)
	require.NotNil(t, report.Keys[0].CostUSD)
	assert.InDelta(t, 0.1, *report.Keys[0].CostUSD, 1e-12)
	assert.ErrorIs(t, CorrectCost(ctx, fixture.channel, failed.ID, MaxCostUSD+1), ErrCostOutOfRange)
	require.NoError(t, CorrectCost(ctx, fixture.channel, failed.ID, 0.03))
	require.NoError(t, CorrectCost(ctx, fixture.channel, failed.ID, 0.03))
	require.NoError(t, Finish(ctx, failed, nil))
	require.NoError(t, CorrectCost(ctx, fixture.channel, completed.ID, 0))
	require.NoError(t, Finish(ctx, completed, &Observation{InputTokens: 999, CostUSD: common.GetPointer(20.0)}))
	pending, err := Reserve(ctx, fixture.channel, "invoice-before-finish", candidates, "")
	require.NoError(t, err)
	require.NoError(t, CorrectCost(ctx, fixture.channel, pending.ID, 0.07))
	require.NoError(t, Finish(ctx, pending, &Observation{InputTokens: 40, CostUSD: common.GetPointer(0.5), TokensEstimated: true}))
	report, err = Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	assert.Equal(t, int64(120), report.Keys[0].InputTokens)
	assert.Equal(t, int64(3), report.Keys[0].ActualCostRequests)
	assert.Equal(t, int64(1), report.Keys[0].TokensEstimatedRequests)
	assert.Zero(t, report.Keys[0].UnknownCostRequests)
	assert.Zero(t, report.Keys[0].EstimatedCostRequests)
	require.NotNil(t, report.Keys[0].CostUSD)
	assert.InDelta(t, 0.1, *report.Keys[0].CostUSD, 1e-12)
}

func TestLeaseExpiryReleasesEstimatesAndAllowsLateUsage(t *testing.T) {
	fixture := useLedger(t, true)
	ctx := context.Background()
	identity := strings.Repeat("a", 64)
	reservation, err := Reserve(ctx, fixture.channel, "leased", []Candidate{{Identity: identity, Weight: 1, InputTokens: 100, OutputTokens: 200, CostUSD: common.GetPointer(0.5)}}, "")
	require.NoError(t, err)
	fixture.server.SetTime(fixture.now.Add(14 * time.Minute))
	require.NoError(t, Renew(ctx, reservation))
	fixture.server.SetTime(fixture.now.Add(16 * time.Minute))
	report, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, report.Keys, 1)
	assert.Equal(t, int64(1), report.Keys[0].PendingRequests)
	fixture.server.SetTime(fixture.now.Add(29 * time.Minute))
	report, err = Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	assert.Zero(t, report.Keys[0].PendingRequests)
	assert.Zero(t, report.Keys[0].InputTokens)
	assert.Nil(t, report.Keys[0].CostUSD)
	assert.ErrorIs(t, Renew(ctx, reservation), ErrConflict)
	require.NoError(t, Finish(ctx, reservation, &Observation{InputTokens: 90, OutputTokens: 5, CostUSD: common.GetPointer(0.01), CostActual: true}))
	report, err = Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	assert.Equal(t, int64(90), report.Keys[0].InputTokens)
	assert.Equal(t, int64(5), report.Keys[0].OutputTokens)
	assert.Equal(t, int64(1), report.Keys[0].ActualCostRequests)
}

func TestRollingWindowAndLateInvoiceUseOriginalEventTime(t *testing.T) {
	fixture := useLedger(t, true)
	ctx := context.Background()
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	reservation, err := Reserve(ctx, fixture.channel, "original", []Candidate{{Identity: a, Weight: 1, InputTokens: 100}}, "")
	require.NoError(t, err)
	require.NoError(t, Finish(ctx, reservation, &Observation{InputTokens: 100}))
	fixture.server.SetTime(fixture.now.Add(24*time.Hour - time.Millisecond))
	report, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, report.Keys, 1)
	fixture.server.SetTime(fixture.now.Add(24 * time.Hour))
	report, err = Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	assert.Empty(t, report.Keys)
	assert.Equal(t, fixture.now.Unix(), report.ObservedSince, "collection coverage starts before this empty query window")
	require.NoError(t, CorrectCost(ctx, fixture.channel, reservation.ID, 0.25))
	report, err = Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	assert.Empty(t, report.Keys, "late invoices must not bring old events back into the rolling window")
	report, err = Query(ctx, fixture.channel, 48)
	require.NoError(t, err)
	require.Len(t, report.Keys, 1)
	require.NotNil(t, report.Keys[0].CostUSD)
	assert.Equal(t, 0.25, *report.Keys[0].CostUSD)
	assert.Equal(t, fixture.now.Truncate(time.Hour).Unix(), report.Series[0].Timestamp)
	selected, err := Reserve(ctx, fixture.channel, "new-day", []Candidate{{Identity: a, Weight: 1, InputTokens: 100}, {Identity: b, Weight: 1, InputTokens: 100}}, a)
	require.NoError(t, err)
	assert.Equal(t, a, selected.KeyIdentity)
	fixture.server.SetTime(fixture.now.Add(30 * 24 * time.Hour))
	assert.ErrorIs(t, CorrectCost(ctx, fixture.channel, reservation.ID, 1), ErrNotFound)
}

func TestReportPaginatesEventsWithIdenticalTimestamps(t *testing.T) {
	fixture := useLedger(t, true)
	ctx := context.Background()
	identity := strings.Repeat("a", 64)
	// One more than a page exercises timestamp ties at the pagination boundary.
	for index := range 513 {
		reservation, err := Reserve(ctx, fixture.channel, fmt.Sprintf("page-%04d", index), []Candidate{{Identity: identity, Weight: 1, InputTokens: 1}}, "")
		require.NoError(t, err)
		require.NoError(t, Finish(ctx, reservation, &Observation{InputTokens: 1}))
	}
	report, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, report.Keys, 1)
	assert.Equal(t, int64(513), report.Keys[0].RequestCount)
	assert.Equal(t, int64(513), report.Keys[0].InputTokens)
	require.Len(t, report.Series, 1)
	assert.Equal(t, int64(513), report.Series[0].Keys[0].RequestCount)
}

func TestReportUsesFiveMinuteAndHourlyBuckets(t *testing.T) {
	fixture := useLedger(t, true)
	ctx := context.Background()
	identity := strings.Repeat("a", 64)
	for index, elapsed := range []time.Duration{0, 6 * time.Minute} {
		fixture.server.SetTime(fixture.now.Add(elapsed))
		reservation, err := Reserve(ctx, fixture.channel, fmt.Sprintf("bucket-%d", index), []Candidate{{Identity: identity, Weight: 1, InputTokens: 10}}, "")
		require.NoError(t, err)
		require.NoError(t, Finish(ctx, reservation, &Observation{InputTokens: 10}))
	}
	day, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, day.Series, 2)
	assert.Equal(t, int64(300), day.Series[1].Timestamp-day.Series[0].Timestamp)
	assert.Equal(t, fixture.now.Unix(), day.ObservedSince)
	week, err := Query(ctx, fixture.channel, 7*24)
	require.NoError(t, err)
	require.Len(t, week.Series, 1)
	assert.Equal(t, int64(20), week.Series[0].Keys[0].InputTokens)
}

func TestConcurrentAdmissionsSeePendingUsage(t *testing.T) {
	fixture := useLedger(t, false)
	ctx := context.Background()
	candidates := []Candidate{{Identity: strings.Repeat("a", 64), Weight: 1, InputTokens: 100}, {Identity: strings.Repeat("b", 64), Weight: 1, InputTokens: 100}}
	var workers sync.WaitGroup
	errors := make(chan error, 8)
	for index := range 8 {
		workers.Go(func() {
			_, err := Reserve(ctx, fixture.channel, fmt.Sprintf("concurrent-%d", index), candidates, "")
			errors <- err
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	report, err := Query(ctx, fixture.channel, 24)
	require.NoError(t, err)
	require.Len(t, report.Keys, 2)
	assert.Equal(t, int64(4), report.Keys[0].PendingRequests)
	assert.Equal(t, int64(4), report.Keys[1].PendingRequests)
}

func TestInvalidUsageAndUnavailableRedisFailExplicitly(t *testing.T) {
	fixture := useLedger(t, false)
	ctx := context.Background()
	identity := strings.Repeat("a", 64)
	for _, candidate := range []Candidate{
		{Identity: identity, Weight: 0},
		{Identity: identity, Weight: 1, InputTokens: -1},
		{Identity: identity, Weight: 1, OutputTokens: math.MaxInt64},
		{Identity: identity, Weight: 1, CostUSD: common.GetPointer(math.NaN())},
		{Identity: identity, Weight: 1, CostUSD: common.GetPointer(math.Inf(1))},
		{Identity: "plain-secret", Weight: 1},
	} {
		_, err := Reserve(ctx, fixture.channel, "invalid", []Candidate{candidate}, "")
		require.Error(t, err)
	}
	candidates := []Candidate{{Identity: identity, Weight: 1, InputTokens: 10}}
	reservation, err := Reserve(ctx, fixture.channel, "valid", candidates, "")
	require.NoError(t, err)
	forged := *reservation
	forged.KeyIdentity = strings.Repeat("b", 64)
	assert.ErrorIs(t, Finish(ctx, &forged, nil), ErrConflict)
	_, err = Reserve(ctx, fixture.channel, "valid", []Candidate{{Identity: forged.KeyIdentity, Weight: 1}}, "")
	assert.ErrorIs(t, err, ErrConflict)
	common.RedisEnabled = false
	_, err = Reserve(ctx, fixture.channel, "disabled", candidates, "")
	assert.ErrorIs(t, err, ErrUnavailable)
	common.RedisEnabled = true
	broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 10 * time.Millisecond})
	previous := common.RDB
	common.RDB = broken
	_, err = Reserve(ctx, fixture.channel, "offline", candidates, "")
	common.RDB = previous
	require.NoError(t, broken.Close())
	assert.ErrorIs(t, err, ErrUnavailable)
}
