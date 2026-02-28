// grid_test.go — Tests for the EPG grid compositor.
package epg

import (
	"context"
	"testing"
	"time"
)

// ---------- test fetcher implementation --------------------------------------

type stubFetcher struct {
	programs map[string][]RawProgram
	channels map[string]ChannelInfo
}

func (s *stubFetcher) FetchPrograms(_ context.Context, channelIDs []string, start, end time.Time) (map[string][]RawProgram, error) {
	result := make(map[string][]RawProgram)
	for _, id := range channelIDs {
		if progs, ok := s.programs[id]; ok {
			result[id] = progs
		}
	}
	return result, nil
}

func (s *stubFetcher) FetchChannels(_ context.Context, channelIDs []string) (map[string]ChannelInfo, error) {
	result := make(map[string]ChannelInfo)
	for _, id := range channelIDs {
		if ch, ok := s.channels[id]; ok {
			result[id] = ch
		}
	}
	return result, nil
}

// ---------- helpers ----------------------------------------------------------

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func newCompositor(programs map[string][]RawProgram) *GridCompositor {
	fetcher := &stubFetcher{
		programs: programs,
		channels: map[string]ChannelInfo{
			"ch1": {ID: "ch1", Name: "Channel 1"},
			"ch2": {ID: "ch2", Name: "Channel 2"},
		},
	}
	return NewGridCompositor(fetcher)
}

// ---------- tests ------------------------------------------------------------

// TestAlignToSlot verifies 30-minute boundary alignment.
func TestAlignToSlot(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2026-09-14T20:00:00Z", "2026-09-14T20:00:00Z"},
		{"2026-09-14T20:14:59Z", "2026-09-14T20:00:00Z"},
		{"2026-09-14T20:29:59Z", "2026-09-14T20:00:00Z"},
		{"2026-09-14T20:30:00Z", "2026-09-14T20:30:00Z"},
		{"2026-09-14T20:45:00Z", "2026-09-14T20:30:00Z"},
		{"2026-09-14T21:00:00Z", "2026-09-14T21:00:00Z"},
	}
	for _, tc := range tests {
		in := mustTime(tc.input)
		got := alignToSlot(in)
		want := mustTime(tc.want)
		if !got.Equal(want) {
			t.Errorf("alignToSlot(%s): got %s, want %s", tc.input, got, want)
		}
	}
}

// TestBuildTimeSlots verifies slot generation for a 2-hour window.
func TestBuildTimeSlots(t *testing.T) {
	start := mustTime("2026-09-14T20:00:00Z")
	end := start.Add(2 * time.Hour)
	slots := buildTimeSlots(start, end)

	if len(slots) != 4 {
		t.Fatalf("expected 4 slots for 2h window, got %d", len(slots))
	}
	if !slots[0].Start.Equal(start) {
		t.Errorf("first slot start: got %s, want %s", slots[0].Start, start)
	}
	if !slots[3].End.Equal(end) {
		t.Errorf("last slot end: got %s, want %s", slots[3].End, end)
	}
	for i, s := range slots {
		if s.SlotIndex != i {
			t.Errorf("slot[%d] index: got %d, want %d", i, s.SlotIndex, i)
		}
		if s.End.Sub(s.Start) != 30*time.Minute {
			t.Errorf("slot[%d] duration: got %v, want 30m", i, s.End.Sub(s.Start))
		}
	}
}

// TestComputeSpanSlots verifies slot-span calculation.
func TestComputeSpanSlots(t *testing.T) {
	base := mustTime("2026-09-14T20:00:00Z")
	tests := []struct {
		durationMin int
		wantSpan    int
	}{
		{15, 1},  // < 30 min → span 1
		{30, 1},  // exactly 30 min → span 1
		{45, 2},  // > 30 min → span 2
		{60, 2},  // exactly 1h → span 2
		{90, 3},  // 1.5h → span 3
		{120, 4}, // 2h → span 4
	}
	for _, tc := range tests {
		end := base.Add(time.Duration(tc.durationMin) * time.Minute)
		got := computeSpanSlots(base, end)
		if got != tc.wantSpan {
			t.Errorf("computeSpanSlots(%dmin): got %d, want %d", tc.durationMin, got, tc.wantSpan)
		}
	}
}

// TestProgressPct verifies ProgressPct for a currently-live program.
func TestProgressPct(t *testing.T) {
	// Program: 20:00 - 20:30 (30 min). "Now" = 20:15 → 50%.
	start := mustTime("2026-09-14T20:00:00Z")
	end := start.Add(30 * time.Minute)
	now := start.Add(15 * time.Minute)

	gridStart := start
	slots := buildTimeSlots(start, end.Add(30*time.Minute))
	gp := buildGridProgram("id1", "Test", "", "", start, end, slots, gridStart, now, false)

	if !gp.IsLive {
		t.Error("expected IsLive=true for program at 15-min mark")
	}
	if gp.ProgressPct < 49.9 || gp.ProgressPct > 50.1 {
		t.Errorf("ProgressPct: got %.2f, want ~50.0", gp.ProgressPct)
	}
}

// TestNoInfoGap verifies that gaps in the schedule produce "No Information" placeholders.
func TestNoInfoGap(t *testing.T) {
	// Channel with one program 20:30-21:00 in a 20:00-22:00 window.
	// Expected: gap 20:00-20:30, program 20:30-21:00, gap 21:00-22:00.
	start := mustTime("2026-09-14T20:00:00Z")
	compositor := newCompositor(map[string][]RawProgram{
		"ch1": {
			{
				ID:        "p1",
				ChannelID: "ch1",
				Title:     "News at 9",
				StartTime: mustTime("2026-09-14T20:30:00Z"),
				EndTime:   mustTime("2026-09-14T21:00:00Z"),
			},
		},
	})

	resp, err := compositor.Compose(context.Background(), GridRequest{
		ChannelIDs: []string{"ch1"},
		StartTime:  start,
		Duration:   2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 channel row, got %d", len(resp.Channels))
	}

	progs := resp.Channels[0].Programs
	// Should have: no-info (20:00-20:30), program (20:30-21:00), no-info (21:00-22:00)
	if len(progs) < 3 {
		t.Fatalf("expected ≥3 programs (including gaps), got %d", len(progs))
	}

	// First program must be a no-info gap.
	if !progs[0].IsNoInfo {
		t.Errorf("programs[0]: expected IsNoInfo=true (gap 20:00-20:30)")
	}
	// Second must be the actual program.
	if progs[1].IsNoInfo {
		t.Errorf("programs[1]: expected IsNoInfo=false (News at 9)")
	}
	if progs[1].Title != "News at 9" {
		t.Errorf("programs[1].Title: got %q, want %q", progs[1].Title, "News at 9")
	}
	// Last must be a trailing no-info gap.
	last := progs[len(progs)-1]
	if !last.IsNoInfo {
		t.Errorf("last program: expected IsNoInfo=true (trailing gap)")
	}
}

// TestProgramShorterThan30Min verifies SpanSlots=1 for short programs.
func TestProgramShorterThan30Min(t *testing.T) {
	start := mustTime("2026-09-14T20:00:00Z")
	compositor := newCompositor(map[string][]RawProgram{
		"ch1": {
			{
				ID:        "p1",
				ChannelID: "ch1",
				Title:     "Short Show",
				StartTime: mustTime("2026-09-14T20:00:00Z"),
				EndTime:   mustTime("2026-09-14T20:10:00Z"), // only 10 minutes
			},
		},
	})

	resp, err := compositor.Compose(context.Background(), GridRequest{
		ChannelIDs: []string{"ch1"},
		StartTime:  start,
		Duration:   time.Hour,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	progs := resp.Channels[0].Programs
	var shortProg *GridProgram
	for i := range progs {
		if progs[i].Title == "Short Show" {
			shortProg = &progs[i]
			break
		}
	}
	if shortProg == nil {
		t.Fatal("Short Show not found in grid programs")
	}
	if shortProg.SpanSlots != 1 {
		t.Errorf("SpanSlots for 10-min program: got %d, want 1", shortProg.SpanSlots)
	}
}

// TestMidnightBoundary verifies a program spanning midnight is handled.
func TestMidnightBoundary(t *testing.T) {
	// Program: 23:45 - 00:15 (spans midnight).
	start := mustTime("2026-09-14T23:30:00Z")
	compositor := newCompositor(map[string][]RawProgram{
		"ch1": {
			{
				ID:        "p1",
				ChannelID: "ch1",
				Title:     "Late Night",
				StartTime: mustTime("2026-09-14T23:45:00Z"),
				EndTime:   mustTime("2026-09-15T00:15:00Z"),
			},
		},
	})

	resp, err := compositor.Compose(context.Background(), GridRequest{
		ChannelIDs: []string{"ch1"},
		StartTime:  start,
		Duration:   90 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// Grid window: 23:30-01:00. Program 23:45-00:15 spans midnight.
	// SpanSlots: ceil(30min/30) = 1 slot. Starts at slot 0 or 1.
	progs := resp.Channels[0].Programs
	var found bool
	for _, p := range progs {
		if p.Title == "Late Night" {
			found = true
			if p.SpanSlots < 1 {
				t.Errorf("Late Night SpanSlots: got %d, want ≥1", p.SpanSlots)
			}
		}
	}
	if !found {
		t.Error("Late Night program not found in grid output")
	}
}
