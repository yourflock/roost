// Package recovery provides panic recovery and retry helpers for AntBox (T-7H.1.006).
// The existing backoff.go provides exponential backoff via Retrier.
// This file adds WithRetry — a simpler counted-retry wrapper with panic recovery.
package recovery

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/sirupsen/logrus"
)

// WithRetry wraps a function with retry logic and panic recovery.
// It retries up to maxRetries times with quadratic backoff (attempt² seconds).
// A nil logger is safe — log calls are skipped when logger is nil.
func WithRetry(ctx context.Context, log *logrus.Logger, name string, maxRetries int, fn func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * time.Second
			if log != nil {
				log.WithFields(logrus.Fields{
					"operation":  name,
					"attempt":    attempt,
					"backoff":    backoff.String(),
					"last_error": lastErr,
				}).Warn("retrying after backoff")
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					if log != nil {
						log.WithFields(logrus.Fields{
							"operation": name,
							"panic":     fmt.Sprintf("%v", r),
							"stack":     string(stack),
						}).Error("panic recovered")
					}
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			err = fn(ctx)
		}()

		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("operation %s failed after %d retries: %w", name, maxRetries, lastErr)
}
