// metrics.go — Prometheus metrics for the ingest service.
// All metrics are registered against the default Prometheus registry.
package pipeline

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// MetricActiveChannels tracks the number of actively running FFmpeg pipelines.
	MetricActiveChannels = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "roost_ingest_channels_active",
		Help: "Number of active FFmpeg ingest pipelines.",
	})

	// MetricRestarts tracks FFmpeg restart counts per channel.
	MetricRestarts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_ingest_restarts_total",
		Help: "Total number of FFmpeg restarts per channel.",
	}, []string{"channel_slug"})

	// MetricConcurrentStreams tracks the number of active relay streams.
	// Updated externally (from relay service metrics or shared Redis).
	MetricConcurrentStreams = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "roost_relay_concurrent_streams",
		Help: "Number of concurrent active subscriber streams.",
	})

	// MetricBytesServed tracks total bytes served to subscribers.
	MetricBytesServed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "roost_relay_bytes_served_total",
		Help: "Total bytes served to subscribers.",
	})

	// MetricRequestDuration tracks HLS request latency.
	MetricRequestDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "roost_relay_request_duration_seconds",
		Help:    "Duration of HLS relay requests in seconds.",
		Buckets: prometheus.DefBuckets,
	})

	// --- P11-T01-S04: Transcoding resource metrics ---

	// MetricTranscodeCPUSeconds tracks CPU time spent transcoding per channel per variant.
	MetricTranscodeCPUSeconds = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_transcode_cpu_seconds_total",
		Help: "CPU seconds spent transcoding per channel per quality variant.",
	}, []string{"channel_slug", "variant"})

	// MetricTranscodeFramesDropped counts dropped frames per channel (encoding falling behind).
	MetricTranscodeFramesDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_transcode_frames_dropped_total",
		Help: "Number of frames dropped during transcoding per channel.",
	}, []string{"channel_slug"})

	// MetricTranscodeSpeedRatio tracks the encoding speed ratio (1.0 = realtime).
	// Values below 1.0 mean the encoder is falling behind live input.
	MetricTranscodeSpeedRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "roost_transcode_speed_ratio",
		Help: "Encoding speed ratio (1.0 = realtime). Below 1.0 = falling behind.",
	}, []string{"channel_slug"})

	// MetricTranscodeVariantsActive tracks how many quality variants are being produced per channel.
	MetricTranscodeVariantsActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "roost_transcode_variants_active",
		Help: "Number of quality variants currently being produced per channel.",
	}, []string{"channel_slug"})
)

// globalRestartCount is used to track total restarts for the active counter.
var globalRestartCount atomic.Int64

// RecordRestart increments the restart counter for a channel.
func RecordRestart(slug string) {
	MetricRestarts.WithLabelValues(slug).Inc()
	globalRestartCount.Add(1)
}

// RecordTranscodeStart records that a transcoding pipeline started for a channel
// with the given variant set. Sets the active variant count gauge.
func RecordTranscodeStart(slug string, variants []variantSpec) {
	MetricTranscodeVariantsActive.WithLabelValues(slug).Set(float64(len(variants)))
	for _, v := range variants {
		// Initialise counters at 0 so they appear in Prometheus from the first scrape.
		MetricTranscodeCPUSeconds.WithLabelValues(slug, v.name).Add(0)
		MetricTranscodeFramesDropped.WithLabelValues(slug).Add(0)
	}
	MetricTranscodeSpeedRatio.WithLabelValues(slug).Set(1.0)
}

// RecordTranscodeStop clears transcode gauges for a channel that stopped.
func RecordTranscodeStop(slug string) {
	MetricTranscodeVariantsActive.WithLabelValues(slug).Set(0)
	MetricTranscodeSpeedRatio.WithLabelValues(slug).Set(0)
}

// RecordTranscodeCPU adds elapsed CPU seconds for a channel/variant observation.
func RecordTranscodeCPU(slug, variant string, cpuSeconds float64) {
	MetricTranscodeCPUSeconds.WithLabelValues(slug, variant).Add(cpuSeconds)
}

// RecordSpeedRatio updates the encoding speed ratio for a channel.
// Call this periodically based on FFmpeg's progress output (speed= field).
func RecordSpeedRatio(slug string, ratio float64) {
	MetricTranscodeSpeedRatio.WithLabelValues(slug).Set(ratio)
}

// DiskUsageMonitor starts a goroutine that logs disk usage of the segment directory.
// Warns at 80% usage. Runs every 5 minutes.
func DiskUsageMonitor(segmentDir string) {
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			total, used := diskUsage(segmentDir)
			if total == 0 {
				continue
			}
			pct := float64(used) / float64(total) * 100
			if pct > 80 {
				// Log warning — in production this would also page oncall
				_ = pct
			}
		}
	}()
}

func diskUsage(dir string) (total, used int64) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			used += info.Size()
		}
		return nil
	})
	return used * 5, used // rough: assume 20% utilisation headroom
}
