package service

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// CodeBuddy CLI 会在 --model 的 usage 说明里列出当前支持的模型 ID。
// 该目录通常比上游 /v3/config 更贴近用户本机实际可用的 CLI 选择器，
// 因此作为 /v1/models 的第一动态来源，优先于上游实时目录。
const cliModelsTTL = 5 * time.Minute

var cliModelsCache = modelCache{}

// cliModelCommand 是测试注入点；返回 nil 表示本机没有 CodeBuddy CLI。
var cliModelCommand = func() cliModelRunner {
	if path, err := exec.LookPath("codebuddy"); err != nil || path == "" {
		return nil
	}
	return exec.Command("codebuddy", "--help")
}

func parseCLIModelIDs(output string) []string {
	start := strings.Index(output, "--model <model>")
	if start < 0 {
		return nil
	}
	start += len("--model <model>")
	end := strings.Index(output[start:], "--")
	if end < 0 {
		return nil
	}
	description := output[start : start+end]

	const prefix = "Currently supported:"
	pos := strings.Index(description, prefix)
	if pos < 0 {
		return nil
	}
	list := description[pos+len(prefix):]
	list = strings.Trim(strings.TrimSpace(list), "()")
	out := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, raw := range strings.Split(list, ",") {
		id := strings.TrimSpace(raw)
		id = strings.Trim(id, "`\"'")
		if id == "" || id == "<model>" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func listCLIModels(ctx context.Context) ([]openaiModel, error) {
	if time.Now().Before(cliModelsCache.expiresAt) && len(cliModelsCache.items) > 0 {
		items := append([]openaiModel(nil), cliModelsCache.items...)
		return items, nil
	}

	cmd := cliModelCommand()
	if cmd == nil {
		return nil, errCLINotFound
	}
	output, err := runCLIModelCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}
	ids := parseCLIModelIDs(output)
	if len(ids) == 0 {
		return nil, errEmptyCLIModels
	}

	items := make([]openaiModel, 0, len(ids))
	now := time.Now().Unix()
	for _, id := range ids {
		items = append(items, openaiModel{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: "codebuddy",
		})
	}
	cliModelsCache.items = items
	cliModelsCache.expiresAt = time.Now().Add(cliModelsTTL)
	return append([]openaiModel(nil), items...), nil
}

type cliModelRunner interface {
	CombinedOutput() ([]byte, error)
}

func runCLIModelCommand(ctx context.Context, cmd cliModelRunner) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	done := make(chan result, 1)
	go func() {
		output, err := cmd.CombinedOutput()
		done <- result{output: string(output), err: err}
	}()
	select {
	case res := <-done:
		return res.output, res.err
	case <-runCtx.Done():
		return "", runCtx.Err()
	}
}

type result struct {
	output string
	err    error
}
