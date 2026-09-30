package service

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
