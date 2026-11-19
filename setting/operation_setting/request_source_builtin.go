package operation_setting

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// One option per recognizer keeps edits to different sources independent.
const BuiltinRequestSourceOptionPrefix = "RequestSourceBuiltin."

type BuiltinRequestSourceSetting struct {
	Enabled     bool   `json:"enabled"`
	DisplayName string `json:"display_name"`
}

var builtinRequestSourceSettings sync.Map

func BuiltinRequestSourceSettingFor(name string) BuiltinRequestSourceSetting {
	if value, ok := builtinRequestSourceSettings.Load(name); ok {
		return value.(BuiltinRequestSourceSetting)
	}
	return BuiltinRequestSourceSetting{Enabled: true}
}

func ParseBuiltinRequestSourceOption(key, value string) (BuiltinRequestSourceSetting, error) {
	var input struct {
		Enabled     *bool  `json:"enabled"`
		DisplayName string `json:"display_name"`
	}
	name, ok := strings.CutPrefix(key, BuiltinRequestSourceOptionPrefix)
	if !ok || name == "" || name != NormalizeRequestSourceName(name) || len(value) > 512 ||
		common.UnmarshalJsonStr(value, &input) != nil || input.Enabled == nil {
		return BuiltinRequestSourceSetting{}, common.NewMessage("Invalid parameters")
	}
	display := strings.TrimSpace(input.DisplayName)
	if display != "" && NormalizeRequestSourceName(display) != display {
		return BuiltinRequestSourceSetting{}, common.NewMessage("Invalid parameters")
	}
	if display == name {
		display = ""
	}
	return BuiltinRequestSourceSetting{Enabled: *input.Enabled, DisplayName: display}, nil
}

func LoadBuiltinRequestSourceOption(key, value string) error {
	setting, err := ParseBuiltinRequestSourceOption(key, value)
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(key, BuiltinRequestSourceOptionPrefix)
	if setting.Enabled && setting.DisplayName == "" {
		builtinRequestSourceSettings.Delete(name)
	} else {
		builtinRequestSourceSettings.Store(name, setting)
	}
	return nil
}
