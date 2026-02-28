// Package config provides both env-var (Config/Load) and YAML-file (YAMLConfig/LoadYAML)
// configuration for the AntBox daemon.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// YAMLConfig is the top-level AntBox daemon configuration loaded from a YAML file.
// It supplements the existing env-var Config with structured file-based configuration.
type YAMLConfig struct {
	Server  YAMLServerConfig  `yaml:"server"`
	Owl     YAMLOwlConfig     `yaml:"owl"`
	Tuners  YAMLTunerConfig   `yaml:"tuners"`
	Logging YAMLLoggingConfig `yaml:"logging"`
	Health  YAMLHealthConfig  `yaml:"health"`
}

// YAMLServerConfig holds gRPC and HTTP listen addresses.
type YAMLServerConfig struct {
	GRPCAddr string `yaml:"grpc_addr"` // e.g. ":50051"
	HTTPAddr string `yaml:"http_addr"` // e.g. ":8087"
}

// YAMLOwlConfig holds Owl backend connection settings.
type YAMLOwlConfig struct {
	BackendURL string        `yaml:"backend_url"`
	APIKey     string        `yaml:"api_key"`
	Timeout    time.Duration `yaml:"timeout"`
	RetryCount int           `yaml:"retry_count"`
}

// YAMLTunerConfig holds DVB tuner settings.
type YAMLTunerConfig struct {
	DevicePath    string `yaml:"device_path"`    // e.g. /dev/dvb/adapter0/frontend0
	AutoDiscover  bool   `yaml:"auto_discover"`  // scan all /dev/dvb/adapter*/
	MaxConcurrent int    `yaml:"max_concurrent"` // max simultaneous streams
	BufferSizeMB  int    `yaml:"buffer_size_mb"`
}

// YAMLLoggingConfig holds logging and log-rotation settings.
type YAMLLoggingConfig struct {
	Level      string `yaml:"level"`        // debug, info, warn, error
	Format     string `yaml:"format"`       // json, text
	FilePath   string `yaml:"file_path"`    // /var/log/antbox/daemon.log
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxAgeDays int    `yaml:"max_age_days"`
	MaxBackups int    `yaml:"max_backups"`
}

// YAMLHealthConfig holds health endpoint settings.
type YAMLHealthConfig struct {
	Addr     string `yaml:"addr"`
	Path     string `yaml:"path"`
	Interval string `yaml:"interval"`
}

// LoadYAML reads and validates a YAML config file.
// gopkg.in/yaml.v3 is not in go.mod — this function parses the YAML manually
// using a simple key=value line parser for the subset of fields used.
// TODO: add gopkg.in/yaml.v3 to go.mod for full YAML support.
func LoadYAML(path string) (*YAMLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultYAMLConfig()
	if err := parseYAMLConfig(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// parseYAMLConfig is a minimal YAML parser for antbox config.
// It handles the two-level structure used in antbox.yaml.example.
func parseYAMLConfig(data []byte, cfg *YAMLConfig) error {
	var section string
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")

		// Skip comments and blank lines.
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}

		// Detect section header (no leading spaces, ends with colon, no value after colon).
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if strings.TrimSpace(parts[1]) == "" {
				section = strings.TrimSpace(parts[0])
				continue
			}
		}

		// Key-value pair within a section.
		if strings.Contains(stripped, ":") {
			parts := strings.SplitN(stripped, ":", 2)
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			// Remove inline comment.
			if idx := strings.Index(val, " #"); idx >= 0 {
				val = strings.TrimSpace(val[:idx])
			}
			// Strip surrounding quotes (YAML allows "value" or 'value').
			val = stripQuotes(val)
			applyYAMLField(cfg, section, key, val)
		}
	}
	return nil
}

// applyYAMLField maps a parsed section+key+value into cfg.
func applyYAMLField(cfg *YAMLConfig, section, key, val string) {
	switch section {
	case "server":
		switch key {
		case "grpc_addr":
			cfg.Server.GRPCAddr = val
		case "http_addr":
			cfg.Server.HTTPAddr = val
		}
	case "owl":
		switch key {
		case "backend_url":
			cfg.Owl.BackendURL = val
		case "api_key":
			cfg.Owl.APIKey = val
		case "timeout":
			if d, err := time.ParseDuration(val); err == nil {
				cfg.Owl.Timeout = d
			}
		case "retry_count":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				cfg.Owl.RetryCount = n
			}
		}
	case "tuners":
		switch key {
		case "device_path":
			cfg.Tuners.DevicePath = val
		case "auto_discover":
			cfg.Tuners.AutoDiscover = val == "true"
		case "max_concurrent":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				cfg.Tuners.MaxConcurrent = n
			}
		case "buffer_size_mb":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				cfg.Tuners.BufferSizeMB = n
			}
		}
	case "logging":
		switch key {
		case "level":
			cfg.Logging.Level = val
		case "format":
			cfg.Logging.Format = val
		case "file_path":
			cfg.Logging.FilePath = val
		case "max_size_mb":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				cfg.Logging.MaxSizeMB = n
			}
		case "max_age_days":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				cfg.Logging.MaxAgeDays = n
			}
		case "max_backups":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				cfg.Logging.MaxBackups = n
			}
		}
	case "health":
		switch key {
		case "addr":
			cfg.Health.Addr = val
		case "path":
			cfg.Health.Path = val
		case "interval":
			cfg.Health.Interval = val
		}
	}
}

// Validate returns an error describing every validation failure.
func (c *YAMLConfig) Validate() error {
	var errs []string

	// Server defaults.
	if c.Server.GRPCAddr == "" {
		c.Server.GRPCAddr = ":50051"
	}
	if _, _, err := net.SplitHostPort(c.Server.GRPCAddr); err != nil {
		errs = append(errs, fmt.Sprintf("server.grpc_addr %q is not a valid host:port: %v", c.Server.GRPCAddr, err))
	}

	// Owl validation.
	if c.Owl.BackendURL == "" {
		errs = append(errs, "owl.backend_url is required")
	}
	if c.Owl.APIKey == "" {
		errs = append(errs, "owl.api_key is required")
	}
	if c.Owl.Timeout == 0 {
		c.Owl.Timeout = 30 * time.Second
	}
	if c.Owl.RetryCount == 0 {
		c.Owl.RetryCount = 3
	}

	// Tuner defaults.
	if c.Tuners.MaxConcurrent <= 0 {
		c.Tuners.MaxConcurrent = 2
	}
	if c.Tuners.BufferSizeMB <= 0 {
		c.Tuners.BufferSizeMB = 16
	}

	// Logging defaults and validation.
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[strings.ToLower(c.Logging.Level)] {
		errs = append(errs, fmt.Sprintf("logging.level must be one of: debug, info, warn, error (got %q)", c.Logging.Level))
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Logging.MaxSizeMB <= 0 {
		c.Logging.MaxSizeMB = 100
	}
	if c.Logging.MaxAgeDays <= 0 {
		c.Logging.MaxAgeDays = 7
	}
	if c.Logging.MaxBackups <= 0 {
		c.Logging.MaxBackups = 3
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// DefaultYAMLConfig returns a YAMLConfig with all defaults filled in.
func DefaultYAMLConfig() *YAMLConfig {
	return &YAMLConfig{
		Server: YAMLServerConfig{
			GRPCAddr: ":50051",
			HTTPAddr: ":8087",
		},
		Owl: YAMLOwlConfig{
			BackendURL: "http://localhost:8080",
			APIKey:     "CHANGE_ME",
			Timeout:    30 * time.Second,
			RetryCount: 3,
		},
		Tuners: YAMLTunerConfig{
			AutoDiscover:  true,
			MaxConcurrent: 2,
			BufferSizeMB:  16,
		},
		Logging: YAMLLoggingConfig{
			Level:      "info",
			Format:     "json",
			FilePath:   "/var/log/antbox/daemon.log",
			MaxSizeMB:  100,
			MaxAgeDays: 7,
			MaxBackups: 3,
		},
		Health: YAMLHealthConfig{
			Addr:     ":8087",
			Path:     "/health",
			Interval: "30s",
		},
	}
}


// stripQuotes removes a single layer of surrounding double or single quotes from s.
func stripQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == 39 && last == 39) {
		return s[1 : len(s)-1]
	}
	return s
}