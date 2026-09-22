package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"codebuddy-gateway/global"
)

func PrepareResponsesBody(raw []byte) (*ChatRequestMeta, error) {
	chat, err := responsesToChat(raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(chat)
	if err != nil {
		return nil, err
	}
	meta, err := PrepareChatBody(encoded)
	if err != nil {
		return nil, err
	}
	meta.Protocol = ProtocolResponses
	return meta, nil
}

func PrepareAnthropicBody(raw []byte) (*ChatRequestMeta, error) {
	chat, err := anthropicToChat(raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(chat)
	if err != nil {
		return nil, err
	}
	meta, err := PrepareChatBody(encoded)
	if err != nil {
		return nil, err
	}
	meta.Protocol = ProtocolAnthropic
	return meta, nil
}

func responsesToChat(raw []byte) (map[string]any, error) {
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	model, _ := src["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	chat := map[string]any{"model": model}
	copyChatFields(chat, src, "stream", "temperature", "top_p", "user", "n", "stop", "metadata")
	if v, ok := src["max_output_tokens"]; ok {
		chat["max_tokens"] = v
	}
	if v, ok := src["parallel_tool_calls"]; ok {
		chat["parallel_tool_calls"] = v
	}

	messages := make([]any, 0, 4)
	if src["instructions"] != nil {
		inst, err := convertChatContent(src["instructions"])
		if err != nil {
			return nil, fmt.Errorf("instructions: %w", err)
		}
		if inst != nil && inst != "" {
			messages = append(messages, map[string]any{"role": "system", "content": inst})
		}
	}
	convertedInput, err := convertResponsesInput(src["input"])
	if err != nil {
		return nil, err
	}
	messages = append(messages, convertedInput...)
	chat["messages"] = messages

	if tools := convertResponsesTools(src["tools"]); tools != nil {
		chat["tools"] = tools
	}
	if tc := convertResponsesToolChoice(src["tool_choice"]); tc != nil {
		chat["tool_choice"] = tc
	}
	if r, ok := src["reasoning"].(map[string]any); ok {
		if effort := asString(r["effort"]); effort != "" {
			chat["reasoningEffort"] = effort
		}
		if summary := asString(r["summary"]); summary != "" {
			chat["reasoning_summary"] = summary
		}
	}
	injectUnattendedRuntime(chat)
	return chat, nil
}

func normalizeUpstreamRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "developer", "system":
		return "system"
	case "assistant":
		return "assistant"
	case "tool":
		return "tool"
	default:
		return "user"
	}
}

func convertResponsesInput(v any) ([]any, error) {
	if v == nil {
		return nil, nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return nil, nil
		}
		return []any{map[string]any{"role": "user", "content": s}}, nil
	}
	arr, ok := v.([]any)
	if !ok {
		if m, ok := v.(map[string]any); ok {
			arr = []any{m}
		} else {
			return nil, fmt.Errorf("responses input must be a string, object, or array")
		}
	}
	messages := make([]any, 0, len(arr))
	var pending []any
	flush := func() {
		if len(pending) == 0 {
			return
		}
		messages = append(messages, map[string]any{
			"role":       "assistant",
			"content":    nil,
			"tool_calls": pending,
		})
		pending = nil
	}
	for _, item := range arr {
		if s, ok := item.(string); ok {
			flush()
			messages = append(messages, map[string]any{"role": "user", "content": s})
			continue
		}
		m, _ := item.(map[string]any)
		if m == nil {
			return nil, fmt.Errorf("responses input items must be objects")
		}
		typ := asString(m["type"])
		role := asString(m["role"])
		switch typ {
		case "function_call", "custom_tool_call":
			pending = append(pending, responsesFunctionCallToToolCall(m))
		case "function_call_output", "tool_result", "custom_tool_call_output":
			flush()
			callID := asString(m["call_id"])
			if callID == "" {
				callID = asString(m["tool_use_id"])
			}
			content, err := convertChatContent(firstNonNil(m["output"], m["content"]))
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      content,
			})
		case "reasoning", "item_reference":
			continue
		default:
			flush()
			role = normalizeUpstreamRole(role)
			content, err := convertChatContent(m["content"])
			if err != nil {
				return nil, err
			}
			if content == nil || content == "" {
				if t := asString(m["text"]); t != "" {
					content = t
				}
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		}
	}
	flush()
	return messages, nil
}

func responsesFunctionCallToToolCall(m map[string]any) map[string]any {
	id := asString(m["call_id"])
	if id == "" {
		id = asString(m["id"])
	}
	name := asString(m["name"])
	args := asString(m["arguments"])
	if args == "" {
		if input := m["input"]; input != nil {
			if str, ok := input.(string); ok {
				args = str
			} else {
				args = mustJSON(input)
			}
		}
	}
	if isFreeformTool(name) {
		args = ensureFreeformJSONArgs(args)
	}
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func isFreeformTool(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "apply_patch" || n == "applypatch" || strings.HasSuffix(n, "__apply_patch")
}

func unwrapFreeformArgs(args string) string {
	s := strings.TrimSpace(args)
	if s == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return args
	}
	raw, ok := obj["input"]
	if !ok {
		return args
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		return args
	}
	return inner
}

func ensureFreeformJSONArgs(args string) string {
	s := strings.TrimSpace(args)
	if s == "" {
		b, _ := json.Marshal(map[string]string{"input": ""})
		return string(b)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err == nil {
		if _, ok := obj["input"]; ok {
			return s
		}
	}
	b, _ := json.Marshal(map[string]string{"input": args})
	return string(b)
}

func convertChatContent(v any) (any, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	var arr []any
	switch value := v.(type) {
	case []any:
		arr = value
	case map[string]any:
		arr = []any{value}
	default:
		return nil, fmt.Errorf("content must be a string or an array of blocks")
	}

	parts := make([]any, 0, len(arr))
	var text strings.Builder
	onlyText := true
	for i, item := range arr {
		if s, ok := item.(string); ok {
			text.WriteString(s)
			parts = append(parts, map[string]any{"type": "text", "text": s})
			continue
		}
		m, ok := item.(map[string]any)
		if !ok || m == nil {
			return nil, fmt.Errorf("content block %d must be an object", i)
		}
		typ := strings.ToLower(strings.TrimSpace(asString(m["type"])))
		switch typ {
		case "input_text", "output_text", "text", "summary_text":
			t := asString(m["text"])
			text.WriteString(t)
			parts = append(parts, map[string]any{"type": "text", "text": t})
		case "input_image", "image_url", "image":
			image, err := chatImagePart(m, typ)
			if err != nil {
				return nil, fmt.Errorf("content block %d: %w", i, err)
			}
			onlyText = false
			parts = append(parts, image)
		default:
			return nil, fmt.Errorf("unsupported content block type %q", typ)
		}
	}
	if onlyText {
		return text.String(), nil
	}
	return parts, nil
}

func chatImagePart(m map[string]any, typ string) (map[string]any, error) {
	url, detail, err := imageURLAndDetail(m, typ)
	if err != nil {
		return nil, err
	}
	imageURL := map[string]any{"url": url}
	if detail == "" {
		detail = asString(m["detail"])
	}
	if detail != "" {
		imageURL["detail"] = detail
	}
	return map[string]any{
		"type":      "image_url",
		"image_url": imageURL,
	}, nil
}

func imageURLAndDetail(m map[string]any, typ string) (string, string, error) {
	if typ == "input_image" && strings.TrimSpace(asString(m["file_id"])) != "" {
		return "", "", fmt.Errorf("file_id images are not supported")
	}

	if raw, ok := m["image_url"]; ok {
		switch value := raw.(type) {
		case string:
			return validateImageURL(value)
		case map[string]any:
			url := asString(value["url"])
			if url == "" {
				return "", "", fmt.Errorf("image_url.url must be non-empty")
			}
			validURL, detail, err := validateImageURL(url)
			if err != nil {
				return "", "", err
			}
			if detail == "" {
				detail = asString(value["detail"])
			}
			return validURL, detail, nil
		default:
			return "", "", fmt.Errorf("image_url must be a string or object")
		}
	}

	if src, ok := m["source"].(map[string]any); ok {
		sourceType := strings.ToLower(strings.TrimSpace(asString(src["type"])))
		switch sourceType {
		case "url":
			return validateImageURL(asString(src["url"]))
		case "base64":
			mediaType := asString(src["media_type"])
			data := asString(src["data"])
			if !strings.HasPrefix(strings.ToLower(mediaType), "image/") || data == "" {
				return "", "", fmt.Errorf("base64 images require media_type and data")
			}
			if err := validateBase64(data); err != nil {
				return "", "", fmt.Errorf("invalid base64 image data: %w", err)
			}
			return "data:" + mediaType + ";base64," + data, "", nil
		default:
			return "", "", fmt.Errorf("image source must use url or base64")
		}
	}

	if rawURL := asString(m["url"]); rawURL != "" {
		return validateImageURL(rawURL)
	}
	return "", "", fmt.Errorf("image URL or source is required")
}

func validateImageURL(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", fmt.Errorf("image URL must be non-empty")
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		parts := strings.SplitN(value, ",", 2)
		if len(parts) != 2 || !strings.Contains(strings.ToLower(parts[0]), ";base64") {
			return "", "", fmt.Errorf("image data URL must contain base64 data")
		}
		if err := validateBase64(parts[1]); err != nil {
			return "", "", fmt.Errorf("invalid base64 image data: %w", err)
		}
	}
	return value, "", nil
}

func validateBase64(value string) error {
	if _, err := base64.StdEncoding.DecodeString(value); err == nil {
		return nil
	}
	_, err := base64.RawStdEncoding.DecodeString(value)
	return err
}

func convertResponsesTools(v any) any {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		out = append(out, convertOneResponsesTool(item, "")...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func convertOneResponsesTool(item any, prefix string) []any {
	m, _ := item.(map[string]any)
	if m == nil {
		return nil
	}
	if _, ok := m["function"]; ok {
		return []any{m}
	}
	typ := asString(m["type"])
	switch typ {
	case "namespace":
		childPrefix := asString(m["name"])
		if prefix != "" && childPrefix != "" {
			childPrefix = prefix + "__" + childPrefix
		}
		nested, _ := m["tools"].([]any)
		out := make([]any, 0, len(nested))
		for _, child := range nested {
			out = append(out, convertOneResponsesTool(child, childPrefix)...)
		}
		return out
	case "custom":
		if fn := customToolToFunction(m, prefix); fn != nil {
			return []any{fn}
		}
		return nil
	case "", "function":
		if fn := functionToolFromMap(m, prefix); fn != nil {
			return []any{fn}
		}
		return nil
	default:
		return nil
	}
}

func functionToolFromMap(m map[string]any, prefix string) map[string]any {
	name := asString(m["name"])
	if name == "" {
		return nil
	}
	if prefix != "" {
		name = prefix + "__" + name
	}
	fn := map[string]any{"name": name}
	if d, ok := m["description"]; ok {
		fn["description"] = d
	}
	if p, ok := m["parameters"]; ok {
		fn["parameters"] = p
	} else if p, ok := m["input_schema"]; ok {
		fn["parameters"] = p
	}
	return map[string]any{"type": "function", "function": fn}
}

func customToolToFunction(m map[string]any, prefix string) map[string]any {
	name := asString(m["name"])
	if name == "" {
		return nil
	}
	if prefix != "" {
		name = prefix + "__" + name
	}
	desc := asString(m["description"])
	if desc == "" {
		desc = "Custom tool " + name
	}
	if format, ok := m["format"].(map[string]any); ok {
		if def := asString(format["definition"]); def != "" {
			syntax := asString(format["syntax"])
			if syntax == "" {
				syntax = asString(format["type"])
			}
			extra := "\n\nUse this exact freeform input format"
			if syntax != "" {
				extra += " (" + syntax + ")"
			}
			extra += ":\n" + def
			desc += extra
		}
	}
	if isFreeformTool(name) && !strings.Contains(desc, "*** Begin Patch") {
		desc += "\n\n" + applyPatchJSONHint
	}
	paramName := "input"
	if name == "exec" || strings.HasSuffix(name, "__exec") {
		paramName = "cmd"
	}
	fn := map[string]any{
		"name":        name,
		"description": desc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				paramName: map[string]any{"type": "string"},
			},
			"required": []any{paramName},
		},
	}
	return map[string]any{"type": "function", "function": fn}
}

func convertResponsesToolChoice(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return s
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if _, ok := m["function"]; ok {
		return m
	}
	typ := asString(m["type"])
	switch typ {
	case "auto", "none", "required":
		return typ
	case "function":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": asString(m["name"])},
		}
	default:
		return v
	}
}

func anthropicToChat(raw []byte) (map[string]any, error) {
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	model, _ := src["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	chat := map[string]any{"model": model}
	copyChatFields(chat, src, "stream", "temperature", "top_p", "user", "metadata")
	if v, ok := src["max_tokens"]; ok {
		chat["max_tokens"] = v
	} else {
		chat["max_tokens"] = defaultMaxTokens()
	}
	if v, ok := src["stop_sequences"]; ok {
		chat["stop"] = v
	}

	messages := make([]any, 0, 4)
	if src["system"] != nil {
		sys, err := convertChatContent(src["system"])
		if err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
		if sys != nil && sys != "" {
			messages = append(messages, map[string]any{"role": "system", "content": sys})
		}
	}
	convertedMessages, err := convertAnthropicMessages(src["messages"])
	if err != nil {
		return nil, err
	}
	messages = append(messages, convertedMessages...)
	chat["messages"] = messages

	if tools := convertAnthropicTools(src["tools"]); tools != nil {
		chat["tools"] = tools
	}
	if tc := convertAnthropicToolChoice(src["tool_choice"]); tc != nil {
		chat["tool_choice"] = tc
	}
	if th, ok := src["thinking"].(map[string]any); ok {
		typ := asString(th["type"])
		if typ == "enabled" || typ == "adaptive" {
			chat["reasoningEffort"] = "medium"
			chat["reasoning_summary"] = "auto"
		}
	}
	return chat, nil
}

func convertAnthropicMessages(v any) ([]any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		m, _ := item.(map[string]any)
		if m == nil {
			return nil, fmt.Errorf("anthropic messages must contain objects")
		}
		role := asString(m["role"])
		if role == "" {
			role = "user"
		}
		if s, ok := m["content"].(string); ok {
			out = append(out, map[string]any{"role": role, "content": s})
			continue
		}
		parts, _ := m["content"].([]any)
		if len(parts) == 0 {
			content, err := convertChatContent(m["content"])
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"role": role, "content": content})
			continue
		}

		var contentParts []any
		var toolCalls []any
		hasImage := false
		var toolResults []any
		for i, part := range parts {
			pm, ok := part.(map[string]any)
			if !ok || pm == nil {
				return nil, fmt.Errorf("anthropic content block %d must be an object", i)
			}
			typ := strings.ToLower(strings.TrimSpace(asString(pm["type"])))
			switch typ {
			case "text", "input_text", "output_text", "summary_text", "image":
				content, err := convertChatContent(pm)
				if err != nil {
					return nil, err
				}
				if typ == "image" {
					hasImage = true
				}
				if list, ok := content.([]any); ok {
					contentParts = append(contentParts, list...)
				} else if content != "" {
					contentParts = append(contentParts, map[string]any{"type": "text", "text": content})
				}
			case "tool_use":
				toolCalls = append(toolCalls, map[string]any{
					"id":   asString(pm["id"]),
					"type": "function",
					"function": map[string]any{
						"name":      asString(pm["name"]),
						"arguments": mustJSON(pm["input"]),
					},
				})
			case "tool_result":
				content, err := convertChatContent(pm["content"])
				if err != nil {
					return nil, err
				}
				toolResults = append(toolResults, map[string]any{
					"role":         "tool",
					"tool_call_id": asString(firstNonNil(pm["tool_use_id"], pm["id"])),
					"content":      content,
				})
			default:
				return nil, fmt.Errorf("unsupported Anthropic content block type %q", typ)
			}
		}

		// Anthropic may mix tool_result blocks and normal user content in one
		// message. Keep every tool result adjacent to the preceding tool call,
		// then emit the remaining user content as a separate Chat message.
		out = append(out, toolResults...)
		if len(toolCalls) > 0 {
			assistant := map[string]any{
				"role":       "assistant",
				"tool_calls": toolCalls,
			}
			if len(contentParts) == 0 {
				assistant["content"] = nil
			} else if !hasImage {
				assistant["content"] = joinTextParts(contentParts)
			} else {
				assistant["content"] = contentParts
			}
			out = append(out, assistant)
			continue
		}
		if len(contentParts) == 0 {
			continue
		}
		var content any
		if !hasImage {
			content = joinTextParts(contentParts)
		} else {
			content = contentParts
		}
		if role == "tool" {
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": asString(m["tool_call_id"]),
				"content":      content,
			})
		} else {
			out = append(out, map[string]any{"role": role, "content": content})
		}
	}
	return out, nil
}

func convertAnthropicTools(v any) any {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if _, ok := m["function"]; ok {
			out = append(out, m)
			continue
		}
		name := asString(m["name"])
		if name == "" {
			continue
		}
		fn := map[string]any{"name": name}
		if d, ok := m["description"]; ok {
			fn["description"] = d
		}
		if p, ok := m["input_schema"]; ok {
			fn["parameters"] = p
		} else if p, ok := m["parameters"]; ok {
			fn["parameters"] = p
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func convertAnthropicToolChoice(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return s
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	switch asString(m["type"]) {
	case "auto":
		return "auto"
	case "none":
		return "none"
	case "any":
		return "required"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": asString(m["name"])},
		}
	default:
		return v
	}
}

func joinTextParts(parts []any) string {
	var b strings.Builder
	for _, part := range parts {
		m, _ := part.(map[string]any)
		if m == nil {
			continue
		}
		b.WriteString(asString(m["text"]))
	}
	return b.String()
}

func mustJSON(v any) string {
	if v == nil {
		return "{}"
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return "{}"
		}
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func defaultMaxTokens() int {
	n := global.CORE_CONFIG.CodeBuddy.DefaultMaxTokens
	if n <= 0 {
		return 32000
	}
	return n
}
