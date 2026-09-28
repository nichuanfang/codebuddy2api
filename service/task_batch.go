package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"codebuddy-gateway/global"
	"codebuddy-gateway/model"

	"go.uber.org/zap"
)

const (
	batchDefaultConcurrency = 3
	batchMaxConcurrency     = 8
)

type BatchAccountResult struct {
	AccountID uint     `json:"account_id"`
	Account   string   `json:"account"`
	Status    string   `json:"status"`
	Message   string   `json:"message,omitempty"`
	Credit    int64    `json:"credit"`
	Energy    int64    `json:"energy"`
	Claimed   []string `json:"claimed,omitempty"`
}

type BatchState struct {
	ID           string               `json:"id"`
	Running      bool                 `json:"running"`
	StartedAt    time.Time            `json:"started_at"`
	FinishedAt   *time.Time           `json:"finished_at,omitempty"`
	Total        int                  `json:"total"`
	Done         int                  `json:"done"`
	Concurrent   int                  `json:"concurrency"`
	Results      []BatchAccountResult `json:"results"`
	CreditGained int64                `json:"credit_gained"`
	EnergyGained int64                `json:"energy_gained"`
}

type batchRunner struct {
	mu    sync.Mutex
	state *BatchState
}

var defaultBatch = &batchRunner{}

func (b *batchRunner) Run(ctx context.Context, only []string, concurrency int) (*BatchState, error) {
	b.mu.Lock()
	if b.state != nil && b.state.Running {
		b.mu.Unlock()
		return nil, fmt.Errorf("上一批任务仍在执行中，请等待完成后再启动")
	}
	list, err := model.ListAccounts()
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	targets := make([]model.Account, 0, len(list))
	for _, acc := range list {
		if acc.Status != model.AccountStatusDisabled {
			targets = append(targets, acc)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("没有可执行的账号（已全部停用）")
	}
	if concurrency <= 0 {
		concurrency = batchDefaultConcurrency
	}
	if concurrency > batchMaxConcurrency {
		concurrency = batchMaxConcurrency
	}
	results := make([]BatchAccountResult, len(targets))
	for i, acc := range targets {
		results[i] = BatchAccountResult{AccountID: acc.ID, Account: accountDisplayName(&acc), Status: "pending"}
	}
	state := &BatchState{
		ID:         fmt.Sprintf("batch-%d", time.Now().UnixNano()),
		Running:    true,
		StartedAt:  time.Now(),
		Total:      len(targets),
		Concurrent: concurrency,
		Results:    results,
	}
	b.mu.Lock()
	b.state = state
	b.mu.Unlock()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	client := DefaultClient
	for i := range targets {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			b.runOne(ctx, client, idx, targets[idx], only)
		}(i)
	}
	wg.Wait()
	now := time.Now()
	b.mu.Lock()
	state.FinishedAt = &now
	state.Running = false
	b.mu.Unlock()
	return state, nil
}

func (b *batchRunner) Status() *BatchState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

func (b *batchRunner) snapshotLocked() *BatchState {
	if b.state == nil {
		return nil
	}
	cp := *b.state
	cp.Results = append([]BatchAccountResult(nil), b.state.Results...)
	return &cp
}

func (b *batchRunner) runOne(ctx context.Context, client *UpstreamClient, idx int, acc model.Account, only []string) {
	b.setResult(idx, func(r *BatchAccountResult) { r.Status = "running" })
	summary := client.RunAccountTasks(ctx, &acc, only)
	b.setResult(idx, func(r *BatchAccountResult) {
		if summary.Err != "" {
			if isSkipReason(summary.Err) {
				r.Status = "skipped"
			} else {
				r.Status = "failed"
			}
			r.Message = summary.Err
			return
		}
		r.Status = "done"
		r.Credit = summary.CreditGained
		r.Energy = summary.EnergyGained
		r.Claimed = summary.Claimed
		r.Message = fmt.Sprintf("领取 %d 个任务，+%d 分 +%d 能", len(summary.Claimed), summary.CreditGained, summary.EnergyGained)
	})
	b.mu.Lock()
	if b.state != nil {
		b.state.Done++
		b.state.CreditGained += summary.CreditGained
		b.state.EnergyGained += summary.EnergyGained
	}
	b.mu.Unlock()
	global.CORE_LOG.Info("batch task account finished",
		zap.Uint("account_id", acc.ID),
		zap.String("status", summary.Err),
		zap.Int64("credit", summary.CreditGained))
}

func isSkipReason(msg string) bool {
	return strings.Contains(msg, "正在执行") || strings.Contains(msg, "已有任务")
}

func (b *batchRunner) setResult(idx int, fn func(*BatchAccountResult)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == nil || idx < 0 || idx >= len(b.state.Results) {
		return
	}
	fn(&b.state.Results[idx])
}

func RunBatchTasks(ctx context.Context, only []string, concurrency int) (*BatchState, error) {
	return defaultBatch.Run(ctx, only, concurrency)
}

func BatchTasksStatus() *BatchState {
	return defaultBatch.Status()
}
