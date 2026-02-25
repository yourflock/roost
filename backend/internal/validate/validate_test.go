package validate_test

import (
	"testing"

	"github.com/yourflock/roost/internal/validate"
)

func TestNonEmptyString(t *testing.T) {
	if err := validate.NonEmptyString("name", "hello"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.NonEmptyString("name", "   "); err == nil {
		t.Error("expected error for whitespace-only string")
	}
	if err := validate.NonEmptyString("name", ""); err == nil {
		t.Error("expected error for empty string")
	}
}

func TestMaxLength(t *testing.T) {
	if err := validate.MaxLength("name", "hello", 10); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.MaxLength("name", "hello world!", 5); err == nil {
		t.Error("expected error for too-long string")
	}
}

func TestIsUUID(t *testing.T) {
	if err := validate.IsUUID("id", "550e8400-e29b-41d4-a716-446655440000"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.IsUUID("id", "not-a-uuid"); err == nil {
		t.Error("expected error for invalid UUID")
	}
	if err := validate.IsUUID("id", "' OR 1=1 --"); err == nil {
		t.Error("expected error for SQL injection string")
	}
}

func TestIsEmail(t *testing.T) {
	if err := validate.IsEmail("email", "user@example.com"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.IsEmail("email", "not-an-email"); err == nil {
		t.Error("expected error for non-email")
	}
	if err := validate.IsEmail("email", "<script>alert(1)</script>"); err == nil {
		t.Error("expected error for XSS payload")
	}
}

func TestIsURL(t *testing.T) {
	if err := validate.IsURL("url", "https://example.com/path", false); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.IsURL("url", "http://example.com", true); err == nil {
		t.Error("expected error for http when httpsOnly=true")
	}
	if err := validate.IsURL("url", "https://localhost/admin", false); err == nil {
		t.Error("expected SSRF guard to block localhost")
	}
	if err := validate.IsURL("url", "https://192.168.1.1/", false); err == nil {
		t.Error("expected SSRF guard to block private IP")
	}
	if err := validate.IsURL("url", "javascript:alert(1)", false); err == nil {
		t.Error("expected error for javascript: URL")
	}
}

func TestNoPathTraversal(t *testing.T) {
	if err := validate.NoPathTraversal("path", "safe-file.mp4"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.NoPathTraversal("path", "../../../etc/passwd"); err == nil {
		t.Error("expected error for path traversal")
	}
	if err := validate.NoPathTraversal("path", "file\x00name"); err == nil {
		t.Error("expected error for null byte")
	}
}

func TestIntInRange(t *testing.T) {
	if err := validate.IntInRange("count", 5, 1, 10); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if err := validate.IntInRange("count", 0, 1, 10); err == nil {
		t.Error("expected error for below minimum")
	}
	if err := validate.IntInRange("count", 100, 1, 10); err == nil {
		t.Error("expected error for above maximum")
	}
}

func TestMultiError(t *testing.T) {
	var me validate.MultiError
	if me.HasErrors() {
		t.Error("expected no errors initially")
	}
	me.Add(validate.NonEmptyString("name", ""))
	me.Add(validate.IsEmail("email", "bad"))
	me.Add(nil) // should be no-op
	if !me.HasErrors() {
		t.Error("expected errors after adding")
	}
	if len(me.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(me.Errors))
	}
}
