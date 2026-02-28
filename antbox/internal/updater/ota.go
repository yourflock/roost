// Package updater provides OTA self-update for AntBox (T-7H.2.008).
//
// The daemon periodically checks GitHub Releases for a new version.
// When a newer version is found, it downloads the binary, verifies the
// SHA256 checksum, replaces the current binary, and restarts via exec.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	releasesURL    = "https://api.github.com/repos/unyeco/owl/releases/latest"
	updateInterval = 6 * time.Hour
	userAgent      = "antbox-updater/1.0"
)

// Updater checks for and applies OTA updates.
type Updater struct {
	currentVersion string
	logger         *logrus.Logger
	httpClient     *http.Client
	enabled        bool
}

// New creates an Updater. Set enabled=false to disable OTA updates (useful in managed/NAS installs).
func New(currentVersion string, enabled bool, logger *logrus.Logger) *Updater {
	return &Updater{
		currentVersion: currentVersion,
		logger:         logger,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		enabled:        enabled,
	}
}

// Run starts the periodic update check loop. Blocks until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	if !u.enabled {
		u.logger.Info("[updater] OTA updates disabled")
		return
	}

	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	// Check on start
	u.checkAndApply(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.checkAndApply(ctx)
		}
	}
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (u *Updater) checkAndApply(ctx context.Context) {
	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		u.logger.WithError(err).Warn("[updater] Failed to fetch latest release")
		return
	}

	latestVersion := strings.TrimPrefix(release.TagName, "antbox-v")
	if latestVersion == u.currentVersion {
		u.logger.WithField("version", u.currentVersion).Debug("[updater] Already on latest version")
		return
	}

	u.logger.WithFields(logrus.Fields{
		"current": u.currentVersion,
		"latest":  latestVersion,
	}).Info("[updater] New version available, downloading...")

	// Find the binary for our platform
	binaryName := fmt.Sprintf("antbox-%s-%s", runtime.GOOS, runtime.GOARCH)
	checksumName := binaryName + ".sha256"

	var binaryURL, checksumURL string
	for _, asset := range release.Assets {
		if asset.Name == binaryName {
			binaryURL = asset.BrowserDownloadURL
		}
		if asset.Name == checksumName {
			checksumURL = asset.BrowserDownloadURL
		}
	}

	if binaryURL == "" {
		u.logger.WithField("binary", binaryName).Warn("[updater] No binary found for this platform in release")
		return
	}

	if err := u.downloadAndApply(ctx, binaryURL, checksumURL, latestVersion); err != nil {
		u.logger.WithError(err).Error("[updater] Failed to apply update")
		return
	}

	u.logger.WithField("version", latestVersion).Info("[updater] Update applied, restarting...")
	// Restart the process by exec'ing ourselves
	executable, err := os.Executable()
	if err != nil {
		u.logger.WithError(err).Error("[updater] Failed to find executable path for restart")
		return
	}
	_ = syscall.Exec(executable, os.Args, os.Environ())
}

func (u *Updater) fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := u.httpClient.Do(req)
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

func (u *Updater) downloadAndApply(ctx context.Context, binaryURL, checksumURL, version string) error {
	// Download binary to temp file
	tmpFile, err := os.CreateTemp("", "antbox-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := u.download(ctx, binaryURL, tmpFile); err != nil {
		tmpFile.Close()
		return fmt.Errorf("download binary: %w", err)
	}
	tmpFile.Close()

	// Verify checksum if available
	if checksumURL != "" {
		if err := u.verifyChecksum(ctx, tmpPath, checksumURL); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	// Replace current executable
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomic replace via rename
	backupPath := executable + ".backup"
	if err := os.Rename(executable, backupPath); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}

	if err := os.Rename(tmpPath, executable); err != nil {
		// Restore backup on failure
		os.Rename(backupPath, executable) //nolint:errcheck
		return fmt.Errorf("replace binary: %w", err)
	}

	os.Remove(backupPath) //nolint:errcheck
	return nil
}

func (u *Updater) download(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	_, err = io.Copy(dst, resp.Body)
	return err
}

func (u *Updater) verifyChecksum(ctx context.Context, filePath, checksumURL string) error {
	// Fetch expected checksum
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	checksumBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	expectedChecksum := strings.Fields(string(checksumBytes))[0]

	// Compute actual checksum
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actualChecksum := hex.EncodeToString(h.Sum(nil))

	if actualChecksum != expectedChecksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
	}
	return nil
}
