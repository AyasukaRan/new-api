// Package keyusage coordinates weighted credential allocation through Redis.
// Its ledger is independent of customer billing and never stores credentials.
package keyusage

import "errors"

// MaxCostUSD bounds the precision of a single routing cost observation.
const MaxCostUSD = 1_000_000

var (
	ErrUnavailable    = errors.New("shared key usage storage is unavailable")
	ErrNotFound       = errors.New("key usage reservation was not found")
	ErrConflict       = errors.New("key usage reservation identity conflicts")
	ErrLimit          = errors.New("key usage storage limit exceeded")
	ErrCostOutOfRange = errors.New("key usage cost projection is out of range")
)

type Candidate struct {
	Identity     string   `json:"identity"`
	Weight       int      `json:"weight"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
}

// Reservation freezes the chosen credential. StartedAt is Unix time in seconds.
type Reservation struct {
	ChannelID   int    `json:"channel_id"`
	ID          string `json:"id"`
	KeyIdentity string `json:"key_identity"`
	StartedAt   int64  `json:"started_at"`
}

type Observation struct {
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CostUSD         *float64 `json:"cost_usd"`
	CostActual      bool     `json:"cost_actual"`
	TokensEstimated bool     `json:"tokens_estimated"`
}

type Report struct {
	WindowStart   int64         `json:"window_start"`
	WindowEnd     int64         `json:"window_end"`
	ObservedSince int64         `json:"observed_since"`
	Keys          []KeyStats    `json:"keys"`
	Series        []SeriesPoint `json:"series"`
}

// KeyStats excludes in-flight estimates from usage and cost. RequestCount
// counts attempts, including released reservations; pending has its own count.
// CostUSD sums known costs and is nil only when no completed cost is known.
type KeyStats struct {
	KeyIdentity             string   `json:"key_identity"`
	InputTokens             int64    `json:"input_tokens"`
	OutputTokens            int64    `json:"output_tokens"`
	RequestCount            int64    `json:"request_count"`
	CostUSD                 *float64 `json:"cost_usd"`
	ActualCostRequests      int64    `json:"actual_cost_requests"`
	EstimatedCostRequests   int64    `json:"estimated_cost_requests"`
	UnknownCostRequests     int64    `json:"unknown_cost_requests"`
	PendingRequests         int64    `json:"pending_requests"`
	TokensEstimatedRequests int64    `json:"tokens_estimated_requests"`
}

// SeriesPoint.Timestamp is a bucket's Unix start: five minutes for windows up
// to 24 hours, one hour for longer windows. Edge buckets are clipped to the
// requested rolling window.
type SeriesPoint struct {
	Timestamp int64      `json:"timestamp"`
	Keys      []KeyStats `json:"keys"`
}
