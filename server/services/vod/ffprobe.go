// ffprobe.go — ffprobe wrapper for VOD content analysis.
//
// Probes a media file to determine its codec, resolution, duration, and bitrate.
// Used before transcoding to decide whether re-encoding is needed.
//
// Requires ffprobe to be installed and on PATH (or set FFPROBE_PATH env var).
// ffprobe is part of the FFmpeg distribution: https://ffmpeg.org
package vod

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// ProbeResult contains media stream metadata from ffprobe.
type ProbeResult struct {
	VideoCodec string  // e.g. "h264", "hevc", "av1"
	AudioCodec string  // e.g. "aac", "mp3", "opus"
	Width      int
	Height     int
	Duration   float64 // seconds
	Bitrate    int64   // bits/sec
}

// NeedsTranscode returns true if the content should be re-encoded before HLS packaging.
// Currently, only h264 + aac passes through without re-encoding.
func (p *ProbeResult) NeedsTranscode() bool {
	if p.VideoCodec == "" {
		return false // audio-only content (music/podcast) — use audio transcode path
	}
	return p.VideoCodec != "h264" || p.AudioCodec != "aac"
}

// ffprobeOutput is the top-level JSON structure returned by ffprobe -show_streams.
type ffprobeOutput struct {
	Streams []struct {
		CodecName  string `json:"codec_name"`
		CodecType  string `json:"codec_type"` // "video" or "audio"
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		BitRate    string `json:"bit_rate"`
		Duration   string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

// Probe runs ffprobe on the given file path and returns stream metadata.
// filePath must be an absolute path to a local file.
func Probe(filePath string) (*ProbeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return ProbeCtx(ctx, filePath)
}

// ProbeCtx runs ffprobe with the given context (allows timeout/cancellation).
func ProbeCtx(ctx context.Context, filePath string) (*ProbeResult, error) {
	ffprobePath := getIngestEnv("FFPROBE_PATH", "ffprobe")

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		filePath,
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe exec: %w", err)
	}

	var data ffprobeOutput
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("ffprobe parse: %w", err)
	}

	result := &ProbeResult{}

	// Extract stream-level fields.
	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			result.VideoCodec = s.CodecName
			result.Width = s.Width
			result.Height = s.Height
			if dur, err := strconv.ParseFloat(s.Duration, 64); err == nil && dur > 0 {
				result.Duration = dur
			}
		case "audio":
			result.AudioCodec = s.CodecName
		}
	}

	// Use format-level duration if stream-level was not available.
	if result.Duration == 0 {
		if dur, err := strconv.ParseFloat(data.Format.Duration, 64); err == nil {
			result.Duration = dur
		}
	}

	// Bitrate from format.
	if br, err := strconv.ParseInt(data.Format.BitRate, 10, 64); err == nil {
		result.Bitrate = br
	}

	return result, nil
}

// TranscodeToHLS runs ffmpeg to produce an HLS stream from inputPath.
// Output: index.m3u8 + ts segment files written to the directory containing outputPlaylist.
// Uses libx264 + aac encoding at CRF 23 (reasonable quality/size balance for VOD).
//
// The output is adaptive in concept — a single quality level is produced here.
// For multi-bitrate ABR, this function would be called multiple times or
// an HLS packaging library (e.g., packager) would be used.
func TranscodeToHLS(ctx context.Context, inputPath, outputPlaylist string) error {
	ffmpegPath := getIngestEnv("FFMPEG_PATH", "ffmpeg")

	// Build the HLS output segment template from the playlist path.
	// e.g. /tmp/roost-vod/job123/hls/index.m3u8 → /tmp/roost-vod/job123/hls/seg%05d.ts
	dir := inputPath
	_ = dir // not needed — outputPlaylist dir is used
	segPattern := outputPlaylist[:len(outputPlaylist)-len("index.m3u8")] + "seg%05d.ts"

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-i", inputPath,
		"-codec:v", "libx264",
		"-crf", "23",
		"-preset", "fast",
		"-codec:a", "aac",
		"-b:a", "128k",
		"-hls_time", "6",
		"-hls_list_size", "0",
		"-hls_segment_filename", segPattern,
		"-f", "hls",
		"-y",
		outputPlaylist,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Include last 500 bytes of ffmpeg output for diagnosis.
		tail := string(output)
		if len(tail) > 500 {
			tail = "..." + tail[len(tail)-500:]
		}
		return fmt.Errorf("ffmpeg transcode: %w\noutput: %s", err, tail)
	}
	return nil
}
