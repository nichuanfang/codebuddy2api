package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"codebuddy-gateway/api/handler"
	"codebuddy-gateway/api/middleware"
	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
)

type APIServer struct {
	Listen string
	srv    *http.Server
}

func NewAPIServer() *APIServer {
	return &APIServer{Listen: global.CORE_CONFIG.System.ListenAddr}
}

func (b *APIServer) ServerRun() {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery(), corsMiddleware(), middleware.AccessLog())

	handler.RegisterRoutes(engine)

	b.srv = &http.Server{
		Addr:              b.Listen,
		Handler:           engine,
		ReadHeaderTimeout: 15 * time.Second,
	}

	global.CORE_LOG.Info("server starting on " + b.Listen)
	if err := b.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		global.CORE_LOG.Fatal("listen: " + err.Error())
	}
}

func (b *APIServer) ServerShutdown() {
	if b.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.srv.Shutdown(ctx)
	global.CORE_LOG.Info("server shutdown successfully")
}

func corsMiddleware() gin.HandlerFunc {
	allowed := map[string]struct{}{}
	for _, o := range global.CORE_CONFIG.System.AllowedOrigins {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		_, whitelisted := allowed[origin]
		if origin != "" && (len(allowed) == 0 || whitelisted) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		} else if origin == "" {
			c.Header("Access-Control-Allow-Origin", "*")
		}
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Token, X-Token, api-key, x-api-key, X-Api-Key, anthropic-version, anthropic-beta, anthropic-dangerous-direct-browser-access, openai-beta, OpenAI-Beta")
		c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, DELETE, PUT")
		// credentials 只在白名单命中时才允许。无条件反射 Origin + Allow-Credentials: true
		// 等于告诉浏览器「任何网站都能带凭据跨域调我」，passwordless 部署时是开口。
		if whitelisted {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
