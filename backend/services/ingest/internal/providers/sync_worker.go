// sync_worker.go — Background sync worker for ingest providers.
//
// Every 6 hours (configurable), this worker fetches channels from all active
// providers and upserts them into the channels table. Removed channels are
// marked source_removed=true rather than deleted (preserving history and
// existing subscriber-channel relationships).
//
// Sync steps per provider:
//  1. Fetch channels from provider.GetChannels().
//  2. Upsert to channels table matching on (provider_id, source_external_id).
//     - Preserve manual edits: don't overwrite `is_active`, custom name,
//       category overrides that an admin has explicitly set.
//  3. Mark channels no longer in the provider feed as source_removed=true.
//  4. Update provider.last_sync, provider.channel_count, provider.health_status.
//
// A 20%+ drop in channel count raises an alert without auto-deleting.
package providers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// SyncDB is the minimal database interface the sync worker needs.
// Using an interface allows unit testing without a real Postgres instance.
type SyncDB interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// ProviderRecord represents an ingest_providers row fetched for sync.
type ProviderRecord struct {
	ID                     string
	Name                   string
	ProviderType           string
	Config                 map[string]string // decrypted credentials
	SyncIntervalHours      int
	LastSyncChannelCount   int
}

// SyncWorker manages periodic provider synchronisation.
type SyncWorker struct {
	db             SyncDB
	interval       time.Duration
	alertThreshold float64 // channel drop fraction that triggers an alert (default 0.20)
	alertFn        func(providerID, message string)
}

// NewSyncWorker creates a SyncWorker.
// alertFn is called when a provider's channel count drops by >threshold.
// Pass nil to use a no-op alertFn.
func NewSyncWorker(db SyncDB, interval time.Duration, alertFn func(providerID, message string)) *SyncWorker {
	if alertFn == nil {
		alertFn = func(_, _ string) {}
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &SyncWorker{
		db:             db,
		interval:       interval,
		alertThreshold: 0.20,
		alertFn:        alertFn,
	}
}

// Run starts the periodic sync loop. It blocks until ctx is cancelled.
func (w *SyncWorker) Run(ctx context.Context) {
	// Sync immediately on startup, then on the ticker.
	if err := w.SyncAll(ctx); err != nil {
		log.Printf("[sync_worker] initial sync error: %v", err)
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.SyncAll(ctx); err != nil {
				log.Printf("[sync_worker] sync error: %v", err)
			}
		}
	}
}

// SyncAll fetches all active providers from the DB and syncs each one.
func (w *SyncWorker) SyncAll(ctx context.Context) error {
	providers, err := w.fetchActiveProviders(ctx)
	if err != nil {
		return fmt.Errorf("SyncAll fetch providers: %w", err)
	}
	for _, pr := range providers {
		if err := w.SyncProvider(ctx, pr); err != nil {
			log.Printf("[sync_worker] provider %s (%s) sync failed: %v", pr.Name, pr.ID[:8], err)
			w.updateProviderStatus(ctx, pr.ID, "down", 0)
		}
	}
	return nil
}

// SyncProvider syncs a single provider: fetch channels, upsert, mark removed.
func (w *SyncWorker) SyncProvider(ctx context.Context, pr ProviderRecord) error {
	p, err := NewProvider(pr.ProviderType, pr.Config)
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}

	channels, err := p.GetChannels(ctx)
	if err != nil {
		return fmt.Errorf("GetChannels: %w", err)
	}

	log.Printf("[sync_worker] provider %s: fetched %d channels", pr.Name, len(channels))

	// Alert on significant channel count drop.
	if pr.LastSyncChannelCount > 0 {
		drop := float64(pr.LastSyncChannelCount-len(channels)) / float64(pr.LastSyncChannelCount)
		if drop > w.alertThreshold {
			msg := fmt.Sprintf("channel count dropped from %d to %d (%.0f%% drop) — not auto-deleting",
				pr.LastSyncChannelCount, len(channels), drop*100)
			log.Printf("[sync_worker] ALERT provider %s: %s", pr.Name, msg)
			w.alertFn(pr.ID, msg)
		}
	}

	// Upsert channels and mark removed ones.
	if err := w.upsertChannels(ctx, pr.ID, channels); err != nil {
		return fmt.Errorf("upsertChannels: %w", err)
	}

	// Update provider metadata.
	w.updateProviderStatus(ctx, pr.ID, "healthy", len(channels))
	return nil
}

// upsertChannels upserts provider channels and marks those no longer present as source_removed.
func (w *SyncWorker) upsertChannels(ctx context.Context, providerID string, channels []IngestChannel) error {
	if len(channels) == 0 {
		return nil
	}

	// Build a set of external IDs from the current sync.
	currentIDs := make(map[string]bool, len(channels))
	for _, ch := range channels {
		currentIDs[ch.ID] = true
	}

	// Upsert each channel.
	for _, ch := range channels {
		_, err := w.db.ExecContext(ctx, `
			INSERT INTO channels (
				name, slug, source_url, source_type, is_active,
				provider_id, source_external_id, source_removed,
				logo_url, category
			) VALUES (
				$1, $2, $3, 'hls', true,
				$4, $5, false,
				$6, $7
			)
			ON CONFLICT (provider_id, source_external_id) WHERE provider_id IS NOT NULL
			DO UPDATE SET
				source_url       = EXCLUDED.source_url,
				logo_url         = COALESCE(NULLIF(channels.logo_url, ''), EXCLUDED.logo_url),
				source_removed   = false,
				updated_at       = now()
			`,
			ch.Name,
			toSlug(ch.Name),
			ch.StreamURL,
			providerID,
			ch.ID,
			ch.LogoURL,
			ch.Category,
		)
		if err != nil {
			log.Printf("[sync_worker] upsert channel %q: %v", ch.Name, err)
			// Continue — don't abort entire sync for one bad channel.
		}
	}

	// Mark channels from this provider that are no longer present as source_removed.
	_, err := w.db.ExecContext(ctx, `
		UPDATE channels
		SET source_removed = true, updated_at = now()
		WHERE provider_id = $1
		  AND source_removed = false
		  AND source_external_id NOT IN (
			  SELECT UNNEST($2::text[])
		  )
		`,
		providerID,
		externalIDArray(channels),
	)
	return err
}

// fetchActiveProviders returns all enabled providers from the database.
func (w *SyncWorker) fetchActiveProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := w.db.QueryContext(ctx, `
		SELECT id, name, provider_type, sync_interval_hours,
		       COALESCE(last_sync_channel_count, 0)
		FROM ingest_providers
		WHERE is_active = true
		ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []ProviderRecord
	for rows.Next() {
		var pr ProviderRecord
		err := rows.Scan(&pr.ID, &pr.Name, &pr.ProviderType,
			&pr.SyncIntervalHours, &pr.LastSyncChannelCount)
		if err != nil {
			return nil, err
		}
		// Note: In production, Config would be decrypted from the credentials JSONB.
		// That step is intentionally omitted here — the caller is responsible for
		// decrypting credentials before calling SyncProvider.
		providers = append(providers, pr)
	}
	return providers, rows.Err()
}

// updateProviderStatus updates last_sync, health_status, and channel_count.
func (w *SyncWorker) updateProviderStatus(ctx context.Context, providerID, status string, channelCount int) {
	_, err := w.db.ExecContext(ctx, `
		UPDATE ingest_providers
		SET last_sync = now(),
		    health_status = $1,
		    channel_count = $2,
		    last_sync_status = $1,
		    last_sync_channel_count = $2,
		    updated_at = now()
		WHERE id = $3
	`, status, channelCount, providerID)
	if err != nil {
		log.Printf("[sync_worker] update provider status %s: %v", providerID[:8], err)
	}
}

// toSlug converts a channel name to a URL-safe slug.
// Duplicate slugs are handled by the DB unique constraint — callers add a suffix if needed.
func toSlug(name string) string {
	slug := make([]byte, 0, len(name))
	for _, c := range []byte(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			slug = append(slug, c)
		case c >= 'A' && c <= 'Z':
			slug = append(slug, c+32)
		case c == ' ' || c == '-' || c == '_':
			if len(slug) > 0 && slug[len(slug)-1] != '-' {
				slug = append(slug, '-')
			}
		}
	}
	// Trim trailing dash.
	for len(slug) > 0 && slug[len(slug)-1] == '-' {
		slug = slug[:len(slug)-1]
	}
	if len(slug) == 0 {
		return "channel"
	}
	return string(slug)
}

// externalIDArray extracts external IDs from channels for use in SQL ANY($1::text[]).
func externalIDArray(channels []IngestChannel) interface{} {
	ids := make([]string, len(channels))
	for i, ch := range channels {
		ids[i] = ch.ID
	}
	// Return as a format that lib/pq can use with UNNEST.
	return "{" + join(ids) + "}"
}

func join(ss []string) string {
	b := make([]byte, 0, len(ss)*16)
	for i, s := range ss {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, []byte(s)...)
		b = append(b, '"')
	}
	return string(b)
}
