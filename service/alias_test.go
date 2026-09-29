package service

import (
	"encoding/json"
	"strings"
	"testing"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"
)

func TestResolveModelAliasOpenAIFallback(t *testing.T) {
	resetGoodModelCache()
	global.CORE_CONFIG.Gateway = config.Gateway{
		ModelAlias:    []config.ModelAlias{{From: "gpt-4", To: "glm-5.1"}, {From: "gpt-4o", To: "glm-5.2"}},
		FallbackModel: "deepseek-v4.1-flash",
	}
	cases := []struct {
		in, want string
	}{
		{"gpt-4", "glm-5.1"},
		{"gpt-4o", "glm-5.2"},
		{"gpt-5.6-luna", "deepseek-v4.1-flash"},
		{"GPT-5.4", "deepseek-v4.1-flash"},
		{"o3-mini", "deepseek-v4.1-flash"},
		{"codex-mini", "deepseek-v4.1-flash"},
		{"deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{"hy3", "hy3"},
		{"glm-5.1", "glm-5.1"},
	}
	for _, tc := range cases {
		if got := ResolveModelAlias(tc.in); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.in, got, tc.want)
		}
	}
}

func TestResolveModelAliasUsesLastGoodModel(t *testing.T) {
	resetGoodModelCache()
	global.CORE_CONFIG.Gateway = config.Gateway{}
	rememberGoodModel("glm-5.1")
	if got := ResolveModelAlias("gpt-5.6-luna"); got != "glm-5.1" {
		t.Fatalf("got %s", got)
	}
}

// 上游把 hy4-preview/hy3-preview/kimi-k3 改名成带后缀的 ID。
// 网关既要继续广告旧名（面板、本地兜底目录），又必须在转发前换成上游当前 ID。
func TestResolveModelAliasBuiltinRenames(t *testing.T) {
	resetGoodModelCache()
	global.CORE_CONFIG.Gateway = config.Gateway{}
	cases := []struct{ in, want string }{
		{"hy4-preview", "hy4-preview-f"},
		{"hy3-preview", "hy3-x"},
		{"kimi-k3", "kimi-k3-1"},
		// 当前 ID 必须原样透传，不能被改写。
		{"hy4-preview-f", "hy4-preview-f"},
		{"hy3-x", "hy3-x"},
		{"kimi-k3-1", "kimi-k3-1"},
		{"hy3", "hy3"},
		{"glm-5.3", "glm-5.3"},
		{"minimax-m3", "minimax-m3"},
	}
	for _, tc := range cases {
		if got := ResolveModelAlias(tc.in); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.in, got, tc.want)
		}
	}
}

// 用户配置的 model-alias 仍然要覆盖内置改名映射。
func TestResolveModelAliasUserOverrideWins(t *testing.T) {
	resetGoodModelCache()
	global.CORE_CONFIG.Gateway = config.Gateway{
		ModelAlias: []config.ModelAlias{{From: "kimi-k3", To: "glm-5.3"}},
	}
	if got := ResolveModelAlias("kimi-k3"); got != "glm-5.3" {
		t.Fatalf("got %s want glm-5.3", got)
	}
}

func TestIsOpenAIHostedModel(t *testing.T) {
	yes := []string{"gpt-5.6-luna", "gpt-4", "o1", "o3-mini", "o4-mini", "codex-mini", "chatgpt-4o"}
	no := []string{"", "deepseek-v4.1-flash", "glm-5.1", "hy3", "codebuddy-codex", "auto", "kimi-k3"}
	for _, name := range yes {
		if !isOpenAIHostedModel(name) {
			t.Fatalf("expected openai hosted: %s", name)
		}
	}
	for _, name := range no {
		if isOpenAIHostedModel(name) {
			t.Fatalf("did not expect openai hosted: %s", name)
		}
	}
}

func TestMaybeRewriteUnavailableModel(t *testing.T) {
	resetGoodModelCache()
	global.CORE_CONFIG.Gateway = config.Gateway{FallbackModel: "deepseek-v4.1-flash"}
	meta := &ChatRequestMeta{
		UpstreamModel: "gpt-5.6-luna",
		Body:          []byte(`{"model":"gpt-5.6-luna","messages":[]}`),
	}
	if !maybeRewriteUnavailableModel(meta) {
		t.Fatal("expected rewrite")
	}
	if meta.UpstreamModel != "deepseek-v4.1-flash" {
		t.Fatalf("upstream=%s", meta.UpstreamModel)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "deepseek-v4.1-flash" {
		t.Fatalf("body model=%v", body["model"])
	}
	if maybeRewriteUnavailableModel(meta) {
		t.Fatal("should not rewrite the same model again")
	}
}

func TestPrepareResponsesBodyRemapsLuna(t *testing.T) {
	resetGoodModelCache()
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, FallbackModel: "deepseek-v4.1-flash"}
	meta, err := PrepareResponsesBody([]byte(`{
		"model":"gpt-5.6-luna",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if meta.RequestedModel != "gpt-5.6-luna" {
		t.Fatalf("requested=%s", meta.RequestedModel)
	}
	if meta.UpstreamModel != "deepseek-v4.1-flash" {
		t.Fatalf("upstream=%s", meta.UpstreamModel)
	}
	if !strings.Contains(string(meta.Body), `"model":"deepseek-v4.1-flash"`) {
		t.Fatalf("body=%s", meta.Body)
	}
}

func TestIsUpstreamModelUnavailable(t *testing.T) {
	if !isUpstreamModelUnavailable([]byte(`{"code":11102,"msg":"model [gpt-5.6-luna] is only available for authorized users"}`)) {
		t.Fatal("expected 11102")
	}
	if isUpstreamModelUnavailable([]byte(`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`)) {
		t.Fatal("11128 is not a model availability error")
	}
}
