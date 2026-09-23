package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
)

type streamAdapter interface {
	start()
	setID(id string)
	setModel(model string)
	setUsage(u *parsedUsage)
	onReasoning(s string)
	onText(s string)
	onToolCall(tc AggregatedToolCall)
	onFinishReason(s string)
	finish() error
	fail(error) error
}

type toolStreamState struct {
	index       int
	outputIndex int
	itemID      string
	callID      string
	name        string
	args        string
	opened      bool
	closed      bool
}

func newStreamAdapter(proto Protocol, w io.Writer, flusher http.Flusher, model string) streamAdapter {
	if proto.IsAnthropic() {
		return &anthropicAdapter{
			w:       w,
			flusher: flusher,
			id:      newID("msg_"),
			model:   model,
			created: time.Now().Unix(),
			tools:   map[int]*toolStreamState{},
		}
	}
	return &responsesAdapter{
		w:       w,
		flusher: flusher,
		id:      newID("resp_"),
		model:   model,
		created: time.Now().Unix(),
		tools:   map[int]*toolStreamState{},
	}
}

func writeSSEEvent(w io.Writer, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if event != "" {
		buf.WriteString("event: ")
		buf.WriteString(event)
		buf.WriteByte('\n')
	}
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func applyChunkToAdapter(em streamAdapter, chunk map[string]any) {
	if em == nil || chunk == nil {
		return
	}
	if id, _ := chunk["id"].(string); id != "" {
		em.setID(id)
	}
	if model, _ := chunk["model"].(string); model != "" {
		em.setModel(model)
	}
	choices, _ := chunk["choices"].([]any)
	for _, item := range choices {
		choice, _ := item.(map[string]any)
		if choice == nil {
			continue
		}
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			em.onFinishReason(fr)
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
		if s := deltaString(delta["reasoning_content"]); s != "" && global.CORE_CONFIG.Gateway.Passthrough {
			em.onReasoning(s)
		}
		if s := deltaString(delta["content"]); s != "" {
			em.onText(s)
		}
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				em.onToolCall(parseToolCallDelta(tc))
			}
		}
	}
}

type responseSegment struct {
	index int
	id    string
	text  string
}

type responsesAdapter struct {
	w                 io.Writer
	flusher           http.Flusher
	err               error
	id                string
	model             string
	created           int64
	seq               int
	out               int
	reasoningOpen     bool
	reasoningIdx      int
	reasoningItemID   string
	reasoning         bytes.Buffer
	reasoningSegments []responseSegment
	textOpen          bool
	textIdx           int
	textItemID        string
	text              bytes.Buffer
	textSegments      []responseSegment
	toolSegments      []*toolStreamState
	tools             map[int]*toolStreamState
	toolOrder         []int
	usage             *parsedUsage
	finishReason      string
}

func (a *responsesAdapter) emit(typ string, payload map[string]any) {
	if a.err != nil {
		return
	}
	a.seq++
	payload["type"] = typ
	payload["sequence_number"] = a.seq
	a.err = writeSSEEvent(a.w, a.flusher, typ, payload)
}

func (a *responsesAdapter) start() {
	a.emit("response.created", map[string]any{"response": a.skeleton("in_progress")})
	a.emit("response.in_progress", map[string]any{"response": a.skeleton("in_progress")})
}

func (a *responsesAdapter) setID(id string) {
	if a.id == "" && id != "" {
		a.id = ensureID(id, "resp_")
	}
}

func (a *responsesAdapter) setModel(model string) {
	if model != "" {
		a.model = model
	}
}

func (a *responsesAdapter) setUsage(u *parsedUsage) {
	a.usage = u
}

func (a *responsesAdapter) onFinishReason(s string) {
	if s != "" {
		a.finishReason = s
	}
}

func (a *responsesAdapter) onReasoning(s string) {
	if s == "" {
		return
	}
	if !a.reasoningOpen {
		a.reasoning.Reset()
		a.reasoningItemID = newID("rs_")
		a.reasoningIdx = a.out
		a.out++
		a.emit("response.output_item.added", map[string]any{
			"output_index": a.reasoningIdx,
			"item": map[string]any{
				"id":      a.reasoningItemID,
				"type":    "reasoning",
				"summary": []any{},
			},
		})
		a.emit("response.reasoning_summary_part.added", map[string]any{
			"item_id":       a.reasoningItemID,
			"output_index":  a.reasoningIdx,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": ""},
		})
		a.reasoningOpen = true
	}
	a.reasoning.WriteString(s)
	a.emit("response.reasoning_summary_text.delta", map[string]any{
		"item_id":       a.reasoningItemID,
		"output_index":  a.reasoningIdx,
		"summary_index": 0,
		"delta":         s,
	})
}

func (a *responsesAdapter) closeReasoning() {
	if !a.reasoningOpen {
		return
	}
	text := a.reasoning.String()
	a.emit("response.reasoning_summary_text.done", map[string]any{
		"item_id":       a.reasoningItemID,
		"output_index":  a.reasoningIdx,
		"summary_index": 0,
		"text":          text,
	})
	a.emit("response.reasoning_summary_part.done", map[string]any{
		"item_id":       a.reasoningItemID,
		"output_index":  a.reasoningIdx,
		"summary_index": 0,
		"part":          map[string]any{"type": "summary_text", "text": text},
	})
	a.emit("response.output_item.done", map[string]any{
		"output_index": a.reasoningIdx,
		"item": map[string]any{
			"id":   a.reasoningItemID,
			"type": "reasoning",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": text},
			},
		},
	})
	a.reasoningSegments = append(a.reasoningSegments, responseSegment{index: a.reasoningIdx, id: a.reasoningItemID, text: text})
	a.reasoningOpen = false
}

func (a *responsesAdapter) onText(s string) {
	if s == "" {
		return
	}
	a.closeReasoning()
	a.ensureTextItem()
	a.text.WriteString(s)
	a.emit("response.output_text.delta", map[string]any{
		"item_id":       a.textItemID,
		"output_index":  a.textIdx,
		"content_index": 0,
		"delta":         s,
	})
}

func (a *responsesAdapter) closeText() {
	if !a.textOpen {
		return
	}
	text := a.text.String()
	a.emit("response.output_text.done", map[string]any{
		"item_id":       a.textItemID,
		"output_index":  a.textIdx,
		"content_index": 0,
		"text":          text,
	})
	a.emit("response.content_part.done", map[string]any{
		"item_id":       a.textItemID,
		"output_index":  a.textIdx,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": text},
	})
	a.emit("response.output_item.done", map[string]any{
		"output_index": a.textIdx,
		"item": map[string]any{
			"id":     a.textItemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{
				map[string]any{"type": "output_text", "text": text},
			},
		},
	})
	a.textSegments = append(a.textSegments, responseSegment{index: a.textIdx, id: a.textItemID, text: text})
	a.textOpen = false
}

func (a *responsesAdapter) onToolCall(tc AggregatedToolCall) {
	a.closeReasoning()
	a.closeText()
	st, ok := a.tools[tc.Index]
	if !ok {
		st = &toolStreamState{index: tc.Index, outputIndex: a.out}
		a.out++
		a.tools[tc.Index] = st
		a.toolOrder = append(a.toolOrder, tc.Index)
	}
	if tc.ID != "" {
		st.callID = tc.ID
	}
	if tc.Name != "" {
		st.name = tc.Name
	}
	if tc.Arguments != "" {
		st.args += tc.Arguments
	}
	if isFreeformTool(st.name) || st.name == "" {
		return
	}
	if !st.opened {
		a.openFunctionCall(st)
		if st.args != "" {
			a.emit("response.function_call_arguments.delta", map[string]any{
				"item_id":      st.itemID,
				"output_index": st.outputIndex,
				"delta":        st.args,
			})
		}
		return
	}
	if tc.Arguments != "" {
		a.emit("response.function_call_arguments.delta", map[string]any{
			"item_id":      st.itemID,
			"output_index": st.outputIndex,
			"delta":        tc.Arguments,
		})
	}
}

func (a *responsesAdapter) openFunctionCall(st *toolStreamState) {
	if st.opened {
		return
	}
	if st.callID == "" {
		st.callID = newID("call_")
	}
	st.itemID = newID("fc_")
	a.emit("response.output_item.added", map[string]any{
		"output_index": st.outputIndex,
		"item": map[string]any{
			"id":        st.itemID,
			"type":      "function_call",
			"status":    "in_progress",
			"call_id":   st.callID,
			"name":      st.name,
			"arguments": "",
		},
	})
	st.opened = true
}

func (a *responsesAdapter) emitCustomToolCall(st *toolStreamState) {
	if st.callID == "" {
		st.callID = newID("call_")
	}
	if st.itemID == "" {
		st.itemID = newID("ctc_")
	}
	input := unwrapFreeformArgs(st.args)
	st.args = input
	a.emit("response.output_item.added", map[string]any{
		"output_index": st.outputIndex,
		"item": map[string]any{
			"id":      st.itemID,
			"type":    "custom_tool_call",
			"status":  "in_progress",
			"call_id": st.callID,
			"name":    st.name,
			"input":   "",
		},
	})
	if input != "" {
		a.emit("response.custom_tool_call_input.delta", map[string]any{
			"item_id":      st.itemID,
			"output_index": st.outputIndex,
			"delta":        input,
		})
	}
	a.emit("response.custom_tool_call_input.done", map[string]any{
		"item_id":      st.itemID,
		"output_index": st.outputIndex,
		"input":        input,
	})
	a.emit("response.output_item.done", map[string]any{
		"output_index": st.outputIndex,
		"item": map[string]any{
			"id":      st.itemID,
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": st.callID,
			"name":    st.name,
			"input":   input,
		},
	})
	a.toolSegments = append(a.toolSegments, cloneToolState(st))
	st.closed = true
}

func (a *responsesAdapter) closeTools() {
	for _, idx := range a.toolOrder {
		st := a.tools[idx]
		if st == nil || st.closed {
			continue
		}
		if isFreeformTool(st.name) {
			a.emitCustomToolCall(st)
			continue
		}
		if !st.opened {
			if st.callID == "" && st.name == "" && st.args == "" {
				continue
			}
			a.openFunctionCall(st)
			if st.args != "" {
				a.emit("response.function_call_arguments.delta", map[string]any{
					"item_id":      st.itemID,
					"output_index": st.outputIndex,
					"delta":        st.args,
				})
			}
		}
		a.emit("response.function_call_arguments.done", map[string]any{
			"item_id":      st.itemID,
			"output_index": st.outputIndex,
			"arguments":    st.args,
		})
		a.emit("response.output_item.done", map[string]any{
			"output_index": st.outputIndex,
			"item": map[string]any{
				"id":        st.itemID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   st.callID,
				"name":      st.name,
				"arguments": st.args,
			},
		})
		st.opened = false
		st.closed = true
		a.toolSegments = append(a.toolSegments, cloneToolState(st))
	}
}

func (a *responsesAdapter) finish() error {
	a.closeReasoning()
	if a.textOpen {
		a.closeText()
	} else if len(a.toolOrder) == 0 && a.reasoning.Len() == 0 {
		a.ensureTextItem()
		a.closeText()
	}
	a.closeTools()
	status := "completed"
	eventType := "response.completed"
	if a.finishReason == "length" {
		status = "incomplete"
		eventType = "response.incomplete"
	}
	a.emit(eventType, map[string]any{"response": a.full(status)})
	return a.err
}

func (a *responsesAdapter) ensureTextItem() {
	if a.textOpen {
		return
	}
	a.text.Reset()
	a.textItemID = newID("msg_")
	a.textIdx = a.out
	a.out++
	a.emit("response.output_item.added", map[string]any{
		"output_index": a.textIdx,
		"item": map[string]any{
			"id":      a.textItemID,
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
	a.emit("response.content_part.added", map[string]any{
		"item_id":       a.textItemID,
		"output_index":  a.textIdx,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": ""},
	})
	a.textOpen = true
}

func (a *responsesAdapter) skeleton(status string) map[string]any {
	return map[string]any{
		"id":                 a.id,
		"object":             "response",
		"created_at":         a.created,
		"status":             status,
		"model":              a.model,
		"output":             []any{},
		"error":              nil,
		"incomplete_details": nil,
	}
}

func cloneToolState(st *toolStreamState) *toolStreamState {
	if st == nil {
		return nil
	}
	cp := *st
	return &cp
}

func (a *responsesAdapter) full(status string) map[string]any {
	type outputEntry struct {
		index int
		item  map[string]any
	}
	entries := make([]outputEntry, 0, len(a.reasoningSegments)+len(a.textSegments)+len(a.toolSegments))
	for _, segment := range a.reasoningSegments {
		entries = append(entries, outputEntry{index: segment.index, item: map[string]any{
			"id": segment.id, "type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": segment.text},
			},
		}})
	}
	for _, segment := range a.textSegments {
		entries = append(entries, outputEntry{index: segment.index, item: map[string]any{
			"id": segment.id, "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": segment.text}},
		}})
	}
	for _, st := range a.toolSegments {
		if st == nil {
			continue
		}
		if isFreeformTool(st.name) {
			entries = append(entries, outputEntry{index: st.outputIndex, item: map[string]any{
				"id": st.itemID, "type": "custom_tool_call", "status": "completed", "call_id": st.callID,
				"name": st.name, "input": unwrapFreeformArgs(st.args),
			}})
		} else {
			entries = append(entries, outputEntry{index: st.outputIndex, item: map[string]any{
				"id": st.itemID, "type": "function_call", "status": "completed", "call_id": st.callID,
				"name": st.name, "arguments": st.args,
			}})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].index < entries[j].index })
	output := make([]any, 0, len(entries))
	for _, entry := range entries {
		output = append(output, entry.item)
	}
	if len(output) == 0 {
		output = append(output, map[string]any{
			"id": newID("msg_"), "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": ""}},
		})
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	if a.usage != nil {
		usage["input_tokens"] = a.usage.PromptTokens
		usage["output_tokens"] = a.usage.CompletionTokens
		usage["total_tokens"] = a.usage.TotalTokens
	}
	attachResponsesCacheUsage(usage, a.usage)
	var incomplete any
	if status == "incomplete" {
		incomplete = map[string]any{"reason": "max_output_tokens"}
	}
	return map[string]any{
		"id": a.id, "object": "response", "created_at": a.created, "status": status,
		"model": a.model, "output": output, "usage": usage, "error": nil,
		"incomplete_details": incomplete,
	}
}

func (a *responsesAdapter) fail(err error) error {
	a.closeReasoning()
	a.closeText()
	a.closeTools()
	response := a.full("failed")
	response["error"] = map[string]any{"code": "upstream_stream_error", "message": err.Error()}
	a.emit("response.failed", map[string]any{"response": response})
	return a.err
}

type anthropicAdapter struct {
	w            io.Writer
	flusher      http.Flusher
	err          error
	id           string
	model        string
	created      int64
	block        int
	thinkingOpen bool
	thinkingIdx  int
	thinking     bytes.Buffer
	textOpen     bool
	textIdx      int
	text         bytes.Buffer
	tools        map[int]*toolStreamState
	toolOrder    []int
	usage        *parsedUsage
	finishReason string
}

func (a *anthropicAdapter) emit(typ string, payload map[string]any) {
	if a.err != nil {
		return
	}
	if _, ok := payload["type"]; !ok {
		payload["type"] = typ
	}
	a.err = writeSSEEvent(a.w, a.flusher, typ, payload)
}

func (a *anthropicAdapter) start() {
	input := 0
	if a.usage != nil {
		input = a.usage.PromptTokens
	}
	usage := map[string]any{
		"input_tokens":  input,
		"output_tokens": 0,
	}
	attachAnthropicCacheUsage(usage, a.usage)
	a.emit("message_start", map[string]any{
		"message": map[string]any{
			"id":            a.id,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         a.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         usage,
		},
	})
}

func (a *anthropicAdapter) setID(id string) {
	if a.id == "" && id != "" {
		a.id = ensureID(id, "msg_")
	}
}

func (a *anthropicAdapter) setModel(model string) {
	if model != "" {
		a.model = model
	}
}

func (a *anthropicAdapter) setUsage(u *parsedUsage) {
	a.usage = u
}

func (a *anthropicAdapter) onFinishReason(s string) {
	if s != "" {
		a.finishReason = s
	}
}

func (a *anthropicAdapter) onReasoning(s string) {
	if s == "" {
		return
	}
	if !a.thinkingOpen {
		a.thinkingIdx = a.block
		a.block++
		a.emit("content_block_start", map[string]any{
			"index": a.thinkingIdx,
			"content_block": map[string]any{
				"type":     "thinking",
				"thinking": "",
			},
		})
		a.thinkingOpen = true
	}
	a.thinking.WriteString(s)
	a.emit("content_block_delta", map[string]any{
		"index": a.thinkingIdx,
		"delta": map[string]any{"type": "thinking_delta", "thinking": s},
	})
}

func (a *anthropicAdapter) closeThinking() {
	if !a.thinkingOpen {
		return
	}
	a.emit("content_block_stop", map[string]any{"index": a.thinkingIdx})
	a.thinkingOpen = false
}

func (a *anthropicAdapter) onText(s string) {
	if s == "" {
		return
	}
	a.closeThinking()
	if !a.textOpen {
		a.textIdx = a.block
		a.block++
		a.emit("content_block_start", map[string]any{
			"index": a.textIdx,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
		a.textOpen = true
	}
	a.text.WriteString(s)
	a.emit("content_block_delta", map[string]any{
		"index": a.textIdx,
		"delta": map[string]any{"type": "text_delta", "text": s},
	})
}

func (a *anthropicAdapter) closeText() {
	if !a.textOpen {
		return
	}
	a.emit("content_block_stop", map[string]any{"index": a.textIdx})
	a.textOpen = false
}

func (a *anthropicAdapter) onToolCall(tc AggregatedToolCall) {
	a.closeThinking()
	a.closeText()
	st, ok := a.tools[tc.Index]
	if !ok {
		st = &toolStreamState{index: tc.Index, outputIndex: a.block}
		a.block++
		a.tools[tc.Index] = st
		a.toolOrder = append(a.toolOrder, tc.Index)
	}
	if tc.ID != "" {
		st.callID = tc.ID
	}
	if tc.Name != "" {
		st.name = tc.Name
	}
	if !st.opened {
		if st.callID == "" {
			st.callID = newID("toolu_")
		}
		a.emit("content_block_start", map[string]any{
			"index": st.outputIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    st.callID,
				"name":  st.name,
				"input": map[string]any{},
			},
		})
		st.opened = true
	}
	if tc.Arguments != "" {
		st.args += tc.Arguments
		a.emit("content_block_delta", map[string]any{
			"index": st.outputIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Arguments},
		})
	}
}

func (a *anthropicAdapter) closeTools() {
	for _, idx := range a.toolOrder {
		st := a.tools[idx]
		if st == nil || !st.opened {
			continue
		}
		a.emit("content_block_stop", map[string]any{"index": st.outputIndex})
		st.opened = false
	}
}

func (a *anthropicAdapter) finish() error {
	a.closeThinking()
	if a.textOpen {
		a.closeText()
	} else if len(a.toolOrder) == 0 && a.thinking.Len() == 0 {
		a.textIdx = a.block
		a.block++
		a.emit("content_block_start", map[string]any{
			"index":         a.textIdx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		a.textOpen = true
		a.closeText()
	}
	a.closeTools()
	outTokens := 0
	if a.usage != nil {
		outTokens = a.usage.CompletionTokens
	}
	usage := map[string]any{"output_tokens": outTokens}
	attachAnthropicCacheUsage(usage, a.usage)
	a.emit("message_delta", map[string]any{
		"delta": map[string]any{
			"stop_reason":   mapAnthropicStop(a.finishReason),
			"stop_sequence": nil,
		},
		"usage": usage,
	})
	a.emit("message_stop", map[string]any{})
	return a.err
}

func (a *anthropicAdapter) fail(err error) error {
	a.closeThinking()
	a.closeText()
	a.closeTools()
	a.emit("error", map[string]any{"error": map[string]any{"type": "api_error", "message": err.Error()}})
	a.emit("message_stop", map[string]any{})
	return a.err
}

func (p *Proxy) writeCompatJSON(c *gin.Context, resp *http.Response, meta *ChatRequestMeta, start time.Time, cancel context.CancelFunc) (*parsedUsage, error) {
	watchdog := startStreamIdleWatchdog(c.Request.Context(), cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
	defer watchdog.stop()
	result, err := collectSSEWithStart(resp.Body, meta.RequestedModel, start)
	if err != nil {
		if watchdog != nil && watchdog.timedOutNow() {
			return nil, errors.Join(errUpstreamStream, errStreamIdle, err)
		}
		if c.Request.Context().Err() != nil {
			return nil, wrapDownstreamWrite(c.Request.Context().Err())
		}
		return nil, wrapUpstreamStream(err)
	}
	if result.Usage != nil && result.Usage.RequestID == "" {
		result.Usage.RequestID = resp.Header.Get("X-Request-Id")
	}
	collector := NewCaptureCollector()
	collector.Write(jsonResponseText(result))
	result.Usage.Collector = collector
	var encoded []byte
	switch meta.Protocol {
	case ProtocolAnthropic:
		encoded, err = encodeAnthropicJSON(result)
		c.Header("anthropic-version", "2023-06-01")
	default:
		if meta.Compact {
			encoded, err = encodeResponsesCompactionJSON(result)
		} else {
			encoded, err = encodeResponsesJSON(result)
		}
	}
	if err != nil {
		return result.Usage, err
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	if _, writeErr := c.Writer.Write(stripInvisibleFromFrame(encoded)); writeErr != nil {
		return result.Usage, wrapDownstreamWrite(writeErr)
	}
	return result.Usage, nil
}

func (p *Proxy) writeCompatStream(c *gin.Context, resp *http.Response, meta *ChatRequestMeta, start time.Time, cancel context.CancelFunc) (*parsedUsage, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	if meta.Protocol.IsAnthropic() {
		c.Header("anthropic-version", "2023-06-01")
	}
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	sink := newSSESink(c.Writer, flusher)

	// sseSink flushes exactly once per write; passing nil avoids a second flush
	// from writeSSEEvent.
	em := newStreamAdapter(meta.Protocol, sink, nil, meta.RequestedModel)
	em.start()

	decoder := newSSEDecoder(resp.Body)
	var usage *parsedUsage
	var firstTokenMs int64
	var finishReason string
	collector := NewCaptureCollector()
	reqID := resp.Header.Get("X-Request-Id")
	watchdog := startStreamIdleWatchdog(c.Request.Context(), cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
	defer watchdog.stop()
	stopHeartbeat := startSSEHeartbeat(sink)
	defer stopHeartbeat()

	for {
		event, err := decoder.next()
		if err == io.EOF {
			if !normalSSETermination(false, finishReason) {
				failure := wrapUpstreamStream(errSSEMissingEnd)
				em.setUsage(usageWithStreamMeta(usage, collector, firstTokenMs, reqID))
				_ = em.fail(failure)
				return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), failure
			}
			break
		}
		if err != nil {
			failure := streamReadError(err, watchdog, c.Request.Context())
			em.setUsage(usageWithStreamMeta(usage, collector, firstTokenMs, reqID))
			_ = em.fail(failure)
			return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), failure
		}
		watchdog.touch()
		if event.Done {
			break
		}
		if event.FinishReason != "" {
			finishReason = event.FinishReason
		}
		if firstTokenMs == 0 && chunkHasGeneratedToken(event.Chunk) {
			firstTokenMs = time.Since(start).Milliseconds()
		}
		if event.Chunk == nil {
			continue
		}
		parsed := extractUsage(event.Chunk)
		if parsed == nil {
			parsed = &parsedUsage{}
		}
		parsed.Collector = collector
		collector.Write(chunkTextFromChunk(event.Chunk))
		usage = mergeUsage(usage, parsed)
		applyChunkToAdapter(em, event.Chunk)
	}

	if extra, nudgeErr := p.nudgePreambleIfNeeded(c, meta, em); nudgeErr != nil {
		failure := wrapUpstreamStream(nudgeErr)
		em.setUsage(usageWithStreamMeta(usage, collector, firstTokenMs, reqID))
		_ = em.fail(failure)
		return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), failure
	} else if extra != nil {
		usage = mergeUsage(usage, extra)
	}
	usage = usageWithStreamMeta(usage, collector, firstTokenMs, reqID)
	em.setUsage(usage)
	if finishReason != "" {
		em.onFinishReason(finishReason)
	}
	return usage, em.finish()
}

// sseHeartbeatInterval 是心跳间隔。取 15s 是为了留足余量：
// 常见反代/网关的空闲超时通常在 60s 上下，15s 能稳定「喂饱」它们。
const sseHeartbeatInterval = 15 * time.Second

// sseSink 把下游 SSE 的 Write/Flush 串行化。
// 心跳 goroutine 和主循环会同时写同一个 ResponseWriter，不锁就会把 event/data 帧撕开，
// 客户端（Codex）表现为「写着写着突然断了 / Conversation interrupted」。
type sseSink struct {
	mu        sync.Mutex
	w         io.Writer
	flusher   http.Flusher
	lastWrite atomic.Int64
}

func newSSESink(w io.Writer, flusher http.Flusher) *sseSink {
	s := &sseSink{w: w, flusher: flusher}
	s.lastWrite.Store(time.Now().UnixNano())
	return s
}

func (s *sseSink) Write(p []byte) (int, error) {
	if s == nil || s.w == nil {
		return 0, io.ErrClosedPipe
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := stripInvisibleFromFrame(p)
	n, err := s.w.Write(clean)
	if err == nil && s.flusher != nil {
		s.flusher.Flush()
	}
	if err == nil && n == len(clean) {
		s.lastWrite.Store(time.Now().UnixNano())
		return len(p), nil
	}
	return n, err
}

func (s *sseSink) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *sseSink) LastWrite() time.Time {
	if s == nil {
		return time.Time{}
	}
	return time.Unix(0, s.lastWrite.Load())
}

// invisibleByteSeqs 是不可见标记的 UTF-8 字节序列，与 sanitize.go 的
// invisibleMarks 一一对应（那里处理 string，这里处理出站字节）。
var invisibleByteSeqs = [][]byte{
	{0xe2, 0x80, 0x8b},
	{0xe2, 0x80, 0x8c},
	{0xe2, 0x80, 0x8d},
	{0xef, 0xbb, 0xbf},
}

func stripInvisibleFromFrame(p []byte) []byte {
	if len(p) == 0 {
		return p
	}
	out := p
	for _, seq := range invisibleByteSeqs {
		if bytes.Contains(out, seq) {
			out = bytes.ReplaceAll(out, seq, nil)
		}
	}
	return out
}

const sseHeartbeatFrame = "event: ping\ndata: {\"type\":\"ping\"}\n\n"

type sseActivitySource interface {
	LastWrite() time.Time
}

func startSSEHeartbeat(w io.Writer) func() {
	return startSSEHeartbeatInterval(w, sseHeartbeatInterval)
}

func startSSEHeartbeatInterval(w io.Writer, interval time.Duration) func() {
	if interval <= 0 {
		interval = sseHeartbeatInterval
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if source, ok := w.(sseActivitySource); ok {
					last := source.LastWrite()
					if !last.IsZero() && time.Since(last) < interval {
						continue
					}
				}
				if _, err := io.WriteString(w, sseHeartbeatFrame); err != nil {
					return
				}
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}
