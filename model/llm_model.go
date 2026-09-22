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

	// 这些是旧版本内置的模型。它们不再出现在本地兜底目录中，
	// 但不影响用户通过 passthrough 直接请求旧模型 ID。
	legacyIDs := []string{
		"glm-5.2", "glm-5.1", "glm-5.0-turbo", "glm-5v-turbo", "glm-4.7",
		"kimi-k2.7-code", "kimi-k2.6", "deepseek-v4-flash", "deepseek-v3-2-volc",
		"deepseek-r1-0528-lkeap", "minimax-m3", "minimax-m2.7", "hunyuan-chat",
		"codewise-completions",
	}
	return db.Model(&LLMModel{}).Where("model_id IN ?", legacyIDs).Update("enabled", false).Error
}

func defaultModels() []LLMModel {
	return []LLMModel{
		{ModelID: "auto", DisplayName: "Auto", MaxInput: 168000, MaxOutput: 32000, Vision: true, Enabled: true, Sort: 10},
		{ModelID: "hy4-preview", DisplayName: "Hy4 preview", MaxInput: 192000, MaxOutput: 64000, Reasoning: true, Enabled: true, Sort: 20},
		{ModelID: "hy3", DisplayName: "Hy3", MaxInput: 192000, MaxOutput: 64000, Reasoning: true, Enabled: true, Sort: 30},
		{ModelID: "hy3-preview", DisplayName: "Hy3 preview", MaxInput: 192000, MaxOutput: 64000, Reasoning: true, Enabled: true, Sort: 40},
		{ModelID: "deepseek-v4.1-flash", DisplayName: "DeepSeek-V4.1-Flash", MaxInput: 128000, MaxOutput: 32000, Vision: true, Reasoning: true, Enabled: true, Sort: 50},
		{ModelID: "deepseek-v4-pro", DisplayName: "DeepSeek-V4-Pro", MaxInput: 128000, MaxOutput: 32000, Vision: true, Reasoning: true, Enabled: true, Sort: 60},
		{ModelID: "glm-5.3", DisplayName: "GLM-5.3", MaxInput: 200000, MaxOutput: 48000, Reasoning: true, Enabled: true, Sort: 70},
		{ModelID: "glm-5.3-flash", DisplayName: "GLM-5.3-Flash", MaxInput: 200000, MaxOutput: 48000, Reasoning: true, Enabled: true, Sort: 80},
		{ModelID: "kimi-k3", DisplayName: "Kimi-K3", MaxInput: 256000, MaxOutput: 32000, Vision: true, Reasoning: true, Enabled: true, Sort: 90},
	}
}
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
