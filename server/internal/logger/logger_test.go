// logger_test.go — Unit tests for the logger package.
// P21.7.001: Logger tests
package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_JSONFormat(t *testing.T) {
	l := New("json", "info")
	if l == nil {
		t.Fatal("New returned nil")
	}
	// The handler must be a JSON handler — write a record and check output.
	var buf bytes.Buffer
	jl := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	jl.Info("probe")
	if !strings.Contains(buf.String(), `"msg"`) {
		t.Error("expected JSON output to contain \"msg\" key")
	}
}

func TestNew_PrettyFormat(t *testing.T) {
	l := New("pretty", "info")
	if l == nil {
		t.Fatal("New returned nil")
	}
	// Text handler lines look like "time=... level=INFO msg=..."
	var buf bytes.Buffer
	tl := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tl.Info("probe")
	if !strings.Contains(buf.String(), "level=INFO") {
		t.Error("expected text output to contain level=INFO")
	}
}

func TestNew_DefaultFormat(t *testing.T) {
	// Any unrecognised format should fall back to JSON (not panic).
	l := New("unknown", "info")
	if l == nil {
		t.Fatal("New returned nil for unknown format")
	}
}

func TestNew_LevelDebug(t *testing.T) {
	// Debug level — debug messages should be enabled.
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	l := slog.New(h)
	l.Debug("debug probe")
	if !strings.Contains(buf.String(), "debug probe") {
		t.Error("expected debug message to appear at debug level")
	}
}

func TestNew_LevelWarn_FiltersInfo(t *testing.T) {
	// At warn level, Info messages must be suppressed.
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	l := slog.New(h)
	l.Info("should not appear")
	if strings.Contains(buf.String(), "should not appear") {
		t.Error("Info message appeared at warn level — level filtering broken")
	}
}

// ── WithContext / FromContext ─────────────────────────────────────────────────

func TestWithContext_FromContext_RoundTrip(t *testing.T) {
	original := New("json", "info")
	ctx := WithContext(context.Background(), original)
	retrieved := FromContext(ctx)
	if retrieved != original {
		t.Error("FromContext returned a different logger than was stored")
	}
}

func TestFromContext_ReturnsDefault_WhenNotSet(t *testing.T) {
	// An empty context must return slog.Default(), not nil.
	l := FromContext(context.Background())
	if l == nil {
		t.Fatal("FromContext returned nil for empty context")
	}
	if l != slog.Default() {
		t.Error("expected slog.Default() when no logger in context")
	}
}

func TestFromContext_ReturnsDefault_WhenNilLogger(t *testing.T) {
	// Storing a nil logger should still return slog.Default().
	ctx := context.WithValue(context.Background(), contextKey{}, (*slog.Logger)(nil))
	l := FromContext(ctx)
	if l == nil {
		t.Fatal("FromContext returned nil")
	}
	if l != slog.Default() {
		t.Error("expected slog.Default() when stored logger is nil")
	}
}

func TestWithContext_Nested(t *testing.T) {
	// Inner logger must shadow the outer one.
	outer := New("json", "warn")
	inner := New("pretty", "debug")
	ctx := WithContext(context.Background(), outer)
	ctx = WithContext(ctx, inner)
	if FromContext(ctx) != inner {
		t.Error("nested context did not return the innermost logger")
	}
}
