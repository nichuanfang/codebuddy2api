package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The upstream sometimes streams a tool call and then dies mid-arguments:
// either the SSE connection is cut before the argument JSON closes, or the
// model emits a tool call and then a "length"/"stop" finish reason while the
// arguments payload is still not valid JSON. Both produce a function_call
// whose arguments cannot be parsed by the client, which is strictly worse than
// re-asking the model to issue the call again.
//
// Tool-call resume appends the truncated assistant attempt (with the broken
// call left out) plus an explicit instruction to re-issue the tool call, and
// pins tool_choice to "required" so the model cannot answer with prose.
const toolResumeUserNote = "Your previous tool call was cut off before its arguments finished streaming, so calling it would fail with invalid JSON. Re-issue the same tool call now and send the complete, valid JSON arguments in one pass."

func resumeToolChatBody(body []byte, assistantText string, truncated []*toolStreamState) ([]byte, error) {
	var chat map[string]any
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, err
	}
	msgs, ok := chat["messages"].([]any)
	if !ok {
		return nil, errors.New("resume chat messages must be an array")
	}

	// Preserve the assistant's prose so the re-issued call keeps its context,
	// but never replay the truncated tool_calls: an invalid arguments payload
	// poisons the next request and usually triggers a 400 upstream.
	if text := assistantText; text != "" {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": text})
	}
	note := toolResumeUserNote
	if names := truncatedToolNames(truncated); names != "" {
		note += " Truncated call(s): " + names + "."
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": note})
	chat["messages"] = msgs
	if chatHasCallableTools(chat) {
		chat["tool_choice"] = "required"
	}
	return json.Marshal(chat)
}

// truncatedToolNames renders the names of the truncated tool calls for the
// resume instruction, so the model knows exactly which call to redo.
func truncatedToolNames(calls []*toolStreamState) string {
	var names []string
	seen := map[string]struct{}{}
	for _, st := range calls {
		if st == nil || st.name == "" {
			continue
		}
		if _, ok := seen[st.name]; ok {
			continue
		}
		seen[st.name] = struct{}{}
		names = append(names, st.name)
	}
	return joinComma(names)
}

func joinComma(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	out := items[0]
	for _, item := range items[1:] {
		out += ", " + item
	}
	return out
}

// resumeTruncatedToolCalls re-issues the request when the upstream stream ended
// with a tool call whose arguments never finished streaming. It mirrors the
// preamble nudge: the retry reuses the caller's affinity key, streams into the
// same adapter, and its usage is merged into the totals.
//
// The returned bool reports whether every truncated call was repaired.
func (p *Proxy) resumeTruncatedToolCalls(c *gin.Context, meta *ChatRequestMeta, em streamAdapter) (*parsedUsage, bool, error) {
	if p == nil || p.rotator == nil || c == nil || meta == nil || meta.Protocol != ProtocolResponses {
		return nil, false, nil
	}
	resumer, ok := em.(incompleteToolCallResumer)
	if !ok || !resumer.hasToolCalls() {
		return nil, false, nil
	}
	truncated := resumer.incompleteToolCalls()
	if len(truncated) == 0 {
		return nil, false, nil
	}

	policy := policyForModel(meta.UpstreamModel)
	attempts := policy.maxPreambleRetries
	if attempts < 1 {
		attempts = 1
	}
	var totalUsage *parsedUsage
	body := meta.Body
	truncatedNow := truncated
	for attempt := 1; attempt <= attempts; attempt++ {
		assistantText := ""
		if a, ok := em.(*responsesAdapter); ok && a != nil {
			assistantText = a.text.String()
		}
		// Clear the half-streamed arguments first: the retry must open a fresh
		// call rather than append to the partial payload.
		resumer.discardTruncatedToolCalls()
		resumed, err := resumeToolChatBody(body, assistantText, truncatedNow)
		if err != nil {
			global.CORE_LOG.Warn("tool-call resume encode failed", zap.Error(err))
			return totalUsage, false, nil
		}
		acc, err := p.rotator.NextForAffinity(nil, meta.UpstreamModel, meta.AffinityKey)
		if err != nil {
			global.CORE_LOG.Warn("tool-call resume has no account", zap.Error(err))
			return totalUsage, false, nil
		}
		resumeCtx, cancel := context.WithCancel(c.Request.Context())
		resp, err := p.doUpstream(resumeCtx, acc, "/v2/chat/completions", resumed)
		if err != nil {
			cancel()
			if c.Request.Context().Err() != nil {
				return totalUsage, false, wrapDownstreamWrite(c.Request.Context().Err())
			}
			global.CORE_LOG.Warn("tool-call resume upstream failed", zap.Int("attempt", attempt), zap.Error(err))
			return totalUsage, false, nil
		}
		if resp.StatusCode != 200 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			cancel()
			global.CORE_LOG.Warn("tool-call resume upstream status", zap.Int("attempt", attempt), zap.Int("status", resp.StatusCode))
			return totalUsage, false, nil
		}
		global.CORE_LOG.Info("resumed a tool call the upstream cut off mid-arguments",
			zap.String("model", meta.UpstreamModel), zap.Int("attempt", attempt), zap.Int("truncated_calls", len(truncatedNow)))
		watchdog := startStreamIdleWatchdog(resumeCtx, cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
		usage, streamErr := pipeChatSSEToAdapter(resp.Body, em, watchdog)
		watchdogTimedOut := watchdog.timedOutNow()
		watchdog.stop()
		resp.Body.Close()
		cancel()
		if c.Request.Context().Err() != nil {
			return totalUsage, false, wrapDownstreamWrite(c.Request.Context().Err())
		}
		if streamErr == context.Canceled && watchdogTimedOut {
			streamErr = errStreamIdle
		}
		totalUsage = mergeUsage(totalUsage, usage)
		if streamErr != nil {
			global.CORE_LOG.Warn("tool-call resume stream failed", zap.Int("attempt", attempt), zap.Error(streamErr))
			// A transport failure on the retry is not fatal by itself: the
			// adapter may have completed the call before the stream dropped.
		}
		body = resumed
		// The retry may itself be cut off. Stop once the adapter has no tool
		// call left with unfinished arguments.
		if len(resumer.incompleteToolCalls()) == 0 {
			return totalUsage, true, nil
		}
		truncatedNow = resumer.incompleteToolCalls()
	}
	global.CORE_LOG.Warn("tool call still truncated after resume attempts",
		zap.String("model", meta.UpstreamModel), zap.Int("max_attempts", attempts))
	return totalUsage, false, nil
}
