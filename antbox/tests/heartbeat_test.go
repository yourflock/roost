package tests

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"antbox/internal/heartbeat"
)

// mockSender records all heartbeat payloads sent through it.
type mockSender struct {
	mu       sync.Mutex
	payloads [][]byte
	err      error
}

func (m *mockSender) Send(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	// Make a copy to avoid data races.
	cp := make([]byte, len(data))
	copy(cp, data)
	m.payloads = append(m.payloads, cp)
	return nil
}

func (m *mockSender) PayloadCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.payloads)
}

func (m *mockSender) GetPayload(index int) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index >= len(m.payloads) {
		return nil
	}
	return m.payloads[index]
}

func (m *mockSender) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// mockStatusProvider returns configurable status data.
type mockStatusProvider struct {
	mu            sync.Mutex
	devices       []heartbeat.DeviceStatus
	activeEvents  int
	scannerState  string
}

func (m *mockStatusProvider) GetDeviceStatuses() []heartbeat.DeviceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.devices
}

func (m *mockStatusProvider) GetActiveEventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeEvents
}

func (m *mockStatusProvider) GetScannerState() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scannerState
}

func (m *mockStatusProvider) SetActiveEvents(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeEvents = count
}

func (m *mockStatusProvider) SetScannerState(state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scannerState = state
}

func TestHeartbeat_SendsImmediately(t *testing.T) {
	t.Parallel()

	sender := &mockSender{}
	provider := &mockStatusProvider{
		scannerState: "idle",
	}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-test", 100*time.Millisecond, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go reporter.Run(ctx)

	// Wait a bit for the immediate heartbeat.
	time.Sleep(50 * time.Millisecond)
	cancel()

	if sender.PayloadCount() < 1 {
		t.Fatal("expected at least 1 heartbeat (immediate), got 0")
	}

	// Verify the first payload is a valid report.
	var report heartbeat.Report
	if err := json.Unmarshal(sender.GetPayload(0), &report); err != nil {
		t.Fatalf("failed to unmarshal heartbeat: %v", err)
	}

	if report.AntBoxID != "antbox-test" {
		t.Errorf("expected antbox_id 'antbox-test', got %q", report.AntBoxID)
	}
	if report.ScannerState != "idle" {
		t.Errorf("expected scanner_state 'idle', got %q", report.ScannerState)
	}
}

func TestHeartbeat_SendsAtInterval(t *testing.T) {
	t.Parallel()

	sender := &mockSender{}
	provider := &mockStatusProvider{
		scannerState: "idle",
	}
	logger := newTestLogger()

	interval := 50 * time.Millisecond
	reporter := heartbeat.NewReporter("antbox-interval", interval, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go reporter.Run(ctx)

	// Wait for at least 3 intervals (immediate + 2 ticks).
	time.Sleep(interval*3 + 20*time.Millisecond)
	cancel()

	count := sender.PayloadCount()
	// Expect at least 3 heartbeats: 1 immediate + 2 from ticker.
	// Allow some timing slack.
	if count < 3 {
		t.Errorf("expected at least 3 heartbeats, got %d", count)
	}
}

func TestHeartbeat_IncludesDeviceStatuses(t *testing.T) {
	t.Parallel()

	devices := []heartbeat.DeviceStatus{
		{
			DeviceID: "ABCD1234",
			IP:       "192.168.1.100",
			Online:   true,
			Tuners: []heartbeat.TunerStatus{
				{Index: 0, Active: true, Channel: "5.1", SignalStrength: 85},
				{Index: 1, Active: false, SignalStrength: 0},
			},
		},
	}

	sender := &mockSender{}
	provider := &mockStatusProvider{
		devices:      devices,
		activeEvents: 1,
		scannerState: "idle",
	}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-devices", 100*time.Millisecond, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go reporter.Run(ctx)

	time.Sleep(50 * time.Millisecond)
	cancel()

	if sender.PayloadCount() < 1 {
		t.Fatal("expected at least 1 heartbeat")
	}

	var report heartbeat.Report
	if err := json.Unmarshal(sender.GetPayload(0), &report); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(report.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(report.Devices))
	}
	if report.Devices[0].DeviceID != "ABCD1234" {
		t.Errorf("expected device ID ABCD1234, got %s", report.Devices[0].DeviceID)
	}
	if len(report.Devices[0].Tuners) != 2 {
		t.Fatalf("expected 2 tuners, got %d", len(report.Devices[0].Tuners))
	}
	if !report.Devices[0].Tuners[0].Active {
		t.Error("expected tuner 0 to be active")
	}
	if report.ActiveEvents != 1 {
		t.Errorf("expected 1 active event, got %d", report.ActiveEvents)
	}
}

func TestHeartbeat_HandlesSendErrors(t *testing.T) {
	t.Parallel()

	sender := &mockSender{err: context.DeadlineExceeded}
	provider := &mockStatusProvider{scannerState: "idle"}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-err", 50*time.Millisecond, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go reporter.Run(ctx)

	// Let several heartbeats fail.
	time.Sleep(180 * time.Millisecond)
	cancel()

	sent, failed := reporter.Stats()
	if sent != 0 {
		t.Errorf("expected 0 successful sends, got %d", sent)
	}
	if failed == 0 {
		t.Error("expected some failed sends, got 0")
	}

	// LastReport should be nil since no send succeeded.
	if reporter.LastReport() != nil {
		t.Error("expected nil last report when all sends fail")
	}
}

func TestHeartbeat_Stats(t *testing.T) {
	t.Parallel()

	sender := &mockSender{}
	provider := &mockStatusProvider{scannerState: "idle"}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-stats", 30*time.Millisecond, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go reporter.Run(ctx)

	// Wait for a few successful sends.
	time.Sleep(100 * time.Millisecond)

	// Now make sends fail.
	sender.SetError(context.DeadlineExceeded)
	time.Sleep(100 * time.Millisecond)
	cancel()

	sent, failed := reporter.Stats()
	if sent == 0 {
		t.Error("expected some successful sends")
	}
	if failed == 0 {
		t.Error("expected some failed sends")
	}
}

func TestHeartbeat_LastSentAt(t *testing.T) {
	t.Parallel()

	sender := &mockSender{}
	provider := &mockStatusProvider{scannerState: "idle"}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-time", 50*time.Millisecond, sender, provider, logger)

	// Initially, LastSentAt should be zero.
	if !reporter.LastSentAt().IsZero() {
		t.Error("expected zero LastSentAt before any sends")
	}

	ctx, cancel := context.WithCancel(context.Background())
	before := time.Now()
	go reporter.Run(ctx)

	time.Sleep(30 * time.Millisecond)
	cancel()

	after := time.Now()
	lastSent := reporter.LastSentAt()
	if lastSent.IsZero() {
		t.Fatal("expected non-zero LastSentAt after send")
	}
	if lastSent.Before(before) || lastSent.After(after) {
		t.Errorf("LastSentAt %v outside expected range [%v, %v]", lastSent, before, after)
	}
}

func TestHeartbeat_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	sender := &mockSender{}
	provider := &mockStatusProvider{scannerState: "idle"}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-cancel", 10*time.Millisecond, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		reporter.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Success: Run returned after cancel.
	case <-time.After(1 * time.Second):
		t.Fatal("heartbeat reporter did not stop after context cancel")
	}
}

func TestHeartbeat_DynamicStatusChanges(t *testing.T) {
	t.Parallel()

	sender := &mockSender{}
	provider := &mockStatusProvider{
		scannerState: "idle",
		activeEvents: 0,
	}
	logger := newTestLogger()

	reporter := heartbeat.NewReporter("antbox-dynamic", 30*time.Millisecond, sender, provider, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go reporter.Run(ctx)

	// Wait for first heartbeat.
	time.Sleep(20 * time.Millisecond)

	// Change status.
	provider.SetActiveEvents(3)
	provider.SetScannerState("scanning")

	// Wait for another heartbeat.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Check that at least one report reflects the updated state.
	found := false
	for i := 0; i < sender.PayloadCount(); i++ {
		var report heartbeat.Report
		if err := json.Unmarshal(sender.GetPayload(i), &report); err != nil {
			continue
		}
		if report.ActiveEvents == 3 && report.ScannerState == "scanning" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to find a heartbeat reflecting updated status (3 events, scanning)")
	}
}

func TestHeartbeat_ReportJSON(t *testing.T) {
	t.Parallel()

	report := heartbeat.Report{
		AntBoxID:     "antbox-json",
		Timestamp:    time.Now(),
		Uptime:       "5m30s",
		Devices:      nil,
		ActiveEvents: 2,
		ScannerState: "idle",
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded heartbeat.Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.AntBoxID != report.AntBoxID {
		t.Errorf("AntBoxID: got %q, want %q", decoded.AntBoxID, report.AntBoxID)
	}
	if decoded.ActiveEvents != report.ActiveEvents {
		t.Errorf("ActiveEvents: got %d, want %d", decoded.ActiveEvents, report.ActiveEvents)
	}
}
