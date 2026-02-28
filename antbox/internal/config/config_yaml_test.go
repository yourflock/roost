package config_test

import (
	"os"
	"testing"

	"antbox/internal/config"
)

func TestValidateRequiresOwlBackendURL(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.BackendURL = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing owl.backend_url")
	}
}

func TestValidateRequiresAPIKey(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.APIKey = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing owl.api_key")
	}
}

func TestValidateInvalidLogLevel(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.APIKey = "test"
	cfg.Logging.Level = "verbose"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid log level")
	}
}

func TestValidateDefaults(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.APIKey = "test"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
	if cfg.Tuners.MaxConcurrent != 2 {
		t.Error("expected MaxConcurrent default of 2")
	}
	if cfg.Owl.RetryCount != 3 {
		t.Error("expected RetryCount default of 3")
	}
}

func TestLoadFromYAML(t *testing.T) {
	yamlContent := `
server:
  grpc_addr: ":50051"
owl:
  backend_url: "http://owl.local:8080"
  api_key: "test-key"
tuners:
  auto_discover: true
`
	f, err := os.CreateTemp("", "antbox-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(yamlContent)
	f.Close()

	cfg, err := config.LoadYAML(f.Name())
	if err != nil {
		t.Fatalf("LoadYAML failed: %v", err)
	}
	if cfg.Owl.BackendURL != "http://owl.local:8080" {
		t.Errorf("unexpected backend_url: %s", cfg.Owl.BackendURL)
	}
	if cfg.Owl.APIKey != "test-key" {
		t.Errorf("unexpected api_key: %s", cfg.Owl.APIKey)
	}
}

func TestLoadFromYAMLMissingFile(t *testing.T) {
	_, err := config.LoadYAML("/nonexistent/path/antbox.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateGRPCAddr(t *testing.T) {
	cfg := config.DefaultYAMLConfig()
	cfg.Owl.APIKey = "test"
	cfg.Server.GRPCAddr = "not-a-valid-addr"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid grpc_addr")
	}
}
