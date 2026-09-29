package model

import (
	"errors"

	"gorm.io/gorm"
)

type LLMModel struct {
	ID          uint   `gorm:"primarykey" json:"id"`
	ModelID     string `gorm:"size:100;uniqueIndex;not null" json:"model_id"`
	DisplayName string `gorm:"size:200" json:"display_name"`
	MaxInput    int    `json:"max_input"`
	MaxOutput   int    `json:"max_output"`
	Vision      bool   `json:"vision"`
	Reasoning   bool   `json:"reasoning"`
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	Sort        int    `gorm:"default:0" json:"sort"`
	Remark      string `gorm:"size:500" json:"remark"`
}

func (LLMModel) TableName() string { return "llm_models" }

func SeedDefaultModels(db *gorm.DB) error {
	seeds := defaultModels()
	var count int64
	if err := db.Model(&LLMModel{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return db.Create(&seeds).Error
	}

	// 启动时同步新增模型和展示信息，但不覆盖用户在面板里手动设置的 Enabled。
	for _, seed := range seeds {
		var existing LLMModel
		err := db.Where("model_id = ?", seed.ModelID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := db.Create(&seed).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := db.Model(&existing).Updates(map[string]any{
			"display_name": seed.DisplayName,
			"max_input":    seed.MaxInput,
			"max_output":   seed.MaxOutput,
			"vision":       seed.Vision,
			"reasoning":    seed.Reasoning,
			"sort":         seed.Sort,
		}).Error; err != nil {
			return err
		}
	}

	// retiredIDs 是曾经内置、但已从 CodeBuddy 上游彻底下线的模型 ID。
	// 这里只把它们从本地兜底目录里摘掉（禁用），避免 /v1/models
	// 在上游不可用时广告已经不存在的模型；passthrough 仍然放行，
	// 用户也可以自行用 model-alias 把旧名映射到在售模型。
	retiredIDs := []string{
		"glm-5.0-turbo", "glm-5v-turbo", "glm-4.7",
		"kimi-k2.7-code", "kimi-k2.6", "deepseek-v3-2-volc",
		"deepseek-r1-0528-lkeap", "minimax-m2.7", "hunyuan-chat",
		"deepseek-v4.1", "glm-5.3-turbo",
	}
	return db.Model(&LLMModel{}).Where("model_id IN ?", retiredIDs).Update("enabled", false).Error
}

// defaultModels 是上游不可用时的本地兜底目录，顺序与数值都对齐
// CodeBuddy /v3/config 当前给 craft agent 暴露的模型集合。
// 注意：hy4-preview / hy3-preview / kimi-k3 在这里只是兼容别名行，
// 上游在售的是带后缀的 hy4-preview-f / hy3-x / kimi-k3-1，
// 真正发往上游前由 service.builtinModelAliases 完成改名。
func defaultModels() []LLMModel {
	return []LLMModel{
		{ModelID: "hy4-preview", DisplayName: "Hy4 preview", MaxInput: 1000000, MaxOutput: 64000, Vision: true, Reasoning: true, Enabled: true, Sort: 10},
		{ModelID: "hy3", DisplayName: "Hy3", MaxInput: 192000, MaxOutput: 64000, Vision: true, Reasoning: true, Enabled: true, Sort: 20},
		{ModelID: "hy3-preview", DisplayName: "Hy3", MaxInput: 192000, MaxOutput: 64000, Vision: true, Reasoning: true, Enabled: true, Sort: 30},
		{ModelID: "deepseek-v4.1-flash", DisplayName: "Deepseek-V4.1-Flash", MaxInput: 1000000, MaxOutput: 393216, Vision: true, Reasoning: true, Enabled: true, Sort: 40},
		{ModelID: "glm-5.3", DisplayName: "GLM-5.3", MaxInput: 1000000, MaxOutput: 131072, Vision: true, Reasoning: true, Enabled: true, Sort: 50},
		{ModelID: "glm-5.3-flash", DisplayName: "GLM-5.3-Flash", MaxInput: 1000000, MaxOutput: 131072, Vision: true, Reasoning: true, Enabled: true, Sort: 60},
		{ModelID: "glm-5.3-flashx", DisplayName: "GLM-5.3-FlashX", MaxInput: 1000000, MaxOutput: 131072, Vision: true, Reasoning: true, Enabled: true, Sort: 70},
		{ModelID: "kimi-k3", DisplayName: "Kimi-K3", MaxInput: 1000000, MaxOutput: 1048576, Vision: true, Reasoning: true, Enabled: true, Sort: 80},
		{ModelID: "deepseek-v4-pro", DisplayName: "Deepseek-V4-Pro", MaxInput: 1000000, MaxOutput: 393216, Vision: true, Reasoning: true, Enabled: true, Sort: 90},
		{ModelID: "auto", DisplayName: "Auto", MaxInput: 256000, MaxOutput: 32000, Vision: true, Reasoning: true, Enabled: true, Sort: 100},
		{ModelID: "glm-5.2", DisplayName: "GLM-5.2", MaxInput: 1000000, MaxOutput: 131072, Vision: true, Reasoning: true, Enabled: true, Sort: 110},
		{ModelID: "glm-5.1", DisplayName: "GLM-5.1", MaxInput: 200000, MaxOutput: 48000, Vision: true, Reasoning: true, Enabled: true, Sort: 120},
		{ModelID: "minimax-m3", DisplayName: "MiniMax-M3", MaxInput: 512000, MaxOutput: 524288, Vision: true, Reasoning: true, Enabled: true, Sort: 130},
		{ModelID: "deepseek-v4-flash", DisplayName: "Deepseek-V4-Flash", MaxInput: 1000000, MaxOutput: 50000, Vision: true, Reasoning: true, Enabled: true, Sort: 140},
	}
}

// BuiltinModelAliases 是 CodeBuddy 上游改过的模型 ID（旧名 -> 当前名）。
// 旧名仍被 /v1/models 广告，但真正发往上游前必须换成上游当前认可的 ID。
// 用户名下的 gateway.model-alias 优先级更高。
var BuiltinModelAliases = map[string]string{
	"hy4-preview": "hy4-preview-f",
	"hy3-preview": "hy3-x",
	"kimi-k3":     "kimi-k3-1",
}

// builtinDisplayName 给不在 defaultModels() 里的 ID 兜底一个显示名，
// 例如上游在售的 hy4-preview-f / hy3-x / kimi-k3-1。
func builtinDisplayName(modelID string) string {
	if name, ok := builtinDisplayNames[modelID]; ok {
		return name
	}
	return modelID
}

var builtinDisplayNames = map[string]string{
	"hy4-preview-f":     "Hy4 preview",
	"hy3-x":             "Hy3",
	"kimi-k3-1":         "Kimi-K3",
	"auto":              "Auto",
	"glm-5.2":           "GLM-5.2",
	"glm-5.1":           "GLM-5.1",
	"minimax-m3":        "MiniMax-M3",
	"deepseek-v4-flash": "Deepseek-V4-Flash",
}

// Models 返回 /v1/models 的本地兜底列表（含兼容别名）。
// 上游不可用、且用户没有配置 gateway.models 时使用。
func Models() []LLMModel {
	list, err := ListEnabledModels()
	if err != nil {
		return nil
	}
	out := make([]LLMModel, 0, len(list)+len(BuiltinModelAliases))
	seen := map[string]struct{}{}
	for _, m := range list {
		seen[m.ModelID] = struct{}{}
		out = append(out, m)
	}
	for _, from := range aliasOrder {
		to, ok := BuiltinModelAliases[from]
		if !ok {
			continue
		}
		if _, dup := seen[from]; dup {
			continue
		}
		seen[from] = struct{}{}
		out = append(out, LLMModel{
			ModelID:     from,
			DisplayName: builtinDisplayName(from),
			Enabled:     true,
			Sort:        aliasSort(to),
		})
	}
	return out
}

func aliasSort(modelID string) int {
	for _, m := range defaultModels() {
		if m.ModelID == modelID {
			return m.Sort
		}
	}
	return 0
}

// aliasOrder 固定兼容别名的追加顺序，避免 map 迭代带来的随机顺序。
var aliasOrder = []string{"hy4-preview", "hy3-preview", "kimi-k3"}

func ListEnabledModels() ([]LLMModel, error) {
	var list []LLMModel
	err := MustDB().Where("enabled = ?", true).Order("sort asc, id asc").Find(&list).Error
	return list, err
}

func ListModels() ([]LLMModel, error) {
	var list []LLMModel
	err := MustDB().Order("sort asc, id asc").Find(&list).Error
	return list, err
}

func UpsertModel(m *LLMModel) error {
	var existing LLMModel
	err := MustDB().Where("model_id = ?", m.ModelID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return MustDB().Create(m).Error
	}
	if err != nil {
		return err
	}
	m.ID = existing.ID
	return MustDB().Save(m).Error
}

func SetModelEnabled(id uint, enabled bool) error {
	return MustDB().Model(&LLMModel{}).Where("id = ?", id).Update("enabled", enabled).Error
}
