package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesPreservesImagesInMessagesAndToolOutputs(t *testing.T) {
	const dataURL = "data:image/png;base64,aGVsbG8="
	raw := []byte(`{"model":"glm-5.3","input":[
		{"role":"user","content":[{"type":"input_text","text":"Inspect"},{"type":"input_image","image_url":"` + dataURL + `","detail":"high"}]},
		{"type":"function_call","name":"screenshot","call_id":"call_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":"` + dataURL + `"},{"type":"input_text","text":"Screenshot result"},{"type":"input_image","image_url":{"url":"https://example.test/image.png","detail":"low"}}]}
	]}`)
	body, err := responsesToChat(raw)
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	first := messages[0].(map[string]any)
	content := first["content"].([]any)
	if content[0].(map[string]any)["text"] != "Inspect" {
		t.Fatalf("text order lost: %#v", content)
	}
	if got := content[1].(map[string]any)["image_url"].(map[string]any)["detail"]; got != "high" {
		t.Fatalf("detail=%v", got)
	}
	assistant := messages[1].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	tool := messages[2].(map[string]any)
	if call["id"] != tool["tool_call_id"] {
		t.Fatalf("tool call id mismatch: %#v %#v", call, tool)
	}
	toolContent := tool["content"].([]any)
	if len(toolContent) != 3 || toolContent[0].(map[string]any)["type"] != "image_url" || toolContent[1].(map[string]any)["text"] != "Screenshot result" {
		t.Fatalf("tool content=%#v", toolContent)
	}
}

func TestAnthropicPreservesImagesInUserAndToolResult(t *testing.T) {
	const dataURL = "data:image/png;base64,aGVsbG8="
	raw := []byte(`{"model":"glm-5.3","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"screenshot","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},{"type":"text","text":"result"}]},{"type":"text","text":"Continue"}]}]}`)
	body, err := anthropicToChat(raw)
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	tool := messages[1].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" {
		t.Fatalf("tool=%#v", tool)
	}
	toolContent := tool["content"].([]any)
	if !strings.HasPrefix(toolContent[0].(map[string]any)["image_url"].(map[string]any)["url"].(string), "data:image/png;base64,") {
		t.Fatalf("image=%#v", toolContent[0])
	}
	user := messages[2].(map[string]any)
	if user["content"] != "Continue" {
		t.Fatalf("user=%#v", user)
	}
}

func TestImageContentRejectsUnsupportedBlocks(t *testing.T) {
	cases := []string{
		`{"model":"glm-5.3","input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`,
		`{"model":"glm-5.3","input":[{"role":"user","content":[{"type":"unknown_block"}]}]}`,
		`{"model":"glm-5.3","input":[{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"data:image/png;base64,not-base64"}]}]}`,
	}
	for _, raw := range cases {
		if _, err := responsesToChat([]byte(raw)); err == nil {
			t.Fatalf("expected conversion error for %s", raw)
		}
	}
}

func TestConvertedImageJSONShapeIsStable(t *testing.T) {
	body, err := responsesToChat([]byte(`{"model":"glm-5.3","input":[{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.test/a.png","detail":"low"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"detail":"low"`) {
		t.Fatalf("detail missing: %s", encoded)
	}
}
