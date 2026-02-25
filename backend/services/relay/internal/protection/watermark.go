// watermark.go — Per-subscriber invisible video watermarking.
// P16-T03: Content Protection
//
// Forensic watermarking embeds a near-invisible subscriber identifier into
// the video stream. If a subscriber records and redistributes content, the
// watermark can identify the source account for termination.
//
// Implementation: FFmpeg drawtext filter with very low opacity (0.05).
// Actual watermarking requires the FFmpeg process to be started with the
// filter inserted into the filter graph. This package generates the filter
// string — the ingest service applies it when launching the FFmpeg relay.
//
// NOTE: This is a "soft" watermark. It can be defeated with video processing.
// For high-value content, use a dedicated forensic watermarking service
// (e.g. NAGRA, Irdeto, Intertrust) that embeds imperceptible coded patterns.
// This implementation is suitable for deterrence, not DRM.
package protection

import (
	"fmt"
	"strings"
)

// WatermarkConfig defines per-subscriber watermarking parameters.
type WatermarkConfig struct {
	// SubscriberID is embedded as the watermark text.
	// Using the UUID makes the watermark unique per account but unintelligible to
	// casual viewers, minimising the chance of the subscriber noticing it.
	SubscriberID string

	// Opacity controls visibility. 0.05 is nearly invisible on normal content
	// but recoverable with image analysis. Range: 0.01 (invisible) – 0.20 (visible).
	Opacity float32

	// Position controls where the text is rendered.
	// Supported: "top-right" (default), "top-left", "bottom-right", "bottom-left", "center"
	Position string

	// FontSize in pixels. Default: 12. Keep small to minimise visibility.
	FontSize int
}

// DefaultWatermarkConfig returns the default configuration.
// Nearly invisible, top-right corner, small font.
func DefaultWatermarkConfig(subscriberID string) WatermarkConfig {
	return WatermarkConfig{
		SubscriberID: subscriberID,
		Opacity:      0.05,
		Position:     "top-right",
		FontSize:     12,
	}
}

// GetWatermarkFilter returns an FFmpeg drawtext filter string for the given
// subscriber. This string is inserted into the FFmpeg filter graph:
//
//	ffmpeg -i {input} -vf "{filter}" -c:a copy {output}
//
// Examples:
//
//	"drawtext=text='abc123...':x=w-tw-10:y=10:fontsize=12:fontcolor=white@0.05:fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
func GetWatermarkFilter(cfg WatermarkConfig) string {
	if cfg.Opacity <= 0 {
		cfg.Opacity = 0.05
	}
	if cfg.FontSize <= 0 {
		cfg.FontSize = 12
	}
	if cfg.Position == "" {
		cfg.Position = "top-right"
	}

	// Sanitize subscriber ID: only allow alphanumeric and hyphens in the filter
	// to prevent FFmpeg filter injection via a crafted subscriber ID.
	safeID := sanitizeFilterText(cfg.SubscriberID)

	x, y := positionToXY(cfg.Position)
	color := fmt.Sprintf("white@%.2f", cfg.Opacity)

	return fmt.Sprintf(
		"drawtext=text='%s':x=%s:y=%s:fontsize=%d:fontcolor=%s:fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		safeID, x, y, cfg.FontSize, color,
	)
}

// positionToXY converts a position name to FFmpeg x/y expressions.
// Uses video geometry variables: w=width, h=height, tw=text_width, th=text_height.
func positionToXY(position string) (x, y string) {
	switch strings.ToLower(position) {
	case "top-left":
		return "10", "10"
	case "top-right":
		return "w-tw-10", "10"
	case "bottom-left":
		return "10", "h-th-10"
	case "bottom-right":
		return "w-tw-10", "h-th-10"
	case "center":
		return "(w-tw)/2", "(h-th)/2"
	default:
		// Default: top-right
		return "w-tw-10", "10"
	}
}

// sanitizeFilterText removes characters that could break the FFmpeg filter string.
// Only alphanumeric characters and hyphens are safe to embed in a drawtext filter.
func sanitizeFilterText(s string) string {
	var sb strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			sb.WriteRune(c)
		}
	}
	return sb.String()
}
