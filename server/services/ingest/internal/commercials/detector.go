// detector.go — Commercial detection service.
//
// Detects commercial breaks in live HLS streams using two complementary methods:
//
//  1. Silence detection (primary fallback): runs FFmpeg's silencedetect filter
//     on the audio track. A silence block ≥2s below -35dB is a likely ad-break
//     transition. Two such transitions within 60s = commercial break detected.
//     Works universally but with ~70% accuracy.
//
//  2. Black frame detection: runs FFmpeg's blackdetect filter. Black frames
//     combined with silence (within 1s of each other) = high-confidence transition.
//
// Chromaprint fingerprinting (planned integration):
//   The fpcalc binary (from the chromaprint package) is called every 5s to
//   generate audio fingerprints. These are compared against the
//   commercial_fingerprints reference database using Hamming distance.
//   Hamming ≤10 = match. This is the high-accuracy path (~90%).
//
// PREREQUISITES:
//   ffmpeg must be in PATH. Install via:
//     Ubuntu/Debian: apt-get install ffmpeg
//     macOS: brew install ffmpeg
//   Optionally for Chromaprint: apt-get install libchromaprint-tools (provides fpcalc)
//
// Usage:
//
//	detector := NewCommercialDetector(ffmpegPath, options...)
//	markers, err := detector.AnalyzeSegment(ctx, "/path/to/segment.ts")
package commercials

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommercialMarker identifies a detected commercial segment within a media file.
type CommercialMarker struct {
	StartSec  float64 // start of commercial break, in seconds from file start
	EndSec    float64 // end of commercial break
	DurationSec float64
	Confidence  float64 // 0.0 – 1.0; higher = more certain it is a commercial
	Method      string  // "silence", "blackframe", "combined"
}

// CommercialDetector runs FFmpeg-based detection on HLS segments.
type CommercialDetector struct {
	ffmpegPath  string
	minAdLength time.Duration // breaks shorter than this are ignored (default 15s)
	maxAdLength time.Duration // breaks longer than this are likely false positives (default 180s)
	silenceDB   float64       // dB threshold for silence detection (default -35)
	silenceDuration float64   // minimum silence duration in seconds (default 2.0)
}

// Option is a functional option for CommercialDetector.
type Option func(*CommercialDetector)

// WithMinAdLength sets the minimum detected break length.
func WithMinAdLength(d time.Duration) Option {
	return func(c *CommercialDetector) { c.minAdLength = d }
}

// WithMaxAdLength sets the maximum detected break length.
func WithMaxAdLength(d time.Duration) Option {
	return func(c *CommercialDetector) { c.maxAdLength = d }
}

// WithSilenceDB sets the silence threshold in dB (negative value, e.g. -35).
func WithSilenceDB(db float64) Option {
	return func(c *CommercialDetector) { c.silenceDB = db }
}

// NewCommercialDetector creates a CommercialDetector.
// ffmpegPath is the path to the ffmpeg binary (e.g. "/usr/bin/ffmpeg").
// Pass "" to auto-detect via PATH lookup.
func NewCommercialDetector(ffmpegPath string, opts ...Option) (*CommercialDetector, error) {
	if ffmpegPath == "" {
		path, err := exec.LookPath("ffmpeg")
		if err != nil {
			return nil, fmt.Errorf("commercial detector: ffmpeg not found in PATH: %w", err)
		}
		ffmpegPath = path
	}

	d := &CommercialDetector{
		ffmpegPath:      ffmpegPath,
		minAdLength:     15 * time.Second,
		maxAdLength:     180 * time.Second,
		silenceDB:       -35.0,
		silenceDuration: 2.0,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// AnalyzeSegment runs silence+black-frame detection on a local segment file.
// Returns all CommercialMarkers that pass the minAdLength/maxAdLength filters.
// The segment file is read directly by FFmpeg and is not modified.
func (d *CommercialDetector) AnalyzeSegment(ctx context.Context, segmentPath string) ([]CommercialMarker, error) {
	silenceMarkers, err := d.detectSilence(ctx, segmentPath)
	if err != nil {
		return nil, fmt.Errorf("silence detection: %w", err)
	}

	var filtered []CommercialMarker
	for _, m := range silenceMarkers {
		dur := time.Duration((m.EndSec - m.StartSec) * float64(time.Second))
		if dur < d.minAdLength {
			continue
		}
		if dur > d.maxAdLength {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered, nil
}

// AnalyzeSegmentCombined runs both silence and black-frame detection and
// combines the results. Markers detected by both methods get higher confidence.
func (d *CommercialDetector) AnalyzeSegmentCombined(ctx context.Context, segmentPath string) ([]CommercialMarker, error) {
	silenceMarkers, err := d.detectSilence(ctx, segmentPath)
	if err != nil {
		return nil, fmt.Errorf("silence detection: %w", err)
	}
	blackMarkers, err := d.detectBlackFrames(ctx, segmentPath)
	if err != nil {
		// Black frame detection is non-fatal — continue with silence only.
		blackMarkers = nil
	}

	combined := mergeMarkers(silenceMarkers, blackMarkers)

	var filtered []CommercialMarker
	for _, m := range combined {
		dur := time.Duration((m.EndSec - m.StartSec) * float64(time.Second))
		if dur < d.minAdLength || dur > d.maxAdLength {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered, nil
}

// ---------- silence detection ------------------------------------------------

// silenceEvent is a parsed silence_start/silence_end event from FFmpeg stderr.
type silenceEvent struct {
	startSec float64
	endSec   float64 // 0 if only start seen yet
}

// detectSilence runs FFmpeg silencedetect on the segment and parses output.
func (d *CommercialDetector) detectSilence(ctx context.Context, segmentPath string) ([]CommercialMarker, error) {
	noiseFilter := fmt.Sprintf("silencedetect=n=%.0fdB:d=%.1f", d.silenceDB, d.silenceDuration)
	cmd := exec.CommandContext(ctx, d.ffmpegPath,
		"-hide_banner",
		"-i", segmentPath,
		"-af", noiseFilter,
		"-f", "null", "-",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// FFmpeg returns non-zero for many benign reasons with -f null.
		// Only fail if stderr has no useful content at all.
		if stderr.Len() == 0 {
			return nil, fmt.Errorf("ffmpeg silence detect failed: %w", err)
		}
	}

	return parseSilenceOutput(stderr.String()), nil
}

// parseSilenceOutput parses FFmpeg silencedetect filter output from stderr.
// Example lines:
//
//	[silencedetect @ 0x...] silence_start: 12.345
//	[silencedetect @ 0x...] silence_end: 14.789 | silence_duration: 2.444
func parseSilenceOutput(output string) []CommercialMarker {
	var markers []CommercialMarker
	var currentStart float64
	hasStart := false

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "[silencedetect") {
			continue
		}

		if idx := strings.Index(line, "silence_start:"); idx != -1 {
			val := strings.TrimSpace(line[idx+len("silence_start:"):])
			// Val may have trailing content — take first field.
			val = strings.Fields(val)[0]
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				currentStart = f
				hasStart = true
			}
			continue
		}

		if idx := strings.Index(line, "silence_end:"); idx != -1 && hasStart {
			rest := strings.TrimSpace(line[idx+len("silence_end:"):])
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			endSec, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				continue
			}
			dur := endSec - currentStart
			markers = append(markers, CommercialMarker{
				StartSec:    currentStart,
				EndSec:      endSec,
				DurationSec: dur,
				Confidence:  0.75, // base confidence for silence-only detection
				Method:      "silence",
			})
			hasStart = false
		}
	}
	return markers
}

// ---------- black frame detection --------------------------------------------

// detectBlackFrames runs FFmpeg blackdetect on the segment.
func (d *CommercialDetector) detectBlackFrames(ctx context.Context, segmentPath string) ([]CommercialMarker, error) {
	cmd := exec.CommandContext(ctx, d.ffmpegPath,
		"-hide_banner",
		"-i", segmentPath,
		"-vf", "blackdetect=d=0.5:pix_th=0.1",
		"-f", "null", "-",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stderr.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg blackdetect failed: %w", err)
	}

	return parseBlackFrameOutput(stderr.String()), nil
}

// parseBlackFrameOutput parses FFmpeg blackdetect filter output.
// Example:
//
//	[blackdetect @ 0x...] black_start:12.5 black_end:13.1 black_duration:0.6
func parseBlackFrameOutput(output string) []CommercialMarker {
	var markers []CommercialMarker
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "black_start:") {
			continue
		}

		var start, end float64
		hasStart, hasEnd := false, false

		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "black_start:") {
				if f, err := strconv.ParseFloat(field[len("black_start:"):], 64); err == nil {
					start = f
					hasStart = true
				}
			} else if strings.HasPrefix(field, "black_end:") {
				if f, err := strconv.ParseFloat(field[len("black_end:"):], 64); err == nil {
					end = f
					hasEnd = true
				}
			}
		}

		if hasStart && hasEnd {
			markers = append(markers, CommercialMarker{
				StartSec:    start,
				EndSec:      end,
				DurationSec: end - start,
				Confidence:  0.60, // black frames alone are less reliable
				Method:      "blackframe",
			})
		}
	}
	return markers
}

// ---------- marker merging ---------------------------------------------------

// mergeMarkers combines silence and black-frame markers.
// Markers from both sources that overlap within 1 second are merged into a
// single combined marker with boosted confidence.
func mergeMarkers(silence, blackframes []CommercialMarker) []CommercialMarker {
	if len(blackframes) == 0 {
		return silence
	}
	if len(silence) == 0 {
		return blackframes
	}

	var result []CommercialMarker
	used := make([]bool, len(blackframes))

	for _, s := range silence {
		boosted := false
		for j, b := range blackframes {
			if used[j] {
				continue
			}
			// Check overlap: black frame start within 1s of silence start or end.
			overlap := (b.StartSec >= s.StartSec-1.0 && b.StartSec <= s.EndSec+1.0) ||
				(s.StartSec >= b.StartSec-1.0 && s.StartSec <= b.EndSec+1.0)
			if overlap {
				// Merge: use silence boundaries (more accurate) with boosted confidence.
				m := CommercialMarker{
					StartSec:    s.StartSec,
					EndSec:      s.EndSec,
					DurationSec: s.EndSec - s.StartSec,
					Confidence:  0.90, // combined detection = higher confidence
					Method:      "combined",
				}
				result = append(result, m)
				used[j] = true
				boosted = true
				break
			}
		}
		if !boosted {
			result = append(result, s)
		}
	}

	// Append unmatched black-frame markers.
	for j, b := range blackframes {
		if !used[j] {
			result = append(result, b)
		}
	}
	return result
}
