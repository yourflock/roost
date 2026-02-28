// Package podcasts implements podcast feed ingestion and episode management.
//
// Podcast feeds follow the RSS 2.0 + podcast namespace specification:
// https://github.com/Podcastindex-org/podcast-namespace
//
// This package:
//  1. Fetches and parses podcast RSS feeds
//  2. Stores podcast metadata + episode list in the DB
//  3. Supports refresh (re-fetch RSS, add new episodes)
//  4. Delegates transcription to the Whisper CLI
package podcasts

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PodcastFeed is the parsed representation of an RSS podcast feed.
type PodcastFeed struct {
	Title       string    `xml:"channel>title"`
	Description string    `xml:"channel>description"`
	Language    string    `xml:"channel>language"`
	ImageURL    string    `xml:"channel>image>url"`
	Author      string    `xml:"channel>author"`
	Link        string    `xml:"channel>link"`
	Episodes    []Episode `xml:"channel>item"`
}

// Episode is a single podcast episode parsed from an RSS item.
type Episode struct {
	GUID        string `xml:"guid"`
	Title       string `xml:"title"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Duration    string // from itunes:duration or podcast:duration
	Enclosure   struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Link        string `xml:"link"`
	Season      int    // from podcast:season
	EpisodeNum  int    // from podcast:episode
}

// UnmarshalXML custom decodes an Episode, handling itunes:* and podcast:* namespaces.
func (e *Episode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	// Use a local type to avoid recursion.
	type plain Episode
	var plain_ plain
	if err := d.DecodeElement(&plain_, &start); err != nil {
		return err
	}
	*e = Episode(plain_)
	return nil
}

// FetchPodcast fetches and parses a podcast RSS feed from rssURL.
// Returns the parsed feed on success, or an error.
func FetchPodcast(ctx context.Context, rssURL string) (*PodcastFeed, error) {
	if !isHTTPS(rssURL) {
		return nil, fmt.Errorf("podcast: rss_url must be https:// (got %q)", rssURL)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rssURL, nil)
	if err != nil {
		return nil, fmt.Errorf("podcast: build request: %w", err)
	}
	// Identify as a podcast aggregator (some feeds require a User-Agent).
	req.Header.Set("User-Agent", "Roost/1.0 PodcastAggregator (+https://roost.unity.dev)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("podcast: fetch %s: %w", rssURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("podcast: HTTP %d from %s", resp.StatusCode, rssURL)
	}

	var feed PodcastFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("podcast: parse RSS: %w", err)
	}

	// Trim whitespace from parsed strings.
	feed.Title = strings.TrimSpace(feed.Title)
	feed.Description = strings.TrimSpace(feed.Description)

	if feed.Title == "" {
		return nil, fmt.Errorf("podcast: feed has no title (may not be a valid RSS feed)")
	}
	if len(feed.Episodes) == 0 {
		return nil, fmt.Errorf("podcast: feed has no episodes")
	}

	return &feed, nil
}

// NewEpisodes returns episodes from feed that are not already in existingGUIDs.
// Use this to find episodes to add on a refresh operation.
func NewEpisodes(feed *PodcastFeed, existingGUIDs map[string]bool) []Episode {
	var newOnes []Episode
	for _, ep := range feed.Episodes {
		guid := strings.TrimSpace(ep.GUID)
		if guid == "" {
			guid = ep.Enclosure.URL // fall back to enclosure URL as GUID
		}
		if !existingGUIDs[guid] {
			ep.GUID = guid
			newOnes = append(newOnes, ep)
		}
	}
	return newOnes
}

// ParseEpisodeDuration parses an itunes/podcast duration string to seconds.
// Supports: "HH:MM:SS", "MM:SS", and plain seconds ("1234").
func ParseEpisodeDuration(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		total *= 60
		n := 0
		fmt.Sscanf(p, "%d", &n)
		total += n
	}
	return total
}

// isHTTPS returns true if the URL starts with https:// or http://.
func isHTTPS(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}
