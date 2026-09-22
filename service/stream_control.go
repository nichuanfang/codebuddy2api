package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	errUpstreamStream  = errors.New("upstream stream failed")
	errDownstreamWrite = errors.New("downstream client disconnected")
	errStreamIdle      = errors.New("upstream stream idle timeout")
)

func wrapUpstreamStream(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(errUpstreamStream, err)
}

func wrapDownstreamWrite(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(errDownstreamWrite, err)
}

// streamIdleWatchdog cancels the upstream request when no bytes have been
// observed for the configured interval. The timer lives in one goroutine so
// stop/reset cannot race with timer ownership.
type streamIdleWatchdog struct {
	touchCh  chan struct{}
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
	mu       sync.Mutex
	timedOut bool
}

func startStreamIdleWatchdog(ctx context.Context, cancel context.CancelFunc, timeout time.Duration) *streamIdleWatchdog {
	if timeout <= 0 {
		return nil
	}
	w := &streamIdleWatchdog{
		touchCh:  make(chan struct{}, 1),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go func() {
		defer close(w.finished)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.done:
				return
			case <-w.touchCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				w.mu.Lock()
				w.timedOut = true
				cancel()
				w.mu.Unlock()
				return
			}
		}
	}()
	return w
}

func (w *streamIdleWatchdog) touch() {
	if w == nil {
		return
	}
	select {
	case w.touchCh <- struct{}{}:
	default:
	}
}

func (w *streamIdleWatchdog) stop() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
	<-w.finished
}

func (w *streamIdleWatchdog) timedOutNow() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.timedOut
}

func streamReadError(err error, watchdog *streamIdleWatchdog, clientCtx context.Context) error {
	if err == nil {
		return nil
	}
	if watchdog != nil && watchdog.timedOutNow() {
		return errors.Join(errUpstreamStream, errStreamIdle, err)
	}
	if clientCtx != nil && clientCtx.Err() != nil {
		return wrapDownstreamWrite(clientCtx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return errors.Join(errUpstreamStream, err)
	}
	return wrapUpstreamStream(err)
}
