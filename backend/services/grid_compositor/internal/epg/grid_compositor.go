// grid_compositor.go — EPG Grid Compositor.
//
// Assembles a multi-channel EPG grid for a given time window. Used by Owl clients
// to render the live TV guide and by the Roost admin dashboard.
//
// The grid is structured as N channel rows × M 30-minute time slots. Each program
// has a SpanSlots field that maps directly to the CSS grid-column-span property,
// enabling native CSS grid rendering without client-side layout math.
//
// Programs shorter than 30 minutes get SpanSlots=1.
// Programs that straddle a time slot boundary are split into "pre-boundary" and
// "post-boundary" segments.
// Gaps in the program schedule (no EPG data) are filled with a "No Information"
// placeholder program.
package epg

import (
	"context"
	"fmt"
	"math"
	"time"
)

// GridRequest specifies the parameters for a grid query.
type GridRequest struct {
	ChannelIDs []string      // ordered list of channel IDs to include in the grid
	StartTime  time.Time     // grid window start (rounded down to nearest 30 min)
	Duration   time.Duration // total window length, typically 2-4 hours
}

// GridResponse is the complete assembled grid, ready for CSS grid rendering.
type GridResponse struct {
	Channels  []ChannelGridRow // one row per channel, in request order
	TimeSlots []TimeSlot       // 30-min slots spanning the window
}

// TimeSlot represents a 30-minute grid column.
type TimeSlot struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Label     string    `json:"label"` // e.g. "8:00 PM"
	SlotIndex int       `json:"slot_index"`
}

// ChannelGridRow is one channel's row in the EPG grid.
type ChannelGridRow struct {
	Channel  ChannelInfo   `json:"channel"`
	Programs []GridProgram `json:"programs"`
}

// ChannelInfo contains display fields for a channel header.
type ChannelInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	LogoURL string `json:"logo_url"`
	Number  int    `json:"number"`
}

// GridProgram is a single program cell in the grid.
type GridProgram struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Category    string    `json:"category,omitempty"`
	IsLive      bool      `json:"is_live"`       // currently airing
	IsNow       bool      `json:"is_now"`        // true if this program is on right now
	ProgressPct float64   `json:"progress_pct"`  // 0-100, progress through current program
	SpanSlots   int       `json:"span_slots"`    // CSS grid-column-span
	StartsAt    int       `json:"starts_at_slot" json:"starts_at_slot"` // slot index where this program starts
	IsNoInfo    bool      `json:"is_no_info"`    // true for gap-fill placeholders
}

// ProgramFetcher retrieves program data for multiple channels over a time window.
// Implementations are expected to batch-fetch from the DB or cache.
type ProgramFetcher interface {
	FetchPrograms(ctx context.Context, channelIDs []string, start, end time.Time) (map[string][]RawProgram, error)
	FetchChannels(ctx context.Context, channelIDs []string) (map[string]ChannelInfo, error)
}

// RawProgram is a single program as returned from the data layer.
type RawProgram struct {
	ID          string
	ChannelID   string
	Title       string
	Description string
	Category    string
	StartTime   time.Time
	EndTime     time.Time
}

// GridCompositor assembles EPG grids.
type GridCompositor struct {
	fetcher ProgramFetcher
}

// NewGridCompositor creates a GridCompositor.
func NewGridCompositor(fetcher ProgramFetcher) *GridCompositor {
	return &GridCompositor{fetcher: fetcher}
}

// Compose assembles the full grid for a request.
func (gc *GridCompositor) Compose(ctx context.Context, req GridRequest) (*GridResponse, error) {
	if len(req.ChannelIDs) == 0 {
		return nil, fmt.Errorf("grid: no channel IDs provided")
	}
	if req.Duration <= 0 {
		req.Duration = 2 * time.Hour
	}

	// Align start time to the previous 30-minute boundary.
	start := alignToSlot(req.StartTime)
	end := start.Add(req.Duration)

	// Build the time slot list.
	slots := buildTimeSlots(start, end)

	// Fetch programs from the data layer.
	programs, err := gc.fetcher.FetchPrograms(ctx, req.ChannelIDs, start, end)
	if err != nil {
		return nil, fmt.Errorf("grid FetchPrograms: %w", err)
	}

	// Fetch channel metadata.
	channels, err := gc.fetcher.FetchChannels(ctx, req.ChannelIDs)
	if err != nil {
		return nil, fmt.Errorf("grid FetchChannels: %w", err)
	}

	now := time.Now()

	// Assemble grid rows in channel order from the request.
	rows := make([]ChannelGridRow, 0, len(req.ChannelIDs))
	for _, chID := range req.ChannelIDs {
		info, ok := channels[chID]
		if !ok {
			info = ChannelInfo{ID: chID, Name: "Unknown"}
		}

		rawProgs := programs[chID]
		gridProgs := buildChannelRow(rawProgs, slots, now)

		rows = append(rows, ChannelGridRow{
			Channel:  info,
			Programs: gridProgs,
		})
	}

	return &GridResponse{
		Channels:  rows,
		TimeSlots: slots,
	}, nil
}

// ---------- internal helpers -------------------------------------------------

// alignToSlot rounds t down to the previous 30-minute boundary in UTC.
func alignToSlot(t time.Time) time.Time {
	t = t.UTC()
	mins := t.Minute()
	if mins < 30 {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 30, 0, 0, time.UTC)
}

// buildTimeSlots creates 30-minute TimeSlot entries from start to end.
func buildTimeSlots(start, end time.Time) []TimeSlot {
	var slots []TimeSlot
	idx := 0
	for t := start; t.Before(end); t = t.Add(30 * time.Minute) {
		label := t.Format("3:04 PM")
		slots = append(slots, TimeSlot{
			Start:     t,
			End:       t.Add(30 * time.Minute),
			Label:     label,
			SlotIndex: idx,
		})
		idx++
	}
	return slots
}

// buildChannelRow converts raw programs into GridPrograms with SpanSlots and gaps filled.
func buildChannelRow(progs []RawProgram, slots []TimeSlot, now time.Time) []GridProgram {
	if len(slots) == 0 {
		return nil
	}
	gridStart := slots[0].Start
	gridEnd := slots[len(slots)-1].End
	totalSlots := len(slots)

	// Sort programs by start time.
	sortPrograms(progs)

	var result []GridProgram
	cursor := gridStart // tracks next unfilled slot boundary

	for _, p := range progs {
		// Clamp program to grid window.
		pStart := p.StartTime
		pEnd := p.EndTime
		if pEnd.After(gridEnd) {
			pEnd = gridEnd
		}
		if pStart.Before(gridStart) {
			pStart = gridStart
		}
		if !pStart.Before(gridEnd) || !pEnd.After(gridStart) {
			continue // outside window
		}

		// Fill gap before this program.
		if cursor.Before(pStart) {
			gap := buildNoInfoProgram(cursor, pStart, slots, gridStart, now)
			result = append(result, gap...)
		}

		// Build the program cell.
		gp := buildGridProgram(p.ID, p.Title, p.Description, p.Category, pStart, pEnd, slots, gridStart, now, false)
		result = append(result, gp)
		cursor = pEnd
	}

	// Fill trailing gap.
	if cursor.Before(gridEnd) {
		gap := buildNoInfoProgram(cursor, gridEnd, slots, gridStart, now)
		result = append(result, gap...)
	}

	// Ensure SpanSlots never exceeds grid width.
	for i := range result {
		if result[i].SpanSlots > totalSlots {
			result[i].SpanSlots = totalSlots
		}
		if result[i].SpanSlots < 1 {
			result[i].SpanSlots = 1
		}
	}

	return result
}

// buildGridProgram creates one GridProgram entry.
func buildGridProgram(id, title, desc, category string, start, end time.Time, slots []TimeSlot, gridStart time.Time, now time.Time, isNoInfo bool) GridProgram {
	spanSlots := computeSpanSlots(start, end)
	startsAt := computeSlotIndex(start, gridStart)

	isLive := !now.Before(start) && now.Before(end)
	var progress float64
	if isLive {
		elapsed := now.Sub(start).Seconds()
		total := end.Sub(start).Seconds()
		if total > 0 {
			progress = math.Min(100, math.Max(0, elapsed/total*100))
		}
	}

	return GridProgram{
		ID:          id,
		Title:       title,
		Description: desc,
		Category:    category,
		StartTime:   start,
		EndTime:     end,
		IsLive:      isLive,
		IsNow:       isLive,
		ProgressPct: progress,
		SpanSlots:   spanSlots,
		StartsAt:    startsAt,
		IsNoInfo:    isNoInfo,
	}
}

// buildNoInfoProgram creates gap-fill "No Information" programs for a time range.
// Each "No Information" program spans at most one 30-minute slot to avoid
// giant placeholders.
func buildNoInfoProgram(start, end time.Time, slots []TimeSlot, gridStart time.Time, now time.Time) []GridProgram {
	var result []GridProgram
	cursor := start
	for cursor.Before(end) {
		// Each no-info segment fills up to the next 30-min boundary.
		nextBoundary := alignToSlot(cursor).Add(30 * time.Minute)
		segEnd := nextBoundary
		if segEnd.After(end) {
			segEnd = end
		}
		gp := buildGridProgram("", "No Information", "", "", cursor, segEnd, slots, gridStart, now, true)
		result = append(result, gp)
		cursor = segEnd
	}
	return result
}

// computeSpanSlots returns how many 30-minute slots a program occupies.
// Minimum 1. Programs shorter than 30 minutes still occupy 1 slot.
func computeSpanSlots(start, end time.Time) int {
	duration := end.Sub(start)
	slots := int(math.Ceil(duration.Minutes() / 30.0))
	if slots < 1 {
		return 1
	}
	return slots
}

// computeSlotIndex returns the 0-based slot index for a program start time.
func computeSlotIndex(start, gridStart time.Time) int {
	offset := start.Sub(gridStart)
	if offset < 0 {
		return 0
	}
	return int(offset.Minutes() / 30.0)
}

// sortPrograms sorts programs by StartTime ascending.
// Uses a simple insertion sort — program counts per channel are small (<100).
func sortPrograms(progs []RawProgram) {
	for i := 1; i < len(progs); i++ {
		for j := i; j > 0 && progs[j].StartTime.Before(progs[j-1].StartTime); j-- {
			progs[j], progs[j-1] = progs[j-1], progs[j]
		}
	}
}
