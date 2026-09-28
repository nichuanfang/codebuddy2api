package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
)

func testHistoryScope(session string) responseHistoryScope {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer private-key")
	if session != "" {
		headers.Set("X-Session-Id", session)
	}
	return historyScopeFor(headers, "", []byte(`{"model":"glm-5.3"}`))
}

func testToolResponse(id string, calls ...map[string]any) map[string]any {
	output := make([]any, 0, len(calls)+1)
	output = append(output, map[string]any{
		"id": "rs_1", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Inspect first"}},
	})
	for _, call := range calls {
		output = append(output, call)
	}
	return map[string]any{"id": id, "status": "completed", "output": output}
}

func testCall(id, name, typ string) map[string]any {
	item := map[string]any{"id": "fc_" + id, "type": typ, "status": "completed", "call_id": id, "name": name}
	if typ == "custom_tool_call" {
		item["input"] = "*** Begin Patch\n*** End Patch"
	} else {
		item["arguments"] = `{"path":"README.md"}`
	}
	return item
}

func decodeHistoryInput(t *testing.T, raw []byte) []any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["previous_response_id"] != nil {
		t.Fatalf("ordinary previous_response_id should be consumed: %v", body)
	}
	input, _ := body["input"].([]any)
	return input
}

func TestResponsesHistoryRestoresParallelCallsAndReasoning(t *testing.T) {
	oldGateway := global.CORE_CONFIG.Gateway
	defer func() { global.CORE_CONFIG.Gateway = oldGateway }()
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true}
	store := newResponseHistoryStore()
	scope := testHistoryScope("session-a")
	store.record(testToolResponse("resp_parallel", testCall("call_1", "read_file", "function_call"), testCall("call_2", "apply_patch", "custom_tool_call")), scope)
	raw := []byte(`{"model":"glm-5.3","previous_response_id":"resp_parallel","input":[{"type":"function_call_output","call_id":"call_1","output":"file"},{"type":"custom_tool_call_output","call_id":"call_2","output":"patched"}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch","description":"patch"}]}`)
	enriched, err := store.enrich(raw, scope)
	if err != nil {
		t.Fatal(err)
	}
	input := decodeHistoryInput(t, enriched)
	if len(input) != 5 || input[0].(map[string]any)["type"] != "reasoning" || input[1].(map[string]any)["call_id"] != "call_1" || input[2].(map[string]any)["call_id"] != "call_2" {
		t.Fatalf("restored call group=%s", enriched)
	}
	meta, err := PrepareResponsesBody(enriched)
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	if err := json.Unmarshal(meta.Body, &chat); err != nil {
		t.Fatal(err)
	}
	messages := chat["messages"].([]any)
	var assistant map[string]any
	for _, rawMessage := range messages {
		message := rawMessage.(map[string]any)
		if message["role"] == "assistant" && message["tool_calls"] != nil {
			assistant = message
			break
		}
	}
	if assistant == nil || len(assistant["tool_calls"].([]any)) != 2 || assistant["reasoning_content"] != "Inspect first" {
		t.Fatalf("Chat tool group/reasoning=%v", assistant)
	}
}

func TestResponsesHistoryKeepsFullInputWithoutDuplicateCall(t *testing.T) {
	store := newResponseHistoryStore()
	scope := testHistoryScope("session-a")
	store.record(testToolResponse("resp_full", testCall("call_1", "read_file", "function_call")), scope)
	raw := []byte(`{"model":"glm-5.3","previous_response_id":"resp_full","input":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Inspect first"}]},{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	enriched, err := store.enrich(raw, scope)
	if err != nil {
		t.Fatal(err)
	}
	input := decodeHistoryInput(t, enriched)
	if len(input) != 3 {
		t.Fatalf("duplicated client history: %s", enriched)
	}
}

func TestResponsesHistoryPreservesNamespacedTool(t *testing.T) {
	oldGateway := global.CORE_CONFIG.Gateway
	defer func() { global.CORE_CONFIG.Gateway = oldGateway }()
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true}
	store := newResponseHistoryStore()
	scope := testHistoryScope("session-mcp")
	call := testCall("call_mcp", "resolve_library_id", "function_call")
	call["namespace"] = "mcp__context7"
	store.record(testToolResponse("resp_mcp", call), scope)
	raw := []byte(`{"model":"glm-5.3","previous_response_id":"resp_mcp","input":[{"type":"function_call_output","call_id":"call_mcp","output":"found"}],"tools":[{"type":"namespace","name":"context7","tools":[{"type":"function","name":"resolve_library_id","parameters":{"type":"object"}}]}]}`)
	enriched, err := store.enrich(raw, scope)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := PrepareResponsesBody(enriched)
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	if err := json.Unmarshal(meta.Body, &chat); err != nil {
		t.Fatal(err)
	}
	for _, rawMessage := range chat["messages"].([]any) {
		message := rawMessage.(map[string]any)
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
			name := calls[0].(map[string]any)["function"].(map[string]any)["name"]
			if name != "resolve_library_id" && name != "context7__resolve_library_id" {
				t.Fatalf("namespace lost: %s", meta.Body)
			}
			return
		}
	}
	t.Fatalf("no tool call in %s", meta.Body)
}

func TestResponsesHistoryIsolationFallbackAndMissing(t *testing.T) {
	store := newResponseHistoryStore()
	withSession := testHistoryScope("session-a")
	otherSession := testHistoryScope("session-b")
	noSession := testHistoryScope("")
	store.record(testToolResponse("resp_call", testCall("call_1", "read_file", "function_call")), withSession)
	output := []byte(`{"model":"glm-5.3","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	if _, err := store.enrich(output, withSession); err != nil {
		t.Fatalf("unique same-session fallback: %v", err)
	}
	if _, err := store.enrich(output, otherSession); err == nil {
		t.Fatal("cross-session tool call must not be restored")
	}
	store.record(testToolResponse("resp_call_2", testCall("call_1", "read_file", "function_call")), withSession)
	if _, err := store.enrich(output, withSession); err == nil {
		t.Fatal("ambiguous call_id must not be restored")
	}
	store.record(testToolResponse("resp_personal", testCall("call_2", "read_file", "function_call")), noSession)
	bare := []byte(`{"model":"glm-5.3","input":[{"type":"function_call_output","call_id":"call_2","output":"ok"}]}`)
	if _, err := store.enrich(bare, noSession); err == nil {
		t.Fatal("global call_id fallback without session must be disabled")
	}
	withID := []byte(`{"model":"glm-5.3","previous_response_id":"resp_personal","input":[{"type":"function_call_output","call_id":"call_2","output":"ok"}]}`)
	if _, err := store.enrich(withID, noSession); err != nil {
		t.Fatalf("explicit previous ID without session: %v", err)
	}
	if _, err := store.enrich(withID, withSession); err == nil {
		t.Fatal("response ID must not escape its scope")
	}
	otherCredential := http.Header{"Authorization": []string{"Bearer another-key"}, "X-Session-Id": []string{"session-a"}}
	if _, err := store.enrich([]byte(`{"model":"glm-5.3","previous_response_id":"resp_call","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`), historyScopeFor(otherCredential, "", nil)); err == nil {
		t.Fatal("history must not cross caller credentials")
	}
	unknownText := []byte(`{"model":"glm-5.3","previous_response_id":"resp_expired","input":"self-contained"}`)
	if enriched, err := store.enrich(unknownText, noSession); err != nil || !strings.Contains(string(enriched), "self-contained") || strings.Contains(string(enriched), "previous_response_id") {
		t.Fatalf("stateless fallback=%s err=%v", enriched, err)
	}
}

func TestResponsesHistoryBoundsAndExpiry(t *testing.T) {
	store := newResponseHistoryStore()
	now := time.Now()
	store.now = func() time.Time { return now }
	store.maxItems = 1
	scope := testHistoryScope("session-a")
	store.record(testToolResponse("resp_first", testCall("call_1", "read_file", "function_call")), scope)
	store.record(testToolResponse("resp_second", testCall("call_2", "read_file", "function_call")), scope)
	if _, ok := store.snapshot(scope, "resp_first"); ok {
		t.Fatal("oldest response should be evicted")
	}
	if _, ok := store.snapshot(scope, "resp_second"); !ok {
		t.Fatal("newest response was not cached")
	}
	now = now.Add(responseHistoryTTL + time.Second)
	if _, ok := store.snapshot(scope, "resp_second"); ok || store.bytes != 0 {
		t.Fatal("expired response should be removed")
	}
	store.maxEntry = 10
	store.record(testToolResponse("resp_oversized", testCall("call_3", "read_file", "function_call")), scope)
	if _, ok := store.snapshot(scope, "resp_oversized"); ok {
		t.Fatal("oversized response should not be cached")
	}
	store.maxEntry = responseHistoryMaxEntry
	store.maxItems = 10
	store.maxBytes = 400
	store.record(testToolResponse("resp_budget", testCall("call_4", "read_file", "function_call")), scope)
	store.record(testToolResponse("resp_budget_2", testCall("call_5", "read_file", "function_call")), scope)
	if store.bytes > store.maxBytes {
		t.Fatalf("cache bytes=%d limit=%d", store.bytes, store.maxBytes)
	}
	if _, ok := store.snapshot(scope, "resp_budget"); ok {
		t.Fatal("byte budget should evict the oldest response")
	}
	store.record(map[string]any{"id": "resp_incomplete", "status": "incomplete", "output": []any{testCall("call_5", "read_file", "function_call")}}, scope)
	if _, ok := store.snapshot(scope, "resp_incomplete"); ok {
		t.Fatal("incomplete responses must not enter history")
	}
}

func TestResponsesHistoryRecordsJSONAndStreamingOutputs(t *testing.T) {
	oldGateway := global.CORE_CONFIG.Gateway
	defer func() { global.CORE_CONFIG.Gateway = oldGateway }()
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true}
	for _, streaming := range []bool{false, true} {
		store := newResponseHistoryStore()
		proxy := &Proxy{history: store}
		scope := testHistoryScope("session-a")
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		sse := "data: {\"id\":\"chat_1\",\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"reasoning_content\":\"Inspect\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
		upstream := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse))}
		meta := &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "glm-5.3", ClientStream: streaming, historyScope: scope}
		var err error
		if streaming {
			_, err = proxy.writeCompatStream(ctx, upstream, meta, time.Now(), func() {})
		} else {
			_, err = proxy.writeCompatJSON(ctx, upstream, meta, time.Now(), func() {})
		}
		if err != nil {
			t.Fatalf("stream=%v: %v", streaming, err)
		}
		var responseID string
		if streaming {
			for _, frame := range strings.Split(recorder.Body.String(), "\n\n") {
				if !strings.HasPrefix(frame, "event: response.completed\n") {
					continue
				}
				var payload map[string]any
				if json.Unmarshal([]byte(strings.SplitN(frame, "data: ", 2)[1]), &payload) == nil {
					responseID = asString(payload["response"].(map[string]any)["id"])
				}
			}
		} else {
			var payload map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			responseID = asString(payload["id"])
		}
		if responseID == "" {
			t.Fatalf("stream=%v: no response ID in %s", streaming, recorder.Body.String())
		}
		entry, ok := store.snapshot(scope, responseID)
		if !ok || entry.calls["call_1"] == nil {
			t.Fatalf("stream=%v: response not recorded under %s", streaming, responseID)
		}
	}
}

func TestResponsesHistoryHandlerRejectsOrphanToolOutput(t *testing.T) {
	proxy := NewProxy(nil, nil, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3","input":[{"type":"function_call_output","call_id":"call_missing","output":"ok"}]}`))
	proxy.HandleResponses(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "no matching call item") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesUpstreamSSEErrorsDoNotComplete(t *testing.T) {
	cases := []struct{ name, sse, message string }{
		{"named", "event: error\ndata: {\"message\":\"rate limited\",\"code\":\"rate_limit\"}\n\ndata: [DONE]\n\n", "rate limited"},
		{"data.error", "data: {\"error\":{\"message\":\"denied\",\"code\":\"blocked\"}}\n\ndata: [DONE]\n\n", "denied"},
		{"plain text", "event: error\ndata: upstream unavailable\n\ndata: [DONE]\n\n", "upstream unavailable"},
		{"multiline", "event: error\ndata: upstream unavailable\ndata: try again\n\ndata: [DONE]\n\n", "upstream unavailable\ntry again"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := collectSSE(strings.NewReader(tc.sse), "glm-5.3"); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("collectSSE error=%v", err)
			}
			proxy := &Proxy{history: newResponseHistoryStore()}
			for _, streaming := range []bool{false, true} {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				upstream := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.sse))}
				meta := &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "glm-5.3", ClientStream: streaming}
				var err error
				if streaming {
					_, err = proxy.writeCompatStream(ctx, upstream, meta, time.Now(), func() {})
					if !strings.Contains(recorder.Body.String(), "event: response.failed") || strings.Contains(recorder.Body.String(), "event: response.completed") || !strings.Contains(recorder.Body.String(), strings.ReplaceAll(tc.message, "\n", `\n`)) {
						t.Fatalf("stream incorrectly completed: %s", recorder.Body.String())
					}
				} else {
					_, err = proxy.writeCompatJSON(ctx, upstream, meta, time.Now(), func() {})
					if recorder.Body.Len() != 0 {
						t.Fatalf("JSON path emitted success: %s", recorder.Body.String())
					}
				}
				if !errors.Is(err, errUpstreamStream) || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("stream=%v error=%v", streaming, err)
				}
			}
		})
	}
}

func TestResponsesUpstreamSSEErrorReturnsJSON502(t *testing.T) {
	proxy := &Proxy{history: newResponseHistoryStore()}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	upstream := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("event: error\ndata: {\"message\":\"upstream denied\",\"code\":\"denied\"}\n\ndata: [DONE]\n\n"))}
	_, err := proxy.writeCompatJSON(ctx, upstream, &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "glm-5.3"}, time.Now(), func() {})
	if !errors.Is(err, errUpstreamStream) || ctx.Writer.Written() {
		t.Fatalf("must defer JSON error response until relay: %v, body=%s", err, recorder.Body.String())
	}
	writeRelayFailure(ctx, ProtocolResponses, http.StatusBadGateway, err)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"code":"denied"`) || !strings.Contains(recorder.Body.String(), "upstream denied") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesHistorySkipsIncompleteStream(t *testing.T) {
	store := newResponseHistoryStore()
	proxy := &Proxy{history: store}
	scope := testHistoryScope("session-a")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_partial\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse))}
	_, err := proxy.writeCompatStream(ctx, resp, &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "glm-5.3", historyScope: scope}, time.Now(), func() {})
	if err != nil || !strings.Contains(recorder.Body.String(), "event: response.incomplete") || len(store.entries) != 0 {
		t.Fatalf("incomplete stream err=%v cached=%d body=%s", err, len(store.entries), recorder.Body.String())
	}
}

func TestResponsesUpstreamErrorAfterPartialText(t *testing.T) {
	proxy := &Proxy{history: newResponseHistoryStore()}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\nevent: error\ndata: {\"message\":\"failed later\"}\n\ndata: [DONE]\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse))}
	_, err := proxy.writeCompatStream(ctx, resp, &ChatRequestMeta{Protocol: ProtocolResponses, RequestedModel: "glm-5.3"}, time.Now(), func() {})
	if !errors.Is(err, errUpstreamStream) || !strings.Contains(recorder.Body.String(), "event: response.failed") || strings.Contains(recorder.Body.String(), "event: response.completed") {
		t.Fatalf("partial stream error=%v body=%s", err, recorder.Body.String())
	}
}

func codexHistoryScope(sessionHeader, value, credential string) responseHistoryScope {
	headers := http.Header{"Authorization": []string{credential}}
	headers.Set(sessionHeader, value)
	return historyScopeFor(headers, "", []byte(`{"model":"glm-5.3"}`))
}

func TestResponsesHistoryRecognizesCodexSessionHeaders(t *testing.T) {
	store := newResponseHistoryStore()
	credential := "Bearer private-key"
	scope := codexHistoryScope("session-id", "codex-session-a", credential)
	store.record(testToolResponse("resp_codex_session", testCall("call_codex", "read_file", "function_call")), scope)
	if !scope.hasSession {
		t.Fatal("Codex session-id did not create a session-scoped history key")
	}

	output := []byte(`{"model":"glm-5.3","input":[{"type":"function_call_output","call_id":"call_codex","output":"ok"}]}`)
	if _, err := store.enrich(output, scope); err != nil {
		t.Fatalf("Codex session tool history restore: %v", err)
	}
	if _, err := store.enrich(output, codexHistoryScope("session-id", "codex-session-b", credential)); err == nil {
		t.Fatal("different Codex session restored tool history")
	}
	if _, err := store.enrich(output, codexHistoryScope("session-id", "codex-session-a", "Bearer another-key")); err == nil {
		t.Fatal("same Codex session crossed caller credentials")
	}
}

func TestResponsesHistoryCodexThreadIdentityIsScoped(t *testing.T) {
	store := newResponseHistoryStore()
	credential := "Bearer private-key"
	scope := codexHistoryScope("thread-id", "codex-thread-a", credential)
	store.record(testToolResponse("resp_codex_thread", testCall("call_thread", "read_file", "function_call")), scope)
	if !scope.hasSession {
		t.Fatal("Codex thread-id did not create a scoped history key")
	}

	output := []byte(`{"model":"glm-5.3","input":[{"type":"function_call_output","call_id":"call_thread","output":"ok"}]}`)
	if _, err := store.enrich(output, scope); err != nil {
		t.Fatalf("thread-scoped restore: %v", err)
	}
	if _, err := store.enrich(output, codexHistoryScope("thread-id", "codex-thread-a", "Bearer another-key")); err == nil {
		t.Fatal("thread identity crossed caller credentials")
	}
}
