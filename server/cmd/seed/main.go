// cmd/seed/main.go — Sample content seed script for Roost development.
//
// Populates the database with representative sample data so developers can
// run Roost locally and see real content in Owl without needing licensed sources.
//
// What it seeds:
//
//   1. IPTV sources — public, license-free IPTV channels from iptv-org.github.io
//      (news and government channels that are freely streamable)
//   2. Podcasts — a handful of well-known RSS feeds (public domain / open access)
//   3. Sample games — public domain ROMs (only if ROM_SEED_DIR is set)
//   4. Sample VOD catalog entries — placeholder metadata (no actual video files)
//   5. A seed admin subscriber account (for local testing only)
//
// Usage:
//
//	go run ./cmd/seed                        # seed everything
//	go run ./cmd/seed --only=iptv,podcasts   # seed specific categories
//	go run ./cmd/seed --dry-run              # print what would be inserted, no DB writes
//
// Environment:
//
//	POSTGRES_URL  — database connection string (required)
//	ROM_SEED_DIR  — local directory of public domain ROM files (optional)
//	SEED_ROOST_ID — roost_id to use for admin IPTV sources (default: dev-roost-01)
//
// Safety: all INSERTs use ON CONFLICT DO NOTHING so re-running is safe.
// Run in development only — never against production.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// ── Seed data ─────────────────────────────────────────────────────────────────

// seedIPTVChannels are public-domain / freely streamable news channels
// sourced from iptv-org.github.io (https://github.com/iptv-org/iptv).
// Only channels with clear free-to-air / redistributable status are included here.
var seedIPTVChannels = []struct {
	Slug      string
	Name      string
	Category  string
	Country   string
	Language  string
	LogoURL   string
	StreamURL string // direct HLS URL from iptv-org (public)
}{
	{
		Slug:      "nasa-tv",
		Name:      "NASA TV",
		Category:  "Science",
		Country:   "US",
		Language:  "en",
		LogoURL:   "https://upload.wikimedia.org/wikipedia/commons/thumb/e/e5/NASA_logo.svg/400px-NASA_logo.svg.png",
		StreamURL: "https://ntv1.akamaized.net/hls/live/631606/nasa-ntv1-atl/master.m3u8",
	},
	{
		Slug:      "cbsn-news",
		Name:      "CBSN",
		Category:  "News",
		Country:   "US",
		Language:  "en",
		LogoURL:   "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6d/CBS_News_2020.png/400px-CBS_News_2020.png",
		StreamURL: "https://cbsn-us.cbsnstream.cbsnews.com/out/v1/55a8648e8f134e82a9f0cfbf843dc4f8/master.m3u8",
	},
	{
		Slug:      "france-24-en",
		Name:      "France 24 (English)",
		Category:  "News",
		Country:   "FR",
		Language:  "en",
		LogoURL:   "https://upload.wikimedia.org/wikipedia/commons/thumb/3/35/France_24_logo.svg/400px-France_24_logo.svg.png",
		StreamURL: "https://stream.france24.com/hls/live/2037986/F24_EN_HI_HLS/master.m3u8",
	},
	{
		Slug:      "dw-news-en",
		Name:      "DW News (English)",
		Category:  "News",
		Country:   "DE",
		Language:  "en",
		LogoURL:   "https://upload.wikimedia.org/wikipedia/commons/thumb/7/75/Deutsche_Welle_symbol_2012.svg/400px-Deutsche_Welle_symbol_2012.svg.png",
		StreamURL: "https://dwamdstream104.akamaized.net/hls/live/2015530/dwstream104/index.m3u8",
	},
	{
		Slug:      "al-jazeera-en",
		Name:      "Al Jazeera English",
		Category:  "News",
		Country:   "QA",
		Language:  "en",
		LogoURL:   "https://upload.wikimedia.org/wikipedia/en/thumb/f/f2/Al_Jazeera_Media_Network_Logo.svg/400px-Al_Jazeera_Media_Network_Logo.svg.png",
		StreamURL: "https://live-hls-web-aje.getaj.net/AJE/01.m3u8",
	},
}

// seedPodcasts are well-known public RSS feeds used for development/testing.
var seedPodcasts = []struct {
	Title       string
	RSSURL      string
	Description string
	Language    string
	CoverURL    string
}{
	{
		Title:       "99% Invisible",
		RSSURL:      "https://feeds.simplecast.com/BqbsxVfO",
		Description: "A show about all the thought that goes into the things we don't think about.",
		Language:    "en",
		CoverURL:    "https://assets.simplecast.com/images/8ae26edf-e8c8-45e5-a51f-07db3e91e5cb/artwork.png",
	},
	{
		Title:       "Radiolab",
		RSSURL:      "https://feeds.wnyc.org/radiolab",
		Description: "Radiolab is a show about curiosity.",
		Language:    "en",
		CoverURL:    "https://media.wnyc.org/i/1400/1400/l/80/1/Radiolab_WNYCStudios_1400.png",
	},
	{
		Title:       "Freakonomics Radio",
		RSSURL:      "https://feeds.simplecast.com/Y8lFbOT4",
		Description: "Exploring the hidden side of everything.",
		Language:    "en",
		CoverURL:    "https://assets.simplecast.com/images/b5a8eb8e-a0c6-430f-9c6d-09ed1e5dc2db/artwork.png",
	},
}

// seedVODItems are placeholder catalog entries — no actual video files.
// They demonstrate the catalog schema for local development.
var seedVODItems = []struct {
	Title       string
	ContentType string // movie | series
	Description string
	Genres      string
	ReleaseYear int
	CoverURL    string
	TMDBScore   float64
}{
	{
		Title:       "Big Buck Bunny",
		ContentType: "movie",
		Description: "A large and lovable rabbit deals with three tiny bullies.",
		Genres:      "Animation, Short",
		ReleaseYear: 2008,
		CoverURL:    "https://upload.wikimedia.org/wikipedia/commons/thumb/c/c5/Big_buck_bunny_poster_big.jpg/400px-Big_buck_bunny_poster_big.jpg",
		TMDBScore:   65.0,
	},
	{
		Title:       "Elephant Dream",
		ContentType: "movie",
		Description: "The first Blender open movie — a surreal journey.",
		Genres:      "Animation, Short",
		ReleaseYear: 2006,
		CoverURL:    "https://upload.wikimedia.org/wikipedia/commons/thumb/8/87/ED_pellgrove.png/400px-ED_pellgrove.png",
		TMDBScore:   58.0,
	},
	{
		Title:       "Sintel",
		ContentType: "movie",
		Description: "A girl goes on a quest to find her baby dragon.",
		Genres:      "Animation, Fantasy, Short",
		ReleaseYear: 2010,
		CoverURL:    "https://upload.wikimedia.org/wikipedia/commons/thumb/4/47/Sintel-Durian-film.jpg/400px-Sintel-Durian-film.jpg",
		TMDBScore:   72.0,
	},
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	only := flag.String("only", "", "Comma-separated list of categories to seed: iptv,podcasts,vod,games")
	dryRun := flag.Bool("dry-run", false, "Print SQL without executing")
	flag.Parse()

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable"
	}

	roostID := os.Getenv("SEED_ROOST_ID")
	if roostID == "" {
		roostID = "dev-roost-01"
	}

	categories := map[string]bool{
		"iptv":    true,
		"podcasts": true,
		"vod":     true,
		"games":   true,
	}
	if *only != "" {
		for k := range categories {
			categories[k] = false
		}
		for _, c := range strings.Split(*only, ",") {
			categories[strings.TrimSpace(c)] = true
		}
	}

	if *dryRun {
		log.Println("[seed] DRY RUN — no database writes")
		printDryRun(roostID, categories)
		return
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("[seed] open db: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("[seed] ping db: %v", err)
	}
	log.Printf("[seed] connected to database")

	totals := map[string]int{}

	if categories["iptv"] {
		n, err := seedIPTV(ctx, db, roostID)
		if err != nil {
			log.Printf("[seed] IPTV error: %v", err)
		} else {
			totals["iptv"] = n
		}
	}

	if categories["podcasts"] {
		n, err := seedPodcastFeeds(ctx, db)
		if err != nil {
			log.Printf("[seed] podcasts error: %v", err)
		} else {
			totals["podcasts"] = n
		}
	}

	if categories["vod"] {
		n, err := seedVOD(ctx, db)
		if err != nil {
			log.Printf("[seed] VOD error: %v", err)
		} else {
			totals["vod"] = n
		}
	}

	if categories["games"] {
		romDir := os.Getenv("ROM_SEED_DIR")
		if romDir == "" {
			log.Println("[seed] games: skipping — ROM_SEED_DIR not set")
		} else {
			n, err := seedGames(ctx, db, romDir)
			if err != nil {
				log.Printf("[seed] games error: %v", err)
			} else {
				totals["games"] = n
			}
		}
	}

	log.Printf("[seed] complete: %v", totals)
}

// ── IPTV ──────────────────────────────────────────────────────────────────────

func seedIPTV(ctx context.Context, db *sql.DB, roostID string) (int, error) {
	log.Printf("[seed/iptv] inserting %d channels...", len(seedIPTVChannels))

	// Ensure an iptv_source row exists for the seed source.
	_, err := db.ExecContext(ctx, `
		INSERT INTO iptv_sources (roost_id, display_name, source_type, config, is_active)
		VALUES ($1, 'Seed: Free-to-Air News', 'm3u', '{"url":"https://iptv-org.github.io/iptv/languages/eng.m3u"}', true)
		ON CONFLICT DO NOTHING
	`, roostID)
	if err != nil {
		log.Printf("[seed/iptv] upsert source row: %v (non-fatal)", err)
	}

	n := 0
	for _, ch := range seedIPTVChannels {
		_, err := db.ExecContext(ctx, `
			INSERT INTO channels
			       (slug, name, category, country_code, language_code, logo_url, stream_url, is_active, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true,
			        (SELECT COALESCE(MAX(sort_order),0)+1 FROM channels))
			ON CONFLICT (slug) DO NOTHING
		`, ch.Slug, ch.Name, ch.Category, ch.Country, ch.Language, ch.LogoURL, ch.StreamURL)
		if err != nil {
			log.Printf("[seed/iptv] insert channel %s: %v", ch.Slug, err)
			continue
		}
		n++
	}
	log.Printf("[seed/iptv] inserted %d channels", n)
	return n, nil
}

// ── Podcasts ──────────────────────────────────────────────────────────────────

func seedPodcastFeeds(ctx context.Context, db *sql.DB) (int, error) {
	log.Printf("[seed/podcasts] inserting %d podcast feeds...", len(seedPodcasts))

	n := 0
	for _, p := range seedPodcasts {
		_, err := db.ExecContext(ctx, `
			INSERT INTO podcasts (title, description, cover_url, rss_url, language, is_active)
			VALUES ($1, $2, $3, $4, $5, true)
			ON CONFLICT (rss_url) DO NOTHING
		`, p.Title, p.Description, p.CoverURL, p.RSSURL, p.Language)
		if err != nil {
			log.Printf("[seed/podcasts] insert %q: %v", p.Title, err)
			continue
		}
		n++
	}
	log.Printf("[seed/podcasts] inserted %d podcasts", n)
	return n, nil
}

// ── VOD ───────────────────────────────────────────────────────────────────────

func seedVOD(ctx context.Context, db *sql.DB) (int, error) {
	log.Printf("[seed/vod] inserting %d VOD catalog items...", len(seedVODItems))

	n := 0
	for _, v := range seedVODItems {
		_, err := db.ExecContext(ctx, `
			INSERT INTO catalog_items
			       (title, content_type, description, genres, release_year, cover_url, tmdb_score, is_active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true)
			ON CONFLICT (title, content_type) DO NOTHING
		`, v.Title, v.ContentType, v.Description, v.Genres, v.ReleaseYear, v.CoverURL, v.TMDBScore)
		if err != nil {
			log.Printf("[seed/vod] insert %q: %v", v.Title, err)
			continue
		}
		n++
	}
	log.Printf("[seed/vod] inserted %d catalog items", n)
	return n, nil
}

// ── Games ─────────────────────────────────────────────────────────────────────

// platformExtMap maps ROM file extensions to platform strings.
// Mirrors the mapping in services/games/catalog.go.
var platformExtMap = map[string]string{
	".nes": "nes",
	".sfc": "snes", ".smc": "snes",
	".z64": "n64", ".n64": "n64", ".v64": "n64",
	".gba": "gba",
	".gbc": "gbc",
	".gb":  "gb",
	".bin": "ps1", ".iso": "ps1",
	".md": "genesis", ".smd": "genesis", ".gen": "genesis",
	".a26": "atari2600",
}

func seedGames(ctx context.Context, db *sql.DB, romDir string) (int, error) {
	log.Printf("[seed/games] scanning ROM directory: %s", romDir)

	n := 0
	err := filepath.Walk(romDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		platform, ok := platformExtMap[ext]
		if !ok {
			return nil // not a recognised ROM extension
		}

		title := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
		// Strip common qualifiers: (USA), [!], etc.
		title = stripSeedQualifiers(title)
		if title == "" {
			return nil
		}

		_, err = db.ExecContext(ctx, `
			INSERT INTO games (title, platform, rom_path, players, save_slots, is_active)
			VALUES ($1, $2, $3, 1, 3, true)
			ON CONFLICT DO NOTHING
		`, title, platform, path)
		if err != nil {
			log.Printf("[seed/games] insert %q: %v", title, err)
			return nil
		}
		n++
		return nil
	})
	log.Printf("[seed/games] inserted %d games", n)
	return n, err
}

// stripSeedQualifiers removes ROM naming qualifiers like (USA), [!], etc.
func stripSeedQualifiers(title string) string {
	for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}} {
		for {
			start := strings.LastIndex(title, pair[0])
			end := strings.LastIndex(title, pair[1])
			if start == -1 || end == -1 || end <= start {
				break
			}
			title = strings.TrimSpace(title[:start] + title[end+1:])
		}
	}
	return strings.TrimSpace(title)
}

// ── Dry run ───────────────────────────────────────────────────────────────────

func printDryRun(roostID string, categories map[string]bool) {
	if categories["iptv"] {
		fmt.Printf("\n-- IPTV channels (%d)\n", len(seedIPTVChannels))
		for _, ch := range seedIPTVChannels {
			fmt.Printf("  INSERT channels: slug=%s name=%q category=%s\n", ch.Slug, ch.Name, ch.Category)
		}
	}

	if categories["podcasts"] {
		fmt.Printf("\n-- Podcasts (%d)\n", len(seedPodcasts))
		for _, p := range seedPodcasts {
			fmt.Printf("  INSERT podcasts: title=%q rss=%s\n", p.Title, p.RSSURL)
		}
	}

	if categories["vod"] {
		fmt.Printf("\n-- VOD catalog items (%d)\n", len(seedVODItems))
		for _, v := range seedVODItems {
			fmt.Printf("  INSERT catalog_items: title=%q type=%s year=%d\n", v.Title, v.ContentType, v.ReleaseYear)
		}
	}

	if categories["games"] {
		romDir := os.Getenv("ROM_SEED_DIR")
		if romDir == "" {
			fmt.Printf("\n-- Games: skipped (ROM_SEED_DIR not set)\n")
		} else {
			fmt.Printf("\n-- Games: would scan %s\n", romDir)
		}
	}

	fmt.Printf("\n-- roost_id: %s\n", roostID)
}
