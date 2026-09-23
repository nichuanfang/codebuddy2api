package service

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type upstreamErrorInfo struct {
	Message   string
	Type      string
	Code      string
	Param     string
	RequestID string
}

func openaiErrorWithDetails(c *gin.Context, status int, msg, code, param, requestID string) {
	openaiErrorWithType(c, status, msg, "", code, param, requestID)
}

func openaiErrorWithType(c *gin.Context, status int, msg, errorType, code, param, requestID string) {
	if strings.TrimSpace(msg) == "" {
		msg = http.StatusText(status)
	}
	if strings.TrimSpace(code) == "" {
		code = "codebuddy_gateway_error"
	}
	if requestID != "" {
		c.Header("X-Request-Id", requestID)
	}
	if strings.TrimSpace(errorType) == "" {
		errorType = openAIErrorType(status)
	}
	errBody := gin.H{
		"message": msg,
		"type":    errorType,
		"code":    code,
		"param":   nil,
	}
	if strings.TrimSpace(param) != "" {
		errBody["param"] = param
	}
	c.JSON(status, gin.H{"error": errBody})
}

func openAIErrorType(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "timeout_error"
	default:
		if status >= 500 {
			return "server_error"
		}
		return "invalid_request_error"
	}
}

func gatewayErrorFromUpstream(c *gin.Context, proto Protocol, status int, fallback string, raw []byte, responseRequestID string) {
	info := parseUpstreamError(raw)
	if info.Message == "" {
		info.Message = fallback
	}
	if info.Code == "" {
		info.Code = "upstream_error"
	}
	if info.RequestID == "" {
		info.RequestID = responseRequestID
	}
	if !proto.IsAnthropic() && info.Type != "" {
		if info.RequestID != "" {
			c.Header("X-Request-Id", info.RequestID)
		}
		openaiErrorWithType(c, status, info.Message, info.Type, info.Code, info.Param, info.RequestID)
		return
	}
	gatewayErrorWithDetails(c, proto, status, info.Message, info.Code, info.Param, info.RequestID)
}

func parseUpstreamError(raw []byte) upstreamErrorInfo {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return upstreamErrorInfo{}
	}
	info := upstreamErrorInfo{
		Message:   firstErrorString(value, "message", "msg", "detail"),
		Type:      firstErrorString(value, "type"),
		Code:      stringifyErrorValue(firstErrorValue(value, "code")),
		Param:     stringifyErrorValue(firstErrorValue(value, "param")),
		RequestID: firstErrorString(value, "request_id", "requestId"),
	}
	if nested, ok := value["error"].(map[string]any); ok {
		if message := firstErrorString(nested, "message", "msg", "detail"); message != "" {
			info.Message = message
		}
		if errorType := firstErrorString(nested, "type"); errorType != "" {
			info.Type = errorType
		}
		if code := stringifyErrorValue(firstErrorValue(nested, "code")); code != "" {
			info.Code = code
		}
		if param := stringifyErrorValue(firstErrorValue(nested, "param")); param != "" {
			info.Param = param
		}
	}
	return info
}

func firstErrorValue(value map[string]any, keys ...string) any {
	for _, key := range keys {
		if item, ok := value[key]; ok && item != nil {
			return item
		}
	}
	return nil
}

func firstErrorString(value map[string]any, keys ...string) string {
	return stringifyErrorValue(firstErrorValue(value, keys...))
}

func stringifyErrorValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
	case json.Number:
		return typed.String()
	}
	return ""
}
