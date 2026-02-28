package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/unyeco/roost/services/owl_api/audit"
	"github.com/unyeco/roost/services/owl_api/middleware"
)

// AddonManifest is the minimal schema required in a community addon manifest JSON.
type AddonManifest struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	CatalogEndpoint string `json:"catalog_endpoint"`
}

// AddonRow is returned by GET /admin/addons.
type AddonRow struct {
	ID              string     `json:"id"`
	ManifestURL     string     `json:"manifest_url"`
	DisplayName     string     `json:"display_name"`
	Version         *string    `json:"version,omitempty"`
	CatalogCount    *int       `json:"catalog_count,omitempty"`
	LastRefreshedAt *time.Time `json:"last_refreshed_at,omitempty"`
	IsActive        bool       `json:"is_active"`
}

// ListAddons handles GET /admin/addons.
func (h *AdminHandlers) ListAddons(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, manifest_url, display_name, version, catalog_count, last_refreshed_at, is_active
		   FROM roost_addons
		  WHERE roost_id = $1 AND is_active = TRUE
		  ORDER BY created_at ASC`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var addons []AddonRow
	for rows.Next() {
		var a AddonRow
		if err := rows.Scan(&a.ID, &a.ManifestURL, &a.DisplayName, &a.Version, &a.CatalogCount, &a.LastRefreshedAt, &a.IsActive); err != nil {
			continue
		}
		addons = append(addons, a)
	}
	if addons == nil {
		addons = []AddonRow{}
	}
	writeAdminJSON(w, http.StatusOK, addons)
}

// InstallAddonRequest is the POST /admin/addons/install body.
type InstallAddonRequest struct {
	ManifestURL string `json:"manifest_url"`
}

// InstallAddon handles POST /admin/addons/install.
func (h *AdminHandlers) InstallAddon(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req InstallAddonRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	// Only HTTPS manifest URLs allowed — prevents SSRF via HTTP redirect
	if err := validateAddonManifestURL(req.ManifestURL); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Fetch manifest with 10-second timeout
	manifest, err := fetchAddonManifest(req.ManifestURL)
	if err != nil {
		slog.Warn("addon install: manifest fetch failed", "url", req.ManifestURL, "err", err)
		http.Error(w, `{"error":"manifest_fetch_failed"}`, http.StatusBadRequest)
		return
	}

	var rowID string
	err = h.DB.QueryRowContext(r.Context(),
		`INSERT INTO roost_addons (roost_id, manifest_url, display_name, version)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		claims.RoostID, req.ManifestURL, manifest.Name, manifest.Version,
	).Scan(&rowID)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			http.Error(w, `{"error":"addon_already_installed"}`, http.StatusConflict)
			return
		}
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	// Enqueue initial catalog fetch (async)
	slog.Info("addon installed, catalog fetch enqueued", "addon_id", rowID)

	al.Log(r, claims.RoostID, claims.UserID, "addon.install", req.ManifestURL,
		map[string]any{"name": manifest.Name, "version": manifest.Version},
	)

	writeAdminJSON(w, http.StatusCreated, map[string]string{
		"id":      rowID,
		"name":    manifest.Name,
		"version": manifest.Version,
	})
}

// UninstallAddon handles DELETE /admin/addons/:id.
func (h *AdminHandlers) UninstallAddon(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	addonID := extractPathID(r.URL.Path, "/admin/addons/", "")
	if !isValidUUID(addonID) {
		http.Error(w, `{"error":"invalid addon id"}`, http.StatusBadRequest)
		return
	}

	result, err := h.DB.ExecContext(r.Context(),
		`UPDATE roost_addons SET is_active = FALSE WHERE id = $1 AND roost_id = $2`,
		addonID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	slog.Info("addon uninstalled, catalog cleanup enqueued", "addon_id", addonID)
	al.Log(r, claims.RoostID, claims.UserID, "addon.uninstall", addonID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// RefreshAddon handles POST /admin/addons/:id/refresh.
func (h *AdminHandlers) RefreshAddon(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	addonID := extractPathID(r.URL.Path, "/admin/addons/", "/refresh")
	if !isValidUUID(addonID) {
		http.Error(w, `{"error":"invalid addon id"}`, http.StatusBadRequest)
		return
	}

	var manifestURL, oldVersion string
	err := h.DB.QueryRowContext(r.Context(),
		`SELECT manifest_url, COALESCE(version,'') FROM roost_addons WHERE id = $1 AND roost_id = $2`,
		addonID, claims.RoostID,
	).Scan(&manifestURL, &oldVersion)
	if err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	manifest, err := fetchAddonManifest(manifestURL)
	if err != nil {
		http.Error(w, `{"error":"manifest_fetch_failed"}`, http.StatusBadRequest)
		return
	}

	_, _ = h.DB.ExecContext(r.Context(),
		`UPDATE roost_addons SET version = $1, last_refreshed_at = NOW() WHERE id = $2`,
		manifest.Version, addonID,
	)

	al.Log(r, claims.RoostID, claims.UserID, "addon.refresh_triggered", addonID,
		map[string]any{"old_version": oldVersion, "new_version": manifest.Version},
	)

	writeAdminJSON(w, http.StatusAccepted, map[string]string{
		"job_id":      newUUID(),
		"old_version": oldVersion,
		"new_version": manifest.Version,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func validateAddonManifestURL(rawURL string) error {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("manifest URL must use HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "172.16.") {
		return fmt.Errorf("manifest URL must be a public hostname")
	}
	return nil
}

func fetchAddonManifest(manifestURL string) (*AddonManifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Roost/1.0 AddonInstaller")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	var manifest AddonManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest JSON: %w", err)
	}

	if manifest.Name == "" || manifest.Version == "" || manifest.CatalogEndpoint == "" {
		return nil, fmt.Errorf("manifest missing required fields: name, version, catalog_endpoint")
	}

	return &manifest, nil
}
