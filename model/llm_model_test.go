package model

import (
	"path/filepath"
	"testing"

	"codebuddy-gateway/global"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gateway.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&LLMModel{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// 兜底目录必须按 Codex /model 选择器的顺序返回：
// hy4-preview hy3 hy3-preview deepseek-v4.1-flash glm-5.3 glm-5.3-flash
// glm-5.3-flashx kimi-k3 deepseek-v4-pro
func TestDefaultModelsMatchesSelectorOrder(t *testing.T) {
	want := []string{
		"hy4-preview",
		"hy3",
		"hy3-preview",
		"deepseek-v4.1-flash",
		"glm-5.3",
		"glm-5.3-flash",
		"glm-5.3-flashx",
		"kimi-k3",
		"deepseek-v4-pro",
	}
	models := defaultModels()
	if len(models) < len(want) {
		t.Fatalf("defaultModels len=%d want >= %d", len(models), len(want))
	}
	for i, id := range want {
		if models[i].ModelID != id {
			t.Fatalf("defaultModels[%d]=%q want %q", i, models[i].ModelID, id)
		}
		if !models[i].Enabled {
			t.Fatalf("default model %s should be enabled", id)
		}
	}
}

// 上游在售、但没进 Codex 选择器的模型仍要保留在兜底目录里，避免丢能力。
func TestDefaultModelsKeepsExtraUpstreamModels(t *testing.T) {
	got := map[string]bool{}
	for _, m := range defaultModels() {
		got[m.ModelID] = true
	}
	for _, id := range []string{"auto", "glm-5.2", "glm-5.1", "minimax-m3", "deepseek-v4-flash"} {
		if !got[id] {
			t.Fatalf("expected %s to remain in the local fallback catalog", id)
		}
	}
}

// 兼容别名必须换到上游当前 ID，且不能指向自己。
func TestBuiltinModelAliasesMapRenamedIDs(t *testing.T) {
	cases := map[string]string{
		"hy4-preview": "hy4-preview-f",
		"hy3-preview": "hy3-x",
		"kimi-k3":     "kimi-k3-1",
	}
	for from, to := range cases {
		got, ok := BuiltinModelAliases[from]
		if !ok {
			t.Fatalf("missing alias for %s", from)
		}
		if got != to {
			t.Fatalf("alias %s -> %s want %s", from, got, to)
		}
		if got == from {
			t.Fatalf("alias %s must not map to itself", from)
		}
	}
}

func TestSeedDefaultModelsEnablesSelectorModels(t *testing.T) {
	db := newTestDB(t)
	if err := SeedDefaultModels(db); err != nil {
		t.Fatal(err)
	}

	var enabled []string
	if err := db.Model(&LLMModel{}).Where("enabled = ?", true).Order("sort asc, id asc").Pluck("model_id", &enabled).Error; err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range enabled {
		got[id] = true
	}
	for _, id := range []string{"hy4-preview", "hy3", "hy3-preview", "kimi-k3", "glm-5.3-flashx", "deepseek-v4-pro"} {
		if !got[id] {
			t.Fatalf("expected %s enabled, enabled=%v", id, enabled)
		}
	}
}

func TestSeedDefaultModelsDisablesRetiredIDs(t *testing.T) {
	db := newTestDB(t)
	if err := db.Create(&[]LLMModel{
		{ModelID: "glm-4.7", Enabled: true, Sort: 5},
		{ModelID: "hunyuan-chat", Enabled: true, Sort: 6},
		{ModelID: "kimi-k2.6", Enabled: true, Sort: 7},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaultModels(db); err != nil {
		t.Fatal(err)
	}

	var stillOn []string
	if err := db.Model(&LLMModel{}).
		Where("enabled = ? AND model_id IN ?", true, []string{"glm-4.7", "hunyuan-chat", "kimi-k2.6"}).
		Pluck("model_id", &stillOn).Error; err != nil {
		t.Fatal(err)
	}
	if len(stillOn) != 0 {
		t.Fatalf("retired ids should be disabled, still enabled: %v", stillOn)
	}
}

func TestSeedDefaultModelsIsIdempotentAndKeepsUserToggle(t *testing.T) {
	db := newTestDB(t)
	if err := SeedDefaultModels(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&LLMModel{}).Where("model_id = ?", "glm-5.2").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaultModels(db); err != nil {
		t.Fatal(err)
	}

	var m LLMModel
	if err := db.Where("model_id = ?", "glm-5.2").First(&m).Error; err != nil {
		t.Fatal(err)
	}
	if m.Enabled {
		t.Fatal("user-disabled model must stay disabled after re-seed")
	}

	var count int64
	if err := db.Model(&LLMModel{}).Where("model_id = ?", "glm-5.2").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("re-seed duplicated rows: count=%d", count)
	}
}

// Models() 是 /v1/models 的本地兜底：先给启用中的目录，再补上兼容别名。
func TestModelsAppendsCompatAliases(t *testing.T) {
	db := newTestDB(t)
	if err := SeedDefaultModels(db); err != nil {
		t.Fatal(err)
	}
	prev := global.CORE_DB
	global.CORE_DB = db
	t.Cleanup(func() { global.CORE_DB = prev })

	got := map[string]LLMModel{}
	for _, m := range Models() {
		got[m.ModelID] = m
	}
	// 旧名要在列表里，且必须是启用状态。
	for from := range BuiltinModelAliases {
		m, ok := got[from]
		if !ok {
			t.Fatalf("compat alias %s missing from Models()", from)
		}
		if !m.Enabled {
			t.Fatalf("compat alias %s should be enabled", from)
		}
	}
}
