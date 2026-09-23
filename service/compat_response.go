package service

import (
	"encoding/json"
	"net/http"
	"strings"

	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
)

func encodeChatJSON(result *ChatResult) ([]byte, error) {
	if result == nil {
		result = &ChatResult{}
	}
	if result.Usage == nil {
		result.Usage = &parsedUsage{}
	}
	message := map[string]any{
		"role": "assistant",
	}
	if result.Content == "" && len(result.ToolCalls) > 0 {
		message["content"] = nil
	} else {
		message["content"] = result.Content
	}
	if global.CORE_CONFIG.Gateway.Passthrough && result.Reasoning != "" {
		message["reasoning_content"] = result.Reasoning
	}
	if len(result.ToolCalls) > 0 {
		calls := make([]any, 0, len(result.ToolCalls))
		for _, tc := range result.ToolCalls {
			typ := tc.Type
			if typ == "" {
				typ = "function"
			}
			calls = append(calls, map[string]any{
				"id":   tc.ID,
				"type": typ,
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		message["tool_calls"] = calls
	}
	usageObj := map[string]any{
		"prompt_tokens":     result.Usage.PromptTokens,
		"completion_tokens": result.Usage.CompletionTokens,
		"total_tokens":      result.Usage.TotalTokens,
	}
	attachChatCacheUsage(usageObj, result.Usage)
	if global.CORE_CONFIG.Gateway.Passthrough {
		usageObj["credit"] = result.Usage.Credit
	}
	finish := result.FinishReason
	if finish == "" {
		finish = "stop"
	}
	resp := map[string]any{
		"id":      result.ID,
		"object":  "chat.completion",
		"created": result.Created,
		"model":   result.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			},
		},
		"usage": usageObj,
	}
	return json.Marshal(resp)
}

func encodeResponsesCompactionJSON(result *ChatResult) ([]byte, error) {
	if result == nil {
		result = &ChatResult{}
	}
	if result.Usage == nil {
		result.Usage = &parsedUsage{}
	}
	summary := strings.TrimSpace(result.Content)
	if summary == "" {
		summary = strings.TrimSpace(result.Reasoning)
	}
	if summary == "" {
		summary = "No conversation summary was produced. Continue from the available context."
	}
	id := ensureID(result.ID, "resp_")
	rememberCompactState(id, summary)
	usage := map[string]any{
		"input_tokens":  result.Usage.PromptTokens,
		"output_tokens": result.Usage.CompletionTokens,
		"total_tokens":  result.Usage.TotalTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens":      result.Usage.CacheHitTokens,
			"cache_write_tokens": result.Usage.CacheWriteTokens,
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": result.Usage.ThinkingTokens,
		},
	}
	attachResponsesCacheUsage(usage, result.Usage)
	resp := map[string]any{
		"id":         id,
		"object":     "response.compaction",
		"created_at": result.Created,
		"model":      result.Model,
		"output": []any{
			map[string]any{
				"id":     newID("msg_"),
				"type":   "message",
				"status": "completed",
				"role":   "user",
				"content": []any{map[string]any{
					"type": "input_text",
					"text": summary,
				}},
			},
			map[string]any{
				"id":                newID("cmp_"),
				"type":              "compaction",
				"encrypted_content": encodeCompactEnvelope(summary),
			},
		},
		"usage": usage,
	}
	return json.Marshal(resp)
}

func encodeResponsesJSON(result *ChatResult) ([]byte, error) {
	if result == nil {
		result = &ChatResult{}
	}
	if result.Usage == nil {
		result.Usage = &parsedUsage{}
	}
	id := ensureID(result.ID, "resp_")
	output := make([]any, 0, 2+len(result.ToolCalls))
	// Reasoning is a standard output item in the Responses API, so keep it
	// visible even when passthrough is disabled.
	if result.Reasoning != "" {
		output = append(output, map[string]any{
			"id":   newID("rs_"),
			"type": "reasoning",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": result.Reasoning},
			},
		})
	}
	if result.Content != "" || (len(result.ToolCalls) == 0 && result.Reasoning == "") {
		output = append(output, map[string]any{
			"id":     newID("msg_"),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{
				map[string]any{"type": "output_text", "text": result.Content},
			},
		})
	}
	for _, tc := range result.ToolCalls {
		callID := tc.ID
		if callID == "" {
			callID = newID("call_")
		}
		if isFreeformTool(tc.Name) {
			output = append(output, map[string]any{
				"id":      newID("ctc_"),
				"type":    "custom_tool_call",
				"status":  "completed",
				"call_id": callID,
				"name":    tc.Name,
				"input":   unwrapFreeformArgs(tc.Arguments),
			})
			continue
		}
		output = append(output, map[string]any{
			"id":        newID("fc_"),
			"type":      "function_call",
			"status":    "completed",
			"call_id":   callID,
			"name":      tc.Name,
			"arguments": tc.Arguments,
		})
	}
	status := "completed"
	if result.FinishReason == "length" {
		status = "incomplete"
	}
	var incompleteDetails any
	if status == "incomplete" {
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	}
	resp := map[string]any{
		"id":                 id,
		"object":             "response",
		"created_at":         result.Created,
		"status":             status,
		"model":              result.Model,
		"output":             output,
		"error":              nil,
		"incomplete_details": incompleteDetails,
		"usage": func() map[string]any {
			usage := map[string]any{
				"input_tokens":  result.Usage.PromptTokens,
				"output_tokens": result.Usage.CompletionTokens,
				"total_tokens":  result.Usage.TotalTokens,
			}
			attachResponsesCacheUsage(usage, result.Usage)
			return usage
		}(),
	}
	return json.Marshal(resp)
}

func encodeAnthropicJSON(result *ChatResult) ([]byte, error) {
	if result == nil {
		result = &ChatResult{}
	}
	if result.Usage == nil {
		result.Usage = &parsedUsage{}
	}
	content := make([]any, 0, 2+len(result.ToolCalls))
	// Thinking is a standard content block in the Anthropic Messages API, so
	// keep it visible even when passthrough is disabled.
	if result.Reasoning != "" {
		content = append(content, map[string]any{
			"type":     "thinking",
			"thinking": result.Reasoning,
		})
	}
	if result.Content != "" || (len(result.ToolCalls) == 0 && result.Reasoning == "") {
		content = append(content, map[string]any{
			"type": "text",
			"text": result.Content,
		})
	}
	for _, tc := range result.ToolCalls {
		var input any = map[string]any{}
		if strings.TrimSpace(tc.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
				input = tc.Arguments
			}
		}
		id := tc.ID
		if id == "" {
			id = newID("toolu_")
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  tc.Name,
			"input": input,
		})
	}
	resp := map[string]any{
		"id":            ensureID(result.ID, "msg_"),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         result.Model,
		"stop_reason":   mapAnthropicStop(result.FinishReason),
		"stop_sequence": nil,
		"usage": func() map[string]any {
			usage := map[string]any{
				"input_tokens":  result.Usage.PromptTokens,
				"output_tokens": result.Usage.CompletionTokens,
			}
			attachAnthropicCacheUsage(usage, result.Usage)
			return usage
		}(),
	}
	return json.Marshal(resp)
}

func mapAnthropicStop(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func gatewayError(c *gin.Context, proto Protocol, status int, msg string) {
	gatewayErrorWithDetails(c, proto, status, msg, "codebuddy_gateway_error", "", "")
}

func gatewayErrorWithDetails(c *gin.Context, proto Protocol, status int, msg, code, param, requestID string) {
	if proto.IsAnthropic() {
		c.JSON(status, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    anthropicErrorType(status),
				"message": msg,
			},
		})
		return
	}
	openaiErrorWithDetails(c, status, msg, code, param, requestID)
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadRequest:
		return "invalid_request_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func estimateTokenCount(raw []byte) int {
	n := len(strings.TrimSpace(string(raw))) / 4
	if n < 1 {
		return 1
	}
	return n
}
