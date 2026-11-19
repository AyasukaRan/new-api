package service

import (
	"cmp"
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type UnidentifiedSourceGroup struct {
	Kind          string                                     `json:"kind"`
	Conditions    []operation_setting.RequestSourceCondition `json:"conditions"`
	Count         int                                        `json:"count"`
	LastSeen      int64                                      `json:"last_seen"`
	CurrentSource string                                     `json:"current_source"`
}

type SourceReviewCursor struct {
	model.SourceReviewPosition
	Snapshot int64 `json:"s"`
	Days     int   `json:"d"`
}

type UnidentifiedSourceReview struct {
	NextCursor  string                    `json:"next_cursor"`
	Days        int                       `json:"days"`
	SampleLimit int                       `json:"sample_limit"`
	Truncated   bool                      `json:"truncated"`
	Reviewed    int                       `json:"reviewed"`
	Groups      []UnidentifiedSourceGroup `json:"groups"`
}

func ReviewUnidentifiedRequestSources(ctx context.Context, now int64, days, limit int, cursor model.SourceReviewPosition) (UnidentifiedSourceReview, error) {
	report := UnidentifiedSourceReview{Days: days, SampleLimit: limit, Groups: []UnidentifiedSourceGroup{}}
	window := model.SourceReviewWindow{SnapshotStart: now - int64(days)*86400, SnapshotEnd: now}
	window.Start, window.End = window.SnapshotStart, now
	if cursor.WindowEnd > 0 {
		window.End = cursor.WindowEnd
	}
	if days > 7 {
		width := int64(7 * 86400)
		if window.End < now {
			width = 86400
		}
		window.Start = max(window.SnapshotStart, window.End-width+1)
	}
	requests, next, err := model.RecentUnidentifiedSourceRequests(ctx, window, cursor, limit)
	if err != nil {
		return report, err
	}
	if next != nil {
		next.WindowEnd = window.End
	} else if window.Start > window.SnapshotStart {
		next = &model.SourceReviewPosition{WindowEnd: window.Start - 1}
	}
	if next != nil {
		raw, err := common.Marshal(SourceReviewCursor{SourceReviewPosition: *next, Snapshot: now, Days: days})
		if err != nil {
			return report, err
		}
		report.Truncated = true
		report.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	type signature struct{ kind, current, header, match, value string }
	groups := make(map[signature]int)
	for _, request := range requests {
		if request.Count <= 0 {
			continue
		}
		var header http.Header
		key := signature{kind: "no_capture"}
		if common.UnmarshalJsonStr(request.Headers, &header) == nil && len(header) > 0 {
			// Canonicalize old or imported maps as net/http does on ingress.
			normalized := make(http.Header, len(header))
			for name, values := range header {
				field := http.CanonicalHeaderKey(name)
				normalized[field] = append(normalized[field], values...)
			}
			key.kind, key.current = "no_signature", IdentifyRequestClient(normalized)
			// A declaration can be a valid display label yet still be a person's
			// name or an installation identifier. Show the rule that matched it.
			if key.current != "" && strings.EqualFold(key.current, strings.TrimSpace(normalized.Get("X-Client-Name"))) &&
				identifyBuiltinRequestClient(normalized) == "X-Client-Name" && operation_setting.IdentifyConfiguredRequestSource(normalized) == "" &&
				operation_setting.BuiltinRequestSourceSettingFor("X-Client-Name").DisplayName == "" {
				key.current = "X-Client-Name"
			}
			condition := requestSourceSignature(normalized)
			if condition.Header != "" {
				key.kind, key.header, key.match, key.value = "signature", condition.Header, condition.Match, condition.Value
			}
		}
		i, ok := groups[key]
		if !ok {
			i = len(report.Groups)
			groups[key] = i
			group := UnidentifiedSourceGroup{Kind: key.kind, CurrentSource: key.current, Conditions: []operation_setting.RequestSourceCondition{}}
			if key.kind == "signature" {
				group.Conditions = append(group.Conditions, operation_setting.RequestSourceCondition{Header: key.header, Match: key.match, Value: key.value})
			}
			report.Groups = append(report.Groups, group)
		}
		report.Groups[i].Count += request.Count
		report.Groups[i].LastSeen = max(report.Groups[i].LastSeen, request.CreatedAt)
		report.Reviewed += request.Count
	}
	slices.SortStableFunc(report.Groups, func(a, b UnidentifiedSourceGroup) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(b.LastSeen, a.LastSeen))
	})
	return report, nil
}

// Operators need a reusable declaration, not the raw header: versions, URLs,
// comments, UUIDs, IP addresses and opaque suffixes must not leave the service.
var sourceProductName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,30}$`)
var sourceProductID = regexp.MustCompile(`(?i)([0-9]{4}|[0-9a-f]{10}|^akia|^asia|^ya29[.]|^npm_|^glpat-|^pk_(live|test)|^rk_(live|test))`)

func publicSourceProduct(value string) bool {
	return sourceProductName.MatchString(value) && !sourceProductID.MatchString(value) && operation_setting.NormalizeRequestSourceName(value) != ""
}

func requestSourceSignature(header http.Header) operation_setting.RequestSourceCondition {
	ua := header.Get("User-Agent")
	if len(ua) > 2048 || strings.IndexAny(ua, "\r\n") >= 0 || len(header.Values("User-Agent")) != 1 {
		ua = ""
	}
	for _, marker := range []string{" Edg/", " Firefox/", "; wv)", " Mobile Safari/"} {
		if strings.HasPrefix(ua, "Mozilla/5.0 ") && strings.Contains(ua, marker) {
			return operation_setting.RequestSourceCondition{Header: "User-Agent", Match: "contains", Value: marker}
		}
	}
	// Some clients explicitly declare only their public product (or a public
	// namespace) instead of the usual product/version syntax. Bare arbitrary
	// strings, including installation IDs, are never presented as evidence.
	switch ua {
	case "llmag-cpa-adapter", "Kelivo":
		return operation_setting.RequestSourceCondition{Header: "User-Agent", Match: "exact", Value: ua}
	}
	for _, prefix := range []string{"air-plugin:idea/", "ai-sdk/openai-compatible/", "ai-sdk/provider-utils/"} {
		if rest, ok := strings.CutPrefix(ua, prefix); ok && len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			return operation_setting.RequestSourceCondition{Header: "User-Agent", Match: "prefix", Value: prefix}
		}
	}
	product, rest, versioned := strings.Cut(ua, "/")
	if strings.HasPrefix(ua, "P/JS ") && len(ua) > 5 && ua[5] >= '0' && ua[5] <= '9' {
		return operation_setting.RequestSourceCondition{Header: "User-Agent", Match: "prefix", Value: "P/JS "}
	}
	if versioned && publicSourceProduct(product) && !strings.EqualFold(product, "Mozilla") &&
		len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		return operation_setting.RequestSourceCondition{Header: "User-Agent", Match: "prefix", Value: product + "/"}
	}
	// These identity-bearing headers may include paths, hostnames or random IDs.
	// Avoid offering broad matches on their contents and on generic proxies.
	return operation_setting.RequestSourceCondition{}
}
