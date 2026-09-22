package service

import (
	"bytes"
	"context"
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

func TestSSEDecoderSupportsCRLFMultilineAndImplicitEvents(t *testing.T) {
	raw := "data: {\"choices\":[\r\n" +
		"data: {\"delta\":{\"content\":\"hi\"}}]}\r\n\r\n" +
		"data: [DONE]\r\n"
	decoder := newSSEDecoder(strings.NewReader(raw))
	event, err := decoder.next()
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Chunk == nil || !chunkHasGeneratedToken(event.Chunk) {
		t.Fatalf("unexpected first event: %#v", event)
	}
	event, err = decoder.next()
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || !event.Done {
		t.Fatalf("expected done event, got %#v", event)
	}
}

func TestCollectSSERejectsPrematureEOF(t *testing.T) {
	_, err := collectSSE(strings.NewReader(`data: {"choices":[{"delta":{"content":"partial"}}]}`), "model")
	if err == nil || !strings.Contains(err.Error(), "normal termination") {
		t.Fatalf("expected missing termination error, got %v", err)
	}
}

func TestCollectSSEAcceptsFinishReasonEOF(t *testing.T) {
	result, err := collectSSE(strings.NewReader(`data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`), "model")
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || result.FinishReason != "stop" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSSEDecoderRejectsMalformedAndOversizedEvents(t *testing.T) {
	if _, err := collectSSE(strings.NewReader("data: {bad}\n\n"), "model"); err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("expected malformed JSON error, got %v", err)
	}
	tooLarge := "data: {\"x\":\"" + strings.Repeat("a", maxSSEEventBytes) + "\"}\n\n"
	if _, err := collectSSE(strings.NewReader(tooLarge), "model"); err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("expected oversized event error, got %v", err)
	}
}

func TestResponsesLengthUsesIncompleteEvent(t *testing.T) {
	var buf bytes.Buffer
	ad := newStreamAdapter(ProtocolResponses, &buf, nil, "model")
	ad.start()
	ad.onText("partial")
	ad.onFinishReason("length")
	if err := ad.finish(); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "event: response.incomplete") {
		t.Fatalf("missing incomplete event: %s", raw)
	}
	if strings.Contains(raw, "event: response.completed") {
		t.Fatalf("length response must not be completed: %s", raw)
	}
	if !strings.Contains(raw, `"status":"incomplete"`) || !strings.Contains(raw, `"reason":"max_output_tokens"`) {
		t.Fatalf("missing incomplete details: %s", raw)
	}
}

func TestResponsesReasoningSegmentsRemainInFinalOutput(t *testing.T) {
	var buf bytes.Buffer
	ad := newStreamAdapter(ProtocolResponses, &buf, nil, "model")
	ad.start()
	ad.onReasoning("first")
	ad.onText("answer")
	ad.onReasoning("second")
	ad.onFinishReason("stop")
	if err := ad.finish(); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.Contains(raw, `"text":"first"`) || !strings.Contains(raw, `"text":"second"`) {
		t.Fatalf("reasoning segments were lost: %s", raw)
	}
}

func TestResponsesPrematureEOFEmitsFailedNotCompleted(t *testing.T) {
	var buf bytes.Buffer
	ad := newStreamAdapter(ProtocolResponses, &buf, nil, "model")
	ad.start()
	ad.onText("partial")
	if err := ad.fail(errSSEMissingEnd); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "event: response.failed") {
		t.Fatalf("missing failed event: %s", raw)
	}
	if strings.Contains(raw, "event: response.completed") {
		t.Fatalf("premature EOF must not be completed: %s", raw)
	}
	if !strings.Contains(raw, `"code":"upstream_stream_error"`) {
		t.Fatalf("missing failure code: %s", raw)
	}
}

func TestWriteCompatStreamPrematureEOFEmitsProtocolFailure(t *testing.T) {
	old := global.CORE_CONFIG.Gateway
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, StreamIdleTimeoutSeconds: 1}
	defer func() { global.CORE_CONFIG.Gateway = old }()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`data: {"choices":[{"delta":{"content":"partial"}}]}`,
	))}
	proxy := &Proxy{}
	_, err := proxy.writeCompatStream(ctx, resp, &ChatRequestMeta{
		Protocol: ProtocolResponses, RequestedModel: "model", ClientStream: true,
	}, time.Now(), func() {})
	if err == nil || !strings.Contains(err.Error(), "normal termination") {
		t.Fatalf("expected stream termination error, got %v", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed") || strings.Contains(body, "event: response.completed") {
		t.Fatalf("unexpected protocol output: %s", body)
	}
}

func TestStreamIdleWatchdogCancelsAndStops(t *testing.T) {
	ctx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	ctx, cancel := context.WithCancel(ctx)
	watchdog := startStreamIdleWatchdog(ctx, cancel, 5*time.Millisecond)
	if watchdog == nil {
		t.Fatal("watchdog is nil")
	}
	defer watchdog.stop()
	deadline := time.Now().Add(time.Second)
	for !watchdog.timedOutNow() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !watchdog.timedOutNow() {
		t.Fatal("watchdog did not time out")
	}
	if ctx.Err() == nil {
		t.Fatal("watchdog did not cancel upstream context")
	}
}
