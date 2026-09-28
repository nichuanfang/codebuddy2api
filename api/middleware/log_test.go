package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAccessLogSetsAndPreservesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(AccessLog())
	engine.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	for _, tc := range []struct {
		name string
		id   string
	}{
		{name: "generated"},
		{name: "provided", id: "client-request-123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tc.id != "" {
				req.Header.Set("X-Request-Id", tc.id)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)

			got := recorder.Header().Get("X-Request-Id")
			if got == "" {
				t.Fatal("response is missing X-Request-Id")
			}
			if tc.id != "" && got != tc.id {
				t.Fatalf("X-Request-Id = %q, want %q", got, tc.id)
			}
		})
	}
}
