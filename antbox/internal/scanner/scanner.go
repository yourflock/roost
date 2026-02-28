package scanner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"antbox/internal/hdhomerun"

	"github.com/sirupsen/logrus"
)

// State represents the current state of the channel scanner.
type State string

const (
	// StateIdle means no scan is in progress.
	StateIdle State = "idle"
	// StateScanning means a scan is actively running.
	StateScanning State = "scanning"
	// StateComplete means the last scan finished successfully.
	StateComplete State = "complete"
	// StateFailed means the last scan encountered an error.
	StateFailed State = "failed"
	// StateCancelled means the last scan was cancelled by the user or system.
	StateCancelled State = "cancelled"
)

// Result holds the outcome of a completed channel scan.
type Result struct {
	// Channels is the list of channels found during the scan.
	Channels []hdhomerun.Channel `json:"channels"`
	// Duration is how long the scan took.
	Duration time.Duration `json:"duration"`
	// DeviceIP is the device that was scanned.
	DeviceIP string `json:"device_ip"`
	// Quick indicates whether this was a quick or full scan.
	Quick bool `json:"quick"`
	// StartedAt is when the scan began.
	StartedAt time.Time `json:"started_at"`
	// CompletedAt is when the scan finished.
	CompletedAt time.Time `json:"completed_at"`
	// Error contains the error message if the scan failed.
	Error string `json:"error,omitempty"`
}

// Status provides a snapshot of the scanner's current state and progress.
type Status struct {
	// State is the current scanner state.
	State State `json:"state"`
	// Progress is the current scan progress (only valid when State is StateScanning).
	Progress hdhomerun.ScanProgress `json:"progress"`
	// LastResult is the result of the most recent scan, if any.
	LastResult *Result `json:"last_result,omitempty"`
}

// Scanner wraps HDHomeRun channel scanning with progress tracking,
// cancellation support, and state management.
type Scanner struct {
	client hdhomerun.Client
	logger *logrus.Logger

	mu         sync.RWMutex
	state      State
	progress   hdhomerun.ScanProgress
	lastResult *Result
	cancelFn   context.CancelFunc
}

// New creates a new Scanner with the given HDHomeRun client.
func New(client hdhomerun.Client, logger *logrus.Logger) *Scanner {
	return &Scanner{
		client: client,
		logger: logger,
		state:  StateIdle,
	}
}

// Scan starts a channel scan on the specified device. It returns an error
// if a scan is already in progress. The scan runs asynchronously; use
// GetStatus to check progress and GetResult to retrieve results.
func (s *Scanner) Scan(parentCtx context.Context, deviceIP string, quick bool) error {
	s.mu.Lock()
	if s.state == StateScanning {
		s.mu.Unlock()
		return fmt.Errorf("scan already in progress")
	}

	ctx, cancel := context.WithCancel(parentCtx)
	s.cancelFn = cancel
	s.state = StateScanning
	s.progress = hdhomerun.ScanProgress{}
	s.mu.Unlock()

	s.logger.WithFields(logrus.Fields{
		"device_ip": deviceIP,
		"quick":     quick,
	}).Info("starting channel scan")

	go s.runScan(ctx, deviceIP, quick)
	return nil
}

// runScan executes the actual scan in a goroutine.
func (s *Scanner) runScan(ctx context.Context, deviceIP string, quick bool) {
	startedAt := time.Now()

	channels, err := s.client.ScanChannels(ctx, deviceIP, quick, func(p hdhomerun.ScanProgress) {
		s.mu.Lock()
		s.progress = p
		s.mu.Unlock()

		s.logger.WithFields(logrus.Fields{
			"percent": p.Percent,
			"found":   p.Found,
		}).Debug("scan progress")
	})

	completedAt := time.Now()
	duration := completedAt.Sub(startedAt)

	s.mu.Lock()
	defer s.mu.Unlock()

	result := &Result{
		DeviceIP:    deviceIP,
		Quick:       quick,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    duration,
	}

	if err != nil {
		if ctx.Err() == context.Canceled {
			s.state = StateCancelled
			result.Error = "scan cancelled"
			s.logger.Info("channel scan cancelled")
		} else {
			s.state = StateFailed
			result.Error = err.Error()
			s.logger.WithError(err).Error("channel scan failed")
		}
	} else {
		s.state = StateComplete
		result.Channels = channels
		s.logger.WithFields(logrus.Fields{
			"channels_found": len(channels),
			"duration":       duration.String(),
		}).Info("channel scan complete")
	}

	s.lastResult = result
	s.cancelFn = nil
}

// Cancel cancels any in-progress scan.
func (s *Scanner) Cancel() {
	s.mu.RLock()
	cancel := s.cancelFn
	s.mu.RUnlock()

	if cancel != nil {
		cancel()
	}
}

// GetStatus returns the current scanner status including state and progress.
func (s *Scanner) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return Status{
		State:      s.state,
		Progress:   s.progress,
		LastResult: s.lastResult,
	}
}

// IsScanning returns true if a scan is currently in progress.
func (s *Scanner) IsScanning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == StateScanning
}
