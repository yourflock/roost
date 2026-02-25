// Package logger provides structured logging using stdlib log/slog.
// P21.1.001: Structured Logging Package
//
// This package is separate from pkg/logging (logrus-based, used by older services)
// and is used by all new P21+ services. It wraps log/slog with:
//   - JSON output (production) or text output (dev/pretty)
//   - Context propagation (inject logger into context, retrieve it later)
//   - Configurable log levels
//
// Usage:
//
//	log := logger.New("json", "info")
//	ctx  := logger.WithContext(ctx, log)
//	// ... later:
//	logger.FromContext(ctx).Info("request complete", "status", 200)
package logger

import (
	"context"
	"log/slog"
	"os"
)

// contextKey is an unexported type for context keys in this package.
type contextKey struct{}

// New creates a *slog.Logger with the given format and level.
//
// format: "json" (default, production) or "pretty" (text, development).
// level:  "debug", "info" (default), "warn", "error".
//
// The logger always writes to os.Stdout. Source file/line is included
// via AddSource: true so log lines are greppable to exact callsites.
func New(format, level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: true,
	}

	if format == "pretty" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// WithContext returns a new context that carries the given logger.
// Retrieve it with FromContext.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

// FromContext returns the logger stored in ctx by WithContext.
// If no logger is present (or ctx is nil), it returns slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
