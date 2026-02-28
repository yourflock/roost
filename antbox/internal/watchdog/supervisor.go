package watchdog

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// ProcessState represents the lifecycle state of a supervised process.
type ProcessState string

const (
	// StateStopped means the process is not running.
	StateStopped ProcessState = "stopped"
	// StateRunning means the process is currently running.
	StateRunning ProcessState = "running"
	// StateRestarting means the process crashed and is being restarted.
	StateRestarting ProcessState = "restarting"
	// StateBackoff means restart attempts are being rate-limited.
	StateBackoff ProcessState = "backoff"
	// StateFatal means the process exceeded the maximum restart attempts.
	StateFatal ProcessState = "fatal"
)

// ProcessSpec describes a process that the watchdog should supervise.
type ProcessSpec struct {
	// Name is a human-readable identifier for the process.
	Name string
	// Command is the executable path.
	Command string
	// Args are command-line arguments.
	Args []string
	// Env are additional environment variables.
	Env []string
	// Dir is the working directory. Empty means inherit from parent.
	Dir string
}

// ProcessInfo provides a snapshot of a supervised process's status.
type ProcessInfo struct {
	// Name is the process name from the spec.
	Name string `json:"name"`
	// State is the current lifecycle state.
	State ProcessState `json:"state"`
	// PID is the OS process ID (0 if not running).
	PID int `json:"pid"`
	// Restarts is the total number of times this process has been restarted.
	Restarts int `json:"restarts"`
	// LastExitCode is the exit code from the most recent termination.
	LastExitCode int `json:"last_exit_code"`
	// LastStarted is when the process was last started.
	LastStarted time.Time `json:"last_started"`
	// LastStopped is when the process last exited.
	LastStopped time.Time `json:"last_stopped,omitempty"`
	// Uptime is the duration the process has been running (if running).
	Uptime string `json:"uptime,omitempty"`
}

// CommandFactory creates exec.Cmd instances. This is extracted as an interface
// so tests can substitute a mock without actually launching processes.
type CommandFactory interface {
	// Create returns an *exec.Cmd ready to be started.
	Create(ctx context.Context, spec ProcessSpec) *exec.Cmd
}

// DefaultCommandFactory uses the standard os/exec package.
type DefaultCommandFactory struct{}

// Create builds an exec.Cmd from the process spec.
func (f *DefaultCommandFactory) Create(ctx context.Context, spec ProcessSpec) *exec.Cmd {
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// supervisedProcess tracks the runtime state of a single process.
type supervisedProcess struct {
	spec         ProcessSpec
	state        ProcessState
	pid          int // stored separately to avoid racing on cmd.Process
	restarts     int
	lastExitCode int
	lastStarted  time.Time
	lastStopped  time.Time
	cancelFn     context.CancelFunc
}

// Supervisor monitors child processes and restarts them on crash.
// It implements exponential backoff for rapid crash loops and gives up
// after a configurable maximum number of restart attempts.
type Supervisor struct {
	factory    CommandFactory
	logger     *logrus.Logger
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration

	mu        sync.RWMutex
	processes map[string]*supervisedProcess
	wg        sync.WaitGroup
}

// NewSupervisor creates a new watchdog supervisor.
//
// Parameters:
//   - factory: creates exec.Cmd instances (use DefaultCommandFactory for production)
//   - logger: structured logger
//   - maxRetries: maximum restart attempts before entering fatal state (0 = unlimited)
//   - baseDelay: initial delay between restarts (exponentially doubled on consecutive crashes)
//   - maxDelay: maximum delay between restarts
func NewSupervisor(factory CommandFactory, logger *logrus.Logger, maxRetries int, baseDelay, maxDelay time.Duration) *Supervisor {
	if baseDelay <= 0 {
		baseDelay = 1 * time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	return &Supervisor{
		factory:    factory,
		logger:     logger,
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
		maxDelay:   maxDelay,
		processes:  make(map[string]*supervisedProcess),
	}
}

// Supervise begins supervising a process. The process is started immediately
// and will be restarted on crash until the context is cancelled or the
// maximum restart attempts are exceeded.
func (s *Supervisor) Supervise(ctx context.Context, spec ProcessSpec) error {
	s.mu.Lock()
	if _, exists := s.processes[spec.Name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("process %q is already supervised", spec.Name)
	}

	proc := &supervisedProcess{
		spec:  spec,
		state: StateStopped,
	}
	s.processes[spec.Name] = proc
	s.mu.Unlock()

	s.wg.Add(1)
	go s.superviseLoop(ctx, proc)
	return nil
}

// superviseLoop is the main supervision goroutine for a single process.
func (s *Supervisor) superviseLoop(ctx context.Context, proc *supervisedProcess) {
	defer s.wg.Done()

	consecutiveFails := 0

	for {
		select {
		case <-ctx.Done():
			s.stopProcess(proc)
			return
		default:
		}

		// Check if we've exceeded max retries.
		if s.maxRetries > 0 && consecutiveFails >= s.maxRetries {
			s.mu.Lock()
			proc.state = StateFatal
			s.mu.Unlock()

			s.logger.WithFields(logrus.Fields{
				"process":        proc.spec.Name,
				"restarts":       proc.restarts,
				"last_exit_code": proc.lastExitCode,
			}).Error("process exceeded maximum restart attempts; entering fatal state")
			return
		}

		// Start the process.
		procCtx, procCancel := context.WithCancel(ctx)

		cmd := s.factory.Create(procCtx, proc.spec)

		s.logger.WithFields(logrus.Fields{
			"process": proc.spec.Name,
			"command": proc.spec.Command,
			"args":    proc.spec.Args,
		}).Info("starting supervised process")

		err := cmd.Start()
		if err != nil {
			procCancel()
			s.mu.Lock()
			proc.state = StateRestarting
			proc.lastStopped = time.Now()
			proc.restarts++
			s.mu.Unlock()

			consecutiveFails++
			s.logger.WithError(err).WithField("process", proc.spec.Name).Error("failed to start process")

			delay := s.backoffDelay(consecutiveFails)
			if !s.sleepWithContext(ctx, delay) {
				s.stopProcess(proc)
				return
			}
			continue
		}

		// cmd.Start() succeeded. Capture PID under the lock so GetInfo
		// never reads cmd.Process directly (which would race).
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}

		s.mu.Lock()
		proc.cancelFn = procCancel
		proc.state = StateRunning
		proc.lastStarted = time.Now()
		proc.pid = pid
		s.mu.Unlock()

		s.logger.WithFields(logrus.Fields{
			"process": proc.spec.Name,
			"pid":     pid,
		}).Info("supervised process started")

		// Wait for the process to exit.
		waitErr := cmd.Wait()
		procCancel()

		s.mu.Lock()
		proc.lastStopped = time.Now()
		proc.pid = 0
		proc.cancelFn = nil
		s.mu.Unlock()

		// Check if the parent context was cancelled (shutdown).
		if ctx.Err() != nil {
			s.mu.Lock()
			proc.state = StateStopped
			s.mu.Unlock()
			return
		}

		// Process exited unexpectedly.
		exitCode := -1
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		} else {
			exitCode = 0
			// Clean exit (code 0) resets the failure counter.
			consecutiveFails = 0
		}

		s.mu.Lock()
		proc.lastExitCode = exitCode
		proc.restarts++
		proc.state = StateRestarting
		s.mu.Unlock()

		if exitCode != 0 {
			consecutiveFails++
		}

		s.logger.WithFields(logrus.Fields{
			"process":           proc.spec.Name,
			"exit_code":         exitCode,
			"restarts":          proc.restarts,
			"consecutive_fails": consecutiveFails,
		}).Warn("supervised process exited; will restart")

		delay := s.backoffDelay(consecutiveFails)

		s.mu.Lock()
		proc.state = StateBackoff
		s.mu.Unlock()

		if !s.sleepWithContext(ctx, delay) {
			s.stopProcess(proc)
			return
		}
	}
}

// stopProcess sends a signal to terminate the running process.
func (s *Supervisor) stopProcess(proc *supervisedProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if proc.cancelFn != nil {
		proc.cancelFn()
		proc.cancelFn = nil
	}
	proc.pid = 0
	proc.state = StateStopped
}

// backoffDelay computes an exponential backoff delay based on the number
// of consecutive failures.
func (s *Supervisor) backoffDelay(consecutiveFails int) time.Duration {
	if consecutiveFails <= 0 {
		return s.baseDelay
	}

	delay := s.baseDelay
	for i := 0; i < consecutiveFails-1; i++ {
		delay *= 2
		if delay > s.maxDelay {
			return s.maxDelay
		}
	}
	return delay
}

// sleepWithContext sleeps for the given duration or until the context is cancelled.
// Returns false if the context was cancelled.
func (s *Supervisor) sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// GetInfo returns the current status of a supervised process.
func (s *Supervisor) GetInfo(name string) (*ProcessInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proc, exists := s.processes[name]
	if !exists {
		return nil, fmt.Errorf("process %q not found", name)
	}

	info := &ProcessInfo{
		Name:         proc.spec.Name,
		State:        proc.state,
		PID:          proc.pid,
		Restarts:     proc.restarts,
		LastExitCode: proc.lastExitCode,
		LastStarted:  proc.lastStarted,
		LastStopped:  proc.lastStopped,
	}

	if proc.state == StateRunning && !proc.lastStarted.IsZero() {
		info.Uptime = time.Since(proc.lastStarted).String()
	}

	return info, nil
}

// GetAllInfo returns the status of all supervised processes.
func (s *Supervisor) GetAllInfo() []ProcessInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	infos := make([]ProcessInfo, 0, len(s.processes))
	for _, proc := range s.processes {
		info := ProcessInfo{
			Name:         proc.spec.Name,
			State:        proc.state,
			PID:          proc.pid,
			Restarts:     proc.restarts,
			LastExitCode: proc.lastExitCode,
			LastStarted:  proc.lastStarted,
			LastStopped:  proc.lastStopped,
		}

		if proc.state == StateRunning && !proc.lastStarted.IsZero() {
			info.Uptime = time.Since(proc.lastStarted).String()
		}

		infos = append(infos, info)
	}

	return infos
}

// Wait blocks until all supervised processes have exited.
func (s *Supervisor) Wait() {
	s.wg.Wait()
}
