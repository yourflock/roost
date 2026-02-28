package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds all configuration for the AntBox edge capture daemon.
type Config struct {
	// AntBoxID is the unique identifier for this AntBox instance.
	AntBoxID string
	// ServerURL is the AntServer WebSocket/HTTP endpoint to connect to.
	ServerURL string
	// HeartbeatInterval is how often to send heartbeat reports to the server.
	HeartbeatInterval time.Duration
	// LogLevel controls logging verbosity (debug, info, warn, error).
	LogLevel string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		AntBoxID:          getEnv("ANTBOX_ID", defaultAntBoxID()),
		ServerURL:         getEnv("ANTBOX_SERVER_URL", "ws://localhost:9090"),
		HeartbeatInterval: getEnvDuration("ANTBOX_HEARTBEAT_INTERVAL", 5*time.Second),
		LogLevel:          getEnv("ANTBOX_LOG_LEVEL", "info"),
	}
}

// Validate checks that all required configuration values are present and valid.
func (c *Config) Validate() error {
	if c.AntBoxID == "" {
		return fmt.Errorf("ANTBOX_ID is required")
	}
	if c.ServerURL == "" {
		return fmt.Errorf("ANTBOX_SERVER_URL is required")
	}
	if c.HeartbeatInterval < 1*time.Second {
		return fmt.Errorf("ANTBOX_HEARTBEAT_INTERVAL must be at least 1s, got %s", c.HeartbeatInterval)
	}
	if c.HeartbeatInterval > 60*time.Second {
		return fmt.Errorf("ANTBOX_HEARTBEAT_INTERVAL must be at most 60s, got %s", c.HeartbeatInterval)
	}
	return nil
}

// defaultAntBoxID generates a default AntBox ID from the hostname.
func defaultAntBoxID() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "antbox-unknown"
	}
	return fmt.Sprintf("antbox-%s", hostname)
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}
