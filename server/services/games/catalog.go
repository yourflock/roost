// Package games implements game ROM catalog management.
//
// Roost supports classic game emulation via LibRetro/RetroArch-compatible cores.
// This package:
//  1. Maintains a catalog of ROM files stored in R2
//  2. Scans local ROM directories to discover new games
//  3. Manages cloud save states (upload/download per slot)
//  4. Integrates with IGDB for metadata (title, cover art, description)
//
// IMPORTANT: Only distribute ROMs for games you have legal rights to.
// Roost (self-hosted) is BYOL — bring your own licensed content.
// Roost uses only properly licensed or public domain ROMs.
package games

import (
	"os"
	"path/filepath"
	"strings"
)

// GamePlatform represents a retro gaming platform.
type GamePlatform string

const (
	PlatformNES    GamePlatform = "nes"
	PlatformSNES   GamePlatform = "snes"
	PlatformN64    GamePlatform = "n64"
	PlatformGBA    GamePlatform = "gba"
	PlatformGBC    GamePlatform = "gbc"
	PlatformGB     GamePlatform = "gb"
	PlatformPS1    GamePlatform = "ps1"
	PlatformGenesis GamePlatform = "genesis"
	PlatformAtari  GamePlatform = "atari2600"
)

// platformExtensions maps file extensions to gaming platforms.
var platformExtensions = map[string]GamePlatform{
	".nes":  PlatformNES,
	".sfc":  PlatformSNES,
	".smc":  PlatformSNES,
	".z64":  PlatformN64,
	".n64":  PlatformN64,
	".v64":  PlatformN64,
	".gba":  PlatformGBA,
	".gbc":  PlatformGBC,
	".gb":   PlatformGB,
	".bin":  PlatformPS1, // many PS1 ROMs use .bin/.cue
	".iso":  PlatformPS1,
	".md":   PlatformGenesis,
	".smd":  PlatformGenesis,
	".gen":  PlatformGenesis,
	".a26":  PlatformAtari,
}

// CoreForPlatform maps platforms to their LibRetro core names.
// These are the core filenames used by RetroArch.
var CoreForPlatform = map[GamePlatform]string{
	PlatformNES:     "nestopia_libretro",
	PlatformSNES:    "snes9x_libretro",
	PlatformN64:     "mupen64plus_next_libretro",
	PlatformGBA:     "mgba_libretro",
	PlatformGBC:     "mgba_libretro",
	PlatformGB:      "mgba_libretro",
	PlatformPS1:     "beetle_psx_libretro",
	PlatformGenesis: "genesis_plus_gx_libretro",
	PlatformAtari:   "stella_libretro",
}

// Game represents a single ROM in the catalog.
type Game struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Platform  GamePlatform `json:"platform"`
	RomPath   string       `json:"rom_path"`   // R2 key: games/{platform}/{id}.rom
	CoverURL  string       `json:"cover_url"`  // IGDB cover image URL
	IGDBSlug  string       `json:"igdb_slug"`
	IGDBScore float64      `json:"igdb_score"`  // 0-100 rating
	Players   int          `json:"players"`     // max simultaneous players (1 or 2 for netplay)
	SaveSlots int          `json:"save_slots"`  // number of save state slots
	Genre     string       `json:"genre"`
	Summary   string       `json:"summary"`
	ReleaseYear int        `json:"release_year"`
}

// ScanROMDirectory walks a local directory, identifies ROM files by extension,
// and returns a slice of Game entries with platform detected from the extension.
// Titles are inferred from filenames (without extension).
// R2 keys are not assigned here — the caller uploads and assigns them.
func ScanROMDirectory(dir string) ([]Game, error) {
	var games []Game

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		platform, ok := platformExtensions[ext]
		if !ok {
			return nil // not a recognised ROM extension
		}

		title := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
		// Strip common parenthetical qualifiers: (USA), (Europe), [!], etc.
		title = stripROMQualifiers(title)

		games = append(games, Game{
			Title:     title,
			Platform:  platform,
			RomPath:   path, // local path — caller replaces with R2 key after upload
			Players:   1,
			SaveSlots: 3,
		})
		return nil
	})

	return games, err
}

// DetectPlatform returns the GamePlatform for a ROM filename, or "" if unknown.
func DetectPlatform(filename string) GamePlatform {
	ext := strings.ToLower(filepath.Ext(filename))
	return platformExtensions[ext]
}

// stripROMQualifiers removes common ROM naming qualifiers from a title.
// Examples: "Super Mario World (USA)" → "Super Mario World"
//           "Zelda [!]" → "Zelda"
func stripROMQualifiers(title string) string {
	// Remove content in parentheses and brackets.
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
