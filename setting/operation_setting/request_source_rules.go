package operation_setting

import (
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"golang.org/x/net/http/httpguts"
)

const RequestSourceRulesOptionKey = "RequestSourceRules"

type RequestSourceRule struct {
	Enabled    bool                     `json:"enabled"`
	Name       string                   `json:"name"`
	Conditions []RequestSourceCondition `json:"conditions"`
}

type RequestSourceCondition struct {
	Header        string `json:"header"`
	Match         string `json:"match"`
	Value         string `json:"value"`
	CaseSensitive bool   `json:"case_sensitive"`
}

type compiledRequestSourceCondition struct {
	RequestSourceCondition
	pattern string
	regex   *regexp.Regexp
}

type compiledRequestSourceRule struct {
	name       string
	conditions []compiledRequestSourceCondition
}

type requestSourceRulesSnapshot struct {
	json  string
	rules []compiledRequestSourceRule
}

var requestSourceRules atomic.Pointer[requestSourceRulesSnapshot]

func ValidateRequestSourceRulesJSON(value string) error {
	_, err := compileRequestSourceRules(value)
	return err
}

// LoadRequestSourceRulesJSON publishes only complete, valid immutable snapshots.
// Invalid saved settings leave the last working rules intact.
func LoadRequestSourceRulesJSON(value string) error {
	snapshot, err := compileRequestSourceRules(value)
	if err != nil {
		return err
	}
	requestSourceRules.Store(snapshot)
	return nil
}

func RequestSourceRulesJSONString() string {
	if snapshot := requestSourceRules.Load(); snapshot != nil {
		return snapshot.json
	}
	return "[]"
}

func compileRequestSourceRules(value string) (*requestSourceRulesSnapshot, error) {
	if len(value) > 128*1024 {
		return nil, common.NewMessage("Request source rules exceed the {{limit}} byte limit", map[string]any{"limit": 128 * 1024})
	}
	var rawRules []map[string]common.RawMessage
	if !utf8.ValidString(value) || common.UnmarshalJsonStr(value, &rawRules) != nil || rawRules == nil {
		return nil, common.NewMessage("Request source rules must be a JSON array")
	}
	if len(rawRules) > 100 {
		return nil, common.NewMessage("Request source rules support at most {{limit}} rules", map[string]any{"limit": 100})
	}
	rules := make([]RequestSourceRule, 0, len(rawRules))
	snapshot := &requestSourceRulesSnapshot{}
	for index, rawRule := range rawRules {
		params := map[string]any{"rule": index + 1}
		for key, raw := range rawRule {
			kind := common.GetJsonType(raw)
			if key == "enabled" && kind == "boolean" || key == "name" && kind == "string" || key == "conditions" && kind == "array" {
				continue
			}
			return nil, common.NewMessage("Request source rule {{rule}} has invalid fields or field types", params)
		}
		var rule RequestSourceRule
		encoded, err := common.Marshal(rawRule)
		if err != nil || common.Unmarshal(encoded, &rule) != nil {
			return nil, common.NewMessage("Request source rule {{rule}} has invalid fields or field types", params)
		}
		rule.Name = NormalizeRequestSourceName(rule.Name)
		if rule.Name == "" {
			return nil, common.NewMessage("Request source rule {{rule}} has an invalid display name", params)
		}
		if len(rule.Conditions) == 0 || len(rule.Conditions) > 8 {
			return nil, common.NewMessage("Request source rule {{rule}} must have 1 to {{limit}} conditions", map[string]any{"rule": index + 1, "limit": 8})
		}
		var rawConditions []map[string]common.RawMessage
		if common.Unmarshal(rawRule["conditions"], &rawConditions) != nil {
			return nil, common.NewMessage("Request source rule {{rule}} has invalid fields or field types", params)
		}
		compiled := compiledRequestSourceRule{name: rule.Name}
		for conditionIndex, condition := range rule.Conditions {
			params := map[string]any{"rule": index + 1, "condition": conditionIndex + 1}
			for key, raw := range rawConditions[conditionIndex] {
				kind := common.GetJsonType(raw)
				if key == "case_sensitive" && kind == "boolean" || (key == "header" || key == "match" || key == "value") && kind == "string" {
					continue
				}
				return nil, common.NewMessage("Request source rule {{rule}} has invalid fields or field types", params)
			}
			if !validRequestSourceHeader(condition.Header) {
				return nil, common.NewMessage("Request source rule {{rule}}, condition {{condition}} has an invalid or sensitive header name", params)
			}
			if condition.Match != "exact" && condition.Match != "prefix" && condition.Match != "contains" && condition.Match != "regex" {
				return nil, common.NewMessage("Request source rule {{rule}}, condition {{condition}} has an unsupported match type", params)
			}
			if len(condition.Value) == 0 || len(condition.Value) > 512 || !utf8.ValidString(condition.Value) {
				params["limit"] = 512
				return nil, common.NewMessage("Request source rule {{rule}}, condition {{condition}} must have a non-empty pattern of at most {{limit}} bytes", params)
			}
			condition.Header = http.CanonicalHeaderKey(condition.Header)
			rule.Conditions[conditionIndex] = condition
			matcher := compiledRequestSourceCondition{RequestSourceCondition: condition, pattern: condition.Value}
			if condition.Match == "regex" {
				if !condition.CaseSensitive {
					matcher.pattern = "(?i:" + matcher.pattern + ")"
				}
				matcher.regex, err = regexp.Compile(matcher.pattern)
				if err != nil {
					return nil, common.NewMessage("Request source rule {{rule}}, condition {{condition}} has an invalid regular expression", params)
				}
			} else if !condition.CaseSensitive {
				matcher.pattern = strings.ToLower(matcher.pattern)
			}
			compiled.conditions = append(compiled.conditions, matcher)
		}
		rules = append(rules, rule)
		if rule.Enabled {
			snapshot.rules = append(snapshot.rules, compiled)
		}
	}
	encoded, err := common.Marshal(rules)
	if err != nil {
		return nil, common.NewMessage("Request source rules must be a JSON array")
	}
	if len(encoded) > 128*1024 {
		return nil, common.NewMessage("Request source rules exceed the {{limit}} byte limit", map[string]any{"limit": 128 * 1024})
	}
	snapshot.json = string(encoded)
	return snapshot, nil
}

func validRequestSourceHeader(name string) bool {
	if !httpguts.ValidHeaderFieldName(name) {
		return false
	}
	compact := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(name))
	for _, sensitive := range []string{"authorization", "authentication", "cookie", "apikey", "accesskey", "password", "secret", "credential"} {
		if strings.Contains(compact, sensitive) {
			return false
		}
	}
	return !strings.HasSuffix(compact, "token")
}

// IdentifyConfiguredRequestSource reads original inbound headers only. Labels
// are fixed operator-defined names, never extracted header text or auth signals.
func IdentifyConfiguredRequestSource(header http.Header) string {
	snapshot := requestSourceRules.Load()
	if snapshot == nil {
		return ""
	}
	for _, rule := range snapshot.rules {
		matched := true
		for _, condition := range rule.conditions {
			if !condition.matchesHeader(header) {
				matched = false
				break
			}
		}
		if matched {
			return rule.name
		}
	}
	return ""
}

func (condition compiledRequestSourceCondition) matchesHeader(header http.Header) bool {
	matched, size, count := false, 0, 0
	for name, values := range header {
		if !strings.EqualFold(name, condition.Header) {
			continue
		}
		for _, value := range values {
			size += len(value)
			count++
			if size > 8192 || count > 32 {
				return false
			}
			if matched {
				continue
			}
			if condition.regex != nil {
				matched = condition.regex.MatchString(value)
				continue
			}
			if !condition.CaseSensitive {
				value = strings.ToLower(value)
			}
			switch condition.Match {
			case "exact":
				matched = value == condition.pattern
			case "prefix":
				matched = strings.HasPrefix(value, condition.pattern)
			case "contains":
				matched = strings.Contains(value, condition.pattern)
			}
		}
	}
	return matched
}

// NormalizeRequestSourceName accepts a public display name, never an identity
// or authorization signal. Keep arbitrary values and credentials out of labels.
func NormalizeRequestSourceName(value string) string {
	if len(value) > 64 || !utf8.ValidString(value) {
		return ""
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune(" ._-", character) {
			return ""
		}
	}
	name := strings.TrimSpace(value)
	if name == "" {
		return ""
	}
	for word := range strings.FieldsSeq(strings.ToLower(name)) {
		if word == "bearer" || word == "basic" {
			return ""
		}
		for _, prefix := range []string{"sk-", "sk_", "ak-", "ak_", "ghp_", "github_pat_", "xoxb-", "xoxp-", "gsk_", "aiza", "eyj"} {
			if strings.HasPrefix(word, prefix) {
				return ""
			}
		}
	}
	return name
}
