// ephemeral.go — Ephemeral stream share links for Roost subscribers.
//
// Allows subscribers to share time-limited stream access links without
// requiring the recipient to have a Roost account. The link is a signed
// JWT that carries all access parameters. Roost validates it at stream
// time with no server-side session lookup required (stateless).
//
// DB table: ephemeral_links — tracks issued links for audit and revocation.
//
// Env vars:
//   ROOST_JWT_SECRET — HMAC-SHA256 signing key for ephemeral JWTs
//   ROOST_BASE_URL   — base URL for share link generation
//
// Routes (registered by caller):
//   POST /api/v1/ephemeral/links          — generate a share link
//   GET  /e/{token}                       — validate token and serve stream info
//   GET  /api/v1/ephemeral/links          — list subscriber's active links
//   DELETE /api/v1/ephemeral/links/{id}   — revoke a link
package ephemeral

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// EphemeralClaims is the JWT payload embedded in every share link.
type EphemeralClaims struct {
	ContentID              string `json:"content_id"`
	ContentType            string `json:"content_type"` // "live" | "vod"
	OriginatorSubscriberID string `json:"originator_subscriber_id"`
	MaxConcurrent          int    `json:"max_concurrent"`
	jwt.RegisteredClaims
}

func jwtSecret() []byte {
	s := os.Getenv("ROOST_JWT_SECRET")
	if s == "" {
		s = "dev-secret-change-in-production"
	}
	return []byte(s)
}

func roostBaseURL() string {
	u := os.Getenv("ROOST_BASE_URL")
	if u == "" {
		u = "https://roost.unity.dev"
	}
	return u
}

// HandleGenerateLink issues a new ephemeral share link.
// POST /api/v1/ephemeral/links
// Auth: X-Subscriber-ID header (set by upstream auth middleware)
// Body: {"content_id":"...", "content_type":"live"|"vod",
//        "expires_in_hours":1-168, "max_concurrent":1-3}
func HandleGenerateLink(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subscriberID := r.Header.Get("X-Subscriber-ID")
		if subscriberID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req struct {
			ContentID      string `json:"content_id"`
			ContentType    string `json:"content_type"`
			ExpiresInHours int    `json:"expires_in_hours"`
			MaxConcurrent  int    `json:"max_concurrent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.ContentID == "" {
			http.Error(w, "content_id required", http.StatusBadRequest)
			return
		}
		if req.ContentType != "live" && req.ContentType != "vod" {
			http.Error(w, "content_type must be live or vod", http.StatusBadRequest)
			return
		}

		// Clamp parameters to safe ranges
		if req.ExpiresInHours < 1 || req.ExpiresInHours > 168 {
			req.ExpiresInHours = 24
		}
		if req.MaxConcurrent < 1 || req.MaxConcurrent > 3 {
			req.MaxConcurrent = 1
		}

		expiresAt := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)
		linkID := uuid.New()

		claims := EphemeralClaims{
			ContentID:              req.ContentID,
			ContentType:            req.ContentType,
			OriginatorSubscriberID: subscriberID,
			MaxConcurrent:          req.MaxConcurrent,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(expiresAt),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ID:        linkID.String(),
			},
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(jwtSecret())
		if err != nil {
			http.Error(w, "failed to sign token", http.StatusInternalServerError)
			return
		}

		_, err = db.ExecContext(r.Context(),
			`INSERT INTO ephemeral_links
			 (id, originator_subscriber_id, content_id, content_type,
			  signed_jwt, max_concurrent, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			linkID, subscriberID, req.ContentID, req.ContentType,
			signed, req.MaxConcurrent, expiresAt,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"link_id":        linkID,
			"share_url":      fmt.Sprintf("%s/e/%s", roostBaseURL(), signed),
			"expires_at":     expiresAt,
			"max_concurrent": req.MaxConcurrent,
			"content_id":     req.ContentID,
			"content_type":   req.ContentType,
		})
	}
}

// HandleValidateEphemeralToken validates a share link JWT and returns stream info.
// GET /e/{token}
// Returns 410 Gone when the token is expired or invalid.
func HandleValidateEphemeralToken(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := r.PathValue("token")
		if tokenStr == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		claims := &EphemeralClaims{}
		_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return jwtSecret(), nil
		})
		if err != nil {
			// Expired or tampered token
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "share link has expired or is invalid",
			})
			return
		}

		// Check for manual revocation (signed_jwt deleted from DB means revoked)
		var linkID string
		err = db.QueryRowContext(r.Context(),
			`SELECT id FROM ephemeral_links WHERE signed_jwt = $1 AND expires_at > NOW()`,
			tokenStr,
		).Scan(&linkID)
		if err == sql.ErrNoRows {
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{"error": "share link revoked or expired"})
			return
		}
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		// Increment view count (best-effort)
		db.ExecContext(r.Context(),
			`UPDATE ephemeral_links SET view_count = view_count + 1 WHERE id = $1`,
			linkID,
		)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"valid":          true,
			"content_id":     claims.ContentID,
			"content_type":   claims.ContentType,
			"max_concurrent": claims.MaxConcurrent,
			"expires_at":     claims.ExpiresAt.Time,
		})
	}
}

// HandleListLinks returns all active ephemeral links for the authenticated subscriber.
// GET /api/v1/ephemeral/links
func HandleListLinks(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subscriberID := r.Header.Get("X-Subscriber-ID")
		if subscriberID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		rows, err := db.QueryContext(r.Context(),
			`SELECT id, content_id, content_type, max_concurrent, view_count, expires_at, created_at
			 FROM ephemeral_links
			 WHERE originator_subscriber_id = $1 AND expires_at > NOW()
			 ORDER BY created_at DESC`,
			subscriberID,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type Link struct {
			ID            string    `json:"id"`
			ContentID     string    `json:"content_id"`
			ContentType   string    `json:"content_type"`
			MaxConcurrent int       `json:"max_concurrent"`
			ViewCount     int       `json:"view_count"`
			ExpiresAt     time.Time `json:"expires_at"`
			CreatedAt     time.Time `json:"created_at"`
			ShareURL      string    `json:"share_url"`
		}

		var links []Link
		for rows.Next() {
			var l Link
			if err := rows.Scan(
				&l.ID, &l.ContentID, &l.ContentType,
				&l.MaxConcurrent, &l.ViewCount, &l.ExpiresAt, &l.CreatedAt,
			); err != nil {
				continue
			}
			// We don't store the JWT separately in the list for brevity;
			// the share URL can be reconstructed by the client from link ID if needed.
			l.ShareURL = fmt.Sprintf("%s/e/[token-omitted]", roostBaseURL())
			links = append(links, l)
		}

		if links == nil {
			links = []Link{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(links)
	}
}

// HandleRevokeLink deletes an ephemeral link before it naturally expires.
// DELETE /api/v1/ephemeral/links/{id}
func HandleRevokeLink(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subscriberID := r.Header.Get("X-Subscriber-ID")
		if subscriberID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		linkID := r.PathValue("id")
		if _, err := uuid.Parse(linkID); err != nil {
			http.Error(w, "invalid link id", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(),
			`DELETE FROM ephemeral_links
			 WHERE id = $1 AND originator_subscriber_id = $2`,
			linkID, subscriberID,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		n, _ := res.RowsAffected()
		if n == 0 {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
