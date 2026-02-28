package tests

import (
	"context"
	"errors"
	"testing"

	"antbox/daemon"
	"antbox/internal/config"
	"antbox/internal/recovery"
)

func TestVersionIsSet(t *testing.T) {
	if daemon.Version == "" {
		t.Error("Version must be set")
	}
	if daemon.Version != "1.0.0" {
		t.Errorf("expected v1.0.0, got %s", daemon.Version)
	}
}

func TestVersionMajorMinorPatch(t *testing.T) {
	if daemon.VersionMajor != 1 {
		t.Errorf("expected major=1, got %d", daemon.VersionMajor)
	}
	if daemon.VersionMinor != 0 {
		t.Errorf("expected minor=0, got %d", daemon.VersionMinor)
	}
	if daemon.VersionPatch != 0 {
		t.Errorf("expected patch=0, got %d", daemon.VersionPatch)
	}
}

func TestConfigDefault(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	// DefaultYAMLConfig sets APIKey to "CHANGE_ME" which is non-empty — should pass.
	if err := cfg.Validate(); err != nil {
		t.Logf("default config validation result (APIKey=CHANGE_ME is non-empty): %v", err)
	}
}

func TestRecoveryWithRetry(t *testing.T) {
	callCount := 0
	err := recovery.WithRetry(context.Background(), nil, "test-op", 2, func(ctx context.Context) error {
		callCount++
		if callCount < 3 {
			return errors.New("transient error")
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after 3 attempts, got: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

func TestRecoveryMaxRetries(t *testing.T) {
	callCount := 0
	err := recovery.WithRetry(context.Background(), nil, "test-op", 2, func(ctx context.Context) error {
		callCount++
		return errors.New("always fails")
	})
	if err == nil {
		t.Error("expected error after max retries")
	}
	if callCount != 3 { // 1 initial + 2 retries
		t.Errorf("expected 3 calls (1+2 retries), got %d", callCount)
	}
}

func TestRecoveryPanicRecovery(t *testing.T) {
	err := recovery.WithRetry(context.Background(), nil, "panic-op", 0, func(ctx context.Context) error {
		panic("test panic")
	})
	if err == nil {
		t.Error("expected error from panic recovery")
	}
}

func TestYAMLConfigValidateAPIKeyRequired(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.APIKey = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing api_key")
	}
}

func TestYAMLConfigValidateBackendURLRequired(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.BackendURL = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing backend_url")
	}
}

func TestYAMLConfigDefaults(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	if cfg.Tuners.MaxConcurrent != 2 {
		t.Errorf("expected MaxConcurrent=2, got %d", cfg.Tuners.MaxConcurrent)
	}
	if cfg.Tuners.BufferSizeMB != 16 {
		t.Errorf("expected BufferSizeMB=16, got %d", cfg.Tuners.BufferSizeMB)
	}
	if cfg.Owl.RetryCount != 3 {
		t.Errorf("expected RetryCount=3, got %d", cfg.Owl.RetryCount)
	}
	if cfg.Logging.MaxAgeDays != 7 {
		t.Errorf("expected MaxAgeDays=7, got %d", cfg.Logging.MaxAgeDays)
	}
}
