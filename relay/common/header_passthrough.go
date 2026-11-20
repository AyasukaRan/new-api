package common

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const clientHeaderPlaceholderPrefix = "{client_header:"

const (
	headerPassthroughAllKey        = "*"
	headerPassthroughRegexPrefix   = "re:"
	headerPassthroughRegexPrefixV2 = "regex:"
)

var passthroughSkipHeaderNamesLower = map[string]struct{}{
	// RFC 7230 hop-by-hop headers.
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},

	"proxy-connection": {},
	"proxy":            {},
	"host":             {},
	"content-length":   {},
	"content-encoding": {},
	"accept-encoding":  {},
	"expect":           {},
	// The adaptor owns the content type and multipart boundary of the actual upstream body.
	"content-type": {},
	// These authenticate the client to the gateway or its ingress.
	"authorization":        {},
	"cookie":               {},
	"cookie2":              {},
	"set-cookie":           {},
	"x-api-key":            {},
	"api-key":              {},
	"x-goog-api-key":       {},
	"mj-api-secret":        {},
	"x-security-proof":     {},
	"new-api-user":         {},
	"x-csrf-token":         {},
	"x-xsrf-token":         {},
	"x-amz-security-token": {},
	// Forwarding metadata is produced from the trusted peer below.
	"forwarded":                {},
	"x-forwarded":              {},
	"x-original-forwarded-for": {},
	"x-original-url":           {},
	"x-original-uri":           {},
	"x-rewrite-url":            {},
	"x-real-ip":                {},
	"true-client-ip":           {},
	"client-ip":                {},
	"x-client-ip":              {},
	"x-cluster-client-ip":      {},
	"x-originating-ip":         {},
	"x-envoy-external-address": {},
	"fastly-client-ip":         {},
	"cf-connecting-ip":         {},
	"cf-connecting-ipv6":       {},
	"cf-pseudo-ipv4":           {},
}

var headerPassthroughRegexCache sync.Map // map[string]*regexp.Regexp

func getHeaderPassthroughRegex(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, errors.New("empty regex pattern")
	}
	if v, ok := headerPassthroughRegexCache.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
		headerPassthroughRegexCache.Delete(pattern)
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	actual, _ := headerPassthroughRegexCache.LoadOrStore(pattern, compiled)
	if re, ok := actual.(*regexp.Regexp); ok {
		return re, nil
	}
	return compiled, nil
}

// relayClientHeaders contains end-to-end client metadata safe for this upstream hop.
func relayClientHeaders(c *gin.Context) http.Header {
	headers := c.Request.Header.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	appcommon.RemoveConnectionHeaders(headers)
	for name := range headers {
		key := strings.ToLower(name)
		_, skip := passthroughSkipHeaderNamesLower[key]
		if skip || strings.HasPrefix(key, "sec-websocket-") || strings.HasPrefix(key, "x-forwarded-") || strings.HasPrefix(key, "cf-access-") || strings.HasPrefix(key, "x-envoy-") {
			delete(headers, name)
		}
	}
	if ip, err := netip.ParseAddr(appcommon.RequestClientIP(c)); err == nil {
		headers.Set("X-Forwarded-For", ip.Unmap().String())
		headers.Set("X-Real-IP", ip.Unmap().String())
	}
	return headers
}

func applyHeaderOverridePlaceholders(template string, clientHeaders http.Header, apiKey string) (string, bool, error) {
	trimmed := strings.TrimSpace(template)
	if strings.HasPrefix(trimmed, clientHeaderPlaceholderPrefix) {
		afterPrefix := trimmed[len(clientHeaderPlaceholderPrefix):]
		end := strings.Index(afterPrefix, "}")
		if end < 0 || end != len(afterPrefix)-1 {
			return "", false, fmt.Errorf("client_header placeholder must be the full value: %q", template)
		}

		name := strings.TrimSpace(afterPrefix[:end])
		if name == "" {
			return "", false, fmt.Errorf("client_header placeholder name is empty: %q", template)
		}
		clientHeaderValue := clientHeaders.Get(name)
		if strings.TrimSpace(clientHeaderValue) == "" {
			return "", false, nil
		}
		// Do not interpolate {api_key} inside client-supplied content.
		return clientHeaderValue, true, nil
	}

	if strings.Contains(template, "{api_key}") {
		template = strings.ReplaceAll(template, "{api_key}", apiKey)
	}
	if strings.TrimSpace(template) == "" {
		return "", false, nil
	}
	return template, true, nil
}

// UpstreamRequestHeaders resolves channel header overrides, with placeholder substitution.
// Supported placeholders:
//   - {api_key}: resolved to the channel API key
//   - {client_header:<name>}: resolved to the incoming request header value
//
// Header passthrough rules (the default is all end-to-end client headers):
//   - "*": false disables the default wildcard, while regex rules remain active
//   - "re:<regex>" / "regex:<regex>": passthrough headers whose names match the regex (Go regexp)
//
// Passthrough rules are applied first, then normal overrides are applied, so explicit overrides win.
func UpstreamRequestHeaders(info *RelayInfo, c *gin.Context) (http.Header, error) {
	headerOverride := make(http.Header)
	if info == nil {
		return headerOverride, nil
	}

	headerOverrideSource := GetEffectiveHeaderOverride(info)
	// Background polling has no client request from which to copy headers.
	hasClientRequest := !info.IsChannelTest && c != nil && c.Request != nil

	var clientHeaders http.Header
	var passthroughRegex []*regexp.Regexp
	if hasClientRequest {
		clientHeaders = relayClientHeaders(c)
		for k := range headerOverrideSource {
			key := strings.TrimSpace(strings.ToLower(k))
			if key == "" {
				continue
			}

			var pattern string
			switch {
			case strings.HasPrefix(key, headerPassthroughRegexPrefix):
				pattern = strings.TrimSpace(key[len(headerPassthroughRegexPrefix):])
			case strings.HasPrefix(key, headerPassthroughRegexPrefixV2):
				pattern = strings.TrimSpace(key[len(headerPassthroughRegexPrefixV2):])
			default:
				continue
			}

			if pattern == "" {
				return nil, types.NewError(fmt.Errorf("header passthrough regex pattern is empty: %q", k), types.ErrorCodeChannelHeaderOverrideInvalid)
			}
			compiled, err := getHeaderPassthroughRegex(pattern)
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeChannelHeaderOverrideInvalid)
			}
			passthroughRegex = append(passthroughRegex, compiled)
		}
	}

	passAll := len(passthroughRegex) == 0
	if configured, exists := headerOverrideSource[headerPassthroughAllKey]; exists {
		passAll = !strings.EqualFold(fmt.Sprint(configured), "false")
	}
	for name, values := range clientHeaders {
		if passAll || slices.ContainsFunc(passthroughRegex, func(re *regexp.Regexp) bool { return re.MatchString(strings.ToLower(name)) }) {
			headerOverride[name] = values
		}
	}

	for k, v := range headerOverrideSource {
		if IsHeaderPassthroughRuleKey(k) {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(k))
		if key == "" {
			continue
		}

		str, ok := v.(string)
		if !ok {
			return nil, types.NewError(nil, types.ErrorCodeChannelHeaderOverrideInvalid)
		}
		if !hasClientRequest && strings.HasPrefix(strings.TrimSpace(str), clientHeaderPlaceholderPrefix) {
			continue
		}

		value, include, err := applyHeaderOverridePlaceholders(str, clientHeaders, info.ApiKey)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeChannelHeaderOverrideInvalid)
		}
		if !include {
			continue
		}

		if key == "x-forwarded-for" || key == "x-real-ip" {
			value = clientHeaders.Get(key)
			if value == "" {
				continue
			}
		}
		headerOverride.Set(key, value)
	}
	return headerOverride, nil
}
