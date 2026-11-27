package deepseek

import (
	"cmp"
	"slices"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func stableToolSchemasEnabled(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && info.UsingGroup != "" &&
		slices.Contains(info.ChannelSetting.DeepSeekStableToolSchemaGroups, info.UsingGroup)
}

// Only schema structure is ordered: values such as defaults, examples and
// extensions remain opaque. Arrays and scalar lexemes are never rewritten.
// Invalid or ambiguous schemas are preserved whole, never partially changed.
func stabilizeToolSchema(raw []byte) []byte {
	if !gjson.ValidBytes(raw) {
		return raw
	}
	value := gjson.ParseBytes(raw)
	if !uniqueToolJSONMembers(value, 0) {
		return raw
	}
	stable, ok := stableJSONSchema(value, false)
	if !ok || stable == string(raw) {
		return raw
	}
	return []byte(stable)
}

func uniqueToolJSONMembers(value gjson.Result, depth int) bool {
	if depth > 128 {
		return false
	}
	valid := true
	if value.IsObject() {
		seen := make(map[string]bool)
		value.ForEach(func(key, child gjson.Result) bool {
			name := key.String()
			valid = !seen[name] && uniqueToolJSONMembers(child, depth+1)
			seen[name] = true
			return valid
		})
	} else if value.IsArray() {
		value.ForEach(func(_, child gjson.Result) bool {
			valid = uniqueToolJSONMembers(child, depth+1)
			return valid
		})
	}
	return valid
}

func stableJSONSchema(value gjson.Result, schemaMap bool) (string, bool) {
	if !schemaMap && (value.Type == gjson.True || value.Type == gjson.False) {
		return value.Raw, true
	}
	if !value.IsObject() {
		return "", false
	}
	type member struct{ name, key, value string }
	members := make([]member, 0)
	valid := true
	value.ForEach(func(key, child gjson.Result) bool {
		name, raw := key.String(), child.Raw
		if schemaMap {
			raw, valid = stableJSONSchema(child, false)
		} else {
			switch name {
			case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
				raw, valid = stableJSONSchema(child, true)
			case "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "additionalItems", "contains", "propertyNames", "not", "if", "then", "else", "contentSchema":
				raw, valid = stableJSONSchema(child, false)
			case "items":
				if child.IsArray() {
					raw, valid = stableJSONSchemaArray(child)
				} else {
					raw, valid = stableJSONSchema(child, false)
				}
			case "allOf", "anyOf", "oneOf", "prefixItems":
				raw, valid = stableJSONSchemaArray(child)
			}
		}
		members = append(members, member{name: name, key: key.Raw, value: raw})
		return valid
	})
	if !valid {
		return "", false
	}
	slices.SortFunc(members, func(a, b member) int { return cmp.Compare(a.name, b.name) })
	var result strings.Builder
	result.WriteByte('{')
	for i, field := range members {
		if i > 0 {
			result.WriteByte(',')
		}
		result.WriteString(field.key)
		result.WriteByte(':')
		result.WriteString(field.value)
	}
	result.WriteByte('}')
	return result.String(), true
}

func stableJSONSchemaArray(value gjson.Result) (string, bool) {
	if !value.IsArray() {
		return "", false
	}
	items := make([]string, 0)
	valid := true
	value.ForEach(func(_, child gjson.Result) bool {
		var raw string
		raw, valid = stableJSONSchema(child, false)
		items = append(items, raw)
		return valid
	})
	return "[" + strings.Join(items, ",") + "]", valid
}

// Native tool arrays retain their declarations, IDs and extension values;
// only ordinary function schemas are replaced, in their original array slot.
func stabilizeNativeToolSchemas(raw []byte, claude bool) []byte {
	if !gjson.ValidBytes(raw) {
		return raw
	}
	tools := gjson.ParseBytes(raw)
	if !tools.IsArray() {
		return raw
	}
	field := "parameters"
	if claude {
		field = "input_schema"
	}
	items := make([]string, 0)
	changed := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		item := tool.Raw
		kind := tool.Get("type").String()
		ordinary := kind == "function"
		if claude {
			ordinary = !tool.Get("type").Exists()
		}
		if tool.IsObject() && ordinary && uniqueToolJSONMembers(tool, 0) {
			schema := tool.Get(field).Raw
			stable := stabilizeToolSchema([]byte(schema))
			if string(stable) != schema {
				if updated, err := sjson.SetRaw(item, field, string(stable)); err == nil {
					item = updated
					changed = true
				}
			}
		}
		items = append(items, item)
		return true
	})
	if !changed {
		return raw
	}
	return []byte("[" + strings.Join(items, ",") + "]")
}
