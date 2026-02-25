// Package config provides centralized configuration loading for all Roost services.
// P19.1.003: ROOST_MODE config and startup validation
// P20.1.001: Feature flag middleware support
package config

import (
	"fmt"
	"os"
	"strings"
)

// Mode represents the Roost operating mode.
type Mode string

const (
	// ModePrivate is for self-hosted personal media servers.
	// Subscriber management, billing, and Owl addon API are disabled.
	ModePrivate Mode = "private"

	// ModePublic is for managed/commercial Roost deployments.
	// Enables subscriber management, Stripe billing, and Owl addon token issuance.
	ModePublic Mode = "public"
)

// Config holds all Roost service configuration.
type Config struct {
	// Core
	Mode    Mode
	Port    string
	BaseURL string

	// Database
	PostgresURL string

	// Redis
	RedisURL string

	// Stripe (public mode only)
	StripeSecretKey      string
	StripeWebhookSecret  string
	StripePublishableKey string

	// Auth
	JWTSecret string

	// Email
	ElasticEmailAPIKey string
	EmailSender        string

	// Flock integration
	FlockOAuthBaseURL   string
	FlockClientID       string
	FlockClientSecret   string
	FlockRedirectURI    string
	FlockServiceToken   string

	// CDN / HLS signing
	CDNHMACSecret  string
	CDNRelayURL    string
	StreamBaseURL  string

	// Cloudflare
	CloudflareAPIKey  string
	CloudflareZoneID  string

	// Logging
	LogLevel  string
	LogFormat string
}

// Load reads configuration from environment variables.
// Required variables for public mode will cause Load to return an error
// if they are missing.
func Load() (*Config, error) {
	c := &Config{
		Mode:        parseMode(getenv("ROOST_MODE", "private")),
		Port:        getenv("PORT", "8080"),
		BaseURL:     getenv("ROOST_BASE_URL", "https://roost.yourflock.com"),
		PostgresURL: getenv("POSTGRES_URL", "postgres://roost:roost@localhost:5432/roost"),
		RedisURL:    getenv("REDIS_URL", ""),

		StripeSecretKey:      os.Getenv("STRIPE_SECRET_KEY"),
		StripeWebhookSecret:  os.Getenv("STRIPE_WEBHOOK_SECRET"),
		StripePublishableKey: os.Getenv("STRIPE_PUBLISHABLE_KEY"),

		JWTSecret: getenv("AUTH_JWT_SECRET", ""),

		ElasticEmailAPIKey: os.Getenv("ELASTIC_EMAIL_API_KEY"),
		EmailSender:        getenv("EMAIL_SENDER", "noreply@yourflock.com"),

		FlockOAuthBaseURL:  getenv("FLOCK_OAUTH_BASE_URL", "https://yourflock.com"),
		FlockClientID:      getenv("FLOCK_CLIENT_ID", "roost"),
		FlockClientSecret:  os.Getenv("FLOCK_CLIENT_SECRET"),
		FlockRedirectURI:   getenv("FLOCK_REDIRECT_URI", "https://roost.yourflock.com/auth/flock/callback"),
		FlockServiceToken:  os.Getenv("FLOCK_SERVICE_TOKEN"),

		CDNHMACSecret: getenv("CDN_HMAC_SECRET", ""),
		CDNRelayURL:   os.Getenv("CDN_RELAY_URL"),
		StreamBaseURL: getenv("STREAM_BASE_URL", "https://stream.yourflock.com"),

		CloudflareAPIKey: os.Getenv("CLOUDFLARE_API_KEY"),
		CloudflareZoneID: os.Getenv("CLOUDFLARE_ZONE_ID"),

		LogLevel:  getenv("ROOST_LOG_LEVEL", "info"),
		LogFormat: getenv("ROOST_LOG_FORMAT", "json"),
	}

	// Validation for mandatory fields.
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("AUTH_JWT_SECRET is required")
	}
	if len(c.JWTSecret) < 32 {
		return nil, fmt.Errorf("AUTH_JWT_SECRET must be at least 32 characters")
	}

	// Public mode additional validation.
	if c.IsPublicMode() {
		if c.StripeSecretKey == "" {
			return nil, fmt.Errorf("STRIPE_SECRET_KEY is required in public mode")
		}
		if c.CDNHMACSecret == "" || len(c.CDNHMACSecret) < 32 {
			return nil, fmt.Errorf("CDN_HMAC_SECRET must be at least 32 bytes in public mode")
		}
	}

	return c, nil
}

// IsPublicMode reports whether Roost is running in public (commercial) mode.
func (c *Config) IsPublicMode() bool {
	return c.Mode == ModePublic
}

// IsPrivateMode reports whether Roost is running in private (self-hosted) mode.
func (c *Config) IsPrivateMode() bool {
	return c.Mode == ModePrivate
}

// parseMode parses the mode string, defaulting to private.
func parseMode(s string) Mode {
	switch strings.ToLower(s) {
	case "public":
		return ModePublic
	default:
		return ModePrivate
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
