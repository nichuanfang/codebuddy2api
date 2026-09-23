package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Protocol string

const (
	ProtocolChat      Protocol = "chat"
	ProtocolResponses Protocol = "responses"
	ProtocolAnthropic Protocol = "anthropic"
)

type AggregatedToolCall struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

type ChatResult struct {
	ID           string
	Model        string
	Created      int64
	Content      string
	Reasoning    string
	FinishReason string
	ToolCalls    []AggregatedToolCall
	Usage        *parsedUsage
}

func (p Protocol) IsAnthropic() bool {
	return p == ProtocolAnthropic
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func ensureID(existing, prefix string) string {
	if strings.HasPrefix(existing, prefix) {
		return existing
	}
	if existing != "" {
		return prefix + existing
	}
	return newID(prefix)
}

func collectSSE(r io.Reader, requestedModel string) (*ChatResult, error) {
	return collectSSEWithStart(r, requestedModel, time.Now())
}

func collectSSEWithStart(r io.Reader, requestedModel string, streamStart time.Time) (*ChatResult, error) {
	result := &ChatResult{Created: time.Now().Unix(), Usage: &parsedUsage{}}
	tools := map[int]*AggregatedToolCall{}
	var content, reasoning strings.Builder
	var firstTokenMs int64
	normalTermination := false
	decoder := newSSEDecoder(r)

	for {
		event, err := decoder.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if event.Done {
			normalTermination = true
			break
		}
		chunk := event.Chunk
		if chunk == nil {
			continue
		}
		if firstTokenMs == 0 && chunkHasGeneratedToken(chunk) {
			firstTokenMs = time.Since(streamStart).Milliseconds()
		}
		if v, ok := chunk["id"].(string); ok && v != "" {
			result.ID = v
		}
		if v, ok := chunk["model"].(string); ok && v != "" {
			result.Model = v
		}
		if v, ok := chunk["created"].(float64); ok && v > 0 {
			result.Created = int64(v)
		}
		result.Usage = mergeUsage(result.Usage, extractUsage(chunk))
		if event.FinishReason != "" {
			result.FinishReason = event.FinishReason
			normalTermination = true
		}
		choices, _ := chunk["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			if choice == nil {
				continue
			}
			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				if msg, ok := choice["message"].(map[string]any); ok {
					delta = msg
				}
			}
			if delta == nil {
				continue
			}
			content.WriteString(deltaString(delta["content"]))
			reasoning.WriteString(deltaString(delta["reasoning_content"]))
			mergeToolCallDeltas(tools, delta["tool_calls"])
		}
	}
	if !normalTermination {
		return nil, errSSEMissingEnd
	}
	if requestedModel != "" {
		result.Model = requestedModel
	}
	result.Content = content.String()
	result.Reasoning = reasoning.String()
	result.ToolCalls = sortedToolCalls(tools)
	if result.FinishReason == "" {
		if len(result.ToolCalls) > 0 {
			result.FinishReason = "tool_calls"
		} else {
			result.FinishReason = "stop"
		}
	}
	if result.Usage == nil {
		result.Usage = &parsedUsage{}
	}
	result.Usage.FirstTokenMs = firstTokenMs
	if result.ID != "" {
		result.Usage.RequestID = result.ID
	}
	return result, nil
}

func parseChatSSELine(line string) (bool, map[string]any) {
	data := strings.TrimSpace(line)
	if !strings.HasPrefix(data, "data:") {
		return false, nil
	}
	data = strings.TrimSpace(strings.TrimPrefix(data, "data:"))
	if data == "[DONE]" {
		return true, nil
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, nil
	}
	return false, chunk
}

func mergeToolCallDeltas(dst map[int]*AggregatedToolCall, raw any) {
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		tc := parseToolCallDelta(item)
		cur := dst[tc.Index]
		if cur == nil {
			cur = &AggregatedToolCall{Index: tc.Index, Type: "function"}
			dst[tc.Index] = cur
		}
		if tc.ID != "" {
			cur.ID = tc.ID
		}
		if tc.Type != "" {
			cur.Type = tc.Type
		}
		if tc.Name != "" {
			cur.Name = tc.Name
		}
		if tc.Arguments != "" {
			cur.Arguments += tc.Arguments
		}
	}
}

func parseToolCallDelta(item any) AggregatedToolCall {
	tc, _ := item.(map[string]any)
	if tc == nil {
		return AggregatedToolCall{}
	}
	out := AggregatedToolCall{
		Index: asInt(tc["index"]),
		ID:    asString(tc["id"]),
		Type:  asString(tc["type"]),
	}
	if fn, ok := tc["function"].(map[string]any); ok {
		out.Name = asString(fn["name"])
		out.Arguments = asString(fn["arguments"])
	}
	if out.Name == "" {
		out.Name = asString(tc["name"])
	}
	if out.Arguments == "" {
		out.Arguments = asString(tc["arguments"])
	}
	if out.Type == "" {
		out.Type = "function"
	}
	return out
}

func sortedToolCalls(src map[int]*AggregatedToolCall) []AggregatedToolCall {
	if len(src) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(src))
	for idx := range src {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	out := make([]AggregatedToolCall, 0, len(idxs))
	for _, idx := range idxs {
		out = append(out, *src[idx])
	}
	return out
}

func deltaString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any, map[string]any:
		return extractTextContent(t)
	default:
		return ""
	}
}

func extractTextContent(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, item := range t {
			if s, ok := item.(string); ok {
				b.WriteString(s)
				continue
			}
			if m, ok := item.(map[string]any); ok {
				if tx := firstText(m); tx != "" {
					b.WriteString(tx)
				}
			}
		}
		return b.String()
	case map[string]any:
		if tx := firstText(t); tx != "" {
			return tx
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func firstText(m map[string]any) string {
	for _, key := range []string{"text", "output", "content", "input_text", "output_text"} {
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		// Do not trim trailing zeroes from the whole JSON token: "10"
		// would incorrectly become "1". FormatFloat preserves integral
		// values and emits the shortest round-trippable representation.
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func copyChatFields(dst, src map[string]any, keys ...string) {
	for _, key := range keys {
		if v, ok := src[key]; ok && v != nil {
			dst[key] = v
		}
	}
}
