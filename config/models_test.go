package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// gateway.models 必须同时支持两种写法：
//   - 简写：直接写模型 ID 字符串
//   - 完整：{id, display-name}
func TestGatewayModelsUnmarshalBothForms(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	raw := `
gateway:
  models:
    - hy4-preview-f
    - hy3
    - id: glm-5.3-flashx
      display-name: GLM-5.3-FlashX
`
	if err := v.ReadConfig(strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var cfg CORE
	if err := v.Unmarshal(&cfg, viper.DecodeHook(ModelDecodeHook())); err != nil {
		t.Fatal(err)
	}

	entries, ok := cfg.Gateway.ConfiguredModels()
	if !ok {
		t.Fatal("expected configured models")
	}
	want := []struct{ id, name string }{
		{"hy4-preview-f", "hy4-preview-f"},
		{"hy3", "hy3"},
		{"glm-5.3-flashx", "GLM-5.3-FlashX"},
	}
	if len(entries) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].ID != w.id {
			t.Fatalf("entries[%d].ID=%q want %q", i, entries[i].ID, w.id)
		}
		if entries[i].DisplayName != w.name {
			t.Fatalf("entries[%d].DisplayName=%q want %q", i, entries[i].DisplayName, w.name)
		}
	}
}

func TestConfiguredModelsEmptyAndDedup(t *testing.T) {
	g := Gateway{}
	if _, ok := g.ConfiguredModels(); ok {
		t.Fatal("empty models should report not-configured")
	}

	g = Gateway{Models: []ModelEntry{{ID: "  "}, {ID: ""}}}
	if _, ok := g.ConfiguredModels(); ok {
		t.Fatal("blank-only models should report not-configured")
	}

	g = Gateway{Models: []ModelEntry{{ID: "hy3"}, {ID: " hy3 "}, {ID: "glm-5.3"}}}
	entries, ok := g.ConfiguredModels()
	if !ok {
		t.Fatal("expected configured models")
	}
	if len(entries) != 2 {
		t.Fatalf("len=%d want 2 (dedup): %#v", len(entries), entries)
	}
}
