// handlers_reseller.go — Reseller API endpoints (P14-T04/T05).
//
// Resellers are third-party partners who can create and manage subscribers on behalf
// of Roost. They authenticate via an API key with a "reseller_" prefix.
//
// Public reseller routes (reseller JWT required):
//   POST /reseller/auth          — validate API key, return JWT with reseller_id claim
//   POST /reseller/subscribers   — create subscriber + link to reseller
//   GET  /reseller/subscribers   — list reseller's subscribers (paginated)
//   DELETE /reseller/subscribers/:id — cancel a subscriber
//   GET  /reseller/revenue       — monthly revenue breakdown with reseller share
//   GET  /reseller/dashboard     — aggregate stats (total subs, MRR, churn)
//
// Admin routes (superowner JWT required):
//   POST /admin/resellers        — create reseller, generate API key (plaintext returned once)
//   GET  /admin/resellers        — list all resellers
package billing

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/unyeco/roost/internal/auth"
)

// ── Types ─────────────────────────────────────────────────────────────────────

// resellerJWTClaims is the JWT claims issued after POST /reseller/auth.
type resellerJWTClaims struct {
	jwt.RegisteredClaims
	ResellerID string `json:"reseller_id"`
	IsReseller bool   `json:"is_reseller"`
}

// resellerInfo is returned in list/detail responses.
type resellerInfo struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	ContactEmail         string          `json:"contact_email"`
	APIKeyPrefix         string          `json:"api_key_prefix"`
	RevenueSharePercent  float64         `json:"revenue_share_percent"`
	Branding             json.RawMessage `json:"branding"`
	IsActive             bool            `json:"is_active"`
	CreatedAt            time.Time       `json:"created_at"`
	SubscriberCount      int             `json:"subscriber_count,omitempty"`
}

// resellerSubscriberRow is a subscriber as seen through the reseller lens.
type resellerSubscriberRow struct {
	SubscriberID string    `json:"subscriber_id"`
	Email        string    `json:"email"`
	DisplayName  *string   `json:"display_name,omitempty"`
	Status       string    `json:"status"`
	LinkedAt     time.Time `json:"linked_at"`
}

// resellerRevenuePeriod is one month of revenue for a reseller.
type resellerRevenuePeriod struct {
	Month              string  `json:"month"` // YYYY-MM
	GrossAmountCents   int     `json:"gross_amount_cents"`
	ResellerShareCents int     `json:"reseller_share_cents"`
	Currency           string  `json:"currency"`
	SubscriberCount    int     `json:"subscriber_count"`
}

// resellerDashboard is the aggregate dashboard stats.
type resellerDashboard struct {
	TotalSubscribers int     `json:"total_subscribers"`
	ActiveThisMonth  int     `json:"active_this_month"`
	MRRCents         int     `json:"mrr_cents"`
	ChurnThisMonth   int     `json:"churn_this_month"`
	ResellerID       string  `json:"reseller_id"`
	ResellerName     string  `json:"reseller_name"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// handleResellerAuth handles POST /reseller/auth.
// Accepts a reseller API key, validates it against the hash, and returns a JWT.
func (s *Server) handleResellerAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.APIKey == "" {
		auth.WriteError(w, http.StatusBadRequest, "invalid_request", "api_key required")
		return
	}

	if !strings.HasPrefix(req.APIKey, "reseller_") {
		auth.WriteError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key format")
		return
	}

	// Extract prefix (first 8 chars after "reseller_" → actually first 8 chars of full key).
	// Prefix is first 8 chars of the raw key (e.g. "reseller").
	prefix := req.APIKey
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}

	keyHash := hashResellerAPIKey(req.APIKey)

	var resellerID, name string
	var isActive bool
	err := s.db.QueryRow(`
		SELECT id, name, is_active FROM resellers
		WHERE api_key_hash = $1 AND api_key_prefix = $2
	`, keyHash, prefix).Scan(&resellerID, &name, &isActive)

	if err == sql.ErrNoRows {
		auth.WriteError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or revoked API key")
		return
	}
	if err != nil {
		// DB error can mean the resellers table doesn't exist yet (migration pending),
		// or a genuine DB error. Return 401 in either case to avoid leaking DB state.
		auth.WriteError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or revoked API key")
		return
	}
	if !isActive {
		auth.WriteError(w, http.StatusForbidden, "reseller_inactive", "reseller account is inactive")
		return
	}

	tokenStr, err := generateResellerJWT(resellerID, name)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "token_error", "failed to generate token")
		return
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"token":       tokenStr,
		"reseller_id": resellerID,
		"expires_in":  86400, // 24 hours
	})
}

// handleResellerSubscribers handles POST/GET /reseller/subscribers.
func (s *Server) handleResellerSubscribers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listResellerSubscribers(w, r)
	case http.MethodPost:
		s.createResellerSubscriber(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

// listResellerSubscribers handles GET /reseller/subscribers.
func (s *Server) listResellerSubscribers(w http.ResponseWriter, r *http.Request) {
	resellerID, ok := s.requireResellerAuth(w, r)
	if !ok {
		return
	}

	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := s.db.Query(`
		SELECT sub.id, sub.email, sub.display_name, sub.status, rs.created_at
		FROM reseller_subscribers rs
		JOIN subscribers sub ON sub.id = rs.subscriber_id
		WHERE rs.reseller_id = $1
		ORDER BY rs.created_at DESC
		LIMIT $2 OFFSET $3
	`, resellerID, limit, offset)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query subscribers")
		return
	}
	defer rows.Close()

	var subs []resellerSubscriberRow
	for rows.Next() {
		var row resellerSubscriberRow
		if err := rows.Scan(&row.SubscriberID, &row.Email, &row.DisplayName, &row.Status, &row.LinkedAt); err != nil {
			continue
		}
		subs = append(subs, row)
	}
	if subs == nil {
		subs = []resellerSubscriberRow{}
	}

	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM reseller_subscribers WHERE reseller_id = $1`, resellerID).Scan(&total)

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"subscribers": subs,
		"total":       total,
		"limit":       limit,
		"offset":      offset,
	})
}

// createResellerSubscriber handles POST /reseller/subscribers.
func (s *Server) createResellerSubscriber(w http.ResponseWriter, r *http.Request) {
	resellerID, ok := s.requireResellerAuth(w, r)
	if !ok {
		return
	}

	var req struct {
		Email       string  `json:"email"`
		Password    string  `json:"password"`
		DisplayName *string `json:"display_name,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if req.Email == "" || req.Password == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_fields", "email and password required")
		return
	}

	// Hash password with bcrypt (cost 12 — strong without excessive latency).
	pwHashBytes, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "hash_error", "failed to hash password")
		return
	}
	pwHash := string(pwHashBytes)

	tx, err := s.db.Begin()
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to start transaction")
		return
	}
	defer tx.Rollback()

	var subscriberID string
	err = tx.QueryRow(`
		INSERT INTO subscribers (email, password_hash, display_name, email_verified, status)
		VALUES ($1, $2, $3, TRUE, 'active')
		RETURNING id
	`, req.Email, pwHash, req.DisplayName).Scan(&subscriberID)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			auth.WriteError(w, http.StatusConflict, "email_exists", "a subscriber with this email already exists")
			return
		}
		auth.WriteError(w, http.StatusInternalServerError, "db_error", fmt.Sprintf("failed to create subscriber: %v", err))
		return
	}

	// Link subscriber to reseller.
	_, err = tx.Exec(`
		INSERT INTO reseller_subscribers (reseller_id, subscriber_id)
		VALUES ($1, $2)
	`, resellerID, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to link subscriber to reseller")
		return
	}

	if err := tx.Commit(); err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "transaction commit failed")
		return
	}

	auth.WriteJSON(w, http.StatusCreated, map[string]interface{}{
		"subscriber_id": subscriberID,
		"email":         req.Email,
		"message":       "subscriber created and linked to reseller",
	})
}

// handleResellerSubscriberByID handles DELETE /reseller/subscribers/:id.
func (s *Server) handleResellerSubscriberByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}

	resellerID, ok := s.requireResellerAuth(w, r)
	if !ok {
		return
	}

	// Extract subscriber ID from path: /reseller/subscribers/{id}
	path := strings.TrimPrefix(r.URL.Path, "/reseller/subscribers/")
	subscriberID := strings.TrimSpace(path)
	if subscriberID == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_id", "subscriber ID required")
		return
	}

	// Verify subscriber belongs to this reseller.
	var exists bool
	_ = s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM reseller_subscribers
			WHERE reseller_id = $1 AND subscriber_id = $2
		)
	`, resellerID, subscriberID).Scan(&exists)

	if !exists {
		auth.WriteError(w, http.StatusNotFound, "not_found", "subscriber not found in this reseller account")
		return
	}

	// Cancel subscriber.
	_, err := s.db.Exec(`
		UPDATE subscribers SET status = 'cancelled' WHERE id = $1
	`, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to cancel subscriber")
		return
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"subscriber_id": subscriberID,
		"message":       "subscriber cancelled",
	})
}

// handleResellerRevenue handles GET /reseller/revenue.
func (s *Server) handleResellerRevenue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	resellerID, ok := s.requireResellerAuth(w, r)
	if !ok {
		return
	}

	rows, err := s.db.Query(`
		SELECT
			TO_CHAR(created_at, 'YYYY-MM') AS month,
			SUM(gross_amount_cents)        AS gross,
			SUM(reseller_share_cents)      AS share,
			currency,
			COUNT(DISTINCT subscriber_id)  AS sub_count
		FROM reseller_revenue
		WHERE reseller_id = $1
		GROUP BY TO_CHAR(created_at, 'YYYY-MM'), currency
		ORDER BY month DESC
		LIMIT 24
	`, resellerID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query revenue")
		return
	}
	defer rows.Close()

	var periods []resellerRevenuePeriod
	for rows.Next() {
		var p resellerRevenuePeriod
		if err := rows.Scan(&p.Month, &p.GrossAmountCents, &p.ResellerShareCents, &p.Currency, &p.SubscriberCount); err != nil {
			continue
		}
		periods = append(periods, p)
	}
	if periods == nil {
		periods = []resellerRevenuePeriod{}
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{"revenue": periods})
}

// handleResellerDashboard handles GET /reseller/dashboard.
func (s *Server) handleResellerDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	resellerID, ok := s.requireResellerAuth(w, r)
	if !ok {
		return
	}

	var d resellerDashboard
	d.ResellerID = resellerID

	// Reseller name.
	_ = s.db.QueryRow(`SELECT name FROM resellers WHERE id = $1`, resellerID).Scan(&d.ResellerName)

	// Total subscribers.
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM reseller_subscribers WHERE reseller_id = $1`, resellerID).Scan(&d.TotalSubscribers)

	// Active this month (new subscribers linked this month).
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM reseller_subscribers
		WHERE reseller_id = $1
		  AND created_at >= date_trunc('month', now())
	`, resellerID).Scan(&d.ActiveThisMonth)

	// MRR (monthly revenue share this month).
	_ = s.db.QueryRow(`
		SELECT COALESCE(SUM(reseller_share_cents), 0) FROM reseller_revenue
		WHERE reseller_id = $1
		  AND created_at >= date_trunc('month', now())
	`, resellerID).Scan(&d.MRRCents)

	// Churn this month (cancelled subscribers).
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM reseller_subscribers rs
		JOIN subscribers sub ON sub.id = rs.subscriber_id
		WHERE rs.reseller_id = $1
		  AND sub.status = 'cancelled'
		  AND sub.updated_at >= date_trunc('month', now())
	`, resellerID).Scan(&d.ChurnThisMonth)

	auth.WriteJSON(w, http.StatusOK, d)
}

// ── Admin handlers ────────────────────────────────────────────────────────────

// handleAdminResellers handles POST and GET /admin/resellers.
func (s *Server) handleAdminResellers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listResellers(w, r)
	case http.MethodPost:
		s.createReseller(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

// listResellers handles GET /admin/resellers.
func (s *Server) listResellers(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperowner(w, r) {
		return
	}

	rows, err := s.db.Query(`
		SELECT
			r.id, r.name, r.contact_email, r.api_key_prefix,
			r.revenue_share_percent, r.branding, r.is_active, r.created_at,
			COUNT(rs.subscriber_id) AS subscriber_count
		FROM resellers r
		LEFT JOIN reseller_subscribers rs ON rs.reseller_id = r.id
		GROUP BY r.id
		ORDER BY r.created_at DESC
	`)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query resellers")
		return
	}
	defer rows.Close()

	var resellers []resellerInfo
	for rows.Next() {
		var ri resellerInfo
		if err := rows.Scan(
			&ri.ID, &ri.Name, &ri.ContactEmail, &ri.APIKeyPrefix,
			&ri.RevenueSharePercent, &ri.Branding, &ri.IsActive, &ri.CreatedAt,
			&ri.SubscriberCount,
		); err != nil {
			continue
		}
		resellers = append(resellers, ri)
	}
	if resellers == nil {
		resellers = []resellerInfo{}
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{"resellers": resellers})
}

// createReseller handles POST /admin/resellers.
// Generates an API key and returns the plaintext once — only the hash is stored.
func (s *Server) createReseller(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperowner(w, r) {
		return
	}

	var req struct {
		Name                string          `json:"name"`
		ContactEmail        string          `json:"contact_email"`
		RevenueSharePercent float64         `json:"revenue_share_percent"`
		Branding            json.RawMessage `json:"branding"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if req.Name == "" || req.ContactEmail == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_fields", "name and contact_email required")
		return
	}
	if req.RevenueSharePercent <= 0 || req.RevenueSharePercent > 100 {
		req.RevenueSharePercent = 30.00
	}
	if req.Branding == nil {
		req.Branding = json.RawMessage("{}")
	}

	// Generate API key: "reseller_" + 32 random bytes hex.
	rawKey, keyHash, err := generateResellerAPIKey()
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "key_gen_error", "failed to generate API key")
		return
	}
	prefix := rawKey
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}

	var resellerID string
	err = s.db.QueryRow(`
		INSERT INTO resellers (name, contact_email, api_key_hash, api_key_prefix, revenue_share_percent, branding)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, req.Name, req.ContactEmail, keyHash, prefix, req.RevenueSharePercent, req.Branding).Scan(&resellerID)

	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			auth.WriteError(w, http.StatusConflict, "email_exists", "a reseller with this email already exists")
			return
		}
		auth.WriteError(w, http.StatusInternalServerError, "db_error", fmt.Sprintf("failed to create reseller: %v", err))
		return
	}

	// Return plaintext key ONCE. After this response, it cannot be recovered.
	auth.WriteJSON(w, http.StatusCreated, map[string]interface{}{
		"id":            resellerID,
		"name":          req.Name,
		"api_key":       rawKey, // shown once — not stored in plaintext
		"api_key_prefix": prefix,
		"message":       "reseller created. Store the api_key securely — it will not be shown again.",
	})
}

// ── Auth helpers ──────────────────────────────────────────────────────────────

// requireResellerAuth validates the reseller JWT and returns the reseller ID.
// Returns empty string and false if authentication fails.
func (s *Server) requireResellerAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		auth.WriteError(w, http.StatusUnauthorized, "missing_token", "Authorization header required")
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		auth.WriteError(w, http.StatusUnauthorized, "invalid_token", "Bearer token required")
		return "", false
	}
	tokenStr := strings.TrimSpace(parts[1])

	secret := os.Getenv("AUTH_JWT_SECRET")
	if secret == "" {
		auth.WriteError(w, http.StatusInternalServerError, "config_error", "JWT secret not configured")
		return "", false
	}

	token, err := jwt.ParseWithClaims(tokenStr, &resellerJWTClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		auth.WriteError(w, http.StatusUnauthorized, "invalid_token", "invalid or expired reseller token")
		return "", false
	}

	claims, ok := token.Claims.(*resellerJWTClaims)
	if !ok || !claims.IsReseller || claims.ResellerID == "" {
		auth.WriteError(w, http.StatusUnauthorized, "invalid_token", "not a reseller token")
		return "", false
	}

	return claims.ResellerID, true
}

// generateResellerAPIKey generates a "reseller_" prefixed API key.
// Returns plaintext key and its SHA-256 hash.
func generateResellerAPIKey() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate key: %w", err)
	}
	raw = "reseller_" + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(h[:])
	return raw, hash, nil
}

// hashResellerAPIKey computes SHA-256 of the raw API key.
func hashResellerAPIKey(rawKey string) string {
	h := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(h[:])
}

// generateResellerJWT creates a 24-hour JWT for an authenticated reseller.
func generateResellerJWT(resellerID, resellerName string) (string, error) {
	secret := os.Getenv("AUTH_JWT_SECRET")
	if secret == "" {
		return "", fmt.Errorf("AUTH_JWT_SECRET not set")
	}

	now := time.Now()
	claims := resellerJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   resellerID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
			Issuer:    "roost-reseller",
			ID:        uuid.New().String(),
		},
		ResellerID: resellerID,
		IsReseller: true,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
