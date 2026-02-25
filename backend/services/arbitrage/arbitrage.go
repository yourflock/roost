// arbitrage.go — Stream Arbitrage engine for Roost.
// Monitors all registered channel sources in real-time and routes playback
// requests to the highest-quality source. Sources are probed every 10 seconds
// by downloading a short segment and measuring bitrate and latency.
//
// Quality score formula:
//   score = (bitrate_kbps / 1000) - (latency_ms / 1000)
//   Minimum score: 0 (clamped).
//
// Admin route:
//   GET /v1/admin/channels/{id}/source-quality — recent quality log for a channel
package arbitrage

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SourceHealth tracks real-time quality metrics for one channel source.
type SourceHealth struct {
	SourceID    string
	SourceURL   string
	Score       float64
	BitrateKbps int
	LatencyMs   int
	LastProbed  time.Time
}

// Arbitrage manages continuous health probing for all active channels.
// Create with New() and call RegisterSource / GetBestSource as needed.
type Arbitrage struct {
	db      *sql.DB
	mu      sync.RWMutex
	sources map[string][]*SourceHealth // channelID → []sources
	quit    chan struct{}
}

// New creates and starts the arbitrage engine. The caller is responsible for
// calling Stop() when the engine is no longer needed.
func New(db *sql.DB) *Arbitrage {
	a := &Arbitrage{
		db:      db,
		sources: make(map[string][]*SourceHealth),
		quit:    make(chan struct{}),
	}
	go a.probeLoop()
	return a
}

// Stop shuts down the background probe loop.
func (a *Arbitrage) Stop() {
	close(a.quit)
}

// RegisterSource adds a source URL for a channel to the probe pool.
// sourceID is typically a UUID from the channel_sources or ingest_providers table.
func (a *Arbitrage) RegisterSource(channelID, sourceID, sourceURL string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Avoid duplicate registration
	for _, s := range a.sources[channelID] {
		if s.SourceID == sourceID {
			s.SourceURL = sourceURL // update URL if it changed
			return
		}
	}
	a.sources[channelID] = append(a.sources[channelID], &SourceHealth{
		SourceID:  sourceID,
		SourceURL: sourceURL,
		Score:     1.0, // optimistic initial score
	})
}

// DeregisterSource removes a source from the probe pool.
func (a *Arbitrage) DeregisterSource(channelID, sourceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := a.sources[channelID]
	for i, s := range list {
		if s.SourceID == sourceID {
			a.sources[channelID] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// GetBestSource returns the highest-scoring healthy source for a channel.
// Returns nil if no sources are registered.
func (a *Arbitrage) GetBestSource(channelID string) *SourceHealth {
	a.mu.RLock()
	defer a.mu.RUnlock()
	sources := a.sources[channelID]
	if len(sources) == 0 {
		return nil
	}
	best := sources[0]
	for _, s := range sources[1:] {
		if s.Score > best.Score {
			best = s
		}
	}
	return best
}

// probeLoop runs every 10 seconds and probes all registered sources in parallel.
func (a *Arbitrage) probeLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.probeAll()
		case <-a.quit:
			return
		}
	}
}

// probeAll takes a snapshot of the source map and probes each source concurrently.
func (a *Arbitrage) probeAll() {
	a.mu.RLock()
	snap := make(map[string][]*SourceHealth, len(a.sources))
	for k, v := range a.sources {
		copied := make([]*SourceHealth, len(v))
		copy(copied, v)
		snap[k] = copied
	}
	a.mu.RUnlock()

	for channelID, sources := range snap {
		for _, src := range sources {
			go a.probeSource(channelID, src)
		}
	}
}

// probeSource fetches up to 64KB from the source URL, measures throughput and
// latency, computes a score, and writes the result to source_quality_log.
func (a *Arbitrage) probeSource(channelID string, src *SourceHealth) {
	start := time.Now()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(src.SourceURL)
	if err != nil {
		a.mu.Lock()
		src.Score = 0
		src.LatencyMs = 5000
		a.mu.Unlock()
		return
	}
	defer resp.Body.Close()

	// Read up to 64KB to measure throughput
	buf := make([]byte, 65536)
	n, _ := io.ReadAtLeast(resp.Body, buf, 1)
	elapsed := time.Since(start)

	latencyMs := int(elapsed.Milliseconds())
	bitrateKbps := 0
	if elapsed.Seconds() > 0 {
		bitrateKbps = int(float64(n*8) / elapsed.Seconds() / 1000)
	}

	score := float64(bitrateKbps)/1000 - float64(latencyMs)/1000
	if score < 0 {
		score = 0
	}

	a.mu.Lock()
	src.LatencyMs = latencyMs
	src.BitrateKbps = bitrateKbps
	src.Score = score
	src.LastProbed = time.Now()
	a.mu.Unlock()

	// Write to DB (best-effort — never block the probe goroutine on DB errors)
	_ = a.db.QueryRow(
		`INSERT INTO source_quality_log
		 (channel_id, source_id, source_url, bitrate_kbps, latency_ms, score)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		channelID,
		src.SourceID,
		src.SourceURL,
		bitrateKbps,
		latencyMs,
		score,
	)
}

// HandleGetSourceQuality returns the 20 most recent quality log entries for a channel.
// GET /v1/admin/channels/{id}/source-quality
// Query param: channel_id (UUID)  — also accepts path value "id" via Go 1.22 routing
func HandleGetSourceQuality(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := r.PathValue("id")
		if channelID == "" {
			channelID = r.URL.Query().Get("channel_id")
		}
		if channelID == "" {
			http.Error(w, "channel id required", http.StatusBadRequest)
			return
		}
		if _, err := uuid.Parse(channelID); err != nil {
			http.Error(w, "invalid channel id", http.StatusBadRequest)
			return
		}

		rows, err := db.QueryContext(r.Context(),
			`SELECT source_url, bitrate_kbps, latency_ms, score, measured_at
			 FROM source_quality_log
			 WHERE channel_id = $1
			 ORDER BY measured_at DESC LIMIT 20`,
			channelID,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type QualityEntry struct {
			SourceURL   string    `json:"source_url"`
			BitrateKbps int       `json:"bitrate_kbps"`
			LatencyMs   int       `json:"latency_ms"`
			Score       float64   `json:"score"`
			MeasuredAt  time.Time `json:"measured_at"`
		}

		var entries []QualityEntry
		for rows.Next() {
			var e QualityEntry
			if err := rows.Scan(
				&e.SourceURL, &e.BitrateKbps, &e.LatencyMs, &e.Score, &e.MeasuredAt,
			); err != nil {
				continue
			}
			entries = append(entries, e)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"channel_id": channelID,
			"entries":    entries,
		})
	}
}
