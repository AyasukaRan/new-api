package service

import (
	"maps"
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type BuiltinRequestSourceRule struct {
	Name string `json:"name"`
	operation_setting.BuiltinRequestSourceSetting
	Examples []map[string]string `json:"examples"`
}

// BuiltinRequestSourceRules shows synthetic examples only. Resolve them with the
// built-in classifier so a custom rule cannot change the reference catalog.
func BuiltinRequestSourceRules() []BuiltinRequestSourceRule {
	examples := []map[string]string{
		{"User-Agent": "Codex Desktop/1.0"},
		{"Editor-Version": "aider/1.0"},
		{"X-Title": "Aider", "HTTP-Referer": "https://aider.chat"},
		{"X-Title": "Cline", "HTTP-Referer": "https://cline.bot"},
		{"User-Agent": "claude/1.0 Electron/1.0"},
		{"User-Agent": "Sub2-authorized-native-search-test"},
		{"User-Agent": "Sub2API-Insight-GroupAvailability-00000000-0000-4000-8000-000000000000"},
		{"Origin": "chrome-extension://modkelfkcfjpgbfmnbnllalkiogfofhb"},
		{"User-Agent": "OpenAI/1.0 (.NET 8.0)"},
		{"User-Agent": "ai-sdk/openai-compatible/1.0.0"},
		{"User-Agent": "ai-sdk/provider-utils/1.0.0"},
		{"User-Agent": "P/JS 1.0.0"},
		{"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
		{"X-Stainless-Lang": "js", "X-Stainless-Runtime": "node"},
		{"X-Stainless-Lang": "js", "X-Stainless-Runtime": "deno"},
		{"X-Stainless-Lang": "js", "X-Stainless-Runtime": "bun"},
		{"X-Client-Name": "Example client"},
		{},
	}
	for _, products := range []map[string]string{requestSourceCLIProducts, requestSourceApplications, requestSourceHTTPClients, requestSourceRuntimes} {
		for _, product := range slices.Sorted(maps.Keys(products)) {
			examples = append(examples, map[string]string{"User-Agent": product + "/1.0"})
		}
	}
	for _, token := range slices.Sorted(maps.Keys(requestSourceSDKs)) {
		examples = append(examples, map[string]string{"User-Agent": token + " 1.0"})
	}
	for _, token := range slices.Sorted(maps.Keys(requestSourceOriginators)) {
		examples = append(examples, map[string]string{"Originator": token})
	}
	for _, language := range slices.Sorted(maps.Keys(requestSourceLanguages)) {
		examples = append(examples, map[string]string{"X-Stainless-Lang": language})
	}
	byName := make(map[string][]map[string]string)
	for _, example := range examples {
		header := make(http.Header, len(example))
		for key, value := range example {
			header.Set(key, value)
		}
		name := identifyBuiltinRequestClient(header)
		if name == "" {
			continue
		}
		byName[name] = append(byName[name], example)
	}
	rules := make([]BuiltinRequestSourceRule, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		setting := operation_setting.BuiltinRequestSourceSettingFor(name)
		if setting.DisplayName == "" {
			setting.DisplayName = name
		}
		rules = append(rules, BuiltinRequestSourceRule{Name: name, Examples: byName[name], BuiltinRequestSourceSetting: setting})
	}
	return rules
}
