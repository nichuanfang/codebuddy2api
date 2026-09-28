package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codebuddy-gateway/global"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AccessLog 用统一的结构化日志记录每个 HTTP 请求。
//
// 相比 gin.Logger() 的默认文本输出，这里做到三点：
//   - 走 global.CORE_LOG，级别与切片策略和核心代码一致，方便按 zap.level 过滤；
//   - 跳过 /healthz 与预检 OPTIONS，避免探活把日志刷满；
//   - 5xx 记为 error、4xx 记为 warn，正常请求记为 info，便于直接按级别排查。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		skipLog := path == "/healthz" || c.Request.Method == http.MethodOptions
		requestID := ""
		if !skipLog {
			requestID = requestIDFor(c)
		}
		start := time.Now()
		c.Next()

		if skipLog || global.CORE_LOG == nil {
			return
		}

		latency := time.Since(start)
		status := c.Writer.Status()
		fields := []zap.Field{
			zap.String("request_id", requestID),
			zap.Int("status", status),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("client_ip", c.ClientIP()),
			zap.Duration("latency", latency),
			zap.Int("bytes", c.Writer.Size()),
		}
		if errors := c.Errors.ByType(gin.ErrorTypePrivate).String(); errors != "" {
			fields = append(fields, zap.String("errors", strings.TrimSpace(errors)))
		}

		switch {
		case status >= http.StatusInternalServerError:
			global.CORE_LOG.Error("http request", fields...)
		case status >= http.StatusBadRequest:
			global.CORE_LOG.Warn("http request", fields...)
		default:
			global.CORE_LOG.Info("http request", fields...)
		}
	}
}

func requestIDFor(c *gin.Context) string {
	requestID := strings.TrimSpace(c.GetHeader("X-Request-Id"))
	if requestID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err == nil {
			requestID = "req_" + hex.EncodeToString(raw[:])
		} else {
			requestID = "req_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
	}
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	c.Request.Header.Set("X-Request-Id", requestID)
	c.Header("X-Request-Id", requestID)
	return requestID
}
