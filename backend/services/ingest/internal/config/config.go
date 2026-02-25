// config.go — Ingest service configuration loaded from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the ingest service.
type Config struct {
	// HTTP
	IngestPort string

	// Database and cache
	PostgresURL string
	RedisURL    string

	// Segment storage
	SegmentDir string

	// Channel management
	ChannelPollInterval time.Duration
	MaxRestarts         int
	RestartWindow       time.Duration
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		IngestPort:          getEnv("INGEST_PORT", "8094"),
		PostgresURL:         getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable"),
		RedisURL:            getEnv("REDIS_URL", "localhost:6379"),
		SegmentDir:          getEnv("SEGMENT_DIR", "/var/roost/segments"),
		ChannelPollInterval: getDuration("CHANNEL_POLL_INTERVAL", 30*time.Second),
		MaxRestarts:         getInt("MAX_RESTARTS", 5),
		RestartWindow:       getDuration("RESTART_WINDOW", 5*time.Minute),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
