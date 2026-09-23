// Package modelmapping resolves channel model names for relay and monitoring.
package modelmapping

import (
	"errors"
	"fmt"

	rootcommon "github.com/QuantumNous/new-api/common"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
)

// ResolveMappedModel follows a channel's model_mapping chain and reports the
// upstream name for originModel, and whether any rename applied.
func ResolveMappedModel(modelMapping string, originModel string) (string, bool, error) {
	if modelMapping == "" || modelMapping == "{}" {
		return originModel, false, nil
	}
	modelMap := make(map[string]string)
	if err := rootcommon.Unmarshal([]byte(modelMapping), &modelMap); err != nil {
		return originModel, false, fmt.Errorf("unmarshal_model_mapping_failed")
	}

	// 支持链式模型重定向，最终使用链尾的模型
	currentModel := originModel
	mapped := false
	visitedModels := map[string]bool{
		currentModel: true,
	}
	for {
		mappedModel, exists := modelMap[currentModel]
		baseModel := hostreasoning.BaseModelName(currentModel)
		if (!exists || mappedModel == "") && baseModel != currentModel {
			mappedModel, exists = modelMap[baseModel]
		}
		if exists && mappedModel != "" {
			// 模型重定向循环检测，避免无限循环
			if visitedModels[mappedModel] {
				if mappedModel == currentModel {
					if currentModel == originModel {
						return originModel, false, nil
					}
					mapped = true
					break
				}
				return originModel, false, errors.New("model_mapping_contains_cycle")
			}
			visitedModels[mappedModel] = true
			currentModel = mappedModel
			mapped = true
		} else {
			break
		}
	}
	if !mapped {
		return originModel, false, nil
	}
	return currentModel, true, nil
}
