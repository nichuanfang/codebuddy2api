package service

import (
	"encoding/json"
	"strings"
	"testing"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"
)

func TestResponsesPreservesLargeCodexContext(t *testing.T) {
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true}
	text := strings.Repeat("abcd", 1_000_000)
	raw, err := json.Marshal(map[string]any{"model": "glm-5.3", "input": text})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := PrepareResponsesBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if got := messages[0].(map[string]any)["content"]; got != text {
		t.Fatalf("large context was changed: got %d bytes, want %d", len(got.(string)), len(text))
	}
}
