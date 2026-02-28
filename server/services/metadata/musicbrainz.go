// musicbrainz.go — MusicBrainz metadata fetcher for music content.
//
// MusicBrainz is an open-source music encyclopedia.
// API docs: https://musicbrainz.org/doc/MusicBrainz_API
//
// Rate limit: 1 request/second without authentication.
// Set User-Agent to identify the application (MusicBrainz requires this).
//
// No API key required for read-only access.
package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

const mbBaseURL = "https://musicbrainz.org/ws/2"

// MBRecording represents a music recording (a specific performance of a track).
type MBRecording struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Length   int    `json:"length"` // milliseconds
	Artist   string
	Album    string
	Year     int
	ISRC     string // International Standard Recording Code
}

// MBRelease represents a music release (album, EP, single).
type MBRelease struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Date        string `json:"date"` // "YYYY-MM-DD" or "YYYY"
	ArtistCredit []struct {
		Name   string `json:"name"`
		Artist struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"artist-credit"`
	Media []struct {
		Tracks []struct {
			Title string `json:"title"`
		} `json:"tracks"`
	} `json:"media"`
}

// MBClient is a minimal MusicBrainz API client.
type MBClient struct {
	httpClient *http.Client
	userAgent  string // required by MusicBrainz ToS
}

// NewMBClient creates a MusicBrainz client.
// userAgent should identify your application: "AppName/Version (contact-email)".
func NewMBClient() *MBClient {
	ua := os.Getenv("MB_USER_AGENT")
	if ua == "" {
		ua = "RoostMediaServer/1.0 (support@roost.unity.dev)"
	}
	return &MBClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		userAgent:  ua,
	}
}

// SearchRecording searches MusicBrainz for a recording by title and artist.
// Returns the best match or an error if nothing is found.
func (c *MBClient) SearchRecording(ctx context.Context, title, artist string) (*MBRecording, error) {
	q := buildMBQuery(title, artist)
	v := url.Values{}
	v.Set("query", q)
	v.Set("limit", "5")
	v.Set("fmt", "json")

	var result struct {
		Recordings []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Length int    `json:"length"`
			ISRCS  []string `json:"isrcs"`
			ArtistCredit []struct {
				Artist struct {
					Name string `json:"name"`
				} `json:"artist"`
			} `json:"artist-credit"`
			Releases []struct {
				Title string `json:"title"`
				Date  string `json:"date"`
			} `json:"releases"`
		} `json:"recordings"`
	}

	if err := c.get(ctx, "/recording?"+v.Encode(), &result); err != nil {
		return nil, err
	}
	if len(result.Recordings) == 0 {
		return nil, fmt.Errorf("musicbrainz: no recordings found for %q by %q", title, artist)
	}

	r := result.Recordings[0]
	rec := &MBRecording{
		ID:     r.ID,
		Title:  r.Title,
		Length: r.Length,
	}
	if len(r.ArtistCredit) > 0 {
		rec.Artist = r.ArtistCredit[0].Artist.Name
	}
	if len(r.Releases) > 0 {
		rec.Album = r.Releases[0].Title
		// Parse year from date string (may be "YYYY-MM-DD" or "YYYY").
		date := r.Releases[0].Date
		if len(date) >= 4 {
			year := 0
			fmt.Sscanf(date[:4], "%d", &year)
			rec.Year = year
		}
	}
	if len(r.ISRCS) > 0 {
		rec.ISRC = r.ISRCS[0]
	}
	return rec, nil
}

// SearchRelease searches MusicBrainz for a release (album/EP/single) by title and artist.
func (c *MBClient) SearchRelease(ctx context.Context, title, artist string) (*MBRelease, error) {
	q := buildMBQuery(title, artist)
	v := url.Values{}
	v.Set("query", q)
	v.Set("limit", "3")
	v.Set("fmt", "json")
	v.Set("inc", "artist-credits")

	var result struct {
		Releases []MBRelease `json:"releases"`
	}
	if err := c.get(ctx, "/release?"+v.Encode(), &result); err != nil {
		return nil, err
	}
	if len(result.Releases) == 0 {
		return nil, fmt.Errorf("musicbrainz: no releases found for %q by %q", title, artist)
	}
	return &result.Releases[0], nil
}

// GetRelease fetches full release details by MusicBrainz release ID (UUID).
func (c *MBClient) GetRelease(ctx context.Context, mbID string) (*MBRelease, error) {
	v := url.Values{}
	v.Set("fmt", "json")
	v.Set("inc", "artist-credits+recordings")

	var release MBRelease
	if err := c.get(ctx, fmt.Sprintf("/release/%s?%s", mbID, v.Encode()), &release); err != nil {
		return nil, err
	}
	return &release, nil
}

// get performs a GET against the MusicBrainz API and decodes JSON.
// Respects MusicBrainz's 1 req/sec rate limit with a 1.1s sleep between calls.
func (c *MBClient) get(ctx context.Context, path string, dst interface{}) error {
	reqURL := mbBaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("musicbrainz: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("musicbrainz: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("musicbrainz: service unavailable — rate limited or maintenance")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("musicbrainz: HTTP %d for %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("musicbrainz: decode response: %w", err)
	}

	// Polite rate limiting: sleep 1.1s after each request.
	// Callers doing bulk lookups should use a rate limiter at a higher level.
	time.Sleep(1100 * time.Millisecond)

	return nil
}

// buildMBQuery builds a Lucene query string for MusicBrainz search.
func buildMBQuery(title, artist string) string {
	if artist == "" {
		return fmt.Sprintf("recording:%q", title)
	}
	return fmt.Sprintf("recording:%q AND artist:%q", title, artist)
}
