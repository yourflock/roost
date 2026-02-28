package command

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"antbox/internal/hdhomerun"
	"antbox/internal/scanner"

	"github.com/sirupsen/logrus"
)

// Type enumerates the supported command types from AntServer.
type Type string

const (
	// CmdScanChannels initiates a channel scan on a device.
	CmdScanChannels Type = "SCAN_CHANNELS"
	// CmdStartEvent starts recording/streaming an event.
	CmdStartEvent Type = "START_EVENT"
	// CmdStopEvent stops an active recording/streaming event.
	CmdStopEvent Type = "STOP_EVENT"
	// CmdHealth requests a health check response.
	CmdHealth Type = "HEALTH"
	// CmdUpdate requests a software update of the AntBox daemon.
	CmdUpdate Type = "UPDATE"
)

// Command represents a command received from AntServer.
type Command struct {
	// ID is the unique identifier for this command instance.
	ID string `json:"id"`
	// Type is the command type.
	Type Type `json:"type"`
	// Payload contains command-specific parameters as raw JSON.
	Payload json.RawMessage `json:"payload"`
	// Timestamp is when the command was issued by the server.
	Timestamp time.Time `json:"timestamp"`
}

// Response represents the result of executing a command.
type Response struct {
	// CommandID is the ID of the command this response is for.
	CommandID string `json:"command_id"`
	// Success indicates whether the command executed successfully.
	Success bool `json:"success"`
	// Data contains command-specific response data.
	Data interface{} `json:"data,omitempty"`
	// Error contains an error message if the command failed.
	Error string `json:"error,omitempty"`
	// Timestamp is when the response was generated.
	Timestamp time.Time `json:"timestamp"`
}

// ScanPayload holds the parameters for a SCAN_CHANNELS command.
type ScanPayload struct {
	DeviceIP string `json:"device_ip"`
	Quick    bool   `json:"quick"`
}

// StartEventPayload holds the parameters for a START_EVENT command.
type StartEventPayload struct {
	EventID    string `json:"event_id"`
	DeviceIP   string `json:"device_ip"`
	TunerIndex int    `json:"tuner_index"`
	Channel    string `json:"channel"`
}

// StopEventPayload holds the parameters for a STOP_EVENT command.
type StopEventPayload struct {
	EventID string `json:"event_id"`
}

// HealthData holds the response data for a HEALTH command.
type HealthData struct {
	Status    string    `json:"status"`
	Uptime    string    `json:"uptime"`
	GoVersion string    `json:"go_version"`
	GOOS      string    `json:"goos"`
	GOARCH    string    `json:"goarch"`
	NumCPU    int       `json:"num_cpu"`
	Timestamp time.Time `json:"timestamp"`
}

// ActiveEvent tracks an event that is currently being recorded/streamed.
type ActiveEvent struct {
	EventID    string `json:"event_id"`
	DeviceIP   string `json:"device_ip"`
	TunerIndex int    `json:"tuner_index"`
	Channel    string `json:"channel"`
	StreamURL  string `json:"stream_url"`
	StartedAt  time.Time `json:"started_at"`
}

// Handler processes commands received from AntServer.
type Handler struct {
	hdClient  hdhomerun.Client
	scanner   *scanner.Scanner
	logger    *logrus.Logger
	startTime time.Time

	mu           sync.RWMutex
	activeEvents map[string]*ActiveEvent
}

// NewHandler creates a new command handler.
func NewHandler(hdClient hdhomerun.Client, sc *scanner.Scanner, logger *logrus.Logger) *Handler {
	return &Handler{
		hdClient:     hdClient,
		scanner:      sc,
		logger:       logger,
		startTime:    time.Now(),
		activeEvents: make(map[string]*ActiveEvent),
	}
}

// Handle processes a command and returns a response.
func (h *Handler) Handle(ctx context.Context, cmd Command) Response {
	h.logger.WithFields(logrus.Fields{
		"command_id":   cmd.ID,
		"command_type": cmd.Type,
	}).Info("processing command")

	var resp Response
	resp.CommandID = cmd.ID
	resp.Timestamp = time.Now()

	switch cmd.Type {
	case CmdScanChannels:
		resp = h.handleScanChannels(ctx, cmd)
	case CmdStartEvent:
		resp = h.handleStartEvent(ctx, cmd)
	case CmdStopEvent:
		resp = h.handleStopEvent(ctx, cmd)
	case CmdHealth:
		resp = h.handleHealth(ctx, cmd)
	case CmdUpdate:
		resp = h.handleUpdate(ctx, cmd)
	default:
		resp.Success = false
		resp.Error = fmt.Sprintf("unknown command type: %s", cmd.Type)
	}

	return resp
}

// handleScanChannels initiates a channel scan.
func (h *Handler) handleScanChannels(ctx context.Context, cmd Command) Response {
	resp := Response{CommandID: cmd.ID, Timestamp: time.Now()}

	var payload ScanPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		resp.Success = false
		resp.Error = fmt.Sprintf("invalid scan payload: %v", err)
		return resp
	}

	if payload.DeviceIP == "" {
		resp.Success = false
		resp.Error = "device_ip is required"
		return resp
	}

	if err := h.scanner.Scan(ctx, payload.DeviceIP, payload.Quick); err != nil {
		resp.Success = false
		resp.Error = fmt.Sprintf("failed to start scan: %v", err)
		return resp
	}

	resp.Success = true
	resp.Data = map[string]interface{}{
		"message":   "scan started",
		"device_ip": payload.DeviceIP,
		"quick":     payload.Quick,
	}
	return resp
}

// handleStartEvent starts recording/streaming a channel.
func (h *Handler) handleStartEvent(ctx context.Context, cmd Command) Response {
	resp := Response{CommandID: cmd.ID, Timestamp: time.Now()}

	var payload StartEventPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		resp.Success = false
		resp.Error = fmt.Sprintf("invalid start_event payload: %v", err)
		return resp
	}

	if payload.EventID == "" || payload.DeviceIP == "" || payload.Channel == "" {
		resp.Success = false
		resp.Error = "event_id, device_ip, and channel are required"
		return resp
	}

	h.mu.RLock()
	if _, exists := h.activeEvents[payload.EventID]; exists {
		h.mu.RUnlock()
		resp.Success = false
		resp.Error = fmt.Sprintf("event %s is already active", payload.EventID)
		return resp
	}
	h.mu.RUnlock()

	// Tune the tuner to the requested channel.
	streamURL, err := h.hdClient.TuneTo(ctx, payload.DeviceIP, payload.TunerIndex, payload.Channel)
	if err != nil {
		resp.Success = false
		resp.Error = fmt.Sprintf("failed to tune: %v", err)
		return resp
	}

	event := &ActiveEvent{
		EventID:    payload.EventID,
		DeviceIP:   payload.DeviceIP,
		TunerIndex: payload.TunerIndex,
		Channel:    payload.Channel,
		StreamURL:  streamURL,
		StartedAt:  time.Now(),
	}

	h.mu.Lock()
	h.activeEvents[payload.EventID] = event
	h.mu.Unlock()

	h.logger.WithFields(logrus.Fields{
		"event_id":   payload.EventID,
		"channel":    payload.Channel,
		"stream_url": streamURL,
	}).Info("event started")

	resp.Success = true
	resp.Data = event
	return resp
}

// handleStopEvent stops an active recording/streaming event.
func (h *Handler) handleStopEvent(_ context.Context, cmd Command) Response {
	resp := Response{CommandID: cmd.ID, Timestamp: time.Now()}

	var payload StopEventPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		resp.Success = false
		resp.Error = fmt.Sprintf("invalid stop_event payload: %v", err)
		return resp
	}

	if payload.EventID == "" {
		resp.Success = false
		resp.Error = "event_id is required"
		return resp
	}

	h.mu.Lock()
	event, exists := h.activeEvents[payload.EventID]
	if !exists {
		h.mu.Unlock()
		resp.Success = false
		resp.Error = fmt.Sprintf("event %s is not active", payload.EventID)
		return resp
	}
	delete(h.activeEvents, payload.EventID)
	h.mu.Unlock()

	h.logger.WithFields(logrus.Fields{
		"event_id": payload.EventID,
		"duration": time.Since(event.StartedAt).String(),
	}).Info("event stopped")

	resp.Success = true
	resp.Data = map[string]interface{}{
		"event_id": payload.EventID,
		"duration": time.Since(event.StartedAt).String(),
	}
	return resp
}

// handleHealth returns the daemon's current health status.
func (h *Handler) handleHealth(_ context.Context, cmd Command) Response {
	return Response{
		CommandID: cmd.ID,
		Success:   true,
		Timestamp: time.Now(),
		Data: HealthData{
			Status:    "healthy",
			Uptime:    time.Since(h.startTime).String(),
			GoVersion: runtime.Version(),
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
			NumCPU:    runtime.NumCPU(),
			Timestamp: time.Now(),
		},
	}
}

// handleUpdate handles a software update command.
// In production, this would download and apply an update.
// For now, it acknowledges the command.
func (h *Handler) handleUpdate(_ context.Context, cmd Command) Response {
	h.logger.Info("update command received (not yet implemented)")
	return Response{
		CommandID: cmd.ID,
		Success:   true,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"message": "update acknowledged; automatic updates not yet implemented",
		},
	}
}

// GetActiveEvents returns a snapshot of all active events.
func (h *Handler) GetActiveEvents() []ActiveEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()

	events := make([]ActiveEvent, 0, len(h.activeEvents))
	for _, event := range h.activeEvents {
		events = append(events, *event)
	}
	return events
}

// ActiveEventCount returns the number of currently active events.
func (h *Handler) ActiveEventCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.activeEvents)
}
