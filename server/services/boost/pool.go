// Package boost implements the Roost Boost IPTV contribution pool.
//
// Roost Boost allows families/subscribers to contribute their own IPTV
// subscription credentials to a shared pool. In return, they unlock Roost's
// pooled live TV feature — sports channels, live events, etc.
//
// Architecture:
//   - Contributed streams are stored in the DB (family_iptv_sources)
//   - BoostPool loads active contributors at startup and refreshes on changes
//   - When a channel is requested, GetBestSource finds the healthiest contributor
//     that has that channel in their playlist
//   - Source credentials are NEVER returned to clients — Roost relays streams
//
// Security model:
//   - Contributor credentials are stored encrypted (AES-256-GCM)
//   - Stream URLs are server-side only; clients get a signed CDN URL
//   - Pool membership is validated against subscriber status
package boost

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// ContributedStream represents a family's IPTV contribution to the Boost pool.
type ContributedStream struct {
	ID            string    `json:"id"`
	FamilyID      string    `json:"family_id"`
	M3U8URL       string    `json:"-"`  // server-side only, never serialised
	Username      string    `json:"-"`  // server-side only
	Password      string    `json:"-"`  // server-side only, encrypted in DB
	Channels      []string  `json:"channels"` // channel slugs covered
	ChannelCount  int       `json:"channel_count"`
	HealthStatus  string    `json:"health_status"`
	ContributedAt time.Time `json:"contributed_at"`
	Active        bool      `json:"active"`
}

// BoostPool manages the in-memory cache of active Boost contributors.
// Sources are loaded from the DB at startup and refreshed every 10 minutes.
type BoostPool struct {
	mu      sync.RWMutex
	streams map[string]*ContributedStream // keyed by source ID

	// channelIndex maps channel slug → list of source IDs that carry it.
	// Used for fast O(1) lookup in GetBestSource.
	channelIndex map[string][]string
}

// NewBoostPool creates an empty BoostPool.
func NewBoostPool() *BoostPool {
	return &BoostPool{
		streams:      make(map[string]*ContributedStream),
		channelIndex: make(map[string][]string),
	}
}

// Contribute adds or replaces a stream in the pool.
// Rebuilds the channel index for the updated source.
func (p *BoostPool) Contribute(stream *ContributedStream) {
	if stream == nil || stream.ID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// Remove old channel index entries for this source.
	if old, ok := p.streams[stream.ID]; ok {
		p.removeFromIndex(old.ID, old.Channels)
	}

	stream.Active = true
	p.streams[stream.ID] = stream

	// Add new channel index entries.
	for _, ch := range stream.Channels {
		p.channelIndex[ch] = appendUnique(p.channelIndex[ch], stream.ID)
	}

	log.Printf("[boost/pool] contributed source %s (family=%s, channels=%d)",
		stream.ID, stream.FamilyID, len(stream.Channels))
}

// Remove removes a source from the pool (e.g., on unsubscribe or credential revocation).
func (p *BoostPool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if s, ok := p.streams[id]; ok {
		p.removeFromIndex(s.ID, s.Channels)
	}
	delete(p.streams, id)
	log.Printf("[boost/pool] removed source %s", id)
}

// GetBestSource returns the healthiest active contributor that carries channelSlug.
// Selection criteria:
//  1. Must be active (Active=true)
//  2. Must be healthy (HealthStatus="healthy")
//  3. Among healthy: prefer the source with highest channel count (most diverse)
//
// Returns nil if no healthy source covers the channel.
func (p *BoostPool) GetBestSource(channelSlug string) (*ContributedStream, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	sourceIDs := p.channelIndex[channelSlug]
	if len(sourceIDs) == 0 {
		return nil, fmt.Errorf("boost: no sources cover channel %q", channelSlug)
	}

	var best *ContributedStream
	for _, id := range sourceIDs {
		s, ok := p.streams[id]
		if !ok || !s.Active || s.HealthStatus != "healthy" {
			continue
		}
		if best == nil || s.ChannelCount > best.ChannelCount {
			best = s
		}
	}

	if best == nil {
		return nil, fmt.Errorf("boost: no healthy sources for channel %q", channelSlug)
	}
	return best, nil
}

// ListContributors returns all active contributors (without credentials).
func (p *BoostPool) ListContributors() []*ContributedStream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*ContributedStream, 0, len(p.streams))
	for _, s := range p.streams {
		// Return a copy without credentials.
		cp := &ContributedStream{
			ID:            s.ID,
			FamilyID:      s.FamilyID,
			Channels:      s.Channels,
			ChannelCount:  s.ChannelCount,
			HealthStatus:  s.HealthStatus,
			ContributedAt: s.ContributedAt,
			Active:        s.Active,
		}
		out = append(out, cp)
	}
	return out
}

// Size returns the number of active contributors.
func (p *BoostPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.streams)
}

// StartRefreshWorker launches a background goroutine that reloads the pool from
// the DB every refreshInterval. Pass a DB loader function that returns the
// current active contributors from the database.
func (p *BoostPool) StartRefreshWorker(
	ctx context.Context,
	refreshInterval time.Duration,
	load func(ctx context.Context) ([]*ContributedStream, error),
) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	// Initial load.
	p.reloadFromDB(ctx, load)

	for {
		select {
		case <-ctx.Done():
			log.Println("[boost/pool] refresh worker stopped")
			return
		case <-ticker.C:
			p.reloadFromDB(ctx, load)
		}
	}
}

// reloadFromDB replaces the pool contents with fresh data from the DB.
func (p *BoostPool) reloadFromDB(
	ctx context.Context,
	load func(ctx context.Context) ([]*ContributedStream, error),
) {
	streams, err := load(ctx)
	if err != nil {
		log.Printf("[boost/pool] reload error: %v", err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Rebuild from scratch.
	p.streams = make(map[string]*ContributedStream, len(streams))
	p.channelIndex = make(map[string][]string, len(streams)*50)

	for _, s := range streams {
		p.streams[s.ID] = s
		for _, ch := range s.Channels {
			p.channelIndex[ch] = append(p.channelIndex[ch], s.ID)
		}
	}

	log.Printf("[boost/pool] reloaded: %d contributors, %d channels indexed",
		len(p.streams), len(p.channelIndex))
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// removeFromIndex removes sourceID from the channelIndex entries for the given channels.
func (p *BoostPool) removeFromIndex(sourceID string, channels []string) {
	for _, ch := range channels {
		ids := p.channelIndex[ch]
		filtered := ids[:0]
		for _, id := range ids {
			if id != sourceID {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			delete(p.channelIndex, ch)
		} else {
			p.channelIndex[ch] = filtered
		}
	}
}

// appendUnique appends s to slice only if it's not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
