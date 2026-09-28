package core

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"codebuddy-gateway/global"

	"github.com/spf13/viper"
)

func Viper(path ...string) *viper.Viper {
	config := "config.yaml"
	if len(path) > 0 && strings.TrimSpace(path[0]) != "" {
		config = strings.TrimSpace(path[0])
	}

	loadDotEnv(".env")

	v := viper.New()
	v.SetConfigFile(config)
	v.SetConfigType("yaml")
	v.SetDefault("passwordless.enabled", false)

	// 日志默认配置：级别 info、写 log/ 目录、保留 30 天。
	// 只需在 config.yaml 的 zap.level 改一处即可调整日志详细程度。
	setZapDefaults(v)

	_ = v.BindEnv("passwordless.enabled", "GATEWAY_PASSWORDLESS", "PASSWORDLESS_ENABLED")
	if err := v.ReadInConfig(); err != nil {
		panic(fmt.Errorf("Fatal error config file: %s \n", err))
	}

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	_ = v.BindEnv("gateway.api-key", "GATEWAY_API_KEY", "API_KEY")
	_ = v.BindEnv("system.listenAddr", "GATEWAY_LISTEN", "LISTEN_ADDR")

	// 允许用环境变量临时调高/调低日志级别，无需改配置文件。
	_ = v.BindEnv("zap.level", "GATEWAY_LOG_LEVEL", "LOG_LEVEL")
	_ = v.BindEnv("zap.director", "GATEWAY_LOG_DIR")
	_ = v.BindEnv("zap.log-in-console", "GATEWAY_LOG_CONSOLE")

	if err := v.Unmarshal(&global.CORE_CONFIG); err != nil {
		panic(err)
	}
	return v
}

// setZapDefaults 设置日志相关默认值，避免最小配置（只有 system/gateway）
// 时日志既不落盘也不输出，排障时看不到任何信息。
func setZapDefaults(v *viper.Viper) {
	v.SetDefault("zap.level", "info")
	v.SetDefault("zap.format", "console")
	v.SetDefault("zap.director", "log")
	v.SetDefault("zap.encode-level", "LowercaseLevelEncoder")
	v.SetDefault("zap.stacktrace-key", "stacktrace")
	v.SetDefault("zap.retention-day", 30)
	// 后台服务默认不往控制台刷日志（发行包会重定向到 log/*.log），
	// 需要前台观察时把 zap.log-in-console 或环境变量设为 true。
	v.SetDefault("zap.log-in-console", false)
	v.SetDefault("zap.show-line", true)
}

func ApplyKeyOverrides(apiKey string) {
	if v := strings.TrimSpace(apiKey); v != "" {
		global.CORE_CONFIG.Gateway.APIKey = v
	}
}

// ApplyLogLevelOverrides 允许命令行参数覆盖日志级别（优先级高于配置与环境变量）。
func ApplyLogLevelOverrides(level string) {
	if v := strings.TrimSpace(level); v != "" {
		global.CORE_CONFIG.Zap.Level = v
	}
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[7:])
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
