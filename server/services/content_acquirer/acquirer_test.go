// acquirer_test.go — Unit tests for the content acquisition pipeline.
// Tests cover job parsing, strategy selection, advisory lock ID generation,
// and sha256 checksum computation. Network and DB calls are not made.
package content_acquirer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ── Advisory lock ID ──────────────────────────────────────────────────────────

func TestAdvisoryLockID_Deterministic(t *testing.T) {
	id1 := advisoryLockID("imdb:tt0111161")
	id2 := advisoryLockID("imdb:tt0111161")
	if id1 != id2 {
		t.Error("advisory lock ID must be deterministic for the same input")
	}
}

func TestAdvisoryLockID_DifferentIDs(t *testing.T) {
	id1 := advisoryLockID("imdb:tt0111161")
	id2 := advisoryLockID("imdb:tt0000001")
	if id1 == id2 {
		t.Error("different canonical IDs must produce different advisory lock IDs")
	}
}

// ── Strategy selection ────────────────────────────────────────────────────────

func TestStrategyFor_Video(t *testing.T) {
	for _, ct := range []string{"movie", "episode", "show"} {
		if got := strategyFor(ct); got != StrategyB {
			t.Errorf("strategyFor(%q) = %v, want StrategyB", ct, got)
		}
	}
}

func TestStrategyFor_Audio(t *testing.T) {
	if got := strategyFor("music"); got != StrategyC {
		t.Errorf("strategyFor(music) = %v, want StrategyC", got)
	}
}

func TestStrategyFor_Direct(t *testing.T) {
	for _, ct := range []string{"game", "podcast"} {
		if got := strategyFor(ct); got != StrategyA {
			t.Errorf("strategyFor(%q) = %v, want StrategyA", ct, got)
		}
	}
}

// ── SHA256 checksum ───────────────────────────────────────────────────────────

func TestSHA256File_KnownContent(t *testing.T) {
	// Write a temp file with known content.
	tmp, err := os.CreateTemp("", "sha256_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	content := []byte("hello roost")
	if _, err = tmp.Write(content); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	got, err := sha256File(tmp.Name())
	if err != nil {
		t.Fatalf("sha256File error: %v", err)
	}
	if len(got) != 64 {
		t.Errorf("expected 64-char hex SHA256, got %d chars: %s", len(got), got)
	}
}

func TestSHA256File_DifferentFiles_DifferentHash(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.bin")
	f2 := filepath.Join(dir, "b.bin")
	os.WriteFile(f1, []byte("content A"), 0644)
	os.WriteFile(f2, []byte("content B"), 0644)

	h1, _ := sha256File(f1)
	h2, _ := sha256File(f2)
	if h1 == h2 {
		t.Error("different file contents must produce different SHA256 hashes")
	}
}

// ── Job JSON parsing ──────────────────────────────────────────────────────────

func TestParseJobJSON_Valid(t *testing.T) {
	raw := `{"job_id":"j1","canonical_id":"imdb:tt0111161","content_type":"movie","source_url":"https://cdn.example.com/file.mkv","target_quality":"1080p","priority":5}`
	var job AcquisitionJob
	if err := parseJobJSON(raw, &job); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if job.CanonicalID != "imdb:tt0111161" {
		t.Errorf("canonical_id: got %q, want %q", job.CanonicalID, "imdb:tt0111161")
	}
	if job.ContentType != "movie" {
		t.Errorf("content_type: got %q, want %q", job.ContentType, "movie")
	}
	if job.SourceURL != "https://cdn.example.com/file.mkv" {
		t.Errorf("source_url: got %q", job.SourceURL)
	}
}

func TestParseJobJSON_MissingCanonicalID(t *testing.T) {
	raw := `{"job_id":"j1","content_type":"movie"}`
	var job AcquisitionJob
	if err := parseJobJSON(raw, &job); err == nil {
		t.Error("expected error for missing canonical_id")
	}
}

func TestParseJobJSON_Empty(t *testing.T) {
	var job AcquisitionJob
	if err := parseJobJSON("{}", &job); err == nil {
		t.Error("expected error for empty JSON object")
	}
}

// ── AcquireContent with nil DB ────────────────────────────────────────────────

func TestAcquireContent_NilDB(t *testing.T) {
	job := AcquisitionJob{
		CanonicalID: "imdb:tt0111161",
		ContentType: "movie",
	}
	err := AcquireContent(context.Background(), nil, job, newTestLogger())
	if err == nil {
		t.Error("expected error when DB is nil")
	}
}
