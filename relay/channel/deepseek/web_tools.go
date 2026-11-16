package deepseek

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
)

// normalizeWebTools upgrades compatible client search declarations for Flash's
// application API. Pro and unsupported tool/constraint combinations stay intact.
// https://api-docs.deepseek.com/guides/web_tools/
func normalizeWebTools(model string, tools json.RawMessage, claude bool) (json.RawMessage, error) {
	if model != "deepseek-flash" || len(tools) == 0 || string(tools) == "null" {
		return tools, nil
	}
	var declarations []json.RawMessage
	if err := common.Unmarshal(tools, &declarations); err != nil {
		return nil, err
	}
	native := make(map[string]bool)
	for _, declaration := range declarations {
		kind := gjson.GetBytes(declaration, "type").String()
		switch kind {
		case "custom", "namespace", "tool_search":
			// Those Responses tools cannot be combined with the new server tools.
			return tools, nil
		case "deepseek_web_search", "deepseek_read_url":
			native[kind] = true
		}
	}

	updated := make([]json.RawMessage, 0, len(declarations))
	changed := false
	for _, declaration := range declarations {
		value := gjson.ParseBytes(declaration)
		kind, name := value.Get("type").String(), value.Get("name").String()
		target := ""
		if !claude && (kind == "web_search" || kind == "web_search_preview") {
			target = "deepseek_web_search"
		} else if claude {
			if strings.HasPrefix(kind, "web_search_") && name == "web_search" {
				target = "deepseek_web_search"
			} else if strings.HasPrefix(kind, "web_fetch_") && name == "web_fetch" {
				target = "deepseek_read_url"
			}
		}
		if target == "" {
			updated = append(updated, declaration)
			continue
		}
		// Domain, location, max-use and other restrictions must not silently
		// disappear when switching providers' tool schemas.
		for key, field := range value.Map() {
			switch key {
			case "type", "name", "search_context_size":
			case "external_web_access":
				if field.Type != gjson.True {
					return tools, nil
				}
			default:
				return tools, nil
			}
		}
		changed = true
		if native[target] {
			continue
		}
		native[target] = true
		converted := map[string]string{"type": target}
		if claude {
			converted["name"] = target
		}
		encoded, err := common.Marshal(converted)
		if err != nil {
			return nil, err
		}
		updated = append(updated, encoded)
	}
	if !changed {
		return tools, nil
	}
	return common.Marshal(updated)
}
