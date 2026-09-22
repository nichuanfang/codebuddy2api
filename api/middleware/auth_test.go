package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
)

func TestOpenAIAuthPasswordless(t *testing.T) {
	previous := global.CORE_CONFIG
	defer func() { global.CORE_CONFIG = previous }()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(OpenAIAuth())
	engine.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	global.CORE_CONFIG = config.CORE{}
	global.CORE_CONFIG.Gateway.APIKey = "sk-required"
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("without passwordless expected 401, got %d", response.Code)
	}

	global.CORE_CONFIG.Passwordless.Enabled = true
	request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("with passwordless expected 204, got %d", response.Code)
	}
}
