// transcriber.go — Whisper-based podcast episode transcription.
//
// Calls the Whisper CLI (OpenAI Whisper or faster-whisper) to transcribe
// a podcast episode audio file, producing a VTT subtitle file.
//
// Whisper install: pip install openai-whisper
// faster-whisper: pip install faster-whisper (recommended for production)
//
// Env vars:
//
//	WHISPER_PATH        — path to whisper binary (default: whisper)
//	WHISPER_MODEL       — model size: tiny | base | small | medium | large (default: base)
//	WHISPER_DEVICE      — device: cpu | cuda (default: cpu)
//	WHISPER_OUTPUT_DIR  — output directory for VTT files (default: /tmp/whisper-output)
//
// Audio is downloaded to a temp file before transcription. After transcription,
// the VTT is read and returned — temp files are cleaned up automatically.
package podcasts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TranscribeResult contains the output of a Whisper transcription.
type TranscribeResult struct {
	VTT      string // full VTT subtitle content
	Language string // detected language code ("en", "ar", etc.)
	Duration int    // estimated audio duration in seconds
}

// Transcribe downloads the audio at audioURL and transcribes it with Whisper.
// Returns VTT subtitle content on success.
// This is a long-running operation — use a context with an appropriate deadline.
func Transcribe(ctx context.Context, audioURL string) (*TranscribeResult, error) {
	model := getEnv("WHISPER_MODEL", "base")
	return TranscribeWithModel(ctx, audioURL, model)
}

// TranscribeWithModel downloads audio and transcribes with the specified Whisper model size.
// modelSize: tiny | base | small | medium | large
func TranscribeWithModel(ctx context.Context, audioURL, modelSize string) (*TranscribeResult, error) {
	// Create a temp directory for this transcription job.
	tmpDir, err := os.MkdirTemp("", "roost-whisper-*")
	if err != nil {
		return nil, fmt.Errorf("whisper: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Download the audio file.
	audioPath := filepath.Join(tmpDir, "episode.mp3")
	if err := downloadAudio(ctx, audioURL, audioPath); err != nil {
		return nil, fmt.Errorf("whisper: download audio: %w", err)
	}

	// Run Whisper.
	vttPath, lang, err := runWhisper(ctx, audioPath, tmpDir, modelSize)
	if err != nil {
		return nil, fmt.Errorf("whisper: transcription failed: %w", err)
	}

	// Read the VTT output.
	vttData, err := os.ReadFile(vttPath)
	if err != nil {
		return nil, fmt.Errorf("whisper: read vtt output: %w", err)
	}

	return &TranscribeResult{
		VTT:      string(vttData),
		Language: lang,
	}, nil
}

// runWhisper executes the Whisper CLI on audioPath and returns the path to the
// generated VTT file and the detected language.
func runWhisper(ctx context.Context, audioPath, outputDir, modelSize string) (vttPath, lang string, err error) {
	whisperPath := getEnv("WHISPER_PATH", "whisper")
	device := getEnv("WHISPER_DEVICE", "cpu")

	// Validate model size.
	validModels := map[string]bool{
		"tiny": true, "base": true, "small": true, "medium": true, "large": true,
	}
	if !validModels[modelSize] {
		modelSize = "base"
	}

	cmd := exec.CommandContext(ctx, whisperPath,
		audioPath,
		"--model", modelSize,
		"--output_format", "vtt",
		"--output_dir", outputDir,
		"--device", device,
		"--verbose", "False",
	)

	out, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		tail := string(out)
		if len(tail) > 500 {
			tail = "..." + tail[len(tail)-500:]
		}
		return "", "", fmt.Errorf("whisper exec: %w\n%s", cmdErr, tail)
	}

	// Whisper writes {input_stem}.vtt in the output directory.
	stem := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	vttPath = filepath.Join(outputDir, stem+".vtt")
	if _, statErr := os.Stat(vttPath); statErr != nil {
		return "", "", fmt.Errorf("whisper: expected output %s not found", vttPath)
	}

	// Extract language from stdout if present (look for "Detected language:")
	detectedLang := "en"
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Detected language:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				detectedLang = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	return vttPath, detectedLang, nil
}

// downloadAudio downloads an audio file from url to destPath.
func downloadAudio(ctx context.Context, url, destPath string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// getEnv returns the env var with a fallback.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
