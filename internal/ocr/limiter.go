package ocr

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

var (
	limiterMu  sync.RWMutex
	ocrLimiter *semaphore.Weighted
)

func SetConcurrencyLimit(max int64) {
	limiterMu.Lock()
	defer limiterMu.Unlock()
	if max <= 0 {
		ocrLimiter = nil
		return
	}
	ocrLimiter = semaphore.NewWeighted(max)
}

func withConcurrencyLimit(ctx context.Context, fn func() (OCRResponse, error)) (OCRResponse, error) {
	limiterMu.RLock()
	limiter := ocrLimiter
	limiterMu.RUnlock()
	if limiter == nil {
		return fn()
	}
	if err := limiter.Acquire(ctx, 1); err != nil {
		return OCRResponse{}, err
	}
	defer limiter.Release(1)
	return fn()
}
