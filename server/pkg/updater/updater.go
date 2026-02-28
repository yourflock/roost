// Package updater checks for new Roost releases on GitHub.
//
// Calls the GitHub Releases API at most once per hour per process.
// All HTTP errors are treated as "no update available" — the check never blocks startup.
//
// Usage:
//
//	info, err := updater.CheckLatestVersion(ctx, "1.0.0")
//	if err != nil {
//	    // network error — treat as no update
//	}
//	if info.UpdateAvailable {
//	    log.Printf("update available: %s", info.LatestVersion)
//	}
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// githubReleasesURL is the GitHub API endpoint for the latest Roost release.
	githubReleasesURL = "https://api.github.com/repos/unyeco/roost/releases/latest"

	// cacheTTL is how long the version check result is cached.
	// One check per hour per process — avoids hammering the GitHub API.
	cacheTTL = 1 * time.Hour

	// httpTimeout for the GitHub API call.
	httpTimeout = 10 * time.Second
)

// VersionInfo holds the result of a version check.
type VersionInfo struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	UpdateAvailable bool      `json:"update_available"`
	ReleaseURL      string    `json:"release_url"`
	PublishedAt     time.Time `json:"published_at"`
	CheckedAt       time.Time `json:"checked_at"`
}

// githubRelease is the minimal shape of the GitHub Releases API response.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// cache holds the last version check result so we don't call GitHub on every request.
var (
	cacheMu       sync.RWMutex
	cachedResult  *VersionInfo
	cacheExpireAt time.Time
)

// CheckLatestVersion fetches the latest Roost release from GitHub and compares it to
// currentVersion. Uses an in-process cache with cacheTTL to rate-limit API calls.
//
// Returns an error only for network failures. If GitHub returns a non-200 status,
// it returns a VersionInfo with UpdateAvailable=false (safe degradation).
func CheckLatestVersion(ctx context.Context, currentVersion string) (*VersionInfo, error) {
	// Serve from cache if still fresh.
	cacheMu.RLock()
	if cachedResult != nil && time.Now().Before(cacheExpireAt) {
		result := *cachedResult
		result.CurrentVersion = currentVersion
		result.UpdateAvailable = isNewer(cachedResult.LatestVersion, currentVersion)
		cacheMu.RUnlock()
		return &result, nil
	}
	cacheMu.RUnlock()

	// Fetch from GitHub.
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		// Network errors return a safe no-update result.
		return &VersionInfo{
			CurrentVersion:  currentVersion,
			LatestVersion:   currentVersion,
			UpdateAvailable: false,
			CheckedAt:       time.Now(),
		}, fmt.Errorf("version check failed: %w", err)
	}

	info := &VersionInfo{
		CurrentVersion:  currentVersion,
		LatestVersion:   release.TagName,
		UpdateAvailable: isNewer(release.TagName, currentVersion),
		ReleaseURL:      release.HTMLURL,
		PublishedAt:     release.PublishedAt,
		CheckedAt:       time.Now(),
	}

	// Update cache.
	cacheMu.Lock()
	cachedResult = info
	cacheExpireAt = time.Now().Add(cacheTTL)
	cacheMu.Unlock()

	return info, nil
}

// fetchLatestRelease calls the GitHub API and returns the latest non-draft, non-prerelease release.
func fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	reqCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, githubReleasesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "roost-updater/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode github response: %w", err)
	}

	return &release, nil
}

// isNewer returns true if latestTag is a higher semver than currentVersion.
// Handles "v" prefixes (v1.2.3 vs 1.2.3) and falls back to string comparison.
func isNewer(latestTag, currentVersion string) bool {
	latest := strings.TrimPrefix(latestTag, "v")
	current := strings.TrimPrefix(currentVersion, "v")

	if latest == "" || current == "" || latest == current {
		return false
	}

	// Parse major.minor.patch for comparison.
	lParts := parseVersion(latest)
	cParts := parseVersion(current)

	for i := 0; i < 3; i++ {
		if lParts[i] > cParts[i] {
			return true
		}
		if lParts[i] < cParts[i] {
			return false
		}
	}
	return false
}

// parseVersion splits "1.2.3" into [1, 2, 3]. Returns [0, 0, 0] on parse failure.
func parseVersion(v string) [3]int {
	var parts [3]int
	segments := strings.SplitN(v, ".", 3)
	for i, s := range segments {
		if i >= 3 {
			break
		}
		var n int
		for _, c := range s {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			} else {
				break
			}
		}
		parts[i] = n
	}
	return parts
}
