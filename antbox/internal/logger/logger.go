// Package logger provides a structured logger for AntBox built on logrus (T-7H.1.005).
// Log rotation is provided by the internal/logrotate package (no external lumberjack dep needed).
package logger

import (
	"io"
	"os"

	"antbox/internal/logrotate"

	"github.com/sirupsen/logrus"
)

// Config holds logger configuration.
type Config struct {
	Level      string // debug, info, warn, error
	Format     string // json, text
	FilePath   string // empty = stderr only
	MaxSizeMB  int    // rotate at this size (default 100)
	MaxAgeDays int    // keep logs this many days (default 7)
	MaxBackups int    // keep this many rotated files (default 3)
}

// New creates a logrus.Logger with optional file rotation via internal/logrotate.
// If FilePath is set the logger writes to a rotating file; otherwise it writes to stderr.
func New(cfg Config) *logrus.Logger {
	log := logrus.New()

	// Set level.
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	log.SetLevel(level)

	// Set formatter.
	if cfg.Format == "text" {
		log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	} else {
		log.SetFormatter(&logrus.JSONFormatter{})
	}

	// Set output.
	if cfg.FilePath != "" {
		maxSizeMB := cfg.MaxSizeMB
		if maxSizeMB <= 0 {
			maxSizeMB = 100
		}
		maxBackups := cfg.MaxBackups
		if maxBackups <= 0 {
			maxBackups = 3
		}

		// Use internal logrotate (no external deps).
		// Derive LogDir from FilePath by using the directory portion.
		dir := dirOf(cfg.FilePath)
		prefix := prefixOf(cfg.FilePath)

		rotCfg := logrotate.Config{
			LogDir:       dir,
			MaxSizeBytes: int64(maxSizeMB) * 1024 * 1024,
			MaxFiles:     maxBackups,
			FilePrefix:   prefix,
		}

		rotator, rotErr := logrotate.New(rotCfg)
		if rotErr != nil {
			// Fall back to stderr and log the setup error.
			log.WithError(rotErr).Warn("failed to open log file — falling back to stderr")
			log.SetOutput(os.Stderr)
		} else {
			// Write to both file and stderr during startup so early messages are visible.
			log.SetOutput(io.MultiWriter(rotator, os.Stderr))
		}
	} else {
		log.SetOutput(os.Stderr)
	}

	return log
}

// dirOf returns the directory portion of a file path.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// prefixOf returns the filename stem (no directory, no extension).
func prefixOf(path string) string {
	base := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			base = path[i+1:]
			break
		}
	}
	// Strip extension.
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '.' {
			return base[:i]
		}
	}
	return base
}
