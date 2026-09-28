package handler

import (
	"codebuddy-gateway/api/handler/openai"
	"codebuddy-gateway/api/handler/ping"
	"codebuddy-gateway/api/middleware"
	"net/http"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(engine *gin.Engine) {
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"code": http.StatusNotFound, "msg": "route not found"})
	})
	engine.GET("/healthz", ping.Healthz)

	v1 := engine.Group("/v1")
	v1.Use(middleware.OpenAIAuth())
	openai.RegisterRoutes(v1)
}
