// Package zerolog provides a privacy-safe logger for Roost stream endpoints.
//
// Zero-logging policy: stream delivery endpoints must never write subscriber
// identity, IP addresses, tokens, or session data to any log sink. This
// package enforces that policy via an allowlist of permitted log fields.
//
// Usage:
//
//	sl := zerolog.New("relay")
//	sl.Log(zerolog.Fields{
//	    "status":      200,
//	    "duration_ms": 45,
//	    "path_prefix": "/hls/",
//	})
//
// Any attempt to log a blocked field (ip, subscriber_id, token, etc.) is
// silently dropped. The function never panics — a misconfigured call logs
// nothing rather than leaking PII.
package zerolog

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
)

// SafeLogger wraps logrus and enforces the PII allowlist on every log call.
// The zero-value is not usable — create instances via New().
type SafeLogger struct {
	entry *logrus.Entry
}

// New creates a SafeLogger for the named service component.
// The service name is embedded in every log line and is always permitted
// (it is a system identifier, not subscriber data).
//
//	sl := zerolog.New("relay")
//	sl := zerolog.New("hls-ingest")
func New(service string) *SafeLogger {
	log := logrus.New()
	log.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
	})
	log.SetOutput(os.Stdout)

	levelStr := os.Getenv("LOG_LEVEL")
	level, err := logrus.ParseLevel(levelStr)
	if err != nil || levelStr == "" {
		level = logrus.InfoLevel
	}
	log.SetLevel(level)

	return &SafeLogger{
		entry: log.WithField("service", service),
	}
}

// Fields is an alias for a map of log fields. Only fields in the allowlist
// (see fields.go) will be written; all others are dropped silently.
type Fields = map[string]interface{}

// Log writes a structured log line at INFO level.
// Only permitted fields (see PermittedFields in fields.go) are included.
// Blocked fields are silently dropped — no warning is emitted.
func (l *SafeLogger) Log(fields Fields) {
	l.entry.WithFields(sanitize(fields)).Info("stream")
}

// LogError writes a structured log line at ERROR level with an error message.
// The error string is included under the "error" key (which is in the allowlist).
// All other fields are sanitized per the standard policy.
func (l *SafeLogger) LogError(err error, fields Fields) {
	if err == nil {
		l.Log(fields)
		return
	}
	merged := make(Fields, len(fields)+1)
	for k, v := range fields {
		merged[k] = v
	}
	merged["error"] = err.Error()
	l.entry.WithFields(sanitize(merged)).Error("stream_error")
}

// LogWarn writes a structured log line at WARN level.
func (l *SafeLogger) LogWarn(message string, fields Fields) {
	merged := make(Fields, len(fields)+1)
	for k, v := range fields {
		merged[k] = v
	}
	merged["message"] = message
	l.entry.WithFields(sanitize(merged)).Warn("stream_warn")
}

// sanitize filters the provided fields map to only permitted keys.
// Returns a new map — never mutates the input.
func sanitize(fields Fields) logrus.Fields {
	safe := make(logrus.Fields, len(fields))
	for k, v := range fields {
		if isPermitted(k) {
			safe[k] = v
		}
		// Silently drop blocked fields — no warning to avoid log spam.
	}
	return safe
}

// Sprintf-style helper for building log messages without PII interpolation.
// The format string itself must not contain PII — this is a developer aid.
func Sprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
