package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseCLIModelIDs(t *testing.T) {
	output := `  --model <model>                                  Model for the current session. Please provide the model ID. Currently supported: (hy4-preview-f, hy3, hy3-x, kimi-k3-2, kimi-k3-2)
  --fallback-model <model>                         Enable fallback
`
	got := parseCLIModelIDs(output)
	want := []string{"hy4-preview-f", "hy3", "hy3-x", "kimi-k3-2"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}

	if got := parseCLIModelIDs("unsupported help output"); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestListCLIModelsNotFound(t *testing.T) {
	prevCommand := cliModelCommand
	prevCacheItems := cliModelsCache.items
	prevCacheExpiresAt := cliModelsCache.expiresAt
	t.Cleanup(func() {
		cliModelCommand = prevCommand
		cliModelsCache.items = prevCacheItems
		cliModelsCache.expiresAt = prevCacheExpiresAt
	})

	cliModelCommand = func() cliModelRunner { return nil }
	cliModelsCache.items = nil
	cliModelsCache.expiresAt = time.Time{}

	if _, err := listCLIModels(context.Background()); !errors.Is(err, errCLINotFound) {
		t.Fatalf("err=%v want errCLINotFound", err)
	}
}

func TestListCLIModelsParsesAndCaches(t *testing.T) {
	prevCommand := cliModelCommand
	prevCacheItems := cliModelsCache.items
	prevCacheExpiresAt := cliModelsCache.expiresAt
	t.Cleanup(func() {
		cliModelCommand = prevCommand
		cliModelsCache.items = prevCacheItems
		cliModelsCache.expiresAt = prevCacheExpiresAt
	})

	var calls int
	cliModelCommand = func() cliModelRunner {
		runner := cliFakeRunner{output: []byte("--model <model> help Currently supported: (glm-5.3-flashx, kimi-k3-2) --fallback")}
		calls++
		return runner
	}
	cliModelsCache.items = nil
	cliModelsCache.expiresAt = time.Time{}

	first, err := listCLIModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "glm-5.3-flashx" || first[1].ID != "kimi-k3-2" {
		t.Fatalf("unexpected models: %#v", first)
	}

	second, err := listCLIModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(second[0].ID, first[0].ID) || calls != 1 {
		t.Fatalf("cache miss or calls=%d", calls)
	}
}

type cliFakeRunner struct {
	output []byte
	err    error
}

func (r cliFakeRunner) CombinedOutput() ([]byte, error) {
	return r.output, r.err
}
