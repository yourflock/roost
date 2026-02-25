// logger.go — Shared structured logging package for all Roost services.
// P16-T01: Structured Logging & Audit Trail
//
// Usage:
//
//	log := logging.NewLogger("billing")
//	log.WithField("subscriber_id", id).Info("checkout complete")
package logging

import (
	"os"

	"github.com/sirupsen/logrus"
)

// NewLogger creates a new logrus logger pre-configured for a named service.
// Output is JSON to stdout. Log level is controlled by LOG_LEVEL env var
// (default: info). The service field is embedded in every log line.
func NewLogger(service string) *logrus.Entry {
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

	return log.WithField("service", service)
}
