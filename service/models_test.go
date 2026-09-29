package service

import (
	"testing"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"
)

// 配置了 gateway.models 时，它必须成为 /v1/models 的唯一来源，
// 且顺序与配置一致，不合并上游目录或别名。
func TestConfiguredOpenAIModelsOverridesEverything(t *testing.T) {
	prev := global.CORE_CONFIG.Gateway.Models
	t.Cleanup(func() { global.CORE_CONFIG.Gateway.Models = prev })
	global.CORE_CONFIG.Gateway.Models = []config.ModelEntry{
		{ID: "hy4-preview-f"},
		{ID: "hy3-x"},
		{ID: "glm-5.3-flashx", DisplayName: "GLM-5.3-FlashX"},
		{ID: "  "},    // 空白项应被忽略
		{ID: "hy3-x"}, // 重复项应被去重
	}

	got, ok := configuredOpenAIModels()
	if !ok {
		t.Fatal("expected configured models to take effect")
	}
	want := []string{"hy4-preview-f", "hy3-x", "glm-5.3-flashx"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("got[%d]=%q want %q", i, got[i].ID, id)
		}
	}
}

// 未配置 gateway.models 时必须回落到实时/兜底目录。
func TestConfiguredOpenAIModelsUnset(t *testing.T) {
	prev := global.CORE_CONFIG.Gateway.Models
	t.Cleanup(func() { global.CORE_CONFIG.Gateway.Models = prev })
	global.CORE_CONFIG.Gateway.Models = nil

	if _, ok := configuredOpenAIModels(); ok {
		t.Fatal("expected no configured override when models is empty")
	}
	global.CORE_CONFIG.Gateway.Models = []config.ModelEntry{{ID: "   "}}
	if _, ok := configuredOpenAIModels(); ok {
		t.Fatal("blank-only configuration should be treated as unset")
	}
}

func TestParseUpstreamModels(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"models":[{"id":"glm-5.3","name":"GLM-5.3"},{"id":"hy4-preview-f","name":"Hy4 preview"},{"id":"glm-5.3","name":"dup"}]}}`)
	got := parseUpstreamModels(raw)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0].ID != "glm-5.3" || got[1].ID != "hy4-preview-f" {
		t.Fatalf("unexpected ids: %#v %#v", got[0], got[1])
	}
}
