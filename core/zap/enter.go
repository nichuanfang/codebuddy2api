package zap

import (
	"fmt"
	"os"

	"codebuddy-gateway/global"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Init 初始化 Zap 日志，对外唯一入口。
func Init() *zap.Logger {
	director := global.CORE_CONFIG.Zap.Director
	if director == "" {
		director = "log"
	}
	if err := os.MkdirAll(director, os.ModePerm); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create log directory %q failed: %v\n", director, err)
	}

	levels := global.CORE_CONFIG.Zap.Levels()
	cores := make([]zapcore.Core, 0, len(levels))
	for i := range levels {
		cores = append(cores, newZapCore(levels[i]))
	}

	logger := zap.New(zapcore.NewTee(cores...))
	if global.CORE_CONFIG.Zap.ShowLine {
		logger = logger.WithOptions(zap.AddCaller())
	}
	return logger
}
