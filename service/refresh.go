package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"codebuddy-gateway/global"
	"codebuddy-gateway/model"

	"go.uber.org/zap"
)

type Refresher struct {
	client  *UpstreamClient
	running atomic.Bool
	// locks 按账号串行化 RefreshAccount。relay 的 ShouldRefresh、watchdog 与
	// RefreshDueAccounts 三条路径可能同时打到同一个账号；上游 refresh_token
	// 多为一次性，并发刷新会互相使对方的 token 失效，后落库的才是有效的，
	// 偶发地把账号刷成「拿着已失效 refresh_token」的状态。
	locks sync.Map // map[uint]*sync.Mutex
}

func NewRefresher(client *UpstreamClient) *Refresher {
	return &Refresher{client: client}
}

// accountLock 返回该账号的专属互斥锁，调用方负责 Unlock。
func (r *Refresher) accountLock(id uint) *sync.Mutex {
	v, _ := r.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (r *Refresher) RefreshAccount(ctx context.Context, acc *model.Account) error {
	if !global.CORE_CONFIG.Refresh.Enabled {
		return fmt.Errorf("credential refresh is disabled")
	}
	if acc == nil {
		return fmt.Errorf("account is nil")
	}
	if acc.JWT == "" {
		return fmt.Errorf("account %d missing jwt", acc.ID)
	}
	mu := r.accountLock(acc.ID)
	mu.Lock()
	defer mu.Unlock()
	timeout := time.Duration(global.CORE_CONFIG.Refresh.Timeout()) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := r.client.RefreshToken(ctx, acc)
	if err != nil {
		return err
	}

	jwtExp, _ := JWTExpiry(result.AccessToken)
	var jwtExpPtr *time.Time
	if !jwtExp.IsZero() {
		jwtExpPtr = &jwtExp
	}
	var refreshExpPtr *time.Time
	if result.RefreshExpiresIn > 0 {
		t := time.Now().Add(time.Duration(result.RefreshExpiresIn) * time.Second)
		refreshExpPtr = &t
	} else if result.RefreshToken != "" {
		if exp, err := JWTExpiry(result.RefreshToken); err == nil {
			refreshExpPtr = &exp
		}
	}

	if err := model.SaveAccountTokens(acc.ID, result.AccessToken, result.RefreshToken, jwtExpPtr, refreshExpPtr); err != nil {
		return err
	}
	acc.JWT = result.AccessToken
	if result.RefreshToken != "" {
		acc.RefreshToken = result.RefreshToken
	}
	acc.JWTExpiresAt = jwtExpPtr
	acc.RefreshExpiresAt = refreshExpPtr
	now := time.Now()
	acc.LastRefresh = &now
	global.CORE_LOG.Info("codebuddy credential refreshed",
		zap.Uint("account_id", acc.ID),
		zap.String("name", acc.Name),
		zap.Time("jwt_expires_at", jwtExp),
	)
	return nil
}

func (r *Refresher) RefreshDueAccounts(ctx context.Context) (scanned, refreshed, failed int) {
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	defer r.running.Store(false)

	list, err := model.ListRefreshableAccounts()
	if err != nil {
		global.CORE_LOG.Error("list accounts for refresh failed", zap.Error(err))
		return
	}
	threshold := time.Duration(global.CORE_CONFIG.Refresh.Threshold()) * 24 * time.Hour
	for i := range list {
		acc := list[i]
		scanned++
		if acc.JWT == "" {
			continue
		}
		if !ShouldRefresh(acc.JWT, threshold) {
			continue
		}
		if err := r.RefreshAccount(ctx, &acc); err != nil {
			failed++
			global.CORE_LOG.Warn("refresh account failed",
				zap.Uint("account_id", acc.ID),
				zap.String("name", acc.Name),
				zap.Error(err),
			)
			continue
		}
		refreshed++
	}
	global.CORE_LOG.Info("credential refresh round done",
		zap.Int("scanned", scanned),
		zap.Int("refreshed", refreshed),
		zap.Int("failed", failed),
	)
	return
}
