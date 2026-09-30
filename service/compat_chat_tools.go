package service

import "strings"

// repairChatMessages fixes malformed tool-call history that some clients
// (notably OpenCat) send back on /v1/chat/completions. The upstream rejects
// such history with 11133.
//
// Two defects are repaired:
//  1. Assistant tool_calls fragments: streamed deltas each appear as a separate
//     entry sharing the same index, where only the first entry carries id/name.
//     Fragments are merged by index, concatenating arguments exactly as the
//     streaming protocol prescribes. Entries that still have neither id nor
//     name after merging are dropped.
//  2. Orphan tool results: role:"tool" messages without tool_call_id cannot be
//     matched to any call and are dropped per the OpenAI message schema.
//
// Clean requests pass through unchanged.
func repairChatMessages(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}
	changed := false
	out := make([]any, 0, len(msgs))
	for _, item := range msgs {
		message, _ := item.(map[string]any)
		if message == nil {
			out = append(out, item)
			continue
		}
		switch strings.ToLower(asString(message["role"])) {
		case "assistant":
			if repairAssistantToolCalls(message) {
				changed = true
			}
		case "tool":
			if asString(message["tool_call_id"]) == "" {
				changed = true
				continue
			}
		}
		out = append(out, message)
	}
	if changed {
		body["messages"] = out
	}
}

func repairAssistantToolCalls(message map[string]any) bool {
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) == 0 {
		return false
	}
	// Streaming deltas always carry "index"; fully-formed history (including
	// gateway-converted Responses history) does not. Only attempt a merge when
	// index fields are actually present, so legitimate parallel tool calls
	// without index are never collapsed.
	hasIndex := false
	for _, raw := range calls {
		if call, _ := raw.(map[string]any); call != nil {
			if _, ok := call["index"]; ok {
				hasIndex = true
				break
			}
		}
	}
	if !hasIndex {
		return false
	}
	merged := map[int]map[string]any{}
	order := make([]int, 0, len(calls))
	fragmented := false
	for _, raw := range calls {
		call, _ := raw.(map[string]any)
		if call == nil {
			fragmented = true
			continue
		}
		index := 0
		if v, ok := call["index"].(float64); ok {
			index = int(v)
		}
		fn, _ := call["function"].(map[string]any)
		name, arguments := "", ""
		if fn != nil {
			name = asString(fn["name"])
			arguments = asString(fn["arguments"])
		}
		existing, seen := merged[index]
		if !seen {
			toolType := asString(call["type"])
			if toolType == "" {
				toolType = "function"
			}
			entry := map[string]any{
				"type":  toolType,
				"id":    asString(call["id"]),
				"index": index,
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			}
			merged[index] = entry
			order = append(order, index)
			continue
		}
		fragmented = true
		entryFn, _ := existing["function"].(map[string]any)
		if asString(existing["id"]) == "" {
			existing["id"] = asString(call["id"])
		}
		if asString(entryFn["name"]) == "" {
			entryFn["name"] = name
		}
		entryFn["arguments"] = asString(entryFn["arguments"]) + arguments
	}
	if !fragmented {
		return false
	}
	repaired := make([]any, 0, len(merged))
	for _, index := range order {
		entry := merged[index]
		entryFn, _ := entry["function"].(map[string]any)
		if asString(entry["id"]) == "" && asString(entryFn["name"]) == "" {
			continue
		}
		repaired = append(repaired, entry)
	}
	message["tool_calls"] = repaired
	return true
}

// normalizeChatTools normalizes tools in a Chat Completions request body so the
// upstream CodeBuddy Chat endpoint accepts them. The upstream validates
// tools[].function.parameters and rejects loose JSON Schema (missing "type",
// "$schema" keywords, nullable "anyOf"/"oneOf" wrappers) with error 11133/11129.
//
// The Responses path already normalizes tools via functionToolFromMap /
// normalizeFunctionParameters; this brings the same guarantees to the Chat
// passthrough path so MCP-style tools from clients like OpenCat work there too.
//
// Normalization rules (conservative — we never drop a tool or change semantics):
//   - ensure parameters is a valid object with "type": "object"
//   - strip meta-schema keywords the upstream rejects: $schema, $id, $comment
//   - collapse single-option nullable anyOf/oneOf ([schema, {type:"null"}]) into schema
//   - recurse into properties/items so nested loose schemas are fixed too
func normalizeChatTools(body map[string]any) {
	list, ok := body["tools"].([]any)
	if !ok || len(list) == 0 {
		return
	}
	for _, item := range list {
		tool, _ := item.(map[string]any)
		if tool == nil {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		fn["parameters"] = normalizeToolSchema(fn["parameters"], true)
	}
}

// normalizeToolSchema returns a copy of the schema with unsupported keywords
// stripped and a valid "type" guaranteed when root is true. root=true means this
// is the top-level function parameters object (must be a valid object schema).
func normalizeToolSchema(v any, root bool) map[string]any {
	if v == nil {
		if root {
			return map[string]any{"type": "object", "properties": map[string]any{}}
		}
		return map[string]any{"type": "object"}
	}
	m, ok := v.(map[string]any)
	if !ok {
		if root {
			return map[string]any{"type": "object", "properties": map[string]any{}}
		}
		return map[string]any{"type": "object"}
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = val
	}
	// Drop meta-schema / annotation keywords the upstream function-call validator
	// rejects. These carry no runtime meaning for the model.
	for _, drop := range []string{"$schema", "$id", "$comment", "$vocabulary"} {
		delete(out, drop)
	}
	// Collapse nullable wrappers: anyOf/oneOf of [realSchema, {type:"null"}] is how
	// MCP expresses optional values; the upstream only accepts a plain schema.
	out = collapseNullableWrapper(out, "anyOf")
	out = collapseNullableWrapper(out, "oneOf")

	// Ensure a valid top-level type. Loose MCP schemas frequently omit it.
	if root {
		if typ, ok := out["type"].(string); !ok || typ == "" {
			out["type"] = "object"
		}
		if _, ok := out["properties"]; !ok {
			out["properties"] = map[string]any{}
		}
	}

	// Recurse into nested object schemas.
	if props, ok := out["properties"].(map[string]any); ok {
		for name, p := range props {
			if pm, ok := p.(map[string]any); ok {
				props[name] = normalizeToolSchema(pm, false)
			}
		}
	}
	if items, ok := out["items"].(map[string]any); ok {
		out["items"] = normalizeToolSchema(items, false)
	}
	return out
}

// collapseNullableWrapper reduces anyOf/oneOf: [schema, {type:"null"}] (or the
// reverse) to schema. Returns the map unchanged when the wrapper is not a
// simple two-branch nullable form.
func collapseNullableWrapper(m map[string]any, key string) map[string]any {
	raw, ok := m[key].([]any)
	if !ok || len(raw) != 2 {
		return m
	}
	isNull := func(v any) bool {
		vm, ok := v.(map[string]any)
		if !ok {
			return false
		}
		t, _ := vm["type"].(string)
		return t == "null" && len(vm) == 1
	}
	var schema any
	switch {
	case isNull(raw[0]) && !isNull(raw[1]):
		schema = raw[1]
	case isNull(raw[1]) && !isNull(raw[0]):
		schema = raw[0]
	default:
		return m
	}
	delete(m, key)
	sm, ok := schema.(map[string]any)
	if !ok {
		return m
	}
	for k, v := range sm {
		m[k] = v
	}
	return m
}
