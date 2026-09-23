package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLooksLikePreambleIncludesRecentAgentPhrases(t *testing.T) {
	for _, text := range []string{
		"Everything is preserved. Now rebuild commit 1.",
		"Rebuilding all three commits now.",
		"Commit 1 rebuilt. Now commit 2.",
		"Continuing — rebuilding the commits now.",
		"开始提交剩余改动。",
	} {
		if !looksLikePreamble(text) {
			t.Fatalf("expected preamble: %q", text)
		}
	}

	for _, text := range []string{
		"All three commits have been rebuilt successfully.",
		"已完成提交，工作区干净。",
		"The requested changes are complete.",
	} {
		if looksLikePreamble(text) {
			t.Fatalf("did not expect completed response to be a preamble: %q", text)
		}
	}
}

func TestPolicyForAgentModels(t *testing.T) {
	tests := []struct {
		model  string
		retry  int
		strict bool
	}{
		{"deepseek-v4.1-flash", 2, true},
		{"deepseek-v4.1-flash-preview", 2, true},
		{"glm-5.3", 1, true},
		{"glm-5.2", 1, true},
		{"kimi-k2.7-code", 1, false},
	}
	for _, tt := range tests {
		got := policyForModel(tt.model)
		if got.maxPreambleRetries != tt.retry || got.strictPrompt != tt.strict {
			t.Fatalf("policy(%q)=%+v, want retries=%d strict=%v", tt.model, got, tt.retry, tt.strict)
		}
	}
}

func TestInjectUnattendedRuntimeAddsStrictPromptForAgentModels(t *testing.T) {
	chat := map[string]any{
		"model": "glm-5.3",
		"messages": []any{
			map[string]any{"role": "user", "content": "commit the changes"},
		},
		"tools": []any{
			map[string]any{"function": map[string]any{"name": "apply_patch"}},
		},
	}
	injectUnattendedRuntime(chat)

	msgs := chat["messages"].([]any)
	var joined []string
	for _, raw := range msgs {
		msg := raw.(map[string]any)
		joined = append(joined, msg["content"].(string))
	}
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("system prompt was not moved to the head: %v", msgs)
	}
	if !strings.Contains(strings.Join(joined, "\n"), modelExecutionNote) {
		t.Fatal("strict execution note was not injected")
	}

	injectUnattendedRuntime(chat)
	if len(chat["messages"].([]any)) != 2 {
		t.Fatal("strict execution note was injected more than once")
	}
	if strings.Count(chat["messages"].([]any)[0].(map[string]any)["content"].(string), modelExecutionNote) != 1 {
		t.Fatal("strict execution note was duplicated")
	}
}

func TestNudgeChatBodyRequiresTool(t *testing.T) {
	body, err := nudgeChatBody([]byte(`{"model":"glm-5.3","messages":[]}`), "Now rebuild commit 1.")
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatal(err)
	}
	if chat["tool_choice"] != "required" {
		t.Fatalf("tool_choice=%v", chat["tool_choice"])
	}
	if len(chat["messages"].([]any)) != 2 {
		t.Fatalf("messages=%v", chat["messages"])
	}
}

func TestInjectUnattendedRuntimeMovesExistingSystemMessagesToHead(t *testing.T) {
	chat := map[string]any{
		"model": "glm-5.3",
		"messages": []any{
			map[string]any{"role": "user", "content": "do it"},
			map[string]any{"role": "system", "content": "late instruction"},
		},
		"tools": []any{map[string]any{"function": map[string]any{"name": "apply_patch"}}},
	}
	injectUnattendedRuntime(chat)
	messages := chat["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages=%v", messages)
	}
	if messages[1].(map[string]any)["role"] != "user" {
		t.Fatalf("messages=%v", messages)
	}
}

func TestNudgeChatBodyRejectsMissingOrInvalidMessages(t *testing.T) {
	for _, body := range []string{
		`{"model":"glm-5.3"}`,
		`{"model":"glm-5.3","messages":{}}`,
	} {
		if _, err := nudgeChatBody([]byte(body), "I will continue."); err == nil || !strings.Contains(err.Error(), "messages must be an array") {
			t.Fatalf("body=%s error=%v", body, err)
		}
	}
}

func TestInjectUnattendedRuntimeAddsSystemPromptAtHeadWithoutExistingSystem(t *testing.T) {
	chat := map[string]any{
		"model": "glm-5.3",
		"messages": []any{
			map[string]any{"role": "user", "content": "do it"},
		},
		"tools": []any{map[string]any{"function": map[string]any{"name": "apply_patch"}}},
	}
	injectUnattendedRuntime(chat)
	messages := chat["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "user" {
		t.Fatalf("messages=%v", messages)
	}
}

func TestNudgeSSEContinuesExistingResponsesLifecycle(t *testing.T) {
	var buf bytes.Buffer
	adapter := newStreamAdapter(ProtocolResponses, &buf, nil, "glm-5.3")
	adapter.start()
	adapter.onText("I will inspect the repository first.")
	_, err := pipeChatSSEToAdapter(strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"apply_patch","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n")), adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.finish(); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if strings.Count(raw, "event: response.created") != 1 || strings.Count(raw, "event: response.in_progress") != 1 {
		t.Fatalf("nudge emitted duplicate lifecycle events: %s", raw)
	}
	if !strings.Contains(raw, "response.custom_tool_call_input.done") || !strings.Contains(raw, "apply_patch") {
		t.Fatalf("tool call missing from nudged stream: %s", raw)
	}
}
