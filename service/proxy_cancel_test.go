package service

import (
	"context"
	"errors"
	"testing"
)

func TestDownstreamRequestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !downstreamRequestCanceled(ctx, context.Canceled) {
		t.Fatal("canceled downstream request should be recognized")
	}
	if downstreamRequestCanceled(context.Background(), context.Canceled) {
		t.Fatal("an upstream-only cancellation must not be treated as downstream cancellation")
	}
	if downstreamRequestCanceled(ctx, errors.New("dial failed")) {
		t.Fatal("unrelated upstream error must not be hidden by cancellation classification")
	}
}
