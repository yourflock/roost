package heartbeat

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// TunerStatus represents the status of a single tuner on a device.
type TunerStatus struct {
	// Index is the tuner number (0-based).
	Index int `json:"index"`
	// Active indicates whether the tuner is currently in use.
	Active bool `json:"active"`
	// Channel is the currently tuned channel (empty if not tuned).
	Channel string `json:"channel,omitempty"`
	// SignalStrength is the signal strength percentage (0-100).
	SignalStrength int `json:"signal_strength"`
}

// DeviceStatus represents the status of a single HDHomeRun device.
type DeviceStatus struct {
	// DeviceID is the unique hardware identifier.
	DeviceID string `json:"device_id"`
	// IP is the device's local IP address.
	IP string `json:"ip"`
	// Online indicates whether the device is reachable.
	Online bool `json:"online"`
	// Tuners contains the status of each tuner on the device.
	Tuners []TunerStatus `json:"tuners"`
}

// Report is the heartbeat payload sent to AntServer.
type Report struct {
	// AntBoxID identifies this AntBox instance.
	AntBoxID string `json:"antbox_id"`
	// Timestamp is when this heartbeat was generated.
	Timestamp time.Time `json:"timestamp"`
	// Uptime is how long this AntBox has been running.
	Uptime string `json:"uptime"`
	// Devices contains the status of all discovered HDHomeRun devices.
	Devices []DeviceStatus `json:"devices"`
	// ActiveEvents is the number of events currently being captured.
	ActiveEvents int `json:"active_events"`
	// ScannerState is the current state of the channel scanner.
	ScannerState string `json:"scanner_state"`
}

// Sender is the interface for sending heartbeat reports to the server.
// This abstraction allows testing without a real network connection.
type Sender interface {
	// Send transmits a heartbeat report to the server.
	// Returns an error if the report could not be delivered.
	Send(ctx context.Context, data []byte) error
}

// StatusProvider is the interface for collecting current system status.
// The main application implements this to provide live device and event data.
type StatusProvider interface {
	// GetDeviceStatuses returns the current status of all known devices.
	GetDeviceStatuses() []DeviceStatus
	// GetActiveEventCount returns the number of active capture events.
	GetActiveEventCount() int
	// GetScannerState returns the current scanner state string.
	GetScannerState() string
}

// Reporter sends periodic heartbeat reports to AntServer.
type Reporter struct {
	antBoxID  string
	interval  time.Duration
	sender    Sender
	provider  StatusProvider
	logger    *logrus.Logger
	startTime time.Time

	mu          sync.RWMutex
	lastReport  *Report
	lastSentAt  time.Time
	sendCount   int64
	failCount   int64
}

// NewReporter creates a new heartbeat reporter.
func NewReporter(antBoxID string, interval time.Duration, sender Sender, provider StatusProvider, logger *logrus.Logger) *Reporter {
	return &Reporter{
		antBoxID:  antBoxID,
		interval:  interval,
		sender:    sender,
		provider:  provider,
		logger:    logger,
		startTime: time.Now(),
	}
}

// Run starts the heartbeat loop. It blocks until the context is cancelled.
// Heartbeats are sent at the configured interval. If a send fails, the
// error is logged but the loop continues.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Send an immediate heartbeat on start.
	r.sendHeartbeat(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("heartbeat reporter stopping")
			return
		case <-ticker.C:
			r.sendHeartbeat(ctx)
		}
	}
}

// sendHeartbeat generates and sends a single heartbeat report.
func (r *Reporter) sendHeartbeat(ctx context.Context) {
	report := Report{
		AntBoxID:     r.antBoxID,
		Timestamp:    time.Now(),
		Uptime:       time.Since(r.startTime).String(),
		Devices:      r.provider.GetDeviceStatuses(),
		ActiveEvents: r.provider.GetActiveEventCount(),
		ScannerState: r.provider.GetScannerState(),
	}

	data, err := json.Marshal(report)
	if err != nil {
		r.logger.WithError(err).Error("failed to marshal heartbeat report")
		return
	}

	if err := r.sender.Send(ctx, data); err != nil {
		r.mu.Lock()
		r.failCount++
		r.mu.Unlock()

		r.logger.WithError(err).Warn("failed to send heartbeat")
		return
	}

	r.mu.Lock()
	r.lastReport = &report
	r.lastSentAt = time.Now()
	r.sendCount++
	r.mu.Unlock()

	r.logger.WithFields(logrus.Fields{
		"devices":       len(report.Devices),
		"active_events": report.ActiveEvents,
	}).Debug("heartbeat sent")
}

// LastReport returns the most recently sent heartbeat report.
func (r *Reporter) LastReport() *Report {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastReport
}

// LastSentAt returns the time of the most recent successful heartbeat send.
func (r *Reporter) LastSentAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastSentAt
}

// Stats returns the total number of successful and failed heartbeat sends.
func (r *Reporter) Stats() (sent int64, failed int64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sendCount, r.failCount
}
