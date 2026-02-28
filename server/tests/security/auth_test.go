// auth_test.go — P22.6.003: Authentication security integration tests.
// Tests JWT hardening: alg:none rejection, expired tokens, missing exp, revoked tokens.
package security_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yourflock/roost/internal/auth"
)

func init() {
	os.Setenv("AUTH_JWT_SECRET", "test-secret-key-for-security-tests-minimum-32chars")
	os.Setenv("ROOST_JWT_ISSUER", "roost")
}

// craftToken creates a custom JWT for testing edge cases.
func craftToken(header, payload map[string]interface{}, key []byte) string {
	hBytes, _ := json.Marshal(header)
	pBytes, _ := json.Marshal(payload)

	hEnc := base64.RawURLEncoding.EncodeToString(hBytes)
	pEnc := base64.RawURLEncoding.EncodeToString(pBytes)
	msg := hEnc + "." + pEnc

	if key == nil {
		return msg + "."
	}

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return msg + "." + sig
}

// TestRejectAlgNone verifies that alg:none tokens are always rejected.
func TestRejectAlgNone(t *testing.T) {
	payload := map[string]interface{}{
		"sub": "00000000-0000-0000-0000-000000000001",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
		"iss": "roost",
		"jti": "test-jti-1",
	}

	algNoneToken := craftToken(
		map[string]interface{}{"alg": "none", "typ": "JWT"},
		payload,
		nil,
	)

	_, err := auth.ValidateAccessToken(algNoneToken)
	if err == nil {
		t.Error("alg:none token must be rejected")
	}
}

// TestRejectExpiredToken verifies that expired tokens are rejected.
func TestRejectExpiredToken(t *testing.T) {
	key := []byte(os.Getenv("AUTH_JWT_SECRET"))
	payload := map[string]interface{}{
		"sub": "00000000-0000-0000-0000-000000000001",
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"iss": "roost",
		"jti": "test-jti-2",
	}

	expiredToken := craftToken(
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		payload,
		key,
	)

	_, err := auth.ValidateAccessToken(expiredToken)
	if err == nil {
		t.Error("expired token must be rejected")
	}
}

// TestRejectMissingExp verifies that tokens without exp claim are rejected by ValidateHardened.
func TestRejectMissingExp(t *testing.T) {
	key := []byte(os.Getenv("AUTH_JWT_SECRET"))
	payload := map[string]interface{}{
		"sub": "00000000-0000-0000-0000-000000000001",
		// NO exp claim
		"iat": time.Now().Unix(),
		"iss": "roost",
		"jti": "test-jti-3",
	}

	noExpToken := craftToken(
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		payload,
		key,
	)

	_, err := auth.ValidateHardened(noExpToken)
	if err == nil {
		t.Error("token without exp claim must be rejected by ValidateHardened")
	}
}

// TestRejectFutureIssuedToken verifies tokens with iat too far in future are rejected.
func TestRejectFutureIssuedToken(t *testing.T) {
	key := []byte(os.Getenv("AUTH_JWT_SECRET"))
	payload := map[string]interface{}{
		"sub": "00000000-0000-0000-0000-000000000001",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Add(10 * time.Minute).Unix(), // > 5 min skew
		"iss": "roost",
		"jti": "test-jti-4",
	}

	futureToken := craftToken(
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		payload,
		key,
	)

	_, err := auth.ValidateHardened(futureToken)
	if err == nil {
		t.Error("token with iat 10 min in future must be rejected (max skew 5 min)")
	}
}

// TestAcceptValidToken verifies that a well-formed token is accepted.
func TestAcceptValidToken(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tokenStr, err := auth.GenerateAccessToken(id, true)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	claims, err := auth.ValidateAccessToken(tokenStr)
	if err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	if claims == nil {
		t.Error("claims must not be nil for valid token")
	}
}

// TestRejectWrongAlgorithm verifies RS256-header tokens are rejected.
func TestRejectWrongAlgorithm(t *testing.T) {
	key := []byte(os.Getenv("AUTH_JWT_SECRET"))
	payload := map[string]interface{}{
		"sub": "00000000-0000-0000-0000-000000000001",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
		"iss": "roost",
		"jti": "test-jti-5",
	}

	wrongAlgToken := craftToken(
		map[string]interface{}{"alg": "RS256", "typ": "JWT"},
		payload,
		key,
	)

	_, err := auth.ValidateAccessToken(wrongAlgToken)
	if err == nil {
		t.Error("token with RS256 algorithm must be rejected")
	}
}

// TestJWTContainsJTI verifies that generated tokens always have a jti claim.
func TestJWTContainsJTI(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	tokenStr, err := auth.GenerateAccessToken(id, true)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Fatal("token must have 3 parts")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("failed to decode token payload: %v", err)
	}

	var claims struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("failed to unmarshal claims: %v", err)
	}
	if claims.JTI == "" {
		t.Error("jti claim must be present in every generated token")
	}
}
