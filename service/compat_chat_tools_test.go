package service

import (
	"encoding/json"
	"testing"
)

// chatToolParams decodes meta.Body and returns the parameters map of the first tool.
func chatToolParams(t *testing.T, meta *ChatRequestMeta) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatal("no tools in body")
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if fn == nil {
		t.Fatal("no function in first tool")
	}
	params, _ := fn["parameters"].(map[string]any)
	if params == nil {
		t.Fatal("no parameters in first tool")
	}
	return params
}

// The exact MCP loose schema that reproduced the upstream 11133/11129 rejection:
// $schema keyword + nullable anyOf wrapper.
func TestChatToolsNormalizesMCPLooseSchema(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{
		"model":"deepseek-v4.1-flash",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{
			"name":"mcp__myserver__query",
			"description":"query data",
			"parameters":{"$schema":"http://json-schema.org/draft-07/schema#","anyOf":[{"type":"object","properties":{"q":{"type":"string"}}},{"type":"null"}]}
		}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	params := chatToolParams(t, meta)
	if _, ok := params["$schema"]; ok {
		t.Fatalf("$schema not stripped: %v", params)
	}
	if _, ok := params["anyOf"]; ok {
		t.Fatalf("nullable anyOf not collapsed: %v", params)
	}
	if params["type"] != "object" {
		t.Fatalf("type not filled: %v", params)
	}
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("properties missing after collapse: %v", params)
	}
	if _, ok := props["q"]; !ok {
		t.Fatalf("property q lost during collapse: %v", props)
	}
}

func TestChatToolsFillsMissingParametersAndType(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{
		"model":"deepseek-v4.1-flash",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"type":"function","function":{"name":"a","description":"d"}},
			{"type":"function","function":{"name":"b","description":"d","parameters":null}},
			{"type":"function","function":{"name":"c","description":"d","parameters":{"properties":{"x":{"type":"string"}}}}}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]any)
	for i, tool := range tools {
		params, _ := tool.(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
		if params == nil {
			t.Fatalf("tool %d parameters missing", i)
		}
		if params["type"] != "object" {
			t.Fatalf("tool %d type=%v", i, params["type"])
		}
	}
}

func TestChatToolsNestedNullableSchemaIsNormalized(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{
		"model":"deepseek-v4.1-flash",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{
			"name":"mcp__srv__find",
			"description":"find",
			"parameters":{"type":"object","properties":{"limit":{"oneOf":[{"type":"integer"},{"type":"null"}]}}}
		}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	params := chatToolParams(t, meta)
	props := params["properties"].(map[string]any)
	limit := props["limit"].(map[string]any)
	if _, ok := limit["oneOf"]; ok {
		t.Fatalf("nested oneOf not collapsed: %v", limit)
	}
	if limit["type"] != "integer" {
		t.Fatalf("nested limit type=%v", limit["type"])
	}
}

// A clean, already-valid schema must pass through untouched (no regression).
func TestChatToolsLeavesValidSchemaUntouched(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{
		"model":"deepseek-v4.1-flash",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{
			"name":"test","description":"test",
			"parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}
		}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	params := chatToolParams(t, meta)
	if params["type"] != "object" {
		t.Fatalf("type=%v", params["type"])
	}
	req, _ := params["required"].([]any)
	if len(req) != 1 || req[0] != "q" {
		t.Fatalf("required lost: %v", params)
	}
	props := params["properties"].(map[string]any)
	if props["q"].(map[string]any)["type"] != "string" {
		t.Fatalf("property q mangled: %v", props)
	}
}

func TestChatToolsNoToolsIsNoop(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{
		"model":"deepseek-v4.1-flash",
		"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("tools should not be added")
	}
}

// OpenCat streams tool-call deltas as separate entries sharing one index; the
// first carries id/name, the rest carry only argument fragments. This is the
// exact shape captured in the user's debug log (2026-09-30 14:43), which the
// upstream rejected with 11133.
func TestChatMessagesMergeStreamedToolCallFragments(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{"model":"deepseek-v4.1-flash","messages":[
		{"role":"system","content":"Current Time: 2026-09-30"},
		{"role":"user","content":"六安天气"},
		{"role":"assistant","content":"","tool_calls":[
			{"function":{"arguments":"","name":"WebSearch"},"id":"call_00_XAs4pLemr95bRlIuH9G52219","index":0,"type":"function"},
			{"function":{"arguments":"{","name":""},"index":0},
			{"function":{"arguments":"\"query\"","name":""},"index":0},
			{"function":{"arguments":":","name":""},"index":0},
			{"function":{"arguments":"\"六安天气\"","name":""},"index":0},
			{"function":{"arguments":"}","name":""},"index":0}
		]},
		{"role":"tool","tool_call_id":"call_00_XAs4pLemr95bRlIuH9G52219","content":"sunny"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("message count=%d", len(msgs))
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool call count=%d, want 1", len(calls))
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_00_XAs4pLemr95bRlIuH9G52219" {
		t.Fatalf("id=%v", call["id"])
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "WebSearch" {
		t.Fatalf("name=%v", fn["name"])
	}
	if fn["arguments"] != `{"query":"六安天气"}` {
		t.Fatalf("arguments=%q", fn["arguments"])
	}
}

// Orphan tool results (no tool_call_id) must be dropped: the OpenAI schema
// requires tool_call_id and the upstream rejects messages without it.
func TestChatMessagesDropOrphanToolResults(t *testing.T) {
	meta, err := PrepareChatBody([]byte(`{"model":"deepseek-v4.1-flash","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[
			{"function":{"arguments":"{}","name":"WebSearch"},"id":"call_1","index":0,"type":"function"}
		]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"},
		{"role":"tool","content":"orphan1"},
		{"role":"tool","content":"orphan2"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("message count=%d, want 3 (orphans dropped)", len(msgs))
	}
	for _, raw := range msgs {
		message := raw.(map[string]any)
		if message["role"] == "tool" && asString(message["tool_call_id"]) == "" {
			t.Fatalf("orphan tool message survived: %v", message)
		}
	}
}

// Clean history must pass through unchanged (no regression).
func TestChatMessagesLeavesCleanHistoryUntouched(t *testing.T) {
	clean := `{"model":"deepseek-v4.1-flash","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"WebSearch","arguments":"{\"query\":\"x\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"}
	]}`
	meta, err := PrepareChatBody([]byte(clean))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("message count=%d", len(msgs))
	}
	calls := msgs[1].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool call count=%d", len(calls))
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_1" || call["function"].(map[string]any)["arguments"] != `{"query":"x"}` {
		t.Fatalf("call mutated: %v", call)
	}
}
