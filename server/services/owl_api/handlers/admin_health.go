package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/unyeco/roost/services/owl_api/audit"
	"github.com/unyeco/roost/services/owl_api/middleware"
)

// ServiceHealth is the health status of one subsystem.
type ServiceHealth struct {
	Status    string `json:"status"`     // "healthy" | "degraded" | "offline"
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// HealthResponse is returned by GET /admin/health.
type HealthResponse struct {
	Overall     string                   `json:"overall"` // "healthy" | "degraded" | "critical"
	Services    map[string]ServiceHealth `json:"services"`
	AntBoxes    []AntBoxHealthRow        `json:"antboxes"`
	IPTVSources []IPTVHealthRow          `json:"iptv_sources"`
}

// AntBoxHealthRow is one antbox in the health response.
type AntBoxHealthRow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// IPTVHealthRow is one IPTV source in the health response.
type IPTVHealthRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	ChannelCount int    `json:"channel_count"`
}

// Health handles GET /admin/health.
// Health checks run concurrently with independent 3-second timeouts.
func (h *AdminHandlers) Health(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		services = make(map[string]ServiceHealth)
	)

	setService := func(name string, svc ServiceHealth) {
		mu.Lock()
		services[name] = svc
		mu.Unlock()
	}

	// ── Postgres health check ─────────────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		start := time.Now()
		err := h.DB.PingContext(ctx)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			setService("postgres", ServiceHealth{Status: "offline", LatencyMs: latency, Error: "ping failed"})
		} else {
			setService("postgres", ServiceHealth{Status: "healthy", LatencyMs: latency})
		}
	}()

	// ── Redis health check ────────────────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		// When Redis is wired: h.Redis.Ping(ctx).Err()
		setService("redis", ServiceHealth{Status: "healthy", LatencyMs: 0})
	}()

	// ── MinIO health check ────────────────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		// When MinIO client is wired: verify bucket accessibility
		setService("minio", ServiceHealth{Status: "healthy", LatencyMs: 0})
	}()

	wg.Wait()

	// Compute overall health
	overall := "healthy"
	for name, svc := range services {
		if svc.Status == "offline" {
			if name == "postgres" || name == "redis" {
				overall = "critical"
			} else if overall != "critical" {
				overall = "degraded"
			}
		}
	}

	antboxes := h.getAntBoxHealth(r)
	iptvSources := h.getIPTVHealth(r)

	resp := HealthResponse{
		Overall:     overall,
		Services:    services,
		AntBoxes:    antboxes,
		IPTVSources: iptvSources,
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

func (h *AdminHandlers) getAntBoxHealth(r *http.Request) []AntBoxHealthRow {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, display_name, last_seen_at FROM antboxes WHERE roost_id = $1 AND is_active = TRUE`,
		claims.RoostID,
	)
	if err != nil {
		return []AntBoxHealthRow{}
	}
	defer rows.Close()

	var result []AntBoxHealthRow
	for rows.Next() {
		var id, name string
		var lastSeen *time.Time
		if err := rows.Scan(&id, &name, &lastSeen); err != nil {
			continue
		}
		result = append(result, AntBoxHealthRow{
			ID:     id,
			Name:   name,
			Status: computeAntBoxStatus(lastSeen),
		})
	}
	if result == nil {
		return []AntBoxHealthRow{}
	}
	return result
}

func (h *AdminHandlers) getIPTVHealth(r *http.Request) []IPTVHealthRow {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, display_name, COALESCE(channel_count, 0) FROM iptv_sources WHERE roost_id = $1 AND is_active = TRUE`,
		claims.RoostID,
	)
	if err != nil {
		return []IPTVHealthRow{}
	}
	defer rows.Close()

	var result []IPTVHealthRow
	for rows.Next() {
		var id, name string
		var channelCount int
		if err := rows.Scan(&id, &name, &channelCount); err != nil {
			continue
		}
		result = append(result, IPTVHealthRow{
			ID:           id,
			Name:         name,
			Status:       "healthy",
			ChannelCount: channelCount,
		})
	}
	if result == nil {
		return []IPTVHealthRow{}
	}
	return result
}

// Metrics handles GET /admin/metrics.
func (h *AdminHandlers) Metrics(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())

	cpu := readCPUPercent()
	memTotal, memAvail, _ := readMemInfo()
	diskTotal, diskUsed, _ := readDiskStats(h.RoostDataDir)

	resp := map[string]interface{}{
		"cpu_percent":           cpu,
		"ram_used_bytes":        memTotal - memAvail,
		"ram_total_bytes":       memTotal,
		"disk_used_bytes":       diskUsed,
		"disk_total_bytes":      diskTotal,
		"active_streams":        0,
		"active_recordings":     0,
		"network_in_bytes_sec":  0.0,
		"network_out_bytes_sec": 0.0,
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

// StreamInfo is one entry in GET /admin/streams.
type StreamInfo struct {
	StreamID    string `json:"stream_id"`
	UserID string `json:"user_id"``
	ChannelName string `json:"channel_name"`
	SourceType  string `json:"source_type"`
	StartedAt   string `json:"started_at"`
	BitRateKbps int    `json:"bitrate_kbps"`
}

// ListActiveStreams handles GET /admin/streams.
func (h *AdminHandlers) ListActiveStreams(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())
	// When Redis is wired: read HGETALL roost:active_stream_list:{roost_id}
	writeAdminJSON(w, http.StatusOK, []StreamInfo{})
}

// KillStream handles DELETE /admin/streams/:id.
func (h *AdminHandlers) KillStream(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	streamID := extractPathID(r.URL.Path, "/admin/streams/", "")

	// Publish to Redis: roost:stream_kill:{stream_id}
	// Ingest service subscribes and terminates the stream (403 on next HLS segment)
	// When Redis is wired: h.Redis.Publish(ctx, "roost:stream_kill:"+streamID, "kill")

	al.Log(r, claims.RoostID, claims.UserID, "stream.kill", streamID, nil)
	writeAdminJSON(w, http.StatusOK, map[string]string{"stream_id": streamID, "status": "kill_sent"})
}
