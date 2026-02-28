// igdb.go — IGDB metadata fetcher for game catalog enrichment.
//
// IGDB (Internet Games Database) is owned by Twitch/Amazon.
// API access: https://api-docs.igdb.com
// Auth: Twitch OAuth2 client credentials (app ID + secret).
//
// Required env vars:
//
//	IGDB_CLIENT_ID     — Twitch application client ID
//	IGDB_CLIENT_SECRET — Twitch application client secret
//
// Token is cached in-memory with auto-refresh before expiry.
// Rate limit: 4 requests/second (free tier). Production should use a queue.
package games

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const igdbBaseURL = "https://api.igdb.com/v4"
const twitchTokenURL = "https://id.twitch.tv/oauth2/token"
const igdbImageBase = "https://images.igdb.com/igdb/image/upload/t_cover_big/"

// IGDBGame is the metadata returned by IGDB for a game.
type IGDBGame struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Summary     string  `json:"summary"`
	Rating      float64 `json:"rating"`      // 0-100
	RatingCount int     `json:"rating_count"`
	FirstRelease int64  `json:"first_release_date"` // Unix timestamp
	CoverID     string  // populated after cover fetch
	CoverURL    string
	Genres      []string
	Platforms   []string
}

// ReleaseYear returns the release year from the Unix timestamp.
func (g *IGDBGame) ReleaseYear() int {
	if g.FirstRelease == 0 {
		return 0
	}
	return time.Unix(g.FirstRelease, 0).Year()
}

// tokenCache holds a cached Twitch OAuth token.
type tokenCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

var globalTokenCache = &tokenCache{}

// Client is an IGDB API client with token auto-refresh.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
	cache        *tokenCache
}

// NewClient creates an IGDB client from environment variables.
func NewClient() (*Client, error) {
	clientID := os.Getenv("IGDB_CLIENT_ID")
	clientSecret := os.Getenv("IGDB_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("igdb: IGDB_CLIENT_ID and IGDB_CLIENT_SECRET must be set")
	}
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		cache:        globalTokenCache,
	}, nil
}

// SearchIGDB searches IGDB for a game by title, returning the top match.
// Includes cover image URL and genre names in the result.
func (c *Client) SearchIGDB(ctx context.Context, title string) (*IGDBGame, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	// IGDB uses Apicalypse query language in the POST body.
	query := fmt.Sprintf(`
search "%s";
fields id,name,slug,summary,rating,rating_count,first_release_date,
       cover.image_id,genres.name,platforms.name;
limit 5;
`, escapeIGDBString(title))

	var games []struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		Slug         string  `json:"slug"`
		Summary      string  `json:"summary"`
		Rating       float64 `json:"rating"`
		RatingCount  int     `json:"rating_count"`
		FirstRelease int64   `json:"first_release_date"`
		Cover        *struct {
			ImageID string `json:"image_id"`
		} `json:"cover"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Platforms []struct {
			Name string `json:"name"`
		} `json:"platforms"`
	}

	if err := c.post(ctx, token, "/games", query, &games); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return nil, fmt.Errorf("igdb: no results for %q", title)
	}

	g := games[0]
	result := &IGDBGame{
		ID:           g.ID,
		Name:         g.Name,
		Slug:         g.Slug,
		Summary:      g.Summary,
		Rating:       g.Rating,
		RatingCount:  g.RatingCount,
		FirstRelease: g.FirstRelease,
	}
	if g.Cover != nil && g.Cover.ImageID != "" {
		result.CoverURL = igdbImageBase + g.Cover.ImageID + ".jpg"
	}
	for _, genre := range g.Genres {
		result.Genres = append(result.Genres, genre.Name)
	}
	for _, p := range g.Platforms {
		result.Platforms = append(result.Platforms, p.Name)
	}

	return result, nil
}

// GetBySlug fetches a game by its IGDB slug.
func (c *Client) GetBySlug(ctx context.Context, slug string) (*IGDBGame, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
fields id,name,slug,summary,rating,rating_count,first_release_date,
       cover.image_id,genres.name;
where slug = "%s";
limit 1;
`, escapeIGDBString(slug))

	var games []IGDBGame
	if err := c.post(ctx, token, "/games", query, &games); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return nil, fmt.Errorf("igdb: game not found for slug %q", slug)
	}
	return &games[0], nil
}

// ── Token management ──────────────────────────────────────────────────────────

// getToken returns a valid Twitch OAuth token, refreshing if expired.
func (c *Client) getToken(ctx context.Context) (string, error) {
	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()

	if c.cache.token != "" && time.Now().Before(c.cache.expiresAt) {
		return c.cache.token, nil
	}

	return c.refreshToken(ctx)
}

// refreshToken fetches a new Twitch client credentials token.
func (c *Client) refreshToken(ctx context.Context) (string, error) {
	data := fmt.Sprintf("client_id=%s&client_secret=%s&grant_type=client_credentials",
		c.clientID, c.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, twitchTokenURL,
		strings.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("igdb: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("igdb: token fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("igdb: token HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("igdb: token decode: %w", err)
	}

	c.cache.token = result.AccessToken
	// Expire 60s before actual expiry to allow refresh buffer.
	c.cache.expiresAt = time.Now().Add(time.Duration(result.ExpiresIn-60) * time.Second)

	return c.cache.token, nil
}

// post sends an Apicalypse query to an IGDB endpoint and decodes JSON.
func (c *Client) post(ctx context.Context, token, path, query string, dst interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, igdbBaseURL+path,
		bytes.NewBufferString(query))
	if err != nil {
		return fmt.Errorf("igdb: build request: %w", err)
	}
	req.Header.Set("Client-ID", c.clientID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("igdb: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("igdb: rate limited (4 req/s) — slow down")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("igdb: HTTP %d: %s", resp.StatusCode, body)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("igdb: decode: %w", err)
	}
	return nil
}

// escapeIGDBString escapes a string for use in an Apicalypse query.
func escapeIGDBString(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}
