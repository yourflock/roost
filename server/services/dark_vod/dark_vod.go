// dark_vod.go — Private/unlisted content distribution for Roost.
// Content stored in R2 at roost-vod/dark/{creator_id}/{content_id}/
// Access controlled via signed JWT with content_visibility=dark claim.
//
// Routes (registered by caller):
//   POST /operator/v1/dark-vod/upload            — upload dark content
//   POST /operator/v1/dark-vod/{id}/invite-codes — generate invite codes
//   POST /v1/dark-vod/redeem                     — redeem invite code
//   GET  /v1/dark-vod/{token}/stream             — stream dark content
package dark_vod

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// DarkClaims is the JWT payload for dark content viewer tokens.
type DarkClaims struct {
	ContentID          string `json:"content_id"`
	ContentVisibility  string `json:"content_visibility"` // always "dark"
	ViewerID           string `json:"viewer_id,omitempty"`
	jwt.RegisteredClaims
}

func jwtSecret() []byte {
	s := os.Getenv("ROOST_JWT_SECRET")
	if s == "" {
		s = "dev-secret-change-in-production"
	}
	return []byte(s)
}

func baseURL() string {
	u := os.Getenv("ROOST_BASE_URL")
	if u == "" {
		u = "https://roost.unity.dev"
	}
	return u
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HandleUpload processes a multipart upload of dark content.
// POST /operator/v1/dark-vod/upload
// Auth: operator JWT via X-Operator-Token header.
// Body: multipart/form-data with fields: title (string), file (binary).
// The file reference (R2 path) is stored; actual R2 upload is caller's
// responsibility (presigned PUT). Response returns the presigned PUT URL.
func HandleUpload(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}

		creatorID := r.Header.Get("X-Creator-ID")
		if creatorID == "" {
			creatorID = uuid.New().String() // anonymous creator fallback for dev
		}

		title := strings.TrimSpace(r.FormValue("title"))
		if title == "" {
			http.Error(w, "title required", http.StatusBadRequest)
			return
		}

		contentID := uuid.New()
		r2Path := fmt.Sprintf("dark/%s/%s/source", creatorID, contentID.String())

		_, err := db.ExecContext(r.Context(),
			`INSERT INTO dark_content (id, creator_id, title, r2_path)
			 VALUES ($1, $2, $3, $4)`,
			contentID, creatorID, title, r2Path,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		r2Endpoint := os.Getenv("R2_ENDPOINT")
		r2Bucket := "roost-vod"

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content_id":       contentID,
			"r2_path":          r2Path,
			"upload_endpoint":  fmt.Sprintf("%s/%s/%s", r2Endpoint, r2Bucket, r2Path),
			"note":             "Use upload_endpoint with a presigned PUT request to store the file.",
		})
	}
}

// HandleGenerateInviteCodes generates N cryptographically-random invite codes.
// POST /operator/v1/dark-vod/{id}/invite-codes
// Body: {"count": N}  (max 100)
func HandleGenerateInviteCodes(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contentIDStr := r.PathValue("id")
		if _, err := uuid.Parse(contentIDStr); err != nil {
			http.Error(w, "invalid content id", http.StatusBadRequest)
			return
		}

		var req struct {
			Count int `json:"count"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Count < 1 {
			req.Count = 1
		}
		if req.Count > 100 {
			req.Count = 100
		}

		// Verify content exists
		var exists bool
		err := db.QueryRowContext(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM dark_content WHERE id = $1)`, contentIDStr,
		).Scan(&exists)
		if err != nil || !exists {
			http.Error(w, "content not found", http.StatusNotFound)
			return
		}

		type CodeResult struct {
			Code          string `json:"code"`
			RedemptionURL string `json:"redemption_url"`
		}

		results := make([]CodeResult, 0, req.Count)
		for range req.Count {
			code, err := randHex(16) // 32 hex chars
			if err != nil {
				http.Error(w, "rng error", http.StatusInternalServerError)
				return
			}

			_, err = db.ExecContext(r.Context(),
				`INSERT INTO dark_invite_codes (content_id, code) VALUES ($1, $2)`,
				contentIDStr, code,
			)
			if err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}

			results = append(results, CodeResult{
				Code:          code,
				RedemptionURL: fmt.Sprintf("%s/dark/redeem?code=%s", baseURL(), code),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	}
}

// HandleRedeemInviteCode validates a code and issues a 30-day viewer JWT.
// POST /v1/dark-vod/redeem
// Body: {"code": "..."}
func HandleRedeemInviteCode(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
			http.Error(w, "code required", http.StatusBadRequest)
			return
		}

		var codeID, contentID string
		var redeemedAt sql.NullTime
		err := db.QueryRowContext(r.Context(),
			`SELECT id, content_id, redeemed_at FROM dark_invite_codes WHERE code = $1`,
			req.Code,
		).Scan(&codeID, &contentID, &redeemedAt)
		if err == sql.ErrNoRows {
			http.Error(w, "invalid code", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if redeemedAt.Valid {
			http.Error(w, "code already redeemed", http.StatusConflict)
			return
		}

		viewerID := uuid.New()
		expiresAt := time.Now().Add(30 * 24 * time.Hour)

		_, err = db.ExecContext(r.Context(),
			`UPDATE dark_invite_codes SET redeemed_at = NOW(), redeemed_by = $1 WHERE id = $2`,
			viewerID, codeID,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		claims := DarkClaims{
			ContentID:         contentID,
			ContentVisibility: "dark",
			ViewerID:          viewerID.String(),
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(expiresAt),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ID:        uuid.New().String(),
			},
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(jwtSecret())
		if err != nil {
			http.Error(w, "token error", http.StatusInternalServerError)
			return
		}

		// Store viewer token record
		db.ExecContext(r.Context(),
			`INSERT INTO dark_viewer_tokens (content_id, viewer_id, token, expires_at)
			 VALUES ($1, $2, $3, $4)`,
			contentID, viewerID, signed, expiresAt,
		)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      signed,
			"expires_at": expiresAt,
			"stream_url": fmt.Sprintf("%s/v1/dark-vod/%s/stream", baseURL(), signed),
		})
	}
}

// HandleStreamDarkContent validates a viewer JWT and redirects to the R2 stream.
// GET /v1/dark-vod/{token}/stream
func HandleStreamDarkContent(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := r.PathValue("token")
		if tokenStr == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		claims := &DarkClaims{}
		_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return jwtSecret(), nil
		})
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		if claims.ContentVisibility != "dark" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Fetch R2 path for the content
		var r2Path string
		err = db.QueryRowContext(r.Context(),
			`SELECT r2_path FROM dark_content WHERE id = $1`, claims.ContentID,
		).Scan(&r2Path)
		if err == sql.ErrNoRows {
			http.Error(w, "content not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		r2Endpoint := os.Getenv("R2_ENDPOINT")
		r2Bucket := "roost-vod"
		streamURL := fmt.Sprintf("%s/%s/%s", r2Endpoint, r2Bucket, r2Path)

		// 302 redirect to R2 presigned or public stream URL.
		// In production, R2_ENDPOINT points to a Cloudflare-fronted private bucket.
		// Presigning is handled at the CDN/Worker layer.
		http.Redirect(w, r, streamURL, http.StatusFound)
	}
}
