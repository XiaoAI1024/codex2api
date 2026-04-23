package proxy

import (
	"sort"
	"strings"
)

// ModelCapability 描述模型能力，用于请求校验与策略钳位。
type ModelCapability struct {
	AllowedReasoningEfforts []string
	SupportsImages          bool
	OpenAIVisible           bool
	ImageOnly               bool
}

var modelCatalog = map[string]ModelCapability{
	"gpt-5.4": {
		AllowedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		OpenAIVisible:           true,
	},
	"gpt-5.4-mini": {
		AllowedReasoningEfforts: []string{"low", "medium", "high"},
		OpenAIVisible:           true,
	},
	"gpt-5.3-codex": {
		AllowedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		OpenAIVisible:           true,
	},
	"gpt-5.2": {
		AllowedReasoningEfforts: []string{"low", "medium", "high"},
		OpenAIVisible:           true,
	},
	"gpt-5.2-codex": {
		AllowedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		OpenAIVisible:           true,
	},
	"gpt-5.1": {
		AllowedReasoningEfforts: []string{"low", "medium", "high"},
		OpenAIVisible:           true,
	},
	"gpt-5.1-codex": {
		AllowedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		OpenAIVisible:           true,
	},
	"gpt-5.1-codex-mini": {
		AllowedReasoningEfforts: []string{"low", "medium"},
		OpenAIVisible:           true,
	},
	"gpt-5.1-codex-max": {
		AllowedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		OpenAIVisible:           true,
	},
	"gpt-5-codex": {
		AllowedReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
		OpenAIVisible:           true,
	},
	"gpt-5-codex-mini": {
		AllowedReasoningEfforts: []string{"low", "medium"},
		OpenAIVisible:           true,
	},
	"gpt-image-2": {
		SupportsImages: true,
		OpenAIVisible:  true,
		ImageOnly:      true,
	},
}

func normalizeModelID(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// GetModelCatalog 返回模型目录的副本（包含能力元数据）。
func GetModelCatalog() map[string]ModelCapability {
	out := make(map[string]ModelCapability, len(modelCatalog))
	for k, v := range modelCatalog {
		vCopy := v
		if len(v.AllowedReasoningEfforts) > 0 {
			vCopy.AllowedReasoningEfforts = append([]string(nil), v.AllowedReasoningEfforts...)
		}
		out[k] = vCopy
	}
	return out
}

// LookupModelCapability 查询模型能力。
func LookupModelCapability(model string) (ModelCapability, bool) {
	cap, ok := modelCatalog[normalizeModelID(model)]
	return cap, ok
}

// IsModelAllowed 判断模型是否在目录中。
func IsModelAllowed(model string) bool {
	_, ok := LookupModelCapability(model)
	return ok
}

// IsImageOnlyModel 判断是否仅能用于图片端点。
func IsImageOnlyModel(model string) bool {
	cap, ok := LookupModelCapability(model)
	return ok && cap.ImageOnly
}

// SupportsImageRequests 判断模型是否支持图片请求。
func SupportsImageRequests(model string) bool {
	cap, ok := LookupModelCapability(model)
	return ok && cap.SupportsImages
}

// ListPublicModels 返回对 OpenAI 兼容层可见的模型列表。
func ListPublicModels() []string {
	models := make([]string, 0, len(modelCatalog))
	for id, cap := range modelCatalog {
		if cap.OpenAIVisible {
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models
}

// ClampReasoningEffortForModel 按模型能力钳位 reasoning 档位。
// 返回值:
//   - clampedEffort: 钳位后的档位；当输入为空时返回空串。
//   - keepReasoningField: 该模型是否支持 reasoning 字段。
func ClampReasoningEffortForModel(model, effort string) (clampedEffort string, keepReasoningField bool) {
	cap, ok := LookupModelCapability(model)
	if !ok || len(cap.AllowedReasoningEfforts) == 0 {
		return "", false
	}

	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return "", true
	}

	for _, allowed := range cap.AllowedReasoningEfforts {
		if effort == allowed {
			return effort, true
		}
	}
	return cap.AllowedReasoningEfforts[len(cap.AllowedReasoningEfforts)-1], true
}
