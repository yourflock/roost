// Package recovery provides exponential backoff error recovery for AntBox (T-7H.1.002).
package recovery

import (
	"context"
	"math"
	"time"

	"github.com/sirupsen/logrus"
)

// BackoffConfig configures the exponential backoff strategy.
type BackoffConfig struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	MaxAttempts     int
	ResetAfter      time.Duration
}

// DefaultBackoff returns the standard AntBox reconnection backoff (1s → 30s).
func DefaultBackoff() BackoffConfig {
	return BackoffConfig{
		InitialInterval: 1 * time.Second,
		MaxInterval:     30 * time.Second,
		Multiplier:      2.0,
		MaxAttempts:     0, // infinite
		ResetAfter:      5 * time.Minute,
	}
}

// Retrier executes a function with exponential backoff.
type Retrier struct {
	cfg    BackoffConfig
	logger *logrus.Logger
}

// NewRetrier creates a Retrier.
func NewRetrier(cfg BackoffConfig, logger *logrus.Logger) *Retrier {
	return &Retrier{cfg: cfg, logger: logger}
}

// Run calls fn until it returns nil or ctx is cancelled.
func (r *Retrier) Run(ctx context.Context, opName string, fn func(ctx context.Context) error) error {
	attempt := 0
	interval := r.cfg.InitialInterval

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := fn(ctx); err == nil {
			return nil
		} else {
			attempt++
			r.logger.WithFields(logrus.Fields{
				"operation": opName,
				"attempt":   attempt,
				"next_wait": interval.String(),
				"error":     err.Error(),
			}).Warn("operation failed, retrying")

			if r.cfg.MaxAttempts > 0 && attempt >= r.cfg.MaxAttempts {
				return err
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}

		nextInterval := float64(interval) * r.cfg.Multiplier
		interval = time.Duration(math.Min(nextInterval, float64(r.cfg.MaxInterval)))
	}
}

// RunForever loops until ctx is cancelled, backing off on each failure.
func (r *Retrier) RunForever(ctx context.Context, opName string, fn func(ctx context.Context) error) {
	interval := r.cfg.InitialInterval
	attempt := 0

	for ctx.Err() == nil {
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			attempt++
			r.logger.WithFields(logrus.Fields{
				"operation": opName,
				"attempt":   attempt,
				"next_wait": interval.String(),
				"error":     err.Error(),
			}).Warn("operation failed, will retry")

			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}

			nextInterval := float64(interval) * r.cfg.Multiplier
			interval = time.Duration(math.Min(nextInterval, float64(r.cfg.MaxInterval)))
		} else {
			interval = r.cfg.InitialInterval
			attempt = 0
		}
	}
}
