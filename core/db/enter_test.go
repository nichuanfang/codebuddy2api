package db

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGormLoggingDefaultsAreSafe(t *testing.T) {
	if got := getLogMode(""); got != logger.Error {
		t.Fatalf("empty log mode = %v, want error", got)
	}
	if got := getLogMode("unknown"); got != logger.Error {
		t.Fatalf("unknown log mode = %v, want error", got)
	}
	cfg := gormLoggerConfig("info")
	if cfg.LogLevel != logger.Info {
		t.Fatalf("configured log level = %v, want info", cfg.LogLevel)
	}
	if !cfg.ParameterizedQueries {
		t.Fatal("GORM logs must omit SQL parameter values")
	}
	if !cfg.IgnoreRecordNotFoundError {
		t.Fatal("record-not-found noise should be suppressed")
	}
}

func TestGormLoggerDoesNotPrintBoundValues(t *testing.T) {
	var output bytes.Buffer
	gormLogger := logger.New(log.New(&output, "", 0), gormLoggerConfig("info"))
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormLogger})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("CREATE TABLE secrets (value TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	const secret = "sk-sensitive-value-must-not-appear"
	if err := database.Exec("INSERT INTO secrets(value) VALUES (?)", secret).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("GORM log leaked a bound value: %s", output.String())
	}
}
