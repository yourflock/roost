// Package metadata provides external metadata fetchers for VOD content.
// Currently implements TMDB (The Movie Database) for movies and TV shows.
//
// Required env var: TMDB_API_KEY — obtain from https://www.themoviedb.org/settings/api
//
// Rate limit: TMDB allows 50 requests/second on free tier.
// This implementation does not rate-limit — callers should not call in tight loops.
//
// Privacy: Movie/show IDs are public catalog data. No personal data is sent.
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

const tmdbBaseURL = "https://api.themoviedb.org/3"
const tmdbImageBase = "https://image.tmdb.org/t/p/w500"

// TMDBMovie contains the metadata fields returned by TMDB for a movie.
type TMDBMovie struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	VoteCount    int     `json:"vote_count"`
	Genres       []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	Runtime        int    `json:"runtime"` // minutes
	OriginalLanguage string `json:"original_language"`
}

// PosterURL returns the full URL for the movie poster at w500 size.
func (m *TMDBMovie) PosterURL() string {
	if m.PosterPath == "" {
		return ""
	}
	return tmdbImageBase + m.PosterPath
}

// BackdropURL returns the full URL for the movie backdrop at w500 size.
func (m *TMDBMovie) BackdropURL() string {
	if m.BackdropPath == "" {
		return ""
	}
	return tmdbImageBase + m.BackdropPath
}

// GenreNames returns a slice of genre names.
func (m *TMDBMovie) GenreNames() []string {
	names := make([]string, 0, len(m.Genres))
	for _, g := range m.Genres {
		names = append(names, g.Name)
	}
	return names
}

// TMDBShow contains metadata for a TV show.
type TMDBShow struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Overview         string  `json:"overview"`
	FirstAirDate     string  `json:"first_air_date"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	VoteAverage      float64 `json:"vote_average"`
	NumberOfSeasons  int     `json:"number_of_seasons"`
	NumberOfEpisodes int     `json:"number_of_episodes"`
	Genres           []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// PosterURL returns the full URL for the show poster.
func (s *TMDBShow) PosterURL() string {
	if s.PosterPath == "" {
		return ""
	}
	return tmdbImageBase + s.PosterPath
}

// Client is a minimal TMDB API client. Create with NewTMDBClient.
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a TMDB Client using TMDB_API_KEY from the environment.
// Returns an error if the key is not set.
func NewClient() (*Client, error) {
	apiKey := os.Getenv("TMDB_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("tmdb: TMDB_API_KEY is not set")
	}
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// SearchMovie searches TMDB for a movie by title (and optional year).
// Returns the top result or an error if no results are found.
func (c *Client) SearchMovie(ctx context.Context, title, year string) (*TMDBMovie, error) {
	q := url.Values{}
	q.Set("api_key", c.apiKey)
	q.Set("query", title)
	if year != "" {
		q.Set("year", year)
	}

	var result struct {
		Results []TMDBMovie `json:"results"`
	}
	if err := c.get(ctx, "/search/movie?"+q.Encode(), &result); err != nil {
		return nil, err
	}
	if len(result.Results) == 0 {
		return nil, fmt.Errorf("tmdb: no movie results for %q (year=%s)", title, year)
	}
	return &result.Results[0], nil
}

// GetMovieDetails fetches full movie details by TMDB movie ID.
func (c *Client) GetMovieDetails(ctx context.Context, tmdbID int) (*TMDBMovie, error) {
	q := url.Values{}
	q.Set("api_key", c.apiKey)

	var movie TMDBMovie
	if err := c.get(ctx, fmt.Sprintf("/movie/%d?%s", tmdbID, q.Encode()), &movie); err != nil {
		return nil, err
	}
	return &movie, nil
}

// SearchShow searches TMDB for a TV show by name.
func (c *Client) SearchShow(ctx context.Context, name, year string) (*TMDBShow, error) {
	q := url.Values{}
	q.Set("api_key", c.apiKey)
	q.Set("query", name)
	if year != "" {
		q.Set("first_air_date_year", year)
	}

	var result struct {
		Results []TMDBShow `json:"results"`
	}
	if err := c.get(ctx, "/search/tv?"+q.Encode(), &result); err != nil {
		return nil, err
	}
	if len(result.Results) == 0 {
		return nil, fmt.Errorf("tmdb: no TV results for %q", name)
	}
	return &result.Results[0], nil
}

// GetShowDetails fetches full TV show details by TMDB show ID.
func (c *Client) GetShowDetails(ctx context.Context, tmdbID int) (*TMDBShow, error) {
	q := url.Values{}
	q.Set("api_key", c.apiKey)

	var show TMDBShow
	if err := c.get(ctx, fmt.Sprintf("/tv/%d?%s", tmdbID, q.Encode()), &show); err != nil {
		return nil, err
	}
	return &show, nil
}

// get performs a GET request to the TMDB API and decodes the JSON response.
func (c *Client) get(ctx context.Context, path string, dst interface{}) error {
	reqURL := tmdbBaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("tmdb: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("tmdb: invalid API key — check TMDB_API_KEY")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("tmdb: rate limited — slow down requests")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb: HTTP %d for %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("tmdb: decode response: %w", err)
	}
	return nil
}
