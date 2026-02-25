// tmdb_client.go — TMDB API client.
//
// The Movie Database (TMDB) provides metadata for movies and TV shows.
//
// Authentication: Bearer token from TMDB_API_KEY environment variable.
// Base URL: https://api.themoviedb.org/3
// Rate limit: ~50 requests/second (very generous).
// Image base URL: https://image.tmdb.org/t/p/{size}{poster_path}
//
// Supported image sizes:
//   Posters/covers: w92, w154, w185, w342, w500, w780, original
//   Backdrops:      w300, w780, w1280, original
//
// Environment variables:
//   TMDB_API_KEY — Bearer token for TMDB API v4 (required)
package main

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

// TMDBClient is an HTTP client for the TMDB API.
type TMDBClient struct {
	apiKey string
	client *http.Client
}

// NewTMDBClient creates a TMDBClient.
// apiKey is the TMDB Bearer token. If empty, uses TMDB_API_KEY env var.
func NewTMDBClient(apiKey string) *TMDBClient {
	if apiKey == "" {
		apiKey = os.Getenv("TMDB_API_KEY")
	}
	return &TMDBClient{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ---------- response types ---------------------------------------------------

// TMDBGenre is a movie/TV genre.
type TMDBGenre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TMDBCastMember is a single cast entry.
type TMDBCastMember struct {
	Name      string `json:"name"`
	Character string `json:"character"`
	Order     int    `json:"order"`
}

// TMDBCrewMember is a single crew entry.
type TMDBCrewMember struct {
	Name       string `json:"name"`
	Job        string `json:"job"`
	Department string `json:"department"`
}

// TMDBCredits wraps cast and crew arrays.
type TMDBCredits struct {
	Cast []TMDBCastMember `json:"cast"`
	Crew []TMDBCrewMember `json:"crew"`
}

// TMDBMovie is the response from GET /movie/{id}.
type TMDBMovie struct {
	ID          int         `json:"id"`
	IMDBId      string      `json:"imdb_id"`
	Title       string      `json:"title"`
	Overview    string      `json:"overview"`
	ReleaseDate string      `json:"release_date"`
	Runtime     int         `json:"runtime"`
	VoteAverage float64     `json:"vote_average"`
	Genres      []TMDBGenre `json:"genres"`
	PosterPath  string      `json:"poster_path"`
	BackdropPath string     `json:"backdrop_path"`
	Budget      int64       `json:"budget"`
	Revenue     int64       `json:"revenue"`
	Tagline     string      `json:"tagline"`
	Rated       string      `json:"rated,omitempty"` // MPAA rating (not always present)
	Credits     TMDBCredits `json:"credits"`
}

// TMDBTVShow is the response from GET /tv/{id}.
type TMDBTVShow struct {
	ID           int         `json:"id"`
	Name         string      `json:"name"`
	Overview     string      `json:"overview"`
	FirstAirDate string      `json:"first_air_date"`
	LastAirDate  string      `json:"last_air_date"`
	NumberOfSeasons int      `json:"number_of_seasons"`
	NumberOfEpisodes int     `json:"number_of_episodes"`
	VoteAverage  float64     `json:"vote_average"`
	Genres       []TMDBGenre `json:"genres"`
	PosterPath   string      `json:"poster_path"`
	BackdropPath string      `json:"backdrop_path"`
	Status       string      `json:"status"`
}

// TMDBEpisode is the response from GET /tv/{id}/season/{n}/episode/{n}.
type TMDBEpisode struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Overview       string   `json:"overview"`
	AirDate        string   `json:"air_date"`
	EpisodeNumber  int      `json:"episode_number"`
	SeasonNumber   int      `json:"season_number"`
	VoteAverage    float64  `json:"vote_average"`
	StillPath      string   `json:"still_path"`
	Runtime        int      `json:"runtime"`
	GuestStars     []TMDBCastMember `json:"guest_stars"`
	Crew           []TMDBCrewMember `json:"crew"`
}

// TMDBSearchResult is one result from a search endpoint.
type TMDBSearchResult struct {
	ID           int     `json:"id"`
	Title        string  `json:"title,omitempty"`  // movies
	Name         string  `json:"name,omitempty"`   // TV shows
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date,omitempty"`
	FirstAirDate string  `json:"first_air_date,omitempty"`
	MediaType    string  `json:"media_type,omitempty"`
	VoteAverage  float64 `json:"vote_average"`
	PosterPath   string  `json:"poster_path"`
}

// TMDBSearchResponse is the response from search endpoints.
type TMDBSearchResponse struct {
	Page         int                `json:"page"`
	TotalResults int                `json:"total_results"`
	TotalPages   int                `json:"total_pages"`
	Results      []TMDBSearchResult `json:"results"`
}

// ---------- API methods ------------------------------------------------------

// GetMovie fetches movie details including credits (cast + crew).
// tmdbID should be the numeric TMDB movie ID (as a string, e.g. "550").
func (c *TMDBClient) GetMovie(ctx context.Context, tmdbID string) (*TMDBMovie, error) {
	var result TMDBMovie
	path := fmt.Sprintf("/movie/%s?append_to_response=credits", tmdbID)
	if err := c.get(ctx, path, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetTVShow fetches TV series details.
func (c *TMDBClient) GetTVShow(ctx context.Context, tmdbID string) (*TMDBTVShow, error) {
	var result TMDBTVShow
	if err := c.get(ctx, "/tv/"+tmdbID, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetEpisode fetches details for a specific episode.
func (c *TMDBClient) GetEpisode(ctx context.Context, showID string, season, episode int) (*TMDBEpisode, error) {
	var result TMDBEpisode
	path := fmt.Sprintf("/tv/%s/season/%d/episode/%d", showID, season, episode)
	if err := c.get(ctx, path, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SearchMovies searches for movies by title.
func (c *TMDBClient) SearchMovies(ctx context.Context, query string) (*TMDBSearchResponse, error) {
	var result TMDBSearchResponse
	path := "/search/movie?query=" + url.QueryEscape(query)
	if err := c.get(ctx, path, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SearchTV searches for TV shows by title.
func (c *TMDBClient) SearchTV(ctx context.Context, query string) (*TMDBSearchResponse, error) {
	var result TMDBSearchResponse
	path := "/search/tv?query=" + url.QueryEscape(query)
	if err := c.get(ctx, path, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ---------- internal ---------------------------------------------------------

// get makes a GET request to the TMDB API and decodes the response JSON into dest.
func (c *TMDBClient) get(ctx context.Context, path string, dest interface{}) error {
	if c.apiKey == "" {
		return fmt.Errorf("TMDB_API_KEY not set")
	}

	reqURL := tmdbBaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("tmdb request build: %w", err)
	}

	// TMDB v4 uses Bearer token authentication.
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("tmdb: not found: %s", path)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("tmdb: invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb: HTTP %d for %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("tmdb decode %s: %w", path, err)
	}
	return nil
}
