// compositor_test.go — unit tests for the grid compositor package.
package compositor

import (
	"context"
	"strings"
	"testing"
)

// TestMaxInputsMap verifies all layouts have correct input counts.
func TestMaxInputsMap(t *testing.T) {
	tests := []struct {
		layout Layout
		count  int
	}{
		{Layout1x1, 1},
		{Layout2x1, 2},
		{Layout1x2, 2},
		{Layout2x2, 4},
		{Layout3x3, 9},
		{LayoutPiP, 2},
	}
	for _, tt := range tests {
		got, ok := maxInputs[tt.layout]
		if !ok {
			t.Errorf("layout %s missing from maxInputs", tt.layout)
			continue
		}
		if got != tt.count {
			t.Errorf("layout %s: expected %d inputs, got %d", tt.layout, tt.count, got)
		}
	}
}

// TestBuildFFmpegArgsPiP verifies PiP layout generates correct filter_complex.
func TestBuildFFmpegArgsPiP(t *testing.T) {
	sess := &Session{
		ID:           "test-session-id",
		Layout:       LayoutPiP,
		ChannelSlugs: []string{"sports", "news"},
		OutputDir:    "/tmp/compositor/test-session-id",
	}
	args := buildFFmpegArgs(sess, "/var/roost/segments")

	// Must have -filter_complex for PiP
	found := false
	for _, a := range args {
		if a == "-filter_complex" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected -filter_complex in PiP args")
	}

	// Must include both input m3u8 paths
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "sports/stream.m3u8") {
		t.Errorf("expected sports/stream.m3u8 in args: %s", joined)
	}
	if !strings.Contains(joined, "news/stream.m3u8") {
		t.Errorf("expected news/stream.m3u8 in args: %s", joined)
	}
}

// TestBuildFFmpegArgs2x2 verifies 2x2 layout uses xstack filter.
func TestBuildFFmpegArgs2x2(t *testing.T) {
	sess := &Session{
		ID:           "test-2x2",
		Layout:       Layout2x2,
		ChannelSlugs: []string{"ch1", "ch2", "ch3", "ch4"},
		OutputDir:    "/tmp/compositor/test-2x2",
	}
	args := buildFFmpegArgs(sess, "/var/roost/segments")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "xstack") {
		t.Errorf("expected xstack in 2x2 filter_complex: %s", joined)
	}
	// Check all 4 inputs are referenced
	for _, ch := range sess.ChannelSlugs {
		if !strings.Contains(joined, ch+"/stream.m3u8") {
			t.Errorf("expected %s in args", ch+"/stream.m3u8")
		}
	}
}

// TestBuildFFmpegArgs3x3 verifies 3x3 layout includes all 9 scale operations.
func TestBuildFFmpegArgs3x3(t *testing.T) {
	channels := []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}
	sess := &Session{
		ID:           "test-3x3",
		Layout:       Layout3x3,
		ChannelSlugs: channels,
		OutputDir:    "/tmp/compositor/test-3x3",
	}
	args := buildFFmpegArgs(sess, "/var/roost/segments")
	joined := strings.Join(args, " ")

	// Should have 9 scale operations
	count := strings.Count(joined, "scale=426:240")
	if count != 9 {
		t.Errorf("expected 9 scale operations in 3x3, got %d", count)
	}
}

// TestCreateSessionWrongChannelCount verifies validation rejects wrong channel count.
func TestCreateSessionWrongChannelCount(t *testing.T) {
	mgr := New("/tmp/seg", "/tmp/comp")
	ctx := context.Background()
	_, err := mgr.CreateSession(ctx, Layout2x2, []string{"ch1", "ch2"}) // needs 4
	if err == nil {
		t.Error("expected error for wrong channel count, got nil")
	}
	if !strings.Contains(err.Error(), "4") {
		t.Errorf("error should mention required count: %v", err)
	}
}

// TestOutputResolutions verifies all layouts have output resolutions defined.
func TestOutputResolutions(t *testing.T) {
	layouts := []Layout{Layout1x1, Layout2x1, Layout1x2, Layout2x2, Layout3x3, LayoutPiP}
	for _, l := range layouts {
		res, ok := outputResolutions[l]
		if !ok {
			t.Errorf("layout %s has no output resolution defined", l)
			continue
		}
		if !strings.Contains(res, "x") {
			t.Errorf("layout %s resolution %q is not in WxH format", l, res)
		}
	}
}
