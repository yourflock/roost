package tests

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"antbox/internal/watchdog"
)

// mockCommandFactory creates controllable mock commands for testing.
type mockCommandFactory struct {
	mu       sync.Mutex
	behavior func(ctx context.Context) error
	starts   int64
}

func (f *mockCommandFactory) Create(ctx context.Context, spec watchdog.ProcessSpec) *exec.Cmd {
	atomic.AddInt64(&f.starts, 1)

	// Use a real command that we can control: "sleep" with context cancellation.
	// But to properly test, we use a command that exits with a controlled code.
	// We'll use "sh -c" with behavior injected through environment.

	f.mu.Lock()
	behavior := f.behavior
	f.mu.Unlock()

	if behavior != nil {
		// For testability, we create a real command that the watchdog can manage.
		// The test controls its behavior through the context and exit codes.
	}

	// Default: create a short-lived "true" command (exits 0 immediately).
	cmd := exec.CommandContext(ctx, "true")
	return cmd
}

func (f *mockCommandFactory) StartCount() int64 {
	return atomic.LoadInt64(&f.starts)
}

// exitCodeFactory creates commands that exit with a specific code.
type exitCodeFactory struct {
	exitCode int
	delay    time.Duration
	starts   int64
}

func (f *exitCodeFactory) Create(ctx context.Context, spec watchdog.ProcessSpec) *exec.Cmd {
	atomic.AddInt64(&f.starts, 1)

	if f.delay > 0 {
		// Use sleep + exit to simulate a process that runs briefly then crashes.
		sleepSec := fmt.Sprintf("%.3f", f.delay.Seconds())
		cmd := exec.CommandContext(ctx, "sh", "-c",
			fmt.Sprintf("sleep %s; exit %d", sleepSec, f.exitCode))
		return cmd
	}

	if f.exitCode == 0 {
		return exec.CommandContext(ctx, "true")
	}
	return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("exit %d", f.exitCode))
}

func (f *exitCodeFactory) StartCount() int64 {
	return atomic.LoadInt64(&f.starts)
}

// longRunningFactory creates commands that run until cancelled.
type longRunningFactory struct {
	starts int64
}

func (f *longRunningFactory) Create(ctx context.Context, spec watchdog.ProcessSpec) *exec.Cmd {
	atomic.AddInt64(&f.starts, 1)
	// Sleep for a long time; the context cancellation will kill it.
	return exec.CommandContext(ctx, "sleep", "3600")
}

func (f *longRunningFactory) StartCount() int64 {
	return atomic.LoadInt64(&f.starts)
}

func TestWatchdog_SuperviseAndShutdown(t *testing.T) {
	t.Parallel()

	factory := &longRunningFactory{}
	logger := newTestLogger()
	sv := watchdog.NewSupervisor(factory, logger, 5, 50*time.Millisecond, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	spec := watchdog.ProcessSpec{
		Name:    "test-long",
		Command: "sleep",
		Args:    []string{"3600"},
	}

	err := sv.Supervise(ctx, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give the process time to start.
	time.Sleep(100 * time.Millisecond)

	info, err := sv.GetInfo("test-long")
	if err != nil {
		t.Fatalf("failed to get info: %v", err)
	}
	if info.State != watchdog.StateRunning {
		t.Errorf("expected state running, got %s", info.State)
	}
	if info.PID == 0 {
		t.Error("expected non-zero PID")
	}

	// Shutdown.
	cancel()
	sv.Wait()

	info, err = sv.GetInfo("test-long")
	if err != nil {
		t.Fatalf("failed to get info after shutdown: %v", err)
	}
	if info.State != watchdog.StateStopped {
		t.Errorf("expected state stopped after shutdown, got %s", info.State)
	}
}

func TestWatchdog_DuplicateProcess(t *testing.T) {
	t.Parallel()

	factory := &longRunningFactory{}
	logger := newTestLogger()
	sv := watchdog.NewSupervisor(factory, logger, 5, 50*time.Millisecond, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := watchdog.ProcessSpec{Name: "dup-test", Command: "sleep", Args: []string{"3600"}}
	if err := sv.Supervise(ctx, spec); err != nil {
		t.Fatalf("first supervise failed: %v", err)
	}

	err := sv.Supervise(ctx, spec)
	if err == nil {
		t.Fatal("expected error for duplicate process name")
	}

	cancel()
	sv.Wait()
}

func TestWatchdog_RestartsOnCrash(t *testing.T) {
	t.Parallel()

	factory := &exitCodeFactory{
		exitCode: 1,
		delay:    10 * time.Millisecond,
	}
	logger := newTestLogger()

	// Allow 3 retries with very short backoff for faster tests.
	sv := watchdog.NewSupervisor(factory, logger, 3, 20*time.Millisecond, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := watchdog.ProcessSpec{Name: "crash-test", Command: "sh", Args: []string{"-c", "exit 1"}}
	if err := sv.Supervise(ctx, spec); err != nil {
		t.Fatalf("supervise failed: %v", err)
	}

	// Wait for the process to exhaust retries and enter fatal state.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("process did not reach fatal state within timeout")
		default:
			info, err := sv.GetInfo("crash-test")
			if err != nil {
				t.Fatalf("failed to get info: %v", err)
			}
			if info.State == watchdog.StateFatal {
				// Process should have been started multiple times.
				starts := factory.StartCount()
				if starts < 3 {
					t.Errorf("expected at least 3 starts, got %d", starts)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestWatchdog_MaxRetriesExceeded(t *testing.T) {
	t.Parallel()

	factory := &exitCodeFactory{
		exitCode: 42,
		delay:    5 * time.Millisecond,
	}
	logger := newTestLogger()

	sv := watchdog.NewSupervisor(factory, logger, 2, 10*time.Millisecond, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := watchdog.ProcessSpec{Name: "max-retry", Command: "sh"}
	if err := sv.Supervise(ctx, spec); err != nil {
		t.Fatalf("supervise failed: %v", err)
	}

	// Wait for fatal state.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			info, _ := sv.GetInfo("max-retry")
			t.Fatalf("did not reach fatal state; current state: %s", info.State)
		default:
			info, _ := sv.GetInfo("max-retry")
			if info.State == watchdog.StateFatal {
				if info.LastExitCode != 42 {
					t.Errorf("expected last exit code 42, got %d", info.LastExitCode)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestWatchdog_GetAllInfo(t *testing.T) {
	t.Parallel()

	factory := &longRunningFactory{}
	logger := newTestLogger()
	sv := watchdog.NewSupervisor(factory, logger, 5, 50*time.Millisecond, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	spec1 := watchdog.ProcessSpec{Name: "proc-1", Command: "sleep", Args: []string{"3600"}}
	spec2 := watchdog.ProcessSpec{Name: "proc-2", Command: "sleep", Args: []string{"3600"}}

	if err := sv.Supervise(ctx, spec1); err != nil {
		t.Fatalf("supervise proc-1 failed: %v", err)
	}
	if err := sv.Supervise(ctx, spec2); err != nil {
		t.Fatalf("supervise proc-2 failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	infos := sv.GetAllInfo()
	if len(infos) != 2 {
		t.Fatalf("expected 2 processes, got %d", len(infos))
	}

	names := make(map[string]bool)
	for _, info := range infos {
		names[info.Name] = true
	}
	if !names["proc-1"] || !names["proc-2"] {
		t.Errorf("expected proc-1 and proc-2 in info, got %v", names)
	}

	cancel()
	sv.Wait()
}

func TestWatchdog_GetInfo_NotFound(t *testing.T) {
	t.Parallel()

	factory := &longRunningFactory{}
	logger := newTestLogger()
	sv := watchdog.NewSupervisor(factory, logger, 5, 50*time.Millisecond, 200*time.Millisecond)

	_, err := sv.GetInfo("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent process")
	}
}

func TestWatchdog_BackoffDelay(t *testing.T) {
	t.Parallel()

	factory := &exitCodeFactory{
		exitCode: 1,
		delay:    5 * time.Millisecond,
	}
	logger := newTestLogger()

	baseDelay := 50 * time.Millisecond
	sv := watchdog.NewSupervisor(factory, logger, 4, baseDelay, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := watchdog.ProcessSpec{Name: "backoff-test", Command: "sh"}
	start := time.Now()
	if err := sv.Supervise(ctx, spec); err != nil {
		t.Fatalf("supervise failed: %v", err)
	}

	// Wait for fatal state.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("did not reach fatal state")
		default:
			info, _ := sv.GetInfo("backoff-test")
			if info.State == watchdog.StateFatal {
				elapsed := time.Since(start)
				// With base delay 50ms and 4 max retries:
				// retry 1: ~50ms delay
				// retry 2: ~100ms delay
				// retry 3: ~200ms delay
				// Total minimum: ~350ms + process run time
				if elapsed < 300*time.Millisecond {
					t.Errorf("expected at least ~300ms for backoff, got %v", elapsed)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestWatchdog_ProcessInfo_Fields(t *testing.T) {
	t.Parallel()

	factory := &longRunningFactory{}
	logger := newTestLogger()
	sv := watchdog.NewSupervisor(factory, logger, 5, 50*time.Millisecond, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	spec := watchdog.ProcessSpec{Name: "fields-test", Command: "sleep", Args: []string{"3600"}}
	beforeStart := time.Now()
	if err := sv.Supervise(ctx, spec); err != nil {
		t.Fatalf("supervise failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	info, err := sv.GetInfo("fields-test")
	if err != nil {
		t.Fatalf("get info failed: %v", err)
	}

	if info.Name != "fields-test" {
		t.Errorf("expected name 'fields-test', got %q", info.Name)
	}
	if info.State != watchdog.StateRunning {
		t.Errorf("expected state running, got %s", info.State)
	}
	if info.PID <= 0 {
		t.Errorf("expected positive PID, got %d", info.PID)
	}
	if info.Restarts != 0 {
		t.Errorf("expected 0 restarts, got %d", info.Restarts)
	}
	if info.LastStarted.Before(beforeStart) {
		t.Error("LastStarted should be after test start")
	}
	if info.Uptime == "" {
		t.Error("expected non-empty uptime for running process")
	}

	cancel()
	sv.Wait()
}

func TestWatchdog_ContextCancelDuringBackoff(t *testing.T) {
	t.Parallel()

	factory := &exitCodeFactory{
		exitCode: 1,
		delay:    5 * time.Millisecond,
	}
	logger := newTestLogger()

	// Long backoff so we can cancel during it.
	sv := watchdog.NewSupervisor(factory, logger, 10, 2*time.Second, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())

	spec := watchdog.ProcessSpec{Name: "cancel-backoff", Command: "sh"}
	if err := sv.Supervise(ctx, spec); err != nil {
		t.Fatalf("supervise failed: %v", err)
	}

	// Wait for the process to crash and enter backoff.
	time.Sleep(100 * time.Millisecond)

	// Cancel during backoff.
	cancel()

	// Wait should return quickly despite the long backoff timer.
	done := make(chan struct{})
	go func() {
		sv.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(1 * time.Second):
		t.Fatal("Wait did not return after context cancel during backoff")
	}
}

func TestWatchdog_ProcessStates(t *testing.T) {
	t.Parallel()

	// Verify that all process state constants are distinct.
	states := map[watchdog.ProcessState]bool{
		watchdog.StateStopped:    true,
		watchdog.StateRunning:    true,
		watchdog.StateRestarting: true,
		watchdog.StateBackoff:    true,
		watchdog.StateFatal:      true,
	}

	if len(states) != 5 {
		t.Errorf("expected 5 distinct states, got %d", len(states))
	}
}
