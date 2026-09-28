package config

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestZapLogLevelNormalization(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  zapcore.Level
	}{
		{"debug", "debug", zapcore.DebugLevel},
		{"info", "info", zapcore.InfoLevel},
		{"warn", "warn", zapcore.WarnLevel},
		{"error", "error", zapcore.ErrorLevel},
		{"uppercase and spaces", "  WARN ", zapcore.WarnLevel},
		{"empty defaults to info", "", zapcore.InfoLevel},
		{"unknown defaults to info", "verbose", zapcore.InfoLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z := &Zap{Level: tc.input}
			if got := z.LogLevel(); got != tc.want {
				t.Fatalf("LogLevel(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestZapLevelsStartAtConfiguredLevel(t *testing.T) {
	z := &Zap{Level: "warn"}
	levels := z.Levels()
	if len(levels) == 0 {
		t.Fatal("Levels() returned no levels")
	}
	if levels[0] != zapcore.WarnLevel {
		t.Fatalf("first level = %v, want warn", levels[0])
	}
	if last := levels[len(levels)-1]; last != zapcore.FatalLevel {
		t.Fatalf("last level = %v, want fatal", last)
	}

	// 空配置应等同于 info，而不是旧的 debug（旧行为会让最小配置刷出大量日志）。
	empty := (&Zap{}).Levels()
	if empty[0] != zapcore.InfoLevel {
		t.Fatalf("default first level = %v, want info", empty[0])
	}
}
