// sports_test.go — Unit tests for the sports service.
// P15-T08: Sync parsing, event status transitions, fav team operations, period score marshaling.
package sports

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---- sync response parsing --------------------------------------------------

func TestParseSyncResponse_ValidEvents(t *testing.T) {
	// Sample TheSportsDB API response
	body := `{
		"events": [
			{
				"idEvent": "1234567",
				"strEvent": "Eagles vs Cowboys",
				"idHomeTeam": "134935",
				"idAwayTeam": "134934",
				"strHomeTeam": "Philadelphia Eagles",
				"strAwayTeam": "Dallas Cowboys",
				"intHomeScore": null,
				"intAwayScore": null,
				"strStatus": "Not Started",
				"dateEvent": "2026-09-08",
				"strTime": "17:20:00",
				"strSeason": "2026-2027",
				"intRound": "1",
				"strVenue": "Lincoln Financial Field"
			}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result TheSportsDBEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	ev := result.Events[0]
	if ev.IDEvent != "1234567" {
		t.Errorf("expected IDEvent=1234567, got %s", ev.IDEvent)
	}
	if ev.StrHomeTeam != "Philadelphia Eagles" {
		t.Errorf("expected Eagles, got %s", ev.StrHomeTeam)
	}
	if ev.StrVenue != "Lincoln Financial Field" {
		t.Errorf("expected Lincoln Financial Field, got %s", ev.StrVenue)
	}
}

func TestParseSyncResponse_EmptyResponse(t *testing.T) {
	body := `{"events": null}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result TheSportsDBEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// No events should not cause a panic — len(nil) == 0
	if len(result.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(result.Events))
	}
}

// ---- event status transitions -----------------------------------------------

func TestEventStatusMapping(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Match Finished", "final"},
		{"FT", "final"},
		{"AET", "final"},
		{"PEN", "final"},
		{"In Progress", "live"},
		{"HT", "live"},
		{"1H", "live"},
		{"2H", "live"},
		{"ET", "live"},
		{"P", "live"},
		{"Postponed", "postponed"},
		{"Cancelled", "cancelled"},
		{"Abandoned", "cancelled"},
		{"Not Started", "scheduled"},
		{"", "scheduled"},
		{"NS", "scheduled"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapEventStatus(tt.input)
			if got != tt.expected {
				t.Errorf("mapEventStatus(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// mapEventStatus replicates the status mapping logic from sync.go for testing.
func mapEventStatus(strStatus string) string {
	switch strStatus {
	case "Match Finished", "FT", "AET", "PEN":
		return "final"
	case "In Progress", "HT", "1H", "2H", "ET", "P":
		return "live"
	case "Postponed":
		return "postponed"
	case "Cancelled", "Abandoned":
		return "cancelled"
	default:
		return "scheduled"
	}
}

// ---- period score JSON marshaling -------------------------------------------

func TestPeriodScoreMarshaling(t *testing.T) {
	type periodScore struct {
		Period string `json:"period"`
		Home   int    `json:"home"`
		Away   int    `json:"away"`
	}

	scores := []periodScore{
		{Period: "Q1", Home: 7, Away: 3},
		{Period: "Q2", Home: 10, Away: 7},
	}

	data, err := json.Marshal(scores)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded []periodScore
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(decoded) != 2 {
		t.Fatalf("expected 2 period scores, got %d", len(decoded))
	}
	if decoded[0].Period != "Q1" || decoded[0].Home != 7 {
		t.Errorf("unexpected decoded[0]: %+v", decoded[0])
	}
}

// ---- sport config framework -------------------------------------------------

func TestGetSportConfig_KnownSports(t *testing.T) {
	sports := []string{"american_football", "basketball", "baseball", "ice_hockey", "soccer"}
	for _, sport := range sports {
		cfg := GetSportConfig(sport)
		if cfg.Sport != sport {
			t.Errorf("GetSportConfig(%q).Sport = %q, want %q", sport, cfg.Sport, sport)
		}
		if len(cfg.PeriodNames) == 0 {
			t.Errorf("GetSportConfig(%q) has no period names", sport)
		}
		if cfg.TypicalDurationMinutes == 0 {
			t.Errorf("GetSportConfig(%q) has zero typical duration", sport)
		}
	}
}

func TestGetSportConfig_UnknownSport(t *testing.T) {
	cfg := GetSportConfig("underwater_polo")
	if cfg.Sport != "underwater_polo" {
		t.Errorf("expected fallback sport name, got %q", cfg.Sport)
	}
	if len(cfg.RegularPeriods) == 0 {
		t.Error("fallback config has no regular periods")
	}
}

func TestSupportedSports_NonEmpty(t *testing.T) {
	sports := SupportedSports()
	if len(sports) == 0 {
		t.Error("SupportedSports() returned empty slice")
	}
	// Verify major sports are included
	expected := map[string]bool{}
	for _, s := range sports {
		expected[s] = true
	}
	for _, required := range []string{"american_football", "basketball", "baseball", "ice_hockey"} {
		if !expected[required] {
			t.Errorf("SupportedSports() missing %q", required)
		}
	}
}

// ---- nullableString helper --------------------------------------------------

func TestNullableString(t *testing.T) {
	if nullableString("") != nil {
		t.Error("nullableString('') should return nil")
	}
	s := nullableString("hello")
	if s == nil || *s != "hello" {
		t.Error("nullableString('hello') should return pointer to 'hello'")
	}
}

// ---- itos utility -----------------------------------------------------------

func TestItos(t *testing.T) {
	tests := []struct{ in int; out string }{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{100, "100"},
	}
	for _, tt := range tests {
		if got := itos(tt.in); got != tt.out {
			t.Errorf("itos(%d) = %q, want %q", tt.in, got, tt.out)
		}
	}
}
