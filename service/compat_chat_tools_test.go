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
