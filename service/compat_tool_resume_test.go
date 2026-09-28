package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
)

const truncatedCallSSE = "data: {\"id\":\"c1\",\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"ec\"}}]}}]}\n\n"

const completeCallSSE = "data: {\"id\":\"c2\",\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_2\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"echo ok\\\"}\"}}]}},{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"

func toolState(name, args string) *toolStreamState {
	return &toolStreamState{name: name, args: args, binding: nil}
}

func TestToolCallArgsIncomplete(t *testing.T) {
	closed := toolState("shell", `{"a":`)
	closed.closed = true
	cases := []struct {
		name string
		st   *toolStreamState
		want bool
	}{
		{"valid object", toolState("shell", `{"cmd":"ls"}`), false},
		{"empty args", toolState("shell", ""), false},
		{"whitespace args", toolState("shell", "   "), false},
		{"truncated object", toolState("shell", `{"cmd":"ls`), true},
		{"partial token", toolState("shell", `{"cmd":`), true},
		{"closed state", closed, false},
		{"nil state", nil, false},
	}
	for _, tc := range cases {
		if got := toolCallArgsIncomplete(tc.st); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestToolCallArgsIncompleteSkipsFreeformTools(t *testing.T) {
	patch := &toolStreamState{name: "apply_patch", args: "*** Begin Patch\n*** Update File"}
	if toolCallArgsIncomplete(patch) {
		t.Fatal("freeform tool must not be flagged as incomplete JSON")
	}
	custom := &toolStreamState{name: "raw", args: "partial", binding: &responseToolBinding{Kind: responseToolCustom}}
	if toolCallArgsIncomplete(custom) {
		t.Fatal("custom tool must not be flagged as incomplete JSON")
	}
}

func TestResponsesAdapterReportsTruncatedToolCalls(t *testing.T) {
	var buf strings.Builder
	ad := newStreamAdapter(ProtocolResponses, &buf, nil, "deepseek-v4.1-flash").(*responsesAdapter)
	ad.start()
	ad.onToolCall(AggregatedToolCall{Index: 0, ID: "call_1", Name: "shell", Arguments: `{"cmd":"ec`})
	if !ad.hasToolCalls() {
		t.Fatal("adapter should report the emitted tool call")
	}
	truncated := ad.incompleteToolCalls()
	if len(truncated) != 1 || truncated[0].name != "shell" {
		t.Fatalf("expected one truncated call, got %#v", truncated)
	}

	var done strings.Builder
	ad2 := newStreamAdapter(ProtocolResponses, &done, nil, "deepseek-v4.1-flash").(*responsesAdapter)
	ad2.start()
	ad2.onToolCall(AggregatedToolCall{Index: 0, ID: "call_1", Name: "shell", Arguments: `{"cmd":"echo"}`})
	if got := ad2.incompleteToolCalls(); len(got) != 0 {
		t.Fatalf("complete call reported as truncated: %#v", got)
	}
}

func TestResumeToolChatBodyStripsTruncatedCallAndRequiresTool(t *testing.T) {
	body, err := resumeToolChatBody([]byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"go"}],"tools":[{"type":"function","function":{"name":"shell"}}]}`), "Let me inspect first.", []*toolStreamState{toolState("shell", `{"cmd":"ec`)})
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatal(err)
	}
	if chat["tool_choice"] != "required" {
		t.Fatalf("tool_choice=%v", chat["tool_choice"])
	}
	msgs := chat["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" || !strings.Contains(asString(last["content"]), "Re-issue the same tool call") {
		t.Fatalf("resume instruction missing: %#v", msgs)
	}
	if !strings.Contains(asString(last["content"]), "shell") {
		t.Fatalf("truncated tool name missing from instruction: %v", last["content"])
	}
	for _, raw := range msgs {
		if _, ok := raw.(map[string]any)["tool_calls"]; ok {
			t.Fatalf("truncated tool_calls must not be replayed: %#v", msgs)
		}
	}
}

func TestResumeToolChatBodyWithoutToolsSkipsToolChoice(t *testing.T) {
	body, err := resumeToolChatBody([]byte(`{"model":"x","messages":[{"role":"user","content":"go"}]}`), "", []*toolStreamState{toolState("shell", `{"a":`)})
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatal(err)
	}
	if _, ok := chat["tool_choice"]; ok {
		t.Fatalf("tool_choice must be omitted without tools: %v", chat["tool_choice"])
	}
}

// TestWriteCompatStreamResumesTruncatedToolCall drives the full resume path:
// the first upstream response streams a tool call and dies mid-arguments, and
// the retry served by the fake upstream returns a complete call.
func TestWriteCompatStreamResumesTruncatedToolCall(t *testing.T) {
	defer setupRotatorDB(t)()
	addRotatorAccount(t, "acc", "jwt-acc", 100, true, 0)

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Re-issue the same tool call") {
			t.Errorf("resume instruction missing from retry body: %s", body)
		}
		if strings.Contains(string(body), "tool_calls") {
			t.Errorf("broken attempt was replayed: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, completeCallSSE)
	}))
	defer upstream.Close()

	oldGateway := global.CORE_CONFIG.Gateway
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, Upstream: upstream.URL, StreamIdleTimeoutSeconds: 5}
	defer func() { global.CORE_CONFIG.Gateway = oldGateway }()

	proxy := NewProxy(NewUpstreamClient(), NewRotator(), nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	original := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"run echo"}],"tools":[{"type":"function","function":{"name":"shell"}}]}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(truncatedCallSSE))}
	meta := &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "deepseek-v4.1-flash", UpstreamModel: "deepseek-v4.1-flash", ClientStream: true, Body: original}
	if _, err := proxy.writeCompatStream(ctx, resp, meta, time.Now(), func() {}); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	out := recorder.Body.String()
	if strings.Contains(out, "event: response.failed") {
		t.Fatalf("resume should have recovered, got failure: %s", out)
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Fatalf("resume should complete the response: %s", out)
	}
	if !strings.Contains(out, "echo ok") {
		t.Fatalf("recovered tool arguments missing: %s", out)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 upstream resume attempt, got %d", calls.Load())
	}
}

// TestWriteCompatStreamTruncatedToolFailsWithoutRotator confirms the previous
// behavior is preserved when resume cannot run.
func TestWriteCompatStreamTruncatedToolFailsWithoutRotator(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(truncatedCallSSE))}
	proxy := &Proxy{}
	meta := &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "deepseek-v4.1-flash", ClientStream: true, Body: []byte(`{"model":"deepseek-v4.1-flash","messages":[]}`)}
	_, err := proxy.writeCompatStream(ctx, resp, meta, time.Now(), func() {})
	if err == nil {
		t.Fatal("expected stream termination error")
	}
	out := recorder.Body.String()
	if !strings.Contains(out, "event: response.failed") {
		t.Fatalf("expected failure event: %s", out)
	}
	if strings.Contains(out, "event: response.completed") {
		t.Fatalf("truncated call must not complete: %s", out)
	}
}

// TestWriteCompatStreamInvalidArgsOnFinishFails ensures a tool call that ends
// with a finish_reason but still has invalid arguments is never emitted as a
// completed call.
func TestWriteCompatStreamInvalidArgsOnFinishFails(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"ec\"}}]}},{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n",
	))}
	proxy := &Proxy{}
	meta := &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "deepseek-v4.1-flash", ClientStream: true, Body: []byte(`{"model":"deepseek-v4.1-flash","messages":[]}`)}
	if _, err := proxy.writeCompatStream(ctx, resp, meta, time.Now(), func() {}); err == nil {
		t.Fatal("expected a failure for an unparseable tool call")
	}
	out := recorder.Body.String()
	if !strings.Contains(out, "event: response.failed") {
		t.Fatalf("expected failure event: %s", out)
	}
	if strings.Contains(out, "function_call_arguments.done") {
		t.Fatalf("invalid arguments must not be emitted as a completed call: %s", out)
	}
}

// TestWriteCompatStreamResumeClearsStaleFinishReason verifies a recovered call
// is not reported as incomplete just because the interrupted first stream had
// a length finish_reason.
func TestWriteCompatStreamResumeClearsStaleFinishReason(t *testing.T) {
	defer setupRotatorDB(t)()
	addRotatorAccount(t, "acc", "jwt-acc", 100, true, 0)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, completeCallSSE)
	}))
	defer upstream.Close()

	oldGateway := global.CORE_CONFIG.Gateway
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, Upstream: upstream.URL, StreamIdleTimeoutSeconds: 5}
	defer func() { global.CORE_CONFIG.Gateway = oldGateway }()

	proxy := NewProxy(NewUpstreamClient(), NewRotator(), nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	// Interrupted stream: truncated args plus a length finish_reason.
	interrupted := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"ec\"}}]}},{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(interrupted))}
	meta := &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "deepseek-v4.1-flash", UpstreamModel: "deepseek-v4.1-flash", ClientStream: true, Body: []byte(`{"model":"deepseek-v4.1-flash","messages":[],"tools":[{"type":"function","function":{"name":"shell"}}]}`)}
	if _, err := proxy.writeCompatStream(ctx, resp, meta, time.Now(), func() {}); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	out := recorder.Body.String()
	if !strings.Contains(out, "event: response.completed") || strings.Contains(out, "event: response.incomplete") {
		t.Fatalf("recovered call should complete, not stay incomplete: %s", out)
	}
}

func TestCollectSSERejectsInvalidToolArgs(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"ec\"}}]}},{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	if _, err := collectSSE(strings.NewReader(sse), "model"); err == nil {
		t.Fatal("expected invalid tool arguments to be rejected")
	}
	// A freeform tool body is not JSON and must be accepted as-is.
	free := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"apply_patch\",\"arguments\":\"*** Begin Patch\\n\"}}]}},{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	if _, err := collectSSE(strings.NewReader(free), "model"); err != nil {
		t.Fatalf("freeform tool body must be accepted: %v", err)
	}
}
