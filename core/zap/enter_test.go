package zap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"
)

func TestInitFiltersLogsBelowConfiguredLevel(t *testing.T) {
	previous := global.CORE_CONFIG
	t.Cleanup(func() { global.CORE_CONFIG = previous })

	dir := t.TempDir()
	global.CORE_CONFIG = config.CORE{
		Zap: config.Zap{
			Level:        "warn",
			Director:     dir,
			Format:       "console",
			RetentionDay: 30,
		},
	}

	dateDir := time.Now().Format(time.DateOnly)
	logger := Init()
	logger.Debug("filtered-debug")
	logger.Info("filtered-info")
	logger.Warn("visible-warning")
	if err := logger.Sync(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, dateDir, "warn.log"))
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if !strings.Contains(output, "visible-warning") {
		t.Fatalf("warn log does not contain expected message: %q", output)
	}
	if strings.Contains(output, "filtered-debug") || strings.Contains(output, "filtered-info") {
		t.Fatalf("warn log contains messages below configured level: %q", output)
	}

	for _, name := range []string{"debug.log", "info.log"} {
		if _, err := os.Stat(filepath.Join(dir, dateDir, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s created below configured warn level", name)
		}
	}
}
