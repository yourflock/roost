// Package validate provides shared input validation for all Roost HTTP services.
// P22.1.001: Shared Validation Middleware Package
package validate

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ValidationError describes a single field validation failure.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// MultiError collects multiple validation errors for a single request.
type MultiError struct {
	Errors []ValidationError
}

// Add appends a validation error. If err is nil, Add is a no-op.
func (m *MultiError) Add(err error) {
	if err == nil {
		return
	}
	if ve, ok := err.(*ValidationError); ok {
		m.Errors = append(m.Errors, *ve)
	} else {
		m.Errors = append(m.Errors, ValidationError{Field: "request", Message: err.Error()})
	}
}

// HasErrors reports whether any errors have been collected.
func (m *MultiError) HasErrors() bool { return len(m.Errors) > 0 }

// Error returns a pipe-delimited summary of all errors.
func (m *MultiError) Error() string {
	parts := make([]string, len(m.Errors))
	for i, e := range m.Errors {
		parts[i] = e.Error()
	}
	return strings.Join(parts, " | ")
}

// NonEmptyString validates that value is not empty or whitespace-only.
func NonEmptyString(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return &ValidationError{Field: field, Message: "must not be empty"}
	}
	return nil
}

// MinLength validates that value contains at least min runes.
func MinLength(field, value string, min int) error {
	if utf8.RuneCountInString(value) < min {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be at least %d characters", min)}
	}
	return nil
}

// MaxLength validates that value does not exceed max rune count.
func MaxLength(field, value string, max int) error {
	if utf8.RuneCountInString(value) > max {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must not exceed %d characters", max)}
	}
	return nil
}

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsUUID validates that value is a valid UUID.
func IsUUID(field, value string) error {
	if !uuidRE.MatchString(strings.TrimSpace(value)) {
		return &ValidationError{Field: field, Message: "must be a valid UUID"}
	}
	return nil
}

var emailRE = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// IsEmail validates that value looks like an email address.
func IsEmail(field, value string) error {
	v := strings.TrimSpace(value)
	if len(v) > 254 || !emailRE.MatchString(v) {
		return &ValidationError{Field: field, Message: "must be a valid email address"}
	}
	return nil
}

// IsURL validates that value is a valid URL, optionally requiring HTTPS.
// Also blocks private/localhost URLs (SSRF guard).
func IsURL(field, value string, httpsOnly bool) error {
	v := strings.TrimSpace(value)
	u, err := url.ParseRequestURI(v)
	if err != nil || u.Host == "" {
		return &ValidationError{Field: field, Message: "must be a valid URL"}
	}
	if httpsOnly && u.Scheme != "https" {
		return &ValidationError{Field: field, Message: "must use HTTPS"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &ValidationError{Field: field, Message: "must use http or https"}
	}
	host := strings.ToLower(u.Hostname())
	// Also check the raw Host field for malformed IPv6 like "::1" (without brackets).
	rawHost := strings.ToLower(u.Host)
	if host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.Contains(rawHost, "::1") || strings.Contains(rawHost, "::") ||
		strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "172.") {
		return &ValidationError{Field: field, Message: "must not be a private/internal address"}
	}
	return nil
}

var slugRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-]*$`)

// IsAlphanumericSlug validates that value is a safe alphanumeric slug.
func IsAlphanumericSlug(field, value string) error {
	if len(value) > 200 {
		return &ValidationError{Field: field, Message: "must be 200 characters or fewer"}
	}
	if !slugRE.MatchString(value) {
		return &ValidationError{Field: field, Message: "must be alphanumeric (hyphens and underscores allowed)"}
	}
	return nil
}

// countryCodeRE matches ISO 3166-1 alpha-2 codes (two uppercase letters).
var countryCodeRE = regexp.MustCompile(`^[A-Z]{2}$`)

// IsCountryCode validates that value is a valid ISO 3166-1 alpha-2 country code.
func IsCountryCode(field, value string) error {
	if !countryCodeRE.MatchString(strings.TrimSpace(value)) {
		return &ValidationError{Field: field, Message: "must be a valid ISO 3166-1 alpha-2 country code (e.g. US, GB)"}
	}
	return nil
}

// languageCodeRE matches BCP 47 / ISO 639-1 language codes (2-3 lowercase letters, optional region).
var languageCodeRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

// IsLanguageCode validates that value is a valid BCP 47 / ISO 639-1 language code.
func IsLanguageCode(field, value string) error {
	if !languageCodeRE.MatchString(strings.TrimSpace(value)) {
		return &ValidationError{Field: field, Message: "must be a valid language code (e.g. en, en-US, fra)"}
	}
	return nil
}

// IntInRange validates that value is within [min, max] inclusive.
func IntInRange(field string, value, min, max int) error {
	if value < min || value > max {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be between %d and %d", min, max)}
	}
	return nil
}

// NoPathTraversal validates that value contains no path traversal sequences or null bytes.
func NoPathTraversal(field, value string) error {
	if strings.Contains(value, "..") || strings.ContainsRune(value, 0) {
		return &ValidationError{Field: field, Message: "must not contain path traversal sequences or null bytes"}
	}
	return nil
}
