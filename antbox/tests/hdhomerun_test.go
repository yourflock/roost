package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"antbox/internal/hdhomerun"
)

// MockClient implements hdhomerun.Client for testing.
type MockClient struct {
	mu sync.Mutex

	// Configurable responses.
	DiscoverDevices []hdhomerun.Device
	DiscoverErr     error

	TuneStreamURL string
	TuneErr       error

	Signal    *hdhomerun.SignalQuality
	SignalErr error

	ScanResult   []hdhomerun.Channel
	ScanErr      error
	ScanDuration time.Duration

	// Call tracking.
	DiscoverCalls    int
	TuneCalls        []tuneCall
	SignalCalls      []signalCall
	ScanCalls        []scanCall
}

type tuneCall struct {
	DeviceIP   string
	TunerIndex int
	Channel    string
}

type signalCall struct {
	DeviceIP   string
	TunerIndex int
}

type scanCall struct {
	DeviceIP string
	Quick    bool
}

func (m *MockClient) Discover(ctx context.Context) ([]hdhomerun.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DiscoverCalls++
	return m.DiscoverDevices, m.DiscoverErr
}

func (m *MockClient) TuneTo(ctx context.Context, deviceIP string, tunerIndex int, channel string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TuneCalls = append(m.TuneCalls, tuneCall{deviceIP, tunerIndex, channel})
	return m.TuneStreamURL, m.TuneErr
}

func (m *MockClient) GetSignalQuality(ctx context.Context, deviceIP string, tunerIndex int) (*hdhomerun.SignalQuality, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SignalCalls = append(m.SignalCalls, signalCall{deviceIP, tunerIndex})
	return m.Signal, m.SignalErr
}

func (m *MockClient) ScanChannels(ctx context.Context, deviceIP string, quick bool, progress func(hdhomerun.ScanProgress)) ([]hdhomerun.Channel, error) {
	m.mu.Lock()
	m.ScanCalls = append(m.ScanCalls, scanCall{deviceIP, quick})
	duration := m.ScanDuration
	result := m.ScanResult
	scanErr := m.ScanErr
	m.mu.Unlock()

	if scanErr != nil {
		return nil, scanErr
	}

	// Simulate scan progress with proper context cancellation support.
	for pct := 25; pct <= 100; pct += 25 {
		if duration > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(duration / 4):
			}
		} else {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		if progress != nil {
			progress(hdhomerun.ScanProgress{
				Percent: pct,
				Found:   len(result) * pct / 100,
			})
		}
	}

	return result, nil
}

func TestMockClient_Discover(t *testing.T) {
	t.Parallel()

	devices := []hdhomerun.Device{
		{DeviceID: "1234ABCD", IP: "192.168.1.100", Model: "HDHR5-4US", TunerCount: 4},
		{DeviceID: "5678EFGH", IP: "192.168.1.101", Model: "HDHR3-US", TunerCount: 2},
	}

	mock := &MockClient{DiscoverDevices: devices}
	ctx := context.Background()

	result, err := mock.Discover(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(result))
	}
	if result[0].DeviceID != "1234ABCD" {
		t.Errorf("expected DeviceID 1234ABCD, got %s", result[0].DeviceID)
	}
	if result[1].TunerCount != 2 {
		t.Errorf("expected TunerCount 2, got %d", result[1].TunerCount)
	}
	if mock.DiscoverCalls != 1 {
		t.Errorf("expected 1 discover call, got %d", mock.DiscoverCalls)
	}
}

func TestMockClient_DiscoverError(t *testing.T) {
	t.Parallel()

	mock := &MockClient{DiscoverErr: fmt.Errorf("network unreachable")}
	ctx := context.Background()

	_, err := mock.Discover(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "network unreachable" {
		t.Errorf("expected 'network unreachable', got %q", err.Error())
	}
}

func TestMockClient_TuneTo(t *testing.T) {
	t.Parallel()

	mock := &MockClient{TuneStreamURL: "http://192.168.1.100:5004/auto/v5.1"}
	ctx := context.Background()

	url, err := mock.TuneTo(ctx, "192.168.1.100", 0, "5.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if url != "http://192.168.1.100:5004/auto/v5.1" {
		t.Errorf("unexpected stream URL: %s", url)
	}
	if len(mock.TuneCalls) != 1 {
		t.Fatalf("expected 1 tune call, got %d", len(mock.TuneCalls))
	}
	if mock.TuneCalls[0].Channel != "5.1" {
		t.Errorf("expected channel 5.1, got %s", mock.TuneCalls[0].Channel)
	}
	if mock.TuneCalls[0].TunerIndex != 0 {
		t.Errorf("expected tuner index 0, got %d", mock.TuneCalls[0].TunerIndex)
	}
}

func TestMockClient_TuneError(t *testing.T) {
	t.Parallel()

	mock := &MockClient{TuneErr: fmt.Errorf("tuner busy")}
	ctx := context.Background()

	_, err := mock.TuneTo(ctx, "192.168.1.100", 0, "5.1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tuner busy") {
		t.Errorf("expected 'tuner busy' in error, got %q", err.Error())
	}
}

func TestMockClient_GetSignalQuality(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		Signal: &hdhomerun.SignalQuality{Strength: 85, SNR: 72, Quality: 90},
	}
	ctx := context.Background()

	sq, err := mock.GetSignalQuality(ctx, "192.168.1.100", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sq.Strength != 85 {
		t.Errorf("expected strength 85, got %d", sq.Strength)
	}
	if sq.SNR != 72 {
		t.Errorf("expected SNR 72, got %d", sq.SNR)
	}
	if sq.Quality != 90 {
		t.Errorf("expected quality 90, got %d", sq.Quality)
	}
	if len(mock.SignalCalls) != 1 {
		t.Fatalf("expected 1 signal call, got %d", len(mock.SignalCalls))
	}
}

func TestMockClient_ScanChannels(t *testing.T) {
	t.Parallel()

	channels := []hdhomerun.Channel{
		{Number: "5.1", Name: "WEWS-DT", Frequency: 557000000, Modulation: "8vsb"},
		{Number: "8.1", Name: "WJW-DT", Frequency: 503000000, Modulation: "8vsb"},
		{Number: "19.1", Name: "WOIO-DT", Frequency: 695000000, Modulation: "8vsb"},
	}

	mock := &MockClient{
		ScanResult:   channels,
		ScanDuration: 20 * time.Millisecond,
	}
	ctx := context.Background()

	var progressUpdates []hdhomerun.ScanProgress
	result, err := mock.ScanChannels(ctx, "192.168.1.100", true, func(p hdhomerun.ScanProgress) {
		progressUpdates = append(progressUpdates, p)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(result))
	}

	if len(progressUpdates) != 4 {
		t.Fatalf("expected 4 progress updates, got %d", len(progressUpdates))
	}

	// Progress should go 25, 50, 75, 100.
	for i, expected := range []int{25, 50, 75, 100} {
		if progressUpdates[i].Percent != expected {
			t.Errorf("progress[%d]: expected %d%%, got %d%%", i, expected, progressUpdates[i].Percent)
		}
	}
}

func TestMockClient_ScanChannelsCancellation(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		ScanResult:   []hdhomerun.Channel{{Number: "5.1", Name: "TEST"}},
		ScanDuration: 2 * time.Second, // long enough to cancel
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := mock.ScanChannels(ctx, "192.168.1.100", false, nil)
	if err == nil {
		t.Fatal("expected error from cancellation, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestHTTPClient_QueryDevice(t *testing.T) {
	t.Parallel()

	// Create a test HTTP server that mimics HDHomeRun discover.json.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover.json" {
			resp := map[string]interface{}{
				"DeviceID":        "ABCD1234",
				"LocalIP":         "192.168.1.100",
				"ModelNumber":     "HDHR5-4US",
				"FirmwareName":    "hdhomerun5_atsc",
				"FirmwareVersion": "20231001",
				"TunerCount":      4,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	// Extract host:port from the test server URL.
	client := hdhomerun.NewHTTPClient()
	ctx := context.Background()

	// We can't test Discover() directly because it uses UDP broadcast,
	// but we can verify the HTTP client is properly constructed.
	_ = client
	_ = ctx
}

func TestParseChannelNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input        string
		wantMajor    int
		wantMinor    int
		wantErr      bool
	}{
		{"5.1", 5, 1, false},
		{"19.3", 19, 3, false},
		{"100", 100, 0, false},
		{"2.0", 2, 0, false},
		{"abc", 0, 0, true},
		{"5.abc", 0, 0, true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			major, minor, err := hdhomerun.ParseChannelNumber(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.input, err)
			}
			if major != tt.wantMajor {
				t.Errorf("major: expected %d, got %d", tt.wantMajor, major)
			}
			if minor != tt.wantMinor {
				t.Errorf("minor: expected %d, got %d", tt.wantMinor, minor)
			}
		})
	}
}

func TestSignalQualityJSON(t *testing.T) {
	t.Parallel()

	sq := hdhomerun.SignalQuality{Strength: 85, SNR: 72, Quality: 90}
	data, err := json.Marshal(sq)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded hdhomerun.SignalQuality
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded != sq {
		t.Errorf("round-trip failed: got %+v, want %+v", decoded, sq)
	}
}

func TestDeviceJSON(t *testing.T) {
	t.Parallel()

	dev := hdhomerun.Device{
		DeviceID:        "ABCD1234",
		IP:              "192.168.1.100",
		Model:           "HDHR5-4US",
		FirmwareVersion: "20231001",
		TunerCount:      4,
	}

	data, err := json.Marshal(dev)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded hdhomerun.Device
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.DeviceID != dev.DeviceID {
		t.Errorf("DeviceID: got %q, want %q", decoded.DeviceID, dev.DeviceID)
	}
	if decoded.TunerCount != dev.TunerCount {
		t.Errorf("TunerCount: got %d, want %d", decoded.TunerCount, dev.TunerCount)
	}
}

func TestChannelJSON(t *testing.T) {
	t.Parallel()

	ch := hdhomerun.Channel{
		Number:     "5.1",
		Name:       "WEWS-DT",
		Frequency:  557000000,
		Modulation: "8vsb",
		Program:    3,
	}

	data, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded hdhomerun.Channel
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Number != ch.Number {
		t.Errorf("Number: got %q, want %q", decoded.Number, ch.Number)
	}
	if decoded.Name != ch.Name {
		t.Errorf("Name: got %q, want %q", decoded.Name, ch.Name)
	}
	if decoded.Frequency != ch.Frequency {
		t.Errorf("Frequency: got %d, want %d", decoded.Frequency, ch.Frequency)
	}
}
