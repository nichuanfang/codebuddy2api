package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const unattendedRuntimeNote = `Runtime: keep calling tools until the user's request is fully finished. Do not stop after announcing the next step, and do not wait for the user to say continue. File edits must use apply_patch; it is available and working. Never write files with python, heredoc, or shell redirection.`

const applyPatchJSONHint = `When calling this tool, put the complete patch text in the "input" argument. The patch text must start with "*** Begin Patch" and end with "*** End Patch".`

func injectUnattendedRuntime(chat map[string]any) {
	if chat == nil || !chatHasFreeformTool(chat) {
		return
	}
	msgs, _ := chat["messages"].([]any)
	for _, item := range msgs {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if asString(m["role"]) == "system" && asString(m["content"]) == unattendedRuntimeNote {
			return
		}
	}
	chat["messages"] = append(msgs, map[string]any{"role": "system", "content": unattendedRuntimeNote})
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
		"one command", "接下来", "我来", "先改", "马上",
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
	msgs, _ := chat["messages"].([]any)
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
	text, ok := shouldNudgeAdapter(em)
	if !ok {
		return nil, nil
	}
	nudged, err := nudgeChatBody(meta.Body, text)
	if err != nil {
		global.CORE_LOG.Warn("preamble nudge encode failed", zap.Error(err))
		return nil, nil
	}
	acc, err := p.rotator.NextFor(nil, meta.UpstreamModel)
	if err != nil {
		global.CORE_LOG.Warn("preamble nudge has no account", zap.Error(err))
		return nil, nil
	}
	nudgeCtx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	resp, err := p.doUpstream(nudgeCtx, acc, "/v2/chat/completions", nudged)
	if err != nil {
		global.CORE_LOG.Warn("preamble nudge upstream failed", zap.Error(err))
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		global.CORE_LOG.Warn("preamble nudge upstream status", zap.Int("status", resp.StatusCode))
		return nil, nil
	}
	global.CORE_LOG.Info("continued a Codex turn that announced work but did not call tools")
	watchdog := startStreamIdleWatchdog(nudgeCtx, cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
	defer watchdog.stop()
	usage, err := pipeChatSSEToAdapter(resp.Body, em, watchdog)
	if err == context.Canceled && watchdog.timedOutNow() {
		err = errStreamIdle
	}
	if err != nil {
		global.CORE_LOG.Warn("preamble nudge stream failed", zap.Error(err))
	}
	return usage, err
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
