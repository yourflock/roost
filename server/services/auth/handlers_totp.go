// handlers_totp.go — TOTP two-factor authentication handlers.
// P2-T07: Two-Factor Authentication (TOTP)
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	goauth "github.com/unyeco/roost/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// HandleSetup2FA processes POST /auth/2fa/setup.
// Generates a TOTP secret and returns the QR code URI for authenticator app enrollment.
// The secret is NOT yet stored — that happens only after verification.
func HandleSetup2FA(db *sql.DB) http.HandlerFunc {
	return goauth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			goauth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		subscriberID := goauth.SubscriberIDFromContext(r.Context())

		// Fetch email for TOTP issuer label
		var email string
		db.QueryRowContext(r.Context(), `SELECT email FROM subscribers WHERE id = $1`, subscriberID).Scan(&email)

		// Generate TOTP secret (20 random bytes, base32-encoded per RFC 6238)
		secretBytes := make([]byte, 20)
		if _, err := rand.Read(secretBytes); err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA setup failed")
			return
		}
		// Base32-encode for TOTP (RFC 6238 requires base32 secrets)
		secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)

		// Build otpauth:// URI (standard format for authenticator apps)
		otpauthURI := fmt.Sprintf(
			"otpauth://totp/Roost:%s?secret=%s&issuer=Roost&algorithm=SHA1&digits=6&period=30",
			email, secret,
		)

		// Store secret temporarily in a pending_totp table or as a short-lived DB record.
		// We'll use an in-DB temp record that gets confirmed on /auth/2fa/verify-setup.
		// Encrypt secret before storing using AUTH_TOTP_KEY.
		encryptedSecret, err := encryptTOTPSecret(secret)
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA setup failed")
			return
		}

		// Store pending (not yet enabled) — we use the same totp_secret_encrypted column
		// but totp_enabled remains false until verify-setup succeeds.
		db.ExecContext(r.Context(), `
			UPDATE subscribers SET totp_secret_encrypted = $1, totp_enabled = false WHERE id = $2
		`, encryptedSecret, subscriberID)

		goauth.WriteJSON(w, http.StatusOK, map[string]string{
			"secret":       secret,
			"otpauth_uri":  otpauthURI,
			"instructions": "Scan the QR code in your authenticator app, then call POST /auth/2fa/verify-setup with the 6-digit code.",
		})
	}))
}

// HandleVerifySetup2FA processes POST /auth/2fa/verify-setup.
// Verifies the TOTP code matches the pending secret, enables 2FA, and returns backup codes.
func HandleVerifySetup2FA(db *sql.DB) http.HandlerFunc {
	return goauth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			goauth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		subscriberID := goauth.SubscriberIDFromContext(r.Context())

		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			goauth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		// Fetch pending TOTP secret
		var encryptedSecret sql.NullString
		db.QueryRowContext(r.Context(), `
			SELECT totp_secret_encrypted FROM subscribers WHERE id = $1
		`, subscriberID).Scan(&encryptedSecret)

		if !encryptedSecret.Valid || encryptedSecret.String == "" {
			goauth.WriteError(w, http.StatusBadRequest, "no_pending_setup",
				"No pending 2FA setup. Call POST /auth/2fa/setup first.")
			return
		}

		secret, err := decryptTOTPSecret(encryptedSecret.String)
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA verification failed")
			return
		}

		// Verify TOTP code using simple TOTP implementation
		if !verifyTOTPCode(secret, req.Code) {
			goauth.WriteError(w, http.StatusUnauthorized, "invalid_code",
				"Invalid verification code. Check your authenticator app and try again.")
			return
		}

		// Generate 10 backup codes
		backupCodes, backupHashes, err := generateBackupCodes(10)
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA setup failed")
			return
		}

		tx, txErr := db.BeginTx(r.Context(), nil)
		if txErr != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA setup failed")
			return
		}
		defer tx.Rollback()

		// Enable TOTP
		tx.ExecContext(r.Context(), `
			UPDATE subscribers SET totp_enabled = true, totp_verified_at = now() WHERE id = $1
		`, subscriberID)

		// Store backup codes (hashed)
		tx.ExecContext(r.Context(), `DELETE FROM totp_backup_codes WHERE subscriber_id = $1`, subscriberID)
		for _, h := range backupHashes {
			tx.ExecContext(r.Context(), `
				INSERT INTO totp_backup_codes (subscriber_id, code_hash) VALUES ($1, $2)
			`, subscriberID, h)
		}

		if err := tx.Commit(); err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA setup failed")
			return
		}

		goauth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"message":      "Two-factor authentication enabled.",
			"backup_codes": backupCodes, // Shown once — subscriber must save these
		})
	}))
}

// HandleVerify2FA processes POST /auth/2fa/verify.
// Verifies a TOTP or backup code against the temp_token issued during login.
// Returns full access + refresh tokens on success.
func HandleVerify2FA(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			goauth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		var req struct {
			TempToken string `json:"temp_token"`
			Code      string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			goauth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		// Validate temp token
		subscriberID, err := validateTempToken(db, r, req.TempToken)
		if err != nil {
			goauth.WriteError(w, http.StatusUnauthorized, "invalid_temp_token",
				"Temporary token is invalid or expired")
			return
		}

		// Fetch subscriber's TOTP secret and status
		var encryptedSecret string
		var emailVerified bool
		var displayName string
		db.QueryRowContext(r.Context(), `
			SELECT totp_secret_encrypted, email_verified, COALESCE(display_name,'')
			FROM subscribers WHERE id = $1
		`, subscriberID).Scan(&encryptedSecret, &emailVerified, &displayName)

		secret, err := decryptTOTPSecret(encryptedSecret)
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA verification failed")
			return
		}

		// Try TOTP code first, then backup codes
		valid := verifyTOTPCode(secret, req.Code)
		if !valid {
			valid = validateAndConsumeBackupCode(db, r, subscriberID, req.Code)
		}

		if !valid {
			goauth.WriteError(w, http.StatusUnauthorized, "invalid_code",
				"Invalid authentication code")
			return
		}

		// Issue full tokens
		subUUID, _ := parseUUID(subscriberID)
		accessToken, err := goauth.GenerateAccessToken(subUUID, emailVerified)
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA verification failed")
			return
		}

		rawRefresh, refreshHash, err := goauth.GenerateRefreshToken()
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "2FA verification failed")
			return
		}

		db.ExecContext(r.Context(), `
			INSERT INTO refresh_tokens (subscriber_id, token_hash, expires_at)
			VALUES ($1, $2, $3)
		`, subscriberID, refreshHash, time.Now().Add(7*24*time.Hour))

		goauth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"access_token":  accessToken,
			"refresh_token": rawRefresh,
		})
	}
}

// HandleDisable2FA processes DELETE /auth/2fa.
// Requires current password + TOTP code for security.
func HandleDisable2FA(db *sql.DB) http.HandlerFunc {
	return goauth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			goauth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
			return
		}

		subscriberID := goauth.SubscriberIDFromContext(r.Context())

		var req struct {
			Password string `json:"password"`
			Code     string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			goauth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		// Verify password
		var passwordHash, encryptedSecret string
		db.QueryRowContext(r.Context(), `
			SELECT password_hash, COALESCE(totp_secret_encrypted,'')
			FROM subscribers WHERE id = $1
		`, subscriberID).Scan(&passwordHash, &encryptedSecret)

		if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
			goauth.WriteError(w, http.StatusUnauthorized, "invalid_password", "Incorrect password")
			return
		}

		// Verify TOTP code
		secret, err := decryptTOTPSecret(encryptedSecret)
		if err != nil || !verifyTOTPCode(secret, req.Code) {
			goauth.WriteError(w, http.StatusUnauthorized, "invalid_code", "Invalid authenticator code")
			return
		}

		// Disable 2FA
		db.ExecContext(r.Context(), `
			UPDATE subscribers SET totp_enabled = false, totp_secret_encrypted = NULL, totp_verified_at = NULL
			WHERE id = $1
		`, subscriberID)
		db.ExecContext(r.Context(), `DELETE FROM totp_backup_codes WHERE subscriber_id = $1`, subscriberID)

		goauth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Two-factor authentication disabled.",
		})
	}))
}

// Handle2FAStatus processes GET /auth/2fa/status.
func Handle2FAStatus(db *sql.DB) http.HandlerFunc {
	return goauth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			goauth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}

		subscriberID := goauth.SubscriberIDFromContext(r.Context())

		var enabled bool
		db.QueryRowContext(r.Context(), `SELECT totp_enabled FROM subscribers WHERE id = $1`, subscriberID).
			Scan(&enabled)

		var backupCodesRemaining int
		if enabled {
			db.QueryRowContext(r.Context(), `
				SELECT COUNT(*) FROM totp_backup_codes
				WHERE subscriber_id = $1 AND used_at IS NULL
			`, subscriberID).Scan(&backupCodesRemaining)
		}

		goauth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":                 enabled,
			"backup_codes_remaining": backupCodesRemaining,
		})
	}))
}

// HandleRegenerateBackupCodes processes POST /auth/2fa/backup-codes.
// Requires current TOTP code for security.
func HandleRegenerateBackupCodes(db *sql.DB) http.HandlerFunc {
	return goauth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			goauth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		subscriberID := goauth.SubscriberIDFromContext(r.Context())

		var req struct {
			Code string `json:"code"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		var encryptedSecret string
		db.QueryRowContext(r.Context(), `
			SELECT COALESCE(totp_secret_encrypted,'') FROM subscribers WHERE id = $1
		`, subscriberID).Scan(&encryptedSecret)

		secret, err := decryptTOTPSecret(encryptedSecret)
		if err != nil || !verifyTOTPCode(secret, req.Code) {
			goauth.WriteError(w, http.StatusUnauthorized, "invalid_code", "Invalid authenticator code")
			return
		}

		backupCodes, backupHashes, err := generateBackupCodes(10)
		if err != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "Regeneration failed")
			return
		}

		tx, txErr := db.BeginTx(r.Context(), nil)
		if txErr != nil {
			goauth.WriteError(w, http.StatusInternalServerError, "server_error", "Regeneration failed")
			return
		}
		defer tx.Rollback()

		tx.ExecContext(r.Context(), `DELETE FROM totp_backup_codes WHERE subscriber_id = $1`, subscriberID)
		for _, h := range backupHashes {
			tx.ExecContext(r.Context(), `
				INSERT INTO totp_backup_codes (subscriber_id, code_hash) VALUES ($1, $2)
			`, subscriberID, h)
		}

		tx.Commit()

		goauth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"message":      "Backup codes regenerated. Save these — they won't be shown again.",
			"backup_codes": backupCodes,
		})
	}))
}

// --- TOTP Helper Functions ---

// encryptTOTPSecret encrypts a TOTP secret using AES-256-GCM with AUTH_TOTP_KEY.
func encryptTOTPSecret(plaintext string) (string, error) {
	key, err := getTOTPKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptTOTPSecret decrypts an AES-256-GCM encrypted TOTP secret.
func decryptTOTPSecret(ciphertext string) (string, error) {
	key, err := getTOTPKey()
	if err != nil {
		return "", err
	}

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextData := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertextData, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// getTOTPKey returns the 32-byte key from AUTH_TOTP_KEY env var.
func getTOTPKey() ([]byte, error) {
	keyHex := os.Getenv("AUTH_TOTP_KEY")
	if keyHex == "" {
		return nil, fmt.Errorf("AUTH_TOTP_KEY not set")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) < 32 {
		return nil, fmt.Errorf("AUTH_TOTP_KEY must be a 64-char hex string (32 bytes)")
	}
	return key[:32], nil
}

// verifyTOTPCode validates a 6-digit TOTP code against a base32-encoded secret.
// Uses pquerna/otp which applies a ±1 time step window for clock skew tolerance.
func verifyTOTPCode(secret, code string) bool {
	// Normalise: pquerna expects uppercase base32 without padding
	normalised := strings.ToUpper(strings.TrimSpace(secret))
	return totp.Validate(code, normalised)
}

// generateBackupCodes generates n random 8-character alphanumeric backup codes.
// Returns the raw codes (shown to user once) and their SHA-256 hashes (for storage).
func generateBackupCodes(n int) ([]string, []string, error) {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // Omits ambiguous chars I,0,O,1
	codes := make([]string, n)
	hashes := make([]string, n)

	for i := 0; i < n; i++ {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		code := make([]byte, 8)
		for j, v := range b {
			code[j] = charset[v%byte(len(charset))]
		}
		codes[i] = string(code)
		hashes[i] = goauth.HashToken(string(code))
	}
	return codes, hashes, nil
}

// validateAndConsumeBackupCode checks a backup code against the subscriber's stored hashes.
// Single-use: marks the code as used if valid.
func validateAndConsumeBackupCode(db *sql.DB, r *http.Request, subscriberID, code string) bool {
	codeHash := goauth.HashToken(strings.ToUpper(strings.TrimSpace(code)))

	var codeID string
	err := db.QueryRowContext(r.Context(), `
		SELECT id FROM totp_backup_codes
		WHERE subscriber_id = $1 AND code_hash = $2 AND used_at IS NULL
	`, subscriberID, codeHash).Scan(&codeID)

	if err != nil {
		return false
	}

	db.ExecContext(r.Context(), `UPDATE totp_backup_codes SET used_at = now() WHERE id = $1`, codeID)
	return true
}

// generateTempToken creates a short-lived (5 min) token for 2FA continuation.
// Stored in the DB; the subscriber presents it with their TOTP code.
func generateTempToken(db *sql.DB, subscriberID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := "tmp_" + hex.EncodeToString(b)
	hash := goauth.HashToken(raw)

	// Store temp token in refresh_tokens table with very short expiry (reuses schema)
	_, err := db.Exec(`
		INSERT INTO refresh_tokens (subscriber_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, subscriberID, hash, time.Now().Add(5*time.Minute))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// validateTempToken validates a 2FA continuation token. Returns subscriber ID on success.
func validateTempToken(db *sql.DB, r *http.Request, rawToken string) (string, error) {
	if !strings.HasPrefix(rawToken, "tmp_") {
		return "", fmt.Errorf("not a temp token")
	}

	hash := goauth.HashToken(rawToken)

	var subscriberID string
	var expiresAt time.Time
	var revokedAt sql.NullTime

	err := db.QueryRowContext(r.Context(), `
		SELECT subscriber_id, expires_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1
	`, hash).Scan(&subscriberID, &expiresAt, &revokedAt)

	if err != nil || revokedAt.Valid {
		return "", fmt.Errorf("invalid temp token")
	}
	if time.Now().After(expiresAt) {
		return "", fmt.Errorf("temp token expired")
	}

	// Consume (revoke) the temp token — single use
	db.ExecContext(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1`, hash)

	return subscriberID, nil
}
