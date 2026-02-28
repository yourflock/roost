// handler.go — HTTP handlers for the game ROM catalog admin and subscriber APIs.
//
// Admin routes (require superowner JWT):
//
//	GET    /admin/games             — list all games in catalog
//	POST   /admin/games             — add a single game (multipart ROM upload)
//	POST   /admin/games/scan        — scan a local ROM directory, auto-add new ROMs
//
// Subscriber routes (require session token):
//
//	GET    /api/games               — list catalog (no ROM URLs returned)
//	GET    /api/games/{id}/save/{slot}   — download save state from R2
//	PUT    /api/games/{id}/save/{slot}   — upload save state to R2
package games

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GameHandler handles game catalog HTTP routes.
type GameHandler struct {
	DB *sql.DB
}

// NewGameHandler creates a GameHandler backed by db.
func NewGameHandler(db *sql.DB) *GameHandler {
	return &GameHandler{DB: db}
}

// ── Admin handlers ─────────────────────────────────────────────────────────────

// HandleAdminListGames handles GET /admin/games.
func (h *GameHandler) HandleAdminListGames(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT id, title, platform, COALESCE(rom_path,''), COALESCE(cover_url,''),
		       COALESCE(igdb_slug,''), players, save_slots,
		       COALESCE(genre,''), COALESCE(summary,''), COALESCE(release_year::text,'0')
		FROM games
		WHERE is_active = true
		ORDER BY title ASC
		LIMIT 500
	`)
	if err != nil {
		writeGamesError(w, http.StatusInternalServerError, "db_error", "query failed")
		return
	}
	defer rows.Close()

	var games []Game
	for rows.Next() {
		var g Game
		var releaseYearStr string
		if err := rows.Scan(&g.ID, &g.Title, &g.Platform, &g.RomPath, &g.CoverURL,
			&g.IGDBSlug, &g.Players, &g.SaveSlots, &g.Genre, &g.Summary, &releaseYearStr); err != nil {
			continue
		}
		fmt.Sscanf(releaseYearStr, "%d", &g.ReleaseYear)
		games = append(games, g)
	}
	if games == nil {
		games = []Game{}
	}
	writeGamesJSON(w, http.StatusOK, games)
}

// HandleAdminAddGame handles POST /admin/games.
// Accepts multipart form: rom_file (binary), title (text), platform (text).
// Uploads ROM to R2, creates catalog entry, triggers async IGDB metadata fetch.
func (h *GameHandler) HandleAdminAddGame(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil { // 512 MB max
		writeGamesError(w, http.StatusBadRequest, "bad_request", "failed to parse multipart form")
		return
	}

	title := r.FormValue("title")
	platformStr := r.FormValue("platform")

	file, header, err := r.FormFile("rom_file")
	if err != nil {
		writeGamesError(w, http.StatusBadRequest, "missing_field", "rom_file required")
		return
	}
	defer file.Close()

	platform := GamePlatform(platformStr)
	if platform == "" {
		platform = DetectPlatform(header.Filename)
	}
	if platform == "" {
		writeGamesError(w, http.StatusBadRequest, "unknown_platform",
			"cannot detect platform from filename; provide platform field")
		return
	}
	if title == "" {
		title = stripROMQualifiers(strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename)))
	}

	gameID := uuid.New().String()
	bucket := getGamesEnv("R2_GAMES_BUCKET", "roost-content")
	r2Key := fmt.Sprintf("games/%s/%s%s", platform, gameID, strings.ToLower(filepath.Ext(header.Filename)))

	romData, err := io.ReadAll(file)
	if err != nil {
		writeGamesError(w, http.StatusInternalServerError, "read_error", "failed to read ROM")
		return
	}

	if err := uploadROMToR2(r.Context(), bucket, r2Key, romData); err != nil {
		log.Printf("[games] r2 upload: %v", err)
		writeGamesError(w, http.StatusInternalServerError, "upload_error", "failed to upload ROM to R2")
		return
	}

	// Insert catalog entry with minimal data.
	_, err = h.DB.ExecContext(r.Context(), `
		INSERT INTO games (id, title, platform, rom_path, players, save_slots)
		VALUES ($1, $2, $3, $4, 1, 3)
	`, gameID, title, platform, r2Key)
	if err != nil {
		log.Printf("[games] insert: %v", err)
		writeGamesError(w, http.StatusInternalServerError, "db_error", "failed to record game")
		return
	}

	// Async IGDB metadata enrichment.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		enrichFromIGDB(ctx, h.DB, gameID, title)
	}()

	writeGamesJSON(w, http.StatusCreated, map[string]string{
		"id":       gameID,
		"title":    title,
		"platform": string(platform),
		"status":   "added",
	})
}

// HandleAdminScanROMs handles POST /admin/games/scan.
// Scans a local directory (env: ROM_SCAN_DIR) for new ROMs and adds them.
// Body: { "dir": "/path/to/roms" } (optional; falls back to ROM_SCAN_DIR env).
func (h *GameHandler) HandleAdminScanROMs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Dir == "" {
		req.Dir = getGamesEnv("ROM_SCAN_DIR", "/data/roms")
	}

	games, err := ScanROMDirectory(req.Dir)
	if err != nil {
		writeGamesError(w, http.StatusInternalServerError, "scan_error",
			fmt.Sprintf("scan failed: %v", err))
		return
	}
	if len(games) == 0 {
		writeGamesJSON(w, http.StatusOK, map[string]interface{}{
			"scanned": 0,
			"added":   0,
			"message": "no ROM files found in directory",
		})
		return
	}

	added := 0
	for _, g := range games {
		id := uuid.New().String()
		_, err := h.DB.ExecContext(r.Context(), `
			INSERT INTO games (id, title, platform, rom_path, players, save_slots)
			VALUES ($1, $2, $3, $4, 1, 3)
			ON CONFLICT DO NOTHING
		`, id, g.Title, g.Platform, g.RomPath)
		if err == nil {
			added++
		}
	}

	writeGamesJSON(w, http.StatusOK, map[string]interface{}{
		"scanned": len(games),
		"added":   added,
	})
}

// ── Subscriber handlers ────────────────────────────────────────────────────────

// HandleGetSaveState handles GET /api/games/{id}/save/{slot}.
// Returns a pre-signed R2 download URL for the save state.
func (h *GameHandler) HandleGetSaveState(w http.ResponseWriter, r *http.Request, gameID, slotStr string) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeGamesError(w, http.StatusUnauthorized, "unauthorized", "X-Subscriber-ID required")
		return
	}

	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 0 || slot > 9 {
		writeGamesError(w, http.StatusBadRequest, "invalid_slot", "slot must be 0-9")
		return
	}

	bucket := getGamesEnv("R2_SAVES_BUCKET", "roost-content")
	r2Key := fmt.Sprintf("saves/%s/%s/slot%d.sav", subscriberID, gameID, slot)

	// In production: generate a Cloudflare R2 pre-signed URL.
	// For now, return the key so the client can construct the URL.
	// Real implementation: r2Client.GetPresignedURL(bucket, r2Key, 15*time.Minute)
	saveURL := fmt.Sprintf("%s/%s/%s", getGamesEnv("R2_ENDPOINT", ""), bucket, r2Key)

	writeGamesJSON(w, http.StatusOK, map[string]string{
		"save_url": saveURL,
		"r2_key":   r2Key,
		"slot":     strconv.Itoa(slot),
	})
}

// HandlePutSaveState handles PUT /api/games/{id}/save/{slot}.
// Uploads a save state file to R2.
func (h *GameHandler) HandlePutSaveState(w http.ResponseWriter, r *http.Request, gameID, slotStr string) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeGamesError(w, http.StatusUnauthorized, "unauthorized", "X-Subscriber-ID required")
		return
	}

	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 0 || slot > 9 {
		writeGamesError(w, http.StatusBadRequest, "invalid_slot", "slot must be 0-9")
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20)) // 32 MB max save state
	if err != nil {
		writeGamesError(w, http.StatusBadRequest, "read_error", "failed to read save data")
		return
	}

	bucket := getGamesEnv("R2_SAVES_BUCKET", "roost-content")
	r2Key := fmt.Sprintf("saves/%s/%s/slot%d.sav", subscriberID, gameID, slot)

	if err := uploadROMToR2(r.Context(), bucket, r2Key, data); err != nil {
		log.Printf("[games] save upload r2://%s/%s: %v", bucket, r2Key, err)
		writeGamesError(w, http.StatusInternalServerError, "upload_error", "save upload failed")
		return
	}

	writeGamesJSON(w, http.StatusOK, map[string]string{
		"status": "saved",
		"slot":   strconv.Itoa(slot),
		"r2_key": r2Key,
	})
}

// ── IGDB enrichment ───────────────────────────────────────────────────────────

// enrichFromIGDB fetches metadata from IGDB and updates the game record.
func enrichFromIGDB(ctx context.Context, db *sql.DB, gameID, title string) {
	igdbClient, err := NewClient()
	if err != nil {
		log.Printf("[games] igdb client: %v (IGDB_CLIENT_ID/SECRET not set?)", err)
		return
	}

	game, err := igdbClient.SearchIGDB(ctx, title)
	if err != nil {
		log.Printf("[games] igdb search %q: %v", title, err)
		return
	}

	genres := strings.Join(game.Genres, ", ")
	_, _ = db.ExecContext(ctx, `
		UPDATE games
		SET cover_url    = $1,
		    igdb_slug    = $2,
		    igdb_score   = $3,
		    genre        = $4,
		    summary      = $5,
		    release_year = $6,
		    updated_at   = NOW()
		WHERE id = $7
	`, game.CoverURL, game.Slug, game.Rating, genres, game.Summary, game.ReleaseYear(), gameID)
	log.Printf("[games] enriched %s from IGDB: %s (%.0f)", gameID, game.Name, game.Rating)
}

// ── R2 upload ────────────────────────────────────────────────────────────────

// uploadROMToR2 uploads binary data to R2 at the given key.
func uploadROMToR2(ctx context.Context, bucket, key string, data []byte) error {
	endpoint := os.Getenv("R2_ENDPOINT")
	if endpoint == "" {
		return fmt.Errorf("R2_ENDPOINT not set")
	}
	// Full SigV4 signing is delegated to internal/r2 package.
	// This placeholder ensures the upload path is exercised in tests.
	objectURL := fmt.Sprintf("%s/%s/%s", endpoint, bucket, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL,
		strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func getGamesEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func writeGamesJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeGamesError(w http.ResponseWriter, status int, code, msg string) {
	writeGamesJSON(w, status, map[string]string{"error": code, "message": msg})
}
