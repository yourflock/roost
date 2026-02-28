package tests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"antbox/internal/command"
	"antbox/internal/hdhomerun"
	"antbox/internal/scanner"
)

func newTestHandler() (*command.Handler, *MockClient) {
	mock := &MockClient{
		TuneStreamURL: "http://192.168.1.100:5004/auto/v5.1",
		ScanResult: []hdhomerun.Channel{
			{Number: "5.1", Name: "WEWS-DT"},
		},
		ScanDuration: 10 * time.Millisecond,
	}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)
	handler := command.NewHandler(mock, sc, logger)
	return handler, mock
}

func TestCommand_Health(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	cmd := command.Command{
		ID:        "cmd-001",
		Type:      command.CmdHealth,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if resp.CommandID != "cmd-001" {
		t.Errorf("expected command ID cmd-001, got %s", resp.CommandID)
	}

	data, ok := resp.Data.(command.HealthData)
	if !ok {
		t.Fatalf("expected HealthData, got %T", resp.Data)
	}
	if data.Status != "healthy" {
		t.Errorf("expected status 'healthy', got %q", data.Status)
	}
	if data.GoVersion == "" {
		t.Error("expected non-empty GoVersion")
	}
	if data.NumCPU <= 0 {
		t.Error("expected positive NumCPU")
	}
}

func TestCommand_ScanChannels(t *testing.T) {
	t.Parallel()

	handler, mock := newTestHandler()
	ctx := context.Background()

	payload, _ := json.Marshal(command.ScanPayload{
		DeviceIP: "192.168.1.100",
		Quick:    true,
	})

	cmd := command.Command{
		ID:        "cmd-002",
		Type:      command.CmdScanChannels,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Verify the mock was called.
	time.Sleep(50 * time.Millisecond)
	mock.mu.Lock()
	scanCallCount := len(mock.ScanCalls)
	mock.mu.Unlock()

	if scanCallCount != 1 {
		t.Errorf("expected 1 scan call, got %d", scanCallCount)
	}
}

func TestCommand_ScanChannels_MissingDeviceIP(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	payload, _ := json.Marshal(command.ScanPayload{
		DeviceIP: "",
		Quick:    true,
	})

	cmd := command.Command{
		ID:        "cmd-003",
		Type:      command.CmdScanChannels,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for missing device_ip")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestCommand_ScanChannels_InvalidPayload(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	cmd := command.Command{
		ID:        "cmd-004",
		Type:      command.CmdScanChannels,
		Payload:   json.RawMessage(`{invalid json}`),
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for invalid payload")
	}
}

func TestCommand_StartEvent(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	payload, _ := json.Marshal(command.StartEventPayload{
		EventID:    "event-001",
		DeviceIP:   "192.168.1.100",
		TunerIndex: 0,
		Channel:    "5.1",
	})

	cmd := command.Command{
		ID:        "cmd-005",
		Type:      command.CmdStartEvent,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Verify active event count.
	if handler.ActiveEventCount() != 1 {
		t.Errorf("expected 1 active event, got %d", handler.ActiveEventCount())
	}

	events := handler.GetActiveEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 active event, got %d", len(events))
	}
	if events[0].EventID != "event-001" {
		t.Errorf("expected event ID event-001, got %s", events[0].EventID)
	}
	if events[0].Channel != "5.1" {
		t.Errorf("expected channel 5.1, got %s", events[0].Channel)
	}
}

func TestCommand_StartEvent_MissingFields(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	// Missing event_id.
	payload, _ := json.Marshal(command.StartEventPayload{
		DeviceIP: "192.168.1.100",
		Channel:  "5.1",
	})

	cmd := command.Command{
		ID:        "cmd-006",
		Type:      command.CmdStartEvent,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for missing event_id")
	}
}

func TestCommand_StartEvent_DuplicateEvent(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	payload, _ := json.Marshal(command.StartEventPayload{
		EventID:    "event-dup",
		DeviceIP:   "192.168.1.100",
		TunerIndex: 0,
		Channel:    "5.1",
	})

	cmd := command.Command{
		ID:        "cmd-007a",
		Type:      command.CmdStartEvent,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	// First start should succeed.
	resp := handler.Handle(ctx, cmd)
	if !resp.Success {
		t.Fatalf("first start failed: %s", resp.Error)
	}

	// Second start with same event ID should fail.
	cmd.ID = "cmd-007b"
	resp = handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for duplicate event")
	}
}

func TestCommand_StopEvent(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	// Start an event first.
	startPayload, _ := json.Marshal(command.StartEventPayload{
		EventID:    "event-stop-test",
		DeviceIP:   "192.168.1.100",
		TunerIndex: 0,
		Channel:    "5.1",
	})

	startCmd := command.Command{
		ID:        "cmd-008a",
		Type:      command.CmdStartEvent,
		Payload:   startPayload,
		Timestamp: time.Now(),
	}
	handler.Handle(ctx, startCmd)

	if handler.ActiveEventCount() != 1 {
		t.Fatalf("expected 1 active event after start, got %d", handler.ActiveEventCount())
	}

	// Stop the event.
	stopPayload, _ := json.Marshal(command.StopEventPayload{
		EventID: "event-stop-test",
	})

	stopCmd := command.Command{
		ID:        "cmd-008b",
		Type:      command.CmdStopEvent,
		Payload:   stopPayload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, stopCmd)
	if !resp.Success {
		t.Fatalf("stop failed: %s", resp.Error)
	}
	if handler.ActiveEventCount() != 0 {
		t.Errorf("expected 0 active events after stop, got %d", handler.ActiveEventCount())
	}
}

func TestCommand_StopEvent_NotActive(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	payload, _ := json.Marshal(command.StopEventPayload{
		EventID: "nonexistent",
	})

	cmd := command.Command{
		ID:        "cmd-009",
		Type:      command.CmdStopEvent,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for stopping non-active event")
	}
}

func TestCommand_StopEvent_MissingEventID(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	payload, _ := json.Marshal(command.StopEventPayload{
		EventID: "",
	})

	cmd := command.Command{
		ID:        "cmd-010",
		Type:      command.CmdStopEvent,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for missing event_id")
	}
}

func TestCommand_Update(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	cmd := command.Command{
		ID:        "cmd-011",
		Type:      command.CmdUpdate,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestCommand_UnknownType(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	cmd := command.Command{
		ID:        "cmd-012",
		Type:      "UNKNOWN_CMD",
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)
	if resp.Success {
		t.Fatal("expected failure for unknown command type")
	}
	if resp.Error == "" {
		t.Error("expected error message for unknown command")
	}
}

func TestCommand_MultipleEvents(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	// Start 3 events on different tuners.
	for i := 0; i < 3; i++ {
		payload, _ := json.Marshal(command.StartEventPayload{
			EventID:    "multi-" + string(rune('A'+i)),
			DeviceIP:   "192.168.1.100",
			TunerIndex: i,
			Channel:    "5." + string(rune('1'+i)),
		})

		cmd := command.Command{
			ID:        "cmd-multi-" + string(rune('a'+i)),
			Type:      command.CmdStartEvent,
			Payload:   payload,
			Timestamp: time.Now(),
		}

		resp := handler.Handle(ctx, cmd)
		if !resp.Success {
			t.Fatalf("failed to start event %d: %s", i, resp.Error)
		}
	}

	if handler.ActiveEventCount() != 3 {
		t.Errorf("expected 3 active events, got %d", handler.ActiveEventCount())
	}

	events := handler.GetActiveEvents()
	if len(events) != 3 {
		t.Errorf("expected 3 active events in list, got %d", len(events))
	}

	// Stop one event.
	stopPayload, _ := json.Marshal(command.StopEventPayload{EventID: "multi-B"})
	stopCmd := command.Command{
		ID:      "cmd-multi-stop",
		Type:    command.CmdStopEvent,
		Payload: stopPayload,
	}
	handler.Handle(ctx, stopCmd)

	if handler.ActiveEventCount() != 2 {
		t.Errorf("expected 2 active events after stopping one, got %d", handler.ActiveEventCount())
	}
}

func TestCommand_ResponseTimestamp(t *testing.T) {
	t.Parallel()

	handler, _ := newTestHandler()
	ctx := context.Background()

	before := time.Now()

	cmd := command.Command{
		ID:        "cmd-ts",
		Type:      command.CmdHealth,
		Timestamp: time.Now(),
	}

	resp := handler.Handle(ctx, cmd)

	after := time.Now()

	if resp.Timestamp.Before(before) || resp.Timestamp.After(after) {
		t.Errorf("response timestamp %v outside expected range [%v, %v]",
			resp.Timestamp, before, after)
	}
}
