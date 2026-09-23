package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestResponsesErrorBodyIsOpenAICompatible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	openaiErrorWithDetails(ctx, http.StatusBadRequest, "bad parameters", "11133", "input", "req_123")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", recorder.Code)
	}
	if recorder.Header().Get("X-Request-Id") != "req_123" {
		t.Fatalf("request id header=%q", recorder.Header().Get("X-Request-Id"))
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	errBody := body["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" || errBody["code"] != "11133" || errBody["param"] != "input" {
		t.Fatalf("error=%v", errBody)
	}
}

func TestParseUpstreamErrorPreservesNativeFields(t *testing.T) {
	info := parseUpstreamError([]byte(`{"error":{"message":"invalid tool","type":"invalid_request_error","code":"tool_error","param":"tools"},"requestId":"req_nested"}`))
	if info.Message != "invalid tool" || info.Type != "invalid_request_error" || info.Code != "tool_error" || info.Param != "tools" || info.RequestID != "req_nested" {
		t.Fatalf("info=%+v", info)
	}
	info = parseUpstreamError([]byte(`{"type":"invalid_request_error","code":11133,"msg":"request parameters rejected","requestId":"req_top"}`))
	if info.Message != "request parameters rejected" || info.Type != "invalid_request_error" || info.Code != "11133" || info.RequestID != "req_top" {
		t.Fatalf("top-level info=%+v", info)
	}
}

func TestUpstreamGatewayStatusPreservesNativeClientErrors(t *testing.T) {
	if got := upstreamGatewayStatus(http.StatusTooManyRequests, []byte(`{"msg":"rate limit"}`)); got != http.StatusTooManyRequests {
		t.Fatalf("429 mapped to %d", got)
	}
	if got := upstreamGatewayStatus(http.StatusBadRequest, []byte(`{"code":11133,"msg":"request parameters were rejected"}`)); got != http.StatusBadRequest {
		t.Fatalf("11133 mapped to %d", got)
	}
	if got := upstreamGatewayStatus(http.StatusBadRequest, []byte(`provider failure`)); got != http.StatusBadGateway {
		t.Fatalf("generic 400 mapped to %d", got)
	}
	if got := upstreamGatewayStatus(http.StatusInternalServerError, []byte(`{"msg":"failed"}`)); got != http.StatusBadGateway {
		t.Fatalf("500 mapped to %d", got)
	}
}
