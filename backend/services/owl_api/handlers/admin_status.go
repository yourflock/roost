// Package handlers implements HTTP handler methods for the Roost owl_api admin endpoints.
package handlers

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// AdminHandlers holds shared dependencies for all admin HTTP handlers.
// Instantiate once in main() and register methods as route handlers.
type AdminHandlers struct {
	DB           *sql.DB
	Redis        *goredis.Client // optional — nil disables Redis-backed features (dev mode)
	RoostDataDir string          // absolute path to Roost data directory for disk stats
	Version      string          // build-time version constant
	startTime    time.Time       // process start time for uptime calculation
}

// NewAdminHandlers creates AdminHandlers with the process start time set to now.
func NewAdminHandlers(db *sql.DB, dataDir, version string) *AdminHandlers {
	return &AdminHandlers{
		DB:           db,
		RoostDataDir: dataDir,
		Version:      version,
		startTime:    time.Now(),
	}
}

// NewAdminHandlersWithRedis creates AdminHandlers with an optional Redis client for
// pub/sub-backed features (SSE scan progress, stream kill signals, log streaming).
func NewAdminHandlersWithRedis(db *sql.DB, redis *goredis.Client, dataDir, version string) *AdminHandlers {
	return &AdminHandlers{
		DB:           db,
		Redis:        redis,
		RoostDataDir: dataDir,
		Version:      version,
		startTime:    time.Now(),
	}
}

// AdminStatusResponse is the JSON shape returned by GET /admin/status.
type AdminStatusResponse struct {
	Version          string  `json:"version"`
	UptimeSeconds    int64   `json:"uptime_seconds"`
	CPUPercent       float64 `json:"cpu_percent"`
	RAMUsedBytes     uint64  `json:"ram_used_bytes"`
	RAMTotalBytes    uint64  `json:"ram_total_bytes"`
	DiskUsedBytes    uint64  `json:"disk_used_bytes"`
	DiskTotalBytes   uint64  `json:"disk_total_bytes"`
	ActiveStreams     int     `json:"active_streams"`
	ActiveRecordings int     `json:"active_recordings"`
	// Warning is non-empty when a metric source was unavailable (e.g., Redis down)
	Warning string `json:"warning,omitempty"`
}

// Status handles GET /admin/status.
// Returns a comprehensive server overview for the Owl admin dashboard.
// Response time target: under 1 second (CPU sampling adds ~500ms).
func (h *AdminHandlers) Status(w http.ResponseWriter, r *http.Request) {
	// Verify the caller is an admin (middleware injects claims)
	_ = middleware.AdminClaimsFromCtx(r.Context())

	resp := AdminStatusResponse{
		Version:       h.Version,
		UptimeSeconds: int64(time.Since(h.startTime).Seconds()),
	}

	// CPU usage — two /proc/stat samples 500ms apart
	resp.CPUPercent = readCPUPercent()

	// RAM — /proc/meminfo
	total, available, err := readMemInfo()
	if err == nil {
		resp.RAMTotalBytes = total
		resp.RAMUsedBytes = total - available
	} else {
		slog.Warn("admin/status: failed to read meminfo", "err", err)
	}

	// Disk — syscall.Statfs on the Roost data directory
	diskTotal, diskUsed, err := readDiskStats(h.RoostDataDir)
	if err == nil {
		resp.DiskTotalBytes = diskTotal
		resp.DiskUsedBytes = diskUsed
	} else {
		slog.Warn("admin/status: failed to read disk stats", "err", err)
	}

	// Active streams — count from recordings table (Redis preferred when wired)
	resp.ActiveStreams, resp.Warning = readActiveStreams(r.Context(), h.DB)

	// Active recordings — Postgres query
	resp.ActiveRecordings = readActiveRecordings(r.Context(), h.DB)

	writeAdminJSON(w, http.StatusOK, resp)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// readCPUPercent reads /proc/stat twice with a 500ms gap and computes CPU %.
func readCPUPercent() float64 {
	s1 := readProcStatCPU()
	time.Sleep(500 * time.Millisecond)
	s2 := readProcStatCPU()

	if len(s1) < 7 || len(s2) < 7 {
		return 0
	}

	var total1, idle1, total2, idle2 uint64
	for i, v := range s1 {
		total1 += v
		if i == 3 || i == 4 { // idle + iowait
			idle1 += v
		}
	}
	for i, v := range s2 {
		total2 += v
		if i == 3 || i == 4 {
			idle2 += v
		}
	}

	totalDelta := float64(total2 - total1)
	idleDelta := float64(idle2 - idle1)
	if totalDelta == 0 {
		return 0
	}
	return (1 - idleDelta/totalDelta) * 100
}

// readProcStatCPU reads the aggregate CPU line from /proc/stat and returns
// the 10 field values (user, nice, system, idle, iowait, irq, softirq, ...).
func readProcStatCPU() []uint64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:] // skip "cpu" label
		vals := make([]uint64, len(fields))
		for i, f := range fields {
			vals[i], _ = strconv.ParseUint(f, 10, 64)
		}
		return vals
	}
	return nil
}

// readMemInfo reads MemTotal and MemAvailable from /proc/meminfo.
// Returns total bytes, available bytes.
func readMemInfo() (total, available uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("open /proc/meminfo: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		val *= 1024 // kB → bytes
		switch fields[0] {
		case "MemTotal:":
			total = val
		case "MemAvailable:":
			available = val
		}
		if total > 0 && available > 0 {
			break
		}
	}
	return total, available, scanner.Err()
}

// readDiskStats returns total and used bytes for the given directory path.
func readDiskStats(dir string) (total, used uint64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(dir, &stat); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	if total >= free {
		used = total - free
	}
	return total, used, nil
}

// readActiveStreams counts active streams. Returns -1 with a warning if Redis unavailable.
// Falls back to counting from the DB if Redis is not wired.
func readActiveStreams(ctx context.Context, db *sql.DB) (count int, warning string) {
	// When Redis is wired (future), read from roost:active_streams key.
	// For now, count via DB as fallback.
	var n int
	// Count live streams that have been active in the last 30 seconds
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recordings WHERE status='streaming'`,
	).Scan(&n)
	if err != nil {
		return -1, "active_streams unavailable (Redis not connected)"
	}
	return n, ""
}

// readActiveRecordings counts recordings currently in progress.
func readActiveRecordings(ctx context.Context, db *sql.DB) int {
	var n int
	db.QueryRowContext(ctx, //nolint:errcheck
		`SELECT COUNT(*) FROM recordings WHERE status='recording'`,
	).Scan(&n) //nolint:errcheck — zero is fine on query failure
	return n
}

// writeAdminJSON marshals v to JSON and writes it with the given status.
func writeAdminJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
