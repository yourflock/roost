// handlers_sso.go — SSO OAuth integration for Roost subscribers.
// P13-T01: SSO OAuth — state generation + redirect
// P13-T02: SSO OAuth — callback + account link
//
// An external OAuth 2.0 provider acts as an OAuth 2.0 identity provider. Roost implements the relying
// party (RP) side. Subscribers can sign in with SSO or link their existing
// Roost account to their SSO identity.
//
// The OAuth server may not be running yet. All handlers are built to the
// correct spec and degrade gracefully when the OAuth endpoint is unreachable.
//
// Endpoints:
//   GET    /auth/sso/login    — generate state, redirect to OAuth
//   GET    /auth/sso/callback — verify state, exchange code, create/link subscriber
//   PATCH  /auth/sso/link     — link SSO account to existing subscriber (JWT required)
//   DELETE /auth/sso/link     — unlink SSO account from subscriber (JWT required)
//
// Env vars:
//   SSO_BASE_URL  default: https://roost.unity.dev
//   SSO_CLIENT_ID       default: roost
//   SSO_CLIENT_SECRET   (required for callback; gracefully skipped if empty)
//   SSO_REDIRECT_URI    default: https://roost.unity.dev/auth/sso/callback
package billing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/unyeco/roost/internal/auth"
)

// ── OAuth configuration ─────────────────────────────────────────────────

func ssoBaseURL() string {
	if v := os.Getenv("SSO_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://roost.unity.dev"
}

func ssoClientID() string {
	if v := os.Getenv("SSO_CLIENT_ID"); v != "" {
		return v
	}
	return "roost"
}

func ssoClientSecret() string {
	return os.Getenv("SSO_CLIENT_SECRET")
}

func ssoRedirectURI() string {
	if v := os.Getenv("SSO_REDIRECT_URI"); v != "" {
		return v
	}
	return "https://roost.unity.dev/auth/sso/callback"
}

// ── State token store (in-memory, 10-min TTL) ─────────────────────────────────
// In production this should be Redis. An in-process map with TTL is sufficient
// for a single-instance service during the current phase.

var (
	stateMu     sync.Mutex
	stateTokens = make(map[string]time.Time) // state → expires_at
)

func generateOAuthState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := hex.EncodeToString(b)
	expiry := time.Now().Add(10 * time.Minute)

	stateMu.Lock()
	stateTokens[state] = expiry
	// Prune expired states while we hold the lock
	for k, v := range stateTokens {
		if time.Now().After(v) {
			delete(stateTokens, k)
		}
	}
	stateMu.Unlock()
	return state, nil
}

func consumeOAuthState(state string) bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	exp, ok := stateTokens[state]
	if !ok || time.Now().After(exp) {
		return false
	}
	delete(stateTokens, state)
	return true
}

// ── SSO userinfo type ───────────────────────────────────────────────────────

type ssoUserInfo struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	ExternalID string `json:"external_id"`"`
}

// ── Handler: GET /auth/sso/login ────────────────────────────────────────────

// handleSSOLogin generates an OAuth state token and redirects the user to
// the OAuth authorization endpoint.
func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	state, err := generateOAuthState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_error", "failed to generate state token")
		return
	}

	params := url.Values{}
	params.Set("client_id", ssoClientID())
	params.Set("redirect_uri", ssoRedirectURI())
	params.Set("response_type", "code")
	params.Set("scope", "profile email family")
	params.Set("state", state)

	authURL := fmt.Sprintf("%s/oauth/authorize?%s", ssoBaseURL(), params.Encode())
	http.Redirect(w, r, authURL, http.StatusFound)
}

// ── Helper: exchange authorization code for access token ─────────────────────

func exchangeSSOCode(ctx context.Context, code string) (string, error) {
	secret := ssoClientSecret()
	if secret == "" {
		return "", fmt.Errorf("SSO_CLIENT_SECRET not configured")
	}

	tokenURL := fmt.Sprintf("%s/oauth/token", ssoBaseURL())
	body := url.Values{}
	body.Set("grant_type", "authorization_code")
	body.Set("code", code)
	body.Set("client_id", ssoClientID())
	body.Set("client_secret", secret)
	body.Set("redirect_uri", ssoRedirectURI())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("OAuth token endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OAuth token endpoint returned %d: %s", resp.StatusCode, string(respBody))
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in SSO response")
	}
	return tok.AccessToken, nil
}

// ── Helper: fetch SSO userinfo ──────────────────────────────────────────────

func fetchSSOUserInfo(ctx context.Context, accessToken string) (*ssoUserInfo, error) {
	userinfoURL := fmt.Sprintf("%s/oauth/userinfo", ssoBaseURL())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth userinfo endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("OAuth userinfo returned %d: %s", resp.StatusCode, string(body))
	}

	var info ssoUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("parse userinfo: %w", err)
	}
	if info.UserID == "" {
		return nil, fmt.Errorf("OAuth userinfo missing user_id")
	}
	return &info, nil
}

// ── Handler: GET /auth/sso/callback ────────────────────────────────────────

// handleSSOCallback handles the OAuth redirect. It:
//  1. Verifies the state token (CSRF protection)
//  2. Exchanges the authorization code for an SSO access token
//  3. Fetches userinfo from the SSO provider
//  4. Looks up or creates a Roost subscriber linked to this SSO user
//  5. Issues a Roost JWT cookie and redirects to the subscriber dashboard
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")
	oauthErr := q.Get("error")

	// Handle user-denied or SSO error
	if oauthErr != "" {
		http.Redirect(w, r, "/login?error=sso_denied", http.StatusFound)
		return
	}
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing_params", "state and code are required")
		return
	}

	// Verify state (CSRF)
	if !consumeOAuthState(state) {
		writeError(w, http.StatusBadRequest, "invalid_state",
			"OAuth state mismatch or expired — please try again")
		return
	}

	// Exchange code for access token
	accessToken, err := exchangeSSOCode(r.Context(), code)
	if err != nil {
		log.Printf("[sso_auth] code exchange failed: %v", err)
		http.Redirect(w, r, "/login?error=sso_unavailable", http.StatusFound)
		return
	}

	// Fetch userinfo
	userInfo, err := fetchSSOUserInfo(r.Context(), accessToken)
	if err != nil {
		log.Printf("[sso_auth] userinfo fetch failed: %v", err)
		http.Redirect(w, r, "/login?error=sso_unavailable", http.StatusFound)
		return
	}

	// Find or create subscriber
	_, roostJWT, err := s.findOrCreateSSOSubscriber(r.Context(), userInfo)
	if err != nil {
		log.Printf("[sso_auth] subscriber lookup/create failed: %v", err)
		writeError(w, http.StatusInternalServerError, "subscriber_error",
			"failed to link SSO account")
		return
	}

	// Set JWT in a secure HttpOnly cookie and redirect to dashboard
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "roost_token",
		Value:    roostJWT,
		Path:     "/",
		MaxAge:   int((8 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// findOrCreateSSOSubscriber looks up a Roost subscriber by sso_user_id or email.
// If no subscriber exists, creates a new one with email_verified=true (SSO provider verified it).
// Returns (subscriberID string, JWT string, error).
func (s *Server) findOrCreateSSOSubscriber(ctx context.Context, info *ssoUserInfo) (string, string, error) {
	var subscriberID string

	// 1. Lookup by sso_user_id
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM subscribers WHERE sso_user_id = $1`,
		info.UserID,
	).Scan(&subscriberID)
	if err == nil {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE subscribers SET sso_external_id = $1, updated_at = NOW() WHERE id = $2`,
			nullableSSOStr(info.ExternalID), subscriberID)
		jwt, jwtErr := ssoIssueJWT(subscriberID)
		return subscriberID, jwt, jwtErr
	}
	if err != sql.ErrNoRows {
		return "", "", fmt.Errorf("lookup by sso_user_id: %w", err)
	}

	// 2. Lookup by email — link existing Roost account
	if info.Email != "" {
		err2 := s.db.QueryRowContext(ctx,
			`SELECT id FROM subscribers WHERE email = $1`, info.Email).Scan(&subscriberID)
		if err2 == nil {
			_, _ = s.db.ExecContext(ctx,
				`UPDATE subscribers SET sso_user_id=$1, sso_external_id=$2, updated_at=NOW() WHERE id=$3`,
				info.UserID, nullableSSOStr(info.ExternalID), subscriberID)
			jwt, jwtErr := ssoIssueJWT(subscriberID)
		return subscriberID, jwt, jwtErr
		}
	}

	// 3. Create new subscriber
	if info.Email == "" {
		return "", "", fmt.Errorf("OAuth userinfo missing email")
	}
	displayName := info.DisplayName
	if displayName == "" {
		displayName = strings.Split(info.Email, "@")[0]
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO subscribers (email, display_name, email_verified, status, sso_user_id, sso_external_id)
		VALUES ($1, $2, true, 'active', $3, $4)
		RETURNING id
	`, info.Email, displayName, info.UserID, nullableSSOStr(info.ExternalID)).Scan(&subscriberID)
	if err != nil {
		return "", "", fmt.Errorf("create SSO subscriber: %w", err)
	}

	jwt, jwtErr := ssoIssueJWT(subscriberID)
	return subscriberID, jwt, jwtErr
}

// ssoIssueJWT issues a Roost access token (8h TTL) for an SSO session.
func ssoIssueJWT(subscriberIDStr string) (string, error) {
	id, err := uuid.Parse(subscriberIDStr)
	if err != nil {
		return "", fmt.Errorf("invalid subscriber UUID %q: %w", subscriberIDStr, err)
	}
	return auth.GenerateAccessToken(id, true)
}

// nullableSSOStr converts an empty string to nil for nullable DB columns.
func nullableSSOStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ── Handler: PATCH /auth/sso/link ──────────────────────────────────────────

// handleSSOLink links an SSO account to an already-authenticated Roost subscriber.
// PATCH /auth/sso/link
// Body: { "code": "<sso_oauth_code>", "state": "<state_if_any>" }
func (s *Server) handleSSOLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "missing_code", "code is required")
		return
	}
	if req.State != "" && !consumeOAuthState(req.State) {
		writeError(w, http.StatusBadRequest, "invalid_state", "invalid or expired state token")
		return
	}

	accessToken, err := exchangeSSOCode(r.Context(), req.Code)
	if err != nil {
		log.Printf("[sso_auth] link code exchange failed for subscriber=%s: %v", subscriberID, err)
		writeError(w, http.StatusBadGateway, "sso_unavailable",
			"OAuth server is not reachable. Try again later.")
		return
	}

	userInfo, err := fetchSSOUserInfo(r.Context(), accessToken)
	if err != nil {
		log.Printf("[sso_auth] link userinfo failed for subscriber=%s: %v", subscriberID, err)
		writeError(w, http.StatusBadGateway, "sso_unavailable",
			"Could not fetch SSO account info. Try again later.")
		return
	}

	// Check that this sso_user_id is not already linked to a DIFFERENT subscriber
	var existingSubID string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id FROM subscribers WHERE sso_user_id = $1`, userInfo.UserID).Scan(&existingSubID)
	if err == nil && existingSubID != subscriberID {
		writeError(w, http.StatusConflict, "sso_already_linked",
			"This SSO account is already linked to a different Roost subscriber.")
		return
	}

	_, err = s.db.ExecContext(r.Context(),
		`UPDATE subscribers SET sso_user_id=$1, sso_external_id=$2, updated_at=NOW() WHERE id=$3`,
		userInfo.UserID, nullableSSOStr(userInfo.ExternalID), subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to save SSO link")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"linked":true,"sso_user_id":%q,"display_name":%q}`,
		userInfo.UserID, userInfo.DisplayName)
}

// ── Handler: DELETE /auth/sso/link ─────────────────────────────────────────

// handleSSOUnlink removes the SSO link from a subscriber.
// DELETE /auth/sso/link
func (s *Server) handleSSOUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	result, err := s.db.ExecContext(r.Context(),
		`UPDATE subscribers SET sso_user_id=NULL, sso_external_id=NULL, updated_at=NOW() WHERE id=$1`,
		subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to unlink SSO account")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "not_found", "subscriber not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"unlinked":true}`)
}

// handleSSOAuthLink dispatches PATCH and DELETE for /auth/sso/link.
func (s *Server) handleSSOAuthLink(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPatch:
		s.handleSSOLink(w, r)
	case http.MethodDelete:
		s.handleSSOUnlink(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH or DELETE required")
	}
}
