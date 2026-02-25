// tokens.go — Screen time token client for Flock integration.
// P13-T03: Screen Time Token Integration
//
// Calls Flock API to check and consume screen time tokens earned by kids
// completing self-care tasks/chores in the Flock family app.
//
// Graceful degradation: if Flock API is unreachable, streaming is allowed anyway
// (log warning, return nil). This prevents Flock downtime from blocking Roost.
//
// Redis caching: CheckTokenBalance caches balance with 30s TTL to avoid
// hammering Flock API on every stream segment request.
package flock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// FlockClient is the HTTP client for the Flock API.
// Construct with NewFlockClient() or use the package-level DefaultClient.
type FlockClient struct {
	baseURL    string
	httpClient *http.Client

	// Simple in-process token balance cache (no Redis dependency in billing service).
	// Key: flock_user_id, Value: cachedBalance
	cacheMu sync.RWMutex
	cache   map[string]*cachedBalance
}

type cachedBalance struct {
	balance   int
	expiresAt time.Time
}

// activityTimestampMu guards lastActivity map.
var (
	activityMu       sync.Mutex
	lastActivitySent = make(map[string]time.Time) // flockUserID → last sent time
)

// NewFlockClient creates a FlockClient using FLOCK_OAUTH_BASE_URL from env.
func NewFlockClient() *FlockClient {
	baseURL := os.Getenv("FLOCK_OAUTH_BASE_URL")
	if baseURL == "" {
		baseURL = "https://yourflock.com"
	}
	return &FlockClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		cache:      make(map[string]*cachedBalance),
	}
}

// flockServiceToken returns the shared service-to-service token for Flock API calls.
// This is the Roost service account token stored in FLOCK_SERVICE_TOKEN env var.
func flockServiceToken() string {
	return os.Getenv("FLOCK_SERVICE_TOKEN")
}

// CheckTokenBalance returns the current screen time token balance for a Flock user.
// Cached in-process with 30s TTL.
// Returns 0, nil on any error (fail open — kids can still watch on Flock API failure).
func (c *FlockClient) CheckTokenBalance(ctx context.Context, flockUserID string) (int, error) {
	// Check in-process cache
	c.cacheMu.RLock()
	if cached, ok := c.cache[flockUserID]; ok && time.Now().Before(cached.expiresAt) {
		balance := cached.balance
		c.cacheMu.RUnlock()
		return balance, nil
	}
	c.cacheMu.RUnlock()

	// Fetch from Flock API
	url := fmt.Sprintf("%s/api/screen-time/balance?user_id=%s", c.baseURL, flockUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("[flock] CheckTokenBalance: failed to build request: %v", err)
		return 0, nil // fail open
	}
	if tok := flockServiceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[flock] CheckTokenBalance: Flock API unreachable (user=%s): %v — failing open", flockUserID, err)
		return 0, nil // graceful degradation — allow streaming
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("[flock] CheckTokenBalance: unexpected status %d (user=%s): %s — failing open",
			resp.StatusCode, flockUserID, string(body))
		return 0, nil // fail open
	}

	var result struct {
		Balance int `json:"balance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[flock] CheckTokenBalance: failed to decode response: %v — failing open", err)
		return 0, nil
	}

	// Store in cache with 30s TTL
	c.cacheMu.Lock()
	c.cache[flockUserID] = &cachedBalance{
		balance:   result.Balance,
		expiresAt: time.Now().Add(30 * time.Second),
	}
	c.cacheMu.Unlock()

	return result.Balance, nil
}

// ConsumeToken consumes one screen time token for a Flock user.
// reason is a short string like "roost_stream_espn" logged on the Flock side.
// On Flock API failure: logs a warning and returns nil (allow streaming anyway).
func (c *FlockClient) ConsumeToken(ctx context.Context, flockUserID, reason string) error {
	url := fmt.Sprintf("%s/api/screen-time/consume", c.baseURL)
	payload, _ := json.Marshal(map[string]string{
		"user_id": flockUserID,
		"reason":  reason,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[flock] ConsumeToken: failed to build request: %v — allowing stream", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := flockServiceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[flock] ConsumeToken: Flock API unreachable (user=%s): %v — allowing stream", flockUserID, err)
		return nil // graceful degradation
	}
	defer resp.Body.Close()

	// Invalidate cache entry so next check returns fresh balance
	c.cacheMu.Lock()
	delete(c.cache, flockUserID)
	c.cacheMu.Unlock()

	if resp.StatusCode == http.StatusPaymentRequired {
		// 402 = no tokens left; this is the only error that blocks streaming
		return fmt.Errorf("no screen time tokens available")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("[flock] ConsumeToken: unexpected status %d: %s — allowing stream", resp.StatusCode, string(body))
		return nil // fail open
	}
	return nil
}
