package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const unattendedRuntimeNote = `Runtime: keep calling tools until the user's request is fully finished. Do not stop after announcing the next step, and do not wait for the user to say continue. File edits must use apply_patch; it is available and working. Never write files with python, heredoc, or shell redirection.`

const modelExecutionNote = `Do not end a turn with a statement about an action you are about to take. If you say you will inspect, edit, run, rebuild, commit, or continue, call the corresponding tool in the same response. Only stop when the task is complete or a concrete blocker requires user input.`

const applyPatchJSONHint = `When calling this tool, put the complete patch text in the "input" argument. The patch text must start with "*** Begin Patch" and end with "*** End Patch".`

func injectUnattendedRuntime(chat map[string]any) {
	if chat == nil || !chatHasFreeformTool(chat) {
		return
	}
	model := asString(chat["model"])
	msgs, ok := chat["messages"].([]any)
	if !ok {
		return
	}

	// Keep every system instruction at the front. The Responses converter
	// already does this for normal requests, but nudge/injection is also used
	// by tests and future callers after conversion.
	msgs = moveSystemMessagesToHead(msgs)
	strict := requiresStrictExecution(model)
	needRuntime, needStrict := true, strict
	for _, raw := range msgs {
		message, _ := raw.(map[string]any)
		if message == nil || asString(message["role"]) != "system" {
			continue
		}
		content := asString(message["content"])
		if strings.Contains(content, unattendedRuntimeNote) {
			needRuntime = false
		}
		if strings.Contains(content, modelExecutionNote) {
			needStrict = false
		}
	}
	if !needRuntime && !needStrict {
		chat["messages"] = msgs
		return
	}

	var notes []string
	if needRuntime {
		notes = append(notes, unattendedRuntimeNote)
	}
	if needStrict {
		notes = append(notes, modelExecutionNote)
	}
	injected := strings.Join(notes, "\n\n")
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && asString(first["role"]) == "system" {
			if content, ok := first["content"].(string); ok && strings.TrimSpace(content) != "" {
				first["content"] = content + "\n\n" + injected
			} else {
				msgs = append([]any{map[string]any{"role": "system", "content": injected}}, msgs...)
			}
			chat["messages"] = msgs
			return
		}
	}
	chat["messages"] = append([]any{map[string]any{"role": "system", "content": injected}}, msgs...)
}

func moveSystemMessagesToHead(messages []any) []any {
	if len(messages) < 2 {
		return messages
	}
	systems := make([]any, 0, len(messages))
	rest := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message != nil && asString(message["role"]) == "system" {
			systems = append(systems, raw)
		} else {
			rest = append(rest, raw)
		}
	}
	if len(systems) == 0 || len(rest) == 0 {
		return messages
	}
	return append(systems, rest...)
}

type executionPolicy struct {
	maxPreambleRetries int
	strictPrompt       bool
}

func policyForModel(model string) executionPolicy {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "deepseek-v4.1-flash"):
		return executionPolicy{maxPreambleRetries: 2, strictPrompt: true}
	case strings.HasPrefix(model, "glm-"):
		return executionPolicy{maxPreambleRetries: 1, strictPrompt: true}
	default:
		return executionPolicy{maxPreambleRetries: 1}
	}
}

func requiresStrictExecution(model string) bool {
	return policyForModel(model).strictPrompt
}

func chatHasFreeformTool(chat map[string]any) bool {
	tools, _ := chat["tools"].([]any)
	for _, item := range tools {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		fn, _ := m["function"].(map[string]any)
		if fn == nil {
			continue
		}
		if isFreeformTool(asString(fn["name"])) {
			return true
		}
	}
	return false
}

func looksLikePreamble(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if utf8.RuneCountInString(t) > 800 {
		return false
	}
	low := strings.ToLower(t)
	for _, n := range []string{
		"i'll ", "i will ", "let me ", "next:", "next up",
		"going to", "now making", "now i'll", "right after",
		"one command", "now rebuild", "rebuilding ", "now commit",
		"committing ", "continuing", "proceeding", "executing",
		"resuming", "starting", "start by", "接下来", "我来",
		"先改", "马上", "正在执行", "开始提交", "继续提交",
		"现在提交", "准备提交", "开始重建", "继续重建", "现在重建",
		"正在重建",
	} {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

func shouldNudgeAdapter(em streamAdapter) (string, bool) {
	a, ok := em.(*responsesAdapter)
	if !ok || a == nil || len(a.toolOrder) > 0 {
		return "", false
	}
	text := a.text.String()
	return text, looksLikePreamble(text)
}

func nudgeChatBody(body []byte, assistantText string) ([]byte, error) {
	var chat map[string]any
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, err
	}
	msgs, ok := chat["messages"].([]any)
	if !ok {
		return nil, errors.New("nudge chat messages must be an array")
	}
	msgs = append(msgs, map[string]any{"role": "assistant", "content": assistantText})
	msgs = append(msgs, map[string]any{
		"role":    "user",
		"content": "Continue now. Call the tools needed to finish the task. Do not only describe the next step.",
	})
	chat["messages"] = msgs
	chat["tool_choice"] = "required"
	return json.Marshal(chat)
}

func (p *Proxy) nudgePreambleIfNeeded(c *gin.Context, meta *ChatRequestMeta, em streamAdapter) (*parsedUsage, error) {
	if p == nil || p.rotator == nil || c == nil || meta == nil || meta.Protocol != ProtocolResponses {
		return nil, nil
	}
	policy := policyForModel(meta.UpstreamModel)
	var totalUsage *parsedUsage
	body := meta.Body
	for attempt := 1; attempt <= policy.maxPreambleRetries; attempt++ {
		text, ok := shouldNudgeAdapter(em)
		if !ok {
			return totalUsage, nil
		}
		nudged, err := nudgeChatBody(body, text)
		if err != nil {
			global.CORE_LOG.Warn("preamble nudge encode failed", zap.Error(err))
			return totalUsage, nil
		}
		acc, err := p.rotator.NextForAffinity(nil, meta.UpstreamModel, meta.AffinityKey)
		if err != nil {
			global.CORE_LOG.Warn("preamble nudge has no account", zap.Error(err))
			return totalUsage, nil
		}
		nudgeCtx, cancel := context.WithCancel(c.Request.Context())
		resp, err := p.doUpstream(nudgeCtx, acc, "/v2/chat/completions", nudged)
		if err != nil {
			cancel()
			if c.Request.Context().Err() != nil {
				return totalUsage, wrapDownstreamWrite(c.Request.Context().Err())
			}
			global.CORE_LOG.Warn("preamble nudge upstream failed", zap.Int("attempt", attempt), zap.Error(err))
			return totalUsage, nil
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			cancel()
			global.CORE_LOG.Warn("preamble nudge upstream status", zap.Int("attempt", attempt), zap.Int("status", resp.StatusCode))
			return totalUsage, nil
		}
		global.CORE_LOG.Info("continued a Codex turn that announced work but did not call tools", zap.String("model", meta.UpstreamModel), zap.Int("attempt", attempt))
		watchdog := startStreamIdleWatchdog(nudgeCtx, cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
		usage, streamErr := pipeChatSSEToAdapter(resp.Body, em, watchdog)
		watchdogTimedOut := watchdog.timedOutNow()
		watchdog.stop()
		resp.Body.Close()
		cancel()
		if c.Request.Context().Err() != nil {
			return totalUsage, wrapDownstreamWrite(c.Request.Context().Err())
		}
		if streamErr == context.Canceled && watchdogTimedOut {
			streamErr = errStreamIdle
		}
		if streamErr != nil {
			totalUsage = mergeUsage(totalUsage, usage)
			global.CORE_LOG.Warn("preamble nudge stream failed", zap.Int("attempt", attempt), zap.Error(streamErr))
			return totalUsage, streamErr
		}
		totalUsage = mergeUsage(totalUsage, usage)
		body = nudged
	}
	if _, ok := shouldNudgeAdapter(em); ok {
		global.CORE_LOG.Warn("model stopped after announcing work without a tool call", zap.String("model", meta.UpstreamModel), zap.Int("max_attempts", policy.maxPreambleRetries))
	}
	return totalUsage, nil
}

func pipeChatSSEToAdapter(r io.Reader, em streamAdapter, watchdog *streamIdleWatchdog) (*parsedUsage, error) {
	decoder := newSSEDecoder(r)
	var usage *parsedUsage
	finishReason := ""
	for {
		event, err := decoder.next()
		if err == io.EOF {
			if !normalSSETermination(false, finishReason) {
				return usage, errSSEMissingEnd
			}
			return usage, nil
		}
		if err != nil {
			return usage, err
		}
		watchdog.touch()
		if event.Done {
			return usage, nil
		}
		if event.FinishReason != "" {
			finishReason = event.FinishReason
			em.onFinishReason(finishReason)
		}
		if event.Chunk == nil {
			continue
		}
		usage = mergeUsage(usage, extractUsage(event.Chunk))
		applyChunkToAdapter(em, event.Chunk)
	}
}
