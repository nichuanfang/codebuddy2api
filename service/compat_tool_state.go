package service

import (
	"encoding/json"
	"strings"
)

// toolCallArgsIncomplete reports whether a streamed tool call ended with an
// argument payload that is not valid JSON. Empty arguments are treated as
// complete: a tool with no parameters is legitimate and its arguments payload
// is commonly an empty string.
//
// Freeform/custom tools (for example apply_patch) carry a plain-text payload
// rather than JSON, so they are never flagged as incomplete here; a truncated
// patch body is a different failure mode that resume cannot repair by simply
// re-calling the tool.
func toolCallArgsIncomplete(st *toolStreamState) bool {
	if st == nil || st.closed {
		return false
	}
	if toolPayloadIsFreeform(st) {
		return false
	}
	args := strings.TrimSpace(st.args)
	if args == "" {
		return false
	}
	return !json.Valid([]byte(args))
}

// toolPayloadIsFreeform reports whether the tool call carries an opaque text
// body (apply_patch style custom tools) instead of a JSON argument object.
func toolPayloadIsFreeform(st *toolStreamState) bool {
	if st == nil {
		return false
	}
	if st.binding != nil {
		return st.binding.Kind == responseToolCustom || st.binding.InputField != ""
	}
	return isFreeformTool(st.name)
}

// incompleteToolCallResumer is implemented by stream adapters that can report
// tool calls truncated mid-arguments so the proxy can attempt a resume.
//
// discardTruncatedToolCalls clears the half-finished state before a resume so
// the re-issued call starts from a clean slate instead of appending to the
// partial arguments the first attempt already streamed.
type incompleteToolCallResumer interface {
	incompleteToolCalls() []*toolStreamState
	hasToolCalls() bool
	discardTruncatedToolCalls()
}

// discardTruncatedToolCalls removes every truncated tool call from the adapter
// so a resume can re-open a fresh item. Items that never received
// function_call_arguments.done are discarded by the client, so the freshly
// opened item at a new output index becomes the only visible call.
func (a *responsesAdapter) discardTruncatedToolCalls() {
	if a == nil {
		return
	}
	kept := a.toolOrder[:0]
	for _, idx := range a.toolOrder {
		st := a.tools[idx]
		if !toolCallArgsIncomplete(st) {
			kept = append(kept, idx)
			continue
		}
		delete(a.tools, idx)
	}
	a.toolOrder = kept
}

// discardTruncatedToolCalls is a no-op for the Anthropic adapter: it does not
// run the resume path today, and its content blocks cannot be retracted once
// their index was announced.
func (a *anthropicAdapter) discardTruncatedToolCalls() {}

func (a *responsesAdapter) incompleteToolCalls() []*toolStreamState {
	if a == nil {
		return nil
	}
	var out []*toolStreamState
	for _, idx := range a.toolOrder {
		if st := a.tools[idx]; toolCallArgsIncomplete(st) {
			out = append(out, st)
		}
	}
	return out
}

// hasToolCalls reports whether the adapter already emitted at least one tool
// call. It separates "the model announced a tool but never produced one"
// (preamble nudge) from "the model started a tool call and the stream was cut
// mid-arguments" (truncated tool-call resume).
func (a *responsesAdapter) hasToolCalls() bool {
	return a != nil && len(a.toolOrder) > 0
}

func (a *anthropicAdapter) incompleteToolCalls() []*toolStreamState {
	if a == nil {
		return nil
	}
	var out []*toolStreamState
	for _, idx := range a.toolOrder {
		if st := a.tools[idx]; toolCallArgsIncomplete(st) {
			out = append(out, st)
		}
	}
	return out
}

func (a *anthropicAdapter) hasToolCalls() bool {
	return a != nil && len(a.toolOrder) > 0
}
