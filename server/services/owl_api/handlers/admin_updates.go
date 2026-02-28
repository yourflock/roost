package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/unyeco/roost/services/owl_api/audit"
	"github.com/unyeco/roost/services/owl_api/middleware"
)

// UpdateCheckResponse is returned by GET /admin/updates.
type UpdateCheckResponse struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleasedAt      string `json:"released_at,omitempty"`
	ReleaseNotes    string `json:"release_notes,omitempty"`
	DownloadURL     string `json:"download_url,omitempty"`
}

// githubRelease is the shape of the GitHub releases/latest API response.
type githubRelease struct {
	TagName     string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// UpdateCheck handles GET /admin/updates.
func (h *AdminHandlers) UpdateCheck(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())

	// Check Redis cache first (10-minute TTL)
	// When Redis is wired: val, _ := h.Redis.Get(ctx, "roost:update_check_cache").Result()
	// For now, always fetch from GitHub

	release, err := fetchLatestRelease()
	if err != nil {
		slog.Warn("update check: GitHub fetch failed", "err", err)
		// Return current version with no update info
		writeAdminJSON(w, http.StatusOK, UpdateCheckResponse{
			CurrentVersion: h.Version,
			LatestVersion:  h.Version,
		})
		return
	}

	// Find the download URL for this platform
	platform := runtime.GOOS + "-" + runtime.GOARCH // e.g. "linux-amd64"
	downloadURL := ""
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, platform) && !strings.HasSuffix(asset.Name, ".sha256") {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	resp := UpdateCheckResponse{
		CurrentVersion:  h.Version,
		LatestVersion:   release.TagName,
		UpdateAvailable: release.TagName != h.Version && release.TagName > h.Version,
		ReleasedAt:      release.PublishedAt,
		ReleaseNotes:    release.Body,
		DownloadURL:     downloadURL,
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

// ApplyUpdateRequest is the POST /admin/updates/apply body.
type ApplyUpdateRequest struct {
	Version string `json:"version"`
}

// ApplyUpdate handles POST /admin/updates/apply.
// Only accessible to owner role. Downloads, verifies checksum, and schedules restart.
func (h *AdminHandlers) ApplyUpdate(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	// Only owner can trigger updates
	if claims.Role != "owner" {
		http.Error(w, `{"error":"forbidden: only owner can trigger updates"}`, http.StatusForbidden)
		return
	}

	var req ApplyUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	// Verify the download URL is a legitimate GitHub release asset
	release, err := fetchLatestRelease()
	if err != nil || release.TagName != req.Version {
		http.Error(w, `{"error":"version mismatch or release not found"}`, http.StatusBadRequest)
		return
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH
	var downloadURL, checksumURL string
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, platform) {
			if strings.HasSuffix(asset.Name, ".sha256") {
				checksumURL = asset.BrowserDownloadURL
			} else {
				downloadURL = asset.BrowserDownloadURL
			}
		}
	}

	if downloadURL == "" {
		http.Error(w, `{"error":"no binary for this platform"}`, http.StatusBadRequest)
		return
	}

	// Validate download URL is from github.com/unyeco/roost
	if !isValidReleaseURL(downloadURL) || !isValidReleaseURL(checksumURL) {
		http.Error(w, `{"error":"invalid download URL"}`, http.StatusForbidden)
		return
	}

	// Run download + verify + swap in a goroutine to avoid blocking
	go func() {
		if err := downloadAndSwapBinary(downloadURL, checksumURL); err != nil {
			slog.Error("update: binary swap failed", "err", err)
			return
		}
		// Signal graceful restart via Redis
		// When Redis is wired: h.Redis.Set(ctx, "roost:restart_pending:"+claims.RoostID, "graceful", 0)
		slog.Info("update: binary swap complete, restart pending")
	}()

	al.Log(r, claims.RoostID, claims.UserID, "server.update_triggered",
		"", map[string]any{"version": req.Version},
	)

	writeAdminJSON(w, http.StatusOK, map[string]interface{}{
		"status":                     "restart_scheduled",
		"active_streams":             0,
		"estimated_restart_minutes":  1,
	})
}

// Restart handles POST /admin/restart.
func (h *AdminHandlers) Restart(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	if claims.Role != "owner" {
		http.Error(w, `{"error":"forbidden: only owner can restart"}`, http.StatusForbidden)
		return
	}

	// Signal graceful restart via Redis
	// When Redis is wired: h.Redis.Set(ctx, "roost:restart_pending:"+claims.RoostID, "graceful", 0)
	slog.Info("restart requested by owner", "user", claims.UserID)

	al.Log(r, claims.RoostID, claims.UserID, "server.restart_triggered", "", nil)

	writeAdminJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "restart_scheduled",
		"active_streams": 0,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fetchLatestRelease() (*githubRelease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/unyeco/roost/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Roost/1.0 UpdateChecker")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// isValidReleaseURL verifies the URL is a GitHub release asset for unyeco/roost.
func isValidReleaseURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return false
	}
	return u.Scheme == "https" &&
		(strings.HasPrefix(u.Host, "github.com") ||
			strings.HasPrefix(u.Host, "objects.githubusercontent.com")) &&
		strings.Contains(u.Path, "unyeco/roost")
}

// downloadAndSwapBinary downloads the binary, verifies its SHA-256 checksum,
// then atomically replaces the current binary via os.Rename.
func downloadAndSwapBinary(downloadURL, checksumURL string) error {
	// Download binary
	tmpFile, err := os.CreateTemp("", "roost-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if err := downloadToFile(downloadURL, tmpFile); err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	tmpFile.Close()

	// Verify checksum
	if checksumURL != "" {
		expectedHash, err := fetchChecksum(checksumURL)
		if err != nil {
			return fmt.Errorf("fetch checksum: %w", err)
		}
		actualHash, err := sha256File(tmpFile.Name())
		if err != nil {
			return fmt.Errorf("hash binary: %w", err)
		}
		if actualHash != expectedHash {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
		}
	}

	// Make executable
	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Get current binary path
	currentBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// Atomic swap — os.Rename is atomic on the same filesystem
	if err := os.Rename(tmpFile.Name(), currentBinary); err != nil {
		return fmt.Errorf("rename binary: %w", err)
	}

	slog.Info("binary swap complete", "path", currentBinary)
	return nil
}

func downloadToFile(fileURL string, dst *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(dst, resp.Body)
	return err
}

func fetchChecksum(checksumURL string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	// Format: "<hash>  <filename>"
	parts := strings.Fields(string(body))
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid checksum file format")
	}
	return strings.ToLower(parts[0]), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
