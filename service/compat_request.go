package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"codebuddy-gateway/global"
)

func PrepareResponsesBody(raw []byte) (*ChatRequestMeta, error) {
	chat, toolRegistry, downgradedTools, downgradedItems, err := responsesToChatWithRegistry(raw)
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
	meta.ToolRegistry = toolRegistry
	meta.ResponsesDowngradedTools = downgradedTools
	meta.ResponsesDowngradedItems = downgradedItems
	return meta, nil
}

func PrepareResponsesCompactBody(raw []byte) (*ChatRequestMeta, error) {
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	if stream, ok := src["stream"].(bool); ok && stream {
		return nil, fmt.Errorf("responses compact does not support streaming")
	}
	model, _ := src["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if src["input"] == nil {
		if previousID := asString(src["previous_response_id"]); previousID != "" {
			if summary, ok := compactStateFor(previousID); ok {
				src["input"] = []any{summarySystemMessage(summary)}
				delete(src, "previous_response_id")
			} else {
				return nil, fmt.Errorf("previous_response_id %q is not available in this gateway instance", previousID)
			}
		}
	}
	instructions := strings.TrimSpace(asString(src["instructions"]))
	if instructions == "" {
		src["instructions"] = compactInstruction
	} else {
		src["instructions"] = instructions + "\n\n" + compactInstruction
	}
	src["stream"] = false
	src["max_output_tokens"] = 8192
	delete(src, "tools")
	delete(src, "tool_choice")
	delete(src, "parallel_tool_calls")
	encoded, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	chat, err := responsesToChat(encoded)
	if err != nil {
		return nil, err
	}
	encoded, err = json.Marshal(chat)
	if err != nil {
		return nil, err
	}
	meta, err := PrepareChatBody(encoded)
	if err != nil {
		return nil, err
	}
	meta.Protocol = ProtocolResponses
	meta.Compact = true
	meta.ClientStream = false
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
	chat, _, _, _, err := responsesToChatWithRegistry(raw)
	return chat, err
}

func responsesToChatWithRegistry(raw []byte) (map[string]any, *responseToolRegistry, []string, []string, error) {
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("invalid json body")
	}
	model, _ := src["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, nil, nil, nil, fmt.Errorf("model is required")
	}
	chat := map[string]any{"model": model}
	// Responses defaults to non-streaming. PrepareChatBody defaults generic
	// Chat requests to streaming, so make the Responses default explicit and
	// reject a malformed stream value instead of silently changing semantics.
	if rawStream, ok := src["stream"]; ok {
		stream, valid := rawStream.(bool)
		if !valid {
			return nil, nil, nil, nil, fmt.Errorf("stream must be a boolean")
		}
		chat["stream"] = stream
	} else {
		chat["stream"] = false
	}
	copyChatFields(chat, src,
		"temperature", "top_p", "user", "n", "stop", "metadata",
		"frequency_penalty", "presence_penalty", "seed", "service_tier",
		"logprobs", "top_logprobs", "stream_options",
	)
	if v, ok := src["max_output_tokens"]; ok {
		chat["max_tokens"] = normalizeResponsesMaxOutputTokens(v)
	} else if v, ok := src["max_completion_tokens"]; ok {
		chat["max_completion_tokens"] = v
	} else if v, ok := src["max_tokens"]; ok {
		chat["max_tokens"] = v
	}

	messages := make([]any, 0, 4)
	if previousID := asString(src["previous_response_id"]); previousID != "" {
		summary, ok := compactStateFor(previousID)
		if !ok {
			return nil, nil, nil, nil, fmt.Errorf("previous_response_id %q is not available in this gateway instance", previousID)
		}
		messages = append(messages, summarySystemMessage(summary))
	}
	if src["instructions"] != nil {
		inst, err := convertChatContent(src["instructions"])
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("instructions: %w", err)
		}
		if inst != nil && inst != "" {
			messages = append(messages, map[string]any{"role": "system", "content": inst})
		}
	}
	tools, toolRegistry, downgradedTools := convertResponsesToolsWithRegistry(src["tools"])
	convertedInput, downgradedItems, err := convertResponsesInputWithRegistryDiagnostics(src["input"], toolRegistry)
	if err != nil {
		return nil, nil, downgradedTools, downgradedItems, err
	}
	messages = append(messages, convertedInput...)
	messages = collapseSystemMessagesToHead(messages)
	if len(messages) == 0 {
		return nil, nil, downgradedTools, downgradedItems, fmt.Errorf("responses input must contain at least one message or tool result")
	}
	chat["messages"] = messages

	if tools != nil {
		chat["tools"] = tools
		if tc := convertResponsesToolChoice(src["tool_choice"], toolRegistry); tc != nil {
			chat["tool_choice"] = tc
		}
		if isNamedResponsesToolChoice(src["tool_choice"]) {
			appendUniqueString(&downgradedTools, "tool_choice")
		}
		if v, ok := src["parallel_tool_calls"]; ok {
			chat["parallel_tool_calls"] = v
		}
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
	return chat, toolRegistry, downgradedTools, downgradedItems, nil
}

func normalizeResponsesMaxOutputTokens(v any) any {
	switch n := v.(type) {
	case float64:
		if n > 0 && n < 16 {
			return float64(16)
		}
	case json.Number:
		if parsed, err := n.Int64(); err == nil && parsed > 0 && parsed < 16 {
			return int64(16)
		}
	case int:
		if n > 0 && n < 16 {
			return 16
		}
	}
	return v
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
	return convertResponsesInputWithRegistry(v, nil)
}

func convertResponsesInputWithRegistry(v any, registry *responseToolRegistry) ([]any, error) {
	messages, _, err := convertResponsesInputWithRegistryDiagnostics(v, registry)
	return messages, err
}

func convertResponsesInputWithRegistryDiagnostics(v any, registry *responseToolRegistry) ([]any, []string, error) {
	downgraded := make([]string, 0)
	if v == nil {
		return nil, downgraded, nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return nil, downgraded, nil
		}
		return []any{map[string]any{"role": "user", "content": s}}, downgraded, nil
	}
	arr, ok := v.([]any)
	if !ok {
		if m, ok := v.(map[string]any); ok {
			arr = []any{m}
		} else {
			return nil, downgraded, fmt.Errorf("responses input must be a string, object, or array")
		}
	}
	messages := make([]any, 0, len(arr))
	var pendingCalls []any
	var pendingReasoning []string
	flushCalls := func() {
		if len(pendingCalls) == 0 {
			return
		}
		if len(messages) > 0 {
			if previous, ok := messages[len(messages)-1].(map[string]any); ok &&
				asString(previous["role"]) == "assistant" && previous["tool_calls"] == nil {
				previous["tool_calls"] = pendingCalls
				attachReasoningContent(previous, pendingReasoning)
				pendingCalls = nil
				pendingReasoning = nil
				return
			}
		}
		message := map[string]any{"role": "assistant", "content": nil, "tool_calls": pendingCalls}
		attachReasoningContent(message, pendingReasoning)
		messages = append(messages, message)
		pendingCalls = nil
		pendingReasoning = nil
	}
	for _, item := range arr {
		if str, ok := item.(string); ok {
			flushCalls()
			messages = append(messages, map[string]any{"role": "user", "content": str})
			continue
		}
		m, _ := item.(map[string]any)
		if m == nil {
			return nil, downgraded, fmt.Errorf("responses input items must be objects")
		}
		typ := strings.ToLower(strings.TrimSpace(asString(m["type"])))
		role := asString(m["role"])
		switch typ {
		case "function_call", "custom_tool_call":
			pendingCalls = append(pendingCalls, responsesFunctionCallToToolCall(m, registry))
		case "function_call_output", "tool_result", "custom_tool_call_output":
			flushCalls()
			callID := asString(m["call_id"])
			if callID == "" {
				callID = asString(m["tool_use_id"])
			}
			if callID == "" {
				return nil, downgraded, fmt.Errorf("Responses tool output of type %q requires call_id", typ)
			}
			content, err := convertChatContent(firstNonNil(m["output"], m["content"]))
			if err != nil {
				return nil, downgraded, err
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": content})
		case "reasoning":
			if text := responsesReasoningText(m); text != "" {
				pendingReasoning = append(pendingReasoning, text)
			}
		case "compaction":
			flushCalls()
			summary, err := compactionSummaryFromItem(m)
			if err != nil {
				return nil, downgraded, err
			}
			if len(messages) > 0 {
				if previous, ok := messages[len(messages)-1].(map[string]any); ok &&
					asString(previous["role"]) == "user" && strings.TrimSpace(messageText(previous["content"])) == summary {
					messages = messages[:len(messages)-1]
				}
			}
			messages = append(messages, summarySystemMessage(summary))
		case "item_reference":
			// References require server-side state, which this bridge deliberately does not own.
			continue
		case "", "message":
			flushCalls()
		default:
			if isResponsesHostedOutputType(typ) {
				flushCalls()
				content, err := convertHostedHistoryOutput(firstNonNil(m["output"], m["content"], m["result"]))
				if err != nil {
					return nil, downgraded, err
				}
				if content == nil || content == "" {
					content = compactHostedHistoryItem(m)
				}
				messages = append(messages, map[string]any{"role": "user", "content": content})
				appendUniqueString(&downgraded, typ)
				continue
			}
			if isResponsesHostedCallType(typ) {
				appendUniqueString(&downgraded, typ)
				continue
			}
			return nil, downgraded, fmt.Errorf("unsupported Responses input item type %q", typ)
		}
		if typ == "" || typ == "message" {
			role = normalizeUpstreamRole(role)
			content, err := convertChatContent(m["content"])
			if err != nil {
				return nil, downgraded, err
			}
			if content == nil || content == "" {
				if text := asString(m["text"]); text != "" {
					content = text
				}
			}
			message := map[string]any{"role": role, "content": content}
			if role == "assistant" {
				attachReasoningContent(message, pendingReasoning)
			}
			pendingReasoning = nil
			messages = append(messages, message)
		}
	}
	flushCalls()
	if len(pendingReasoning) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			message, ok := messages[i].(map[string]any)
			if ok && asString(message["role"]) == "assistant" {
				attachReasoningContent(message, pendingReasoning)
				pendingReasoning = nil
				break
			}
		}
	}
	return messages, downgraded, nil
}

func isResponsesHostedCallType(typ string) bool {
	switch typ {
	case "web_search_call", "file_search_call", "computer_call", "tool_search_call":
		return true
	default:
		return false
	}
}

func isResponsesHostedOutputType(typ string) bool {
	switch typ {
	case "web_search_call_output", "file_search_call_output", "computer_call_output", "tool_search_call_output":
		return true
	default:
		return false
	}
}

func convertHostedHistoryOutput(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if m, ok := v.(map[string]any); ok {
		typ := strings.ToLower(strings.TrimSpace(asString(m["type"])))
		if typ == "computer_screenshot" {
			copy := cloneToolMap(m)
			copy["type"] = "input_image"
			return convertChatContent(copy)
		}
		if typ == "input_image" || typ == "image_url" {
			return convertChatContent(m)
		}
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	if content, err := convertChatContent(v); err == nil {
		return content, nil
	}
	return mustJSON(v), nil
}

func compactHostedHistoryItem(m map[string]any) string {
	if m == nil {
		return "[hosted tool output unavailable]"
	}
	copyItem := cloneToolMap(m)
	delete(copyItem, "id")
	delete(copyItem, "call_id")
	delete(copyItem, "status")
	encoded, err := json.Marshal(copyItem)
	if err != nil {
		return "[hosted tool output unavailable]"
	}
	return "[hosted tool output] " + string(encoded)
}

func responsesReasoningText(item map[string]any) string {
	var parts []string
	appendText := func(v any) {
		if text := strings.TrimSpace(asString(v)); text != "" {
			parts = append(parts, text)
		}
	}
	if summary, ok := item["summary"].([]any); ok {
		for _, raw := range summary {
			if part, ok := raw.(map[string]any); ok {
				appendText(part["text"])
			}
		}
	}
	if content, ok := item["content"].([]any); ok {
		for _, raw := range content {
			if part, ok := raw.(map[string]any); ok {
				appendText(part["text"])
			}
		}
	}
	appendText(item["text"])
	return strings.Join(parts, "\n\n")
}

func attachReasoningContent(message map[string]any, parts []string) {
	if len(parts) == 0 {
		return
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if text == "" {
		return
	}
	if existing := strings.TrimSpace(asString(message["reasoning_content"])); existing != "" {
		text = existing + "\n\n" + text
	}
	message["reasoning_content"] = text
}

func collapseSystemMessagesToHead(messages []any) []any {
	if len(messages) < 2 {
		return messages
	}
	var system []string
	rest := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || asString(message["role"]) != "system" {
			rest = append(rest, raw)
			continue
		}
		if text := strings.TrimSpace(messageText(message["content"])); text != "" {
			system = append(system, text)
		}
	}
	if len(system) == 0 {
		return rest
	}
	out := make([]any, 0, len(rest)+1)
	out = append(out, map[string]any{"role": "system", "content": strings.Join(system, "\n\n")})
	return append(out, rest...)
}

func responsesFunctionCallToToolCall(m map[string]any, registry *responseToolRegistry) map[string]any {
	id := asString(m["call_id"])
	if id == "" {
		id = asString(m["id"])
	}
	name := asString(m["name"])
	namespace := asString(m["namespace"])
	var binding *responseToolBinding
	if registry != nil {
		name, binding = registry.UpstreamNameForClientCall(namespace, name)
	}
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
	if binding != nil && binding.Kind == responseToolCustom {
		args = ensureCustomJSONArgs(args, binding.InputField)
	} else if isFreeformTool(name) {
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
			if typ == "output_text" {
				t = appendResponsesAnnotations(t, m["annotations"])
			}
			text.WriteString(t)
			parts = append(parts, map[string]any{"type": "text", "text": t})
		case "input_image", "image_url", "image":
			image, err := chatImagePart(m, typ)
			if err != nil {
				return nil, fmt.Errorf("content block %d: %w", i, err)
			}
			onlyText = false
			parts = append(parts, image)
		case "input_file", "file":
			return nil, fmt.Errorf("content block type %q is not supported by CodeBuddy Chat upstream", typ)
		default:
			return nil, fmt.Errorf("unsupported content block type %q", typ)
		}
	}
	if onlyText {
		return text.String(), nil
	}
	return parts, nil
}

func appendResponsesAnnotations(text string, raw any) string {
	annotations, _ := raw.([]any)
	if len(annotations) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	seen := make(map[string]struct{})
	for _, item := range annotations {
		m, _ := item.(map[string]any)
		if m == nil || asString(m["type"]) != "url_citation" {
			continue
		}
		url := strings.TrimSpace(asString(m["url"]))
		lowerURL := strings.ToLower(url)
		if url == "" || (!strings.HasPrefix(lowerURL, "http://") && !strings.HasPrefix(lowerURL, "https://")) {
			continue
		}
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		title := strings.TrimSpace(asString(m["title"]))
		if title == "" {
			title = url
		}
		b.WriteString("\n[Source: ")
		b.WriteString(title)
		b.WriteString("](")
		b.WriteString(url)
		b.WriteString(")")
	}
	return b.String()
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

func convertResponsesToolsWithRegistry(v any) (any, *responseToolRegistry, []string) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, nil, nil
	}
	candidates := make([]responseToolCandidate, 0, len(arr))
	downgraded := make([]string, 0)
	for _, item := range arr {
		collectResponseToolCandidates(item, "", "", &candidates, &downgraded)
	}
	if len(candidates) == 0 {
		return nil, nil, downgraded
	}
	registry := newResponseToolRegistry(candidates)
	out := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		binding := registry.bindingForCandidate(candidate)
		if binding == nil {
			continue
		}
		var tool map[string]any
		if candidate.Kind == responseToolCustom {
			tool = customToolToFunction(candidate.Raw, "")
		} else if _, ok := candidate.Raw["function"]; ok {
			tool = cloneToolMap(candidate.Raw)
		} else {
			tool = functionToolFromMap(candidate.Raw, "")
		}
		if tool == nil {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		fn["name"] = binding.UpstreamName
		out = append(out, tool)
	}
	if len(out) == 0 {
		return nil, nil, downgraded
	}
	return out, registry, downgraded
}

type responseToolCandidate struct {
	Raw           map[string]any
	ClientName    string
	QualifiedName string
	Kind          string
	InputField    string
	// Namespace is the codex-facing namespace for MCP/namespace tools,
	// e.g. "mcp__context7". Empty for plain functions and custom tools.
	Namespace string
}

const (
	responseToolFunction = "function"
	responseToolCustom   = "custom"
)

func collectResponseToolCandidates(item any, prefix, namespace string, out *[]responseToolCandidate, downgraded *[]string) {
	m, _ := item.(map[string]any)
	if m == nil {
		return
	}
	if fn, ok := m["function"].(map[string]any); ok {
		name := asString(fn["name"])
		if name != "" {
			qualified := joinToolName(prefix, name)
			candidateNamespace := namespace
			if candidateNamespace == "" {
				candidateNamespace = normalizeMCPNamespace(asString(m["namespace"]))
			}
			*out = append(*out, responseToolCandidate{Raw: m, ClientName: name, QualifiedName: qualified, Kind: responseToolFunction, Namespace: candidateNamespace})
		}
		return
	}
	typ := asString(m["type"])
	switch typ {
	case "namespace":
		name := asString(m["name"])
		childPrefix := joinToolName(prefix, name)
		namespace := mcpNamespaceFor(prefix, name)
		nested, _ := m["tools"].([]any)
		for _, child := range nested {
			collectResponseToolCandidates(child, childPrefix, namespace, out, downgraded)
		}
	case "custom", "function", "":
		name := asString(m["name"])
		if name == "" {
			return
		}
		kind := responseToolFunction
		inputField := ""
		if typ == "custom" {
			kind = responseToolCustom
			inputField = customToolInputField(name)
		}
		candidateNamespace := namespace
		if candidateNamespace == "" {
			candidateNamespace = normalizeMCPNamespace(asString(m["namespace"]))
		}
		*out = append(*out, responseToolCandidate{
			Raw: m, ClientName: name, QualifiedName: joinToolName(prefix, name),
			Kind: kind, InputField: inputField, Namespace: candidateNamespace,
		})
	default:
		// Responses hosted tools are not executable by the CodeBuddy Chat upstream.
		if typ != "" {
			appendUniqueString(downgraded, typ)
		}
		return
	}
}

// mcpNamespaceFor builds the Codex-facing namespace for a namespace tool.
// Codex registers MCP tools as `mcp__<server>__<tool>`; nested namespace
// groups are joined with "__" so the runtime can still resolve them.
func mcpNamespaceFor(prefix, name string) string {
	return normalizeMCPNamespace(joinToolName(prefix, name))
}

func normalizeMCPNamespace(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(name), "mcp__") {
		return name
	}
	return "mcp__" + name
}

func appendUniqueString(items *[]string, value string) {
	if items == nil || strings.TrimSpace(value) == "" {
		return
	}
	value = strings.TrimSpace(value)
	for _, existing := range *items {
		if existing == value {
			return
		}
	}
	*items = append(*items, value)
}

func joinToolName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	if name == "" {
		return prefix
	}
	return prefix + "__" + name
}

func cloneToolMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func customToolInputField(name string) string {
	if strings.EqualFold(name, "exec") || strings.HasSuffix(strings.ToLower(name), "__exec") {
		return "cmd"
	}
	return "input"
}

func ensureCustomJSONArgs(args, field string) string {
	if field == "" {
		field = "input"
	}
	s := strings.TrimSpace(args)
	if s == "" {
		return mustJSON(map[string]string{field: ""})
	}
	var obj map[string]any
	if json.Unmarshal([]byte(s), &obj) == nil {
		for _, candidate := range customInputFields(field) {
			if value, ok := obj[candidate]; ok {
				if candidate == field {
					return s
				}
				return mustJSON(map[string]any{field: value})
			}
		}
	}
	return mustJSON(map[string]string{field: args})
}

func customInputFields(field string) []string {
	if field == "cmd" {
		return []string{"cmd", "input", "command"}
	}
	return []string{field}
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
	if strict, ok := m["strict"]; ok {
		fn["strict"] = strict
	}
	parameters := m["parameters"]
	if parameters == nil {
		parameters = m["input_schema"]
	}
	fn["parameters"] = normalizeFunctionParameters(parameters)
	return map[string]any{"type": "function", "function": fn}
}

func normalizeFunctionParameters(v any) map[string]any {
	parameters, ok := v.(map[string]any)
	if !ok {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if typ, ok := parameters["type"].(string); !ok || strings.TrimSpace(typ) == "" {
		parameters["type"] = "object"
	}
	return parameters
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

func isNamedResponsesToolChoice(v any) bool {
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "auto", "none", "required", "":
			return false
		default:
			return true
		}
	}
	m, _ := v.(map[string]any)
	if m == nil {
		return false
	}
	if _, ok := m["function"].(map[string]any); ok {
		return true
	}
	return strings.EqualFold(asString(m["type"]), "function")
}

func convertResponsesToolChoice(v any, registry *responseToolRegistry) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "auto", "none", "required":
			return s
		}
		if registry == nil {
			return nil
		}
		_, binding := registry.UpstreamNameForClientCall("", s)
		if binding == nil {
			return nil
		}
		// CodeBuddy's chat request schema currently accepts only the string
		// forms auto/none/required, not OpenAI's named-function object form.
		return "required"
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if fn, ok := m["function"].(map[string]any); ok {
		copyChoice := cloneToolMap(m)
		copyFn := cloneToolMap(fn)
		if registry != nil {
			namespace := asString(copyChoice["namespace"])
			if namespace == "" {
				namespace = asString(copyFn["namespace"])
			}
			_, binding := registry.UpstreamNameForClientCall(namespace, asString(copyFn["name"]))
			if binding == nil {
				return nil
			}
			// The upstream schema cannot select a named function; require a
			// tool call and let the model choose among the registered tools.
			return "required"
		}
		copyChoice["function"] = copyFn
		return copyChoice
	}
	typ := asString(m["type"])
	switch typ {
	case "auto", "none", "required":
		return typ
	case "function":
		name := asString(m["name"])
		if registry != nil {
			_, binding := registry.UpstreamNameForClientCall(asString(m["namespace"]), name)
			if binding == nil {
				return nil
			}
			return "required"
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": name},
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
