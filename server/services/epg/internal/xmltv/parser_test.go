// parser_test.go — Unit tests for the XMLTV parser.
// Tests use the sample.xmltv fixture in testdata/.
// No database or external services required.
package xmltv_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unyeco/roost/services/epg/internal/xmltv"
)

func loadSampleFixture(t *testing.T) *xmltv.Result {
	t.Helper()
	f, err := os.Open("testdata/sample.xmltv")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	result, err := xmltv.ParseReader(f)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return result
}

// TestParseChannelCount verifies all 3 channels are parsed.
func TestParseChannelCount(t *testing.T) {
	result := loadSampleFixture(t)
	if len(result.Channels) != 3 {
		t.Errorf("expected 3 channels, got %d", len(result.Channels))
	}
}

// TestParseChannelFields verifies channel fields are populated.
func TestParseChannelFields(t *testing.T) {
	result := loadSampleFixture(t)
	found := false
	for _, ch := range result.Channels {
		if ch.ID == "roost-news-1" {
			found = true
			if ch.DisplayName != "Roost News One" {
				t.Errorf("expected display name 'Roost News One', got %q", ch.DisplayName)
			}
			if ch.IconSrc == "" {
				t.Error("expected icon src to be populated")
			}
		}
	}
	if !found {
		t.Error("channel roost-news-1 not found in parsed output")
	}
}

// TestParseProgramCount verifies all 30 programs are parsed (10 per channel).
func TestParseProgramCount(t *testing.T) {
	result := loadSampleFixture(t)
	if len(result.Programmes) != 30 {
		t.Errorf("expected 30 programmes, got %d", len(result.Programmes))
	}
}

// TestParseProgramFields verifies programme field parsing.
func TestParseProgramFields(t *testing.T) {
	result := loadSampleFixture(t)
	found := false
	for _, p := range result.Programmes {
		if p.ChannelID == "roost-news-1" && p.Title == "Morning Briefing" {
			found = true
			if p.Description == "" {
				t.Error("expected description to be populated")
			}
			if p.Category != "News" {
				t.Errorf("expected category 'News', got %q", p.Category)
			}
			if p.Rating != "TV-G" {
				t.Errorf("expected rating 'TV-G', got %q", p.Rating)
			}
			if p.Start.IsZero() {
				t.Error("expected start time to be parsed")
			}
			if p.Stop.IsZero() {
				t.Error("expected stop time to be parsed")
			}
			// Start should be 2026-02-24 00:00:00 UTC
			want := time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC)
			if !p.Start.Equal(want) {
				t.Errorf("start time: want %v, got %v", want, p.Start)
			}
		}
	}
	if !found {
		t.Error("Morning Briefing program not found")
	}
}

// TestXMLTVDateParsing verifies the non-standard XMLTV date format is handled.
func TestXMLTVDateParsing(t *testing.T) {
	result := loadSampleFixture(t)
	// All programmes should have valid (non-zero) times
	for _, p := range result.Programmes {
		if p.Start.IsZero() {
			t.Errorf("programme %q: start time is zero", p.Title)
		}
		if p.Stop.IsZero() {
			t.Errorf("programme %q: stop time is zero", p.Title)
		}
		if !p.Stop.After(p.Start) {
			t.Errorf("programme %q: stop (%v) must be after start (%v)", p.Title, p.Stop, p.Start)
		}
	}
}

// TestProgramsPerChannel verifies each channel has exactly 10 programs in fixture.
func TestProgramsPerChannel(t *testing.T) {
	result := loadSampleFixture(t)
	counts := map[string]int{}
	for _, p := range result.Programmes {
		counts[p.ChannelID]++
	}
	expected := map[string]int{
		"roost-news-1":   10,
		"roost-sports-1": 10,
		"roost-movies-1": 10,
	}
	for chID, want := range expected {
		if counts[chID] != want {
			t.Errorf("channel %s: expected %d programs, got %d", chID, want, counts[chID])
		}
	}
}

// TestEmptyDocument verifies parsing an empty TV document returns no error.
func TestEmptyDocument(t *testing.T) {
	const empty = `<?xml version="1.0"?><tv></tv>`
	result, err := xmltv.ParseReader(strings.NewReader(empty))
	if err != nil {
		t.Errorf("empty document: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil for empty document")
	}
	if len(result.Channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(result.Channels))
	}
	if len(result.Programmes) != 0 {
		t.Errorf("expected 0 programmes, got %d", len(result.Programmes))
	}
}

// TestMalformedProgramSkipped verifies malformed programmes are skipped gracefully.
func TestMalformedProgramSkipped(t *testing.T) {
	const doc = `<?xml version="1.0"?><tv>
		<channel id="ch1"><display-name>Test</display-name></channel>
		<programme start="INVALID" stop="ALSOINVALID" channel="ch1">
			<title>Bad Program</title>
		</programme>
		<programme start="20260224000000 +0000" stop="20260224010000 +0000" channel="ch1">
			<title>Good Program</title>
		</programme>
	</tv>`
	result, err := xmltv.ParseReader(strings.NewReader(doc))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Only the good program should appear
	if len(result.Programmes) != 1 {
		t.Errorf("expected 1 valid programme (bad one skipped), got %d", len(result.Programmes))
	}
	if result.Programmes[0].Title != "Good Program" {
		t.Errorf("expected 'Good Program', got %q", result.Programmes[0].Title)
	}
}
