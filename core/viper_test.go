package core

import (
	"os"
	"path/filepath"
	"testing"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"
)

func TestViperLoadsConfiguredLogLevel(t *testing.T) {
	t.Setenv("GATEWAY_LOG_LEVEL", "")
	t.Setenv("LOG_LEVEL", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("system:\n  db: sqlite\nzap:\n  level: warn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := global.CORE_CONFIG
	t.Cleanup(func() { global.CORE_CONFIG = previous })
	global.CORE_CONFIG = config.CORE{}
	Viper(path)

	if got := global.CORE_CONFIG.Zap.Level; got != "warn" {
		t.Fatalf("configured zap.level = %q, want warn", got)
	}
	if got := global.CORE_CONFIG.Zap.Director; got != "log" {
		t.Fatalf("default zap.director = %q, want log", got)
	}
}

func TestViperEnvironmentOverridesConfiguredLogLevel(t *testing.T) {
	t.Setenv("GATEWAY_LOG_LEVEL", "debug")
	t.Setenv("LOG_LEVEL", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("system:\n  db: sqlite\nzap:\n  level: warn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := global.CORE_CONFIG
	t.Cleanup(func() { global.CORE_CONFIG = previous })
	global.CORE_CONFIG = config.CORE{}
	Viper(path)

	if got := global.CORE_CONFIG.Zap.Level; got != "debug" {
		t.Fatalf("environment zap.level = %q, want debug", got)
	}
}
