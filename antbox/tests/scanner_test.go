package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"antbox/internal/hdhomerun"
	"antbox/internal/scanner"

	"github.com/sirupsen/logrus"
)

func newTestLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(&discardWriter{})
	return logger
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func TestScanner_InitialState(t *testing.T) {
	t.Parallel()

	mock := &MockClient{}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)

	status := sc.GetStatus()
	if status.State != scanner.StateIdle {
		t.Errorf("expected initial state idle, got %s", status.State)
	}
	if status.LastResult != nil {
		t.Error("expected nil last result initially")
	}
	if sc.IsScanning() {
		t.Error("expected IsScanning() to be false initially")
	}
}

func TestScanner_SuccessfulScan(t *testing.T) {
	t.Parallel()

	channels := []hdhomerun.Channel{
		{Number: "5.1", Name: "WEWS-DT", Frequency: 557000000},
		{Number: "8.1", Name: "WJW-DT", Frequency: 503000000},
	}

	mock := &MockClient{
		ScanResult:   channels,
		ScanDuration: 20 * time.Millisecond,
	}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)

	ctx := context.Background()
	err := sc.Scan(ctx, "192.168.1.100", true)
	if err != nil {
		t.Fatalf("unexpected error starting scan: %v", err)
	}

	// Wait for the scan to complete.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("scan did not complete within timeout")
		default:
			status := sc.GetStatus()
			if status.State == scanner.StateComplete {
				if status.LastResult == nil {
					t.Fatal("expected non-nil last result")
				}
				if len(status.LastResult.Channels) != 2 {
					t.Errorf("expected 2 channels, got %d", len(status.LastResult.Channels))
				}
				if status.LastResult.DeviceIP != "192.168.1.100" {
					t.Errorf("expected device IP 192.168.1.100, got %s", status.LastResult.DeviceIP)
				}
				if !status.LastResult.Quick {
					t.Error("expected quick=true")
				}
				if status.LastResult.Duration <= 0 {
					t.Error("expected positive duration")
				}
				if status.LastResult.Error != "" {
					t.Errorf("unexpected error in result: %s", status.LastResult.Error)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestScanner_FailedScan(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		ScanErr: fmt.Errorf("device communication error"),
	}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)

	ctx := context.Background()
	err := sc.Scan(ctx, "192.168.1.100", false)
	if err != nil {
		t.Fatalf("unexpected error starting scan: %v", err)
	}

	// Wait for the scan to fail.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("scan did not complete within timeout")
		default:
			status := sc.GetStatus()
			if status.State == scanner.StateFailed {
				if status.LastResult == nil {
					t.Fatal("expected non-nil last result")
				}
				if status.LastResult.Error == "" {
					t.Error("expected error in result")
				}
				if status.LastResult.Channels != nil {
					t.Error("expected nil channels on failure")
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestScanner_CancelledScan(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		ScanResult:   []hdhomerun.Channel{{Number: "5.1"}},
		ScanDuration: 5 * time.Second, // long scan so we can cancel
	}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)

	ctx := context.Background()
	err := sc.Scan(ctx, "192.168.1.100", false)
	if err != nil {
		t.Fatalf("unexpected error starting scan: %v", err)
	}

	// Verify scanning state.
	time.Sleep(20 * time.Millisecond)
	if !sc.IsScanning() {
		t.Error("expected IsScanning() to be true during scan")
	}

	// Cancel the scan.
	sc.Cancel()

	// Wait for the scan to be cancelled.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("scan cancellation did not complete within timeout")
		default:
			status := sc.GetStatus()
			if status.State == scanner.StateCancelled {
				if status.LastResult == nil {
					t.Fatal("expected non-nil last result")
				}
				if status.LastResult.Error != "scan cancelled" {
					t.Errorf("expected 'scan cancelled' error, got %q", status.LastResult.Error)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestScanner_DuplicateScan(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		ScanResult:   []hdhomerun.Channel{{Number: "5.1"}},
		ScanDuration: 2 * time.Second,
	}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)

	ctx := context.Background()
	err := sc.Scan(ctx, "192.168.1.100", true)
	if err != nil {
		t.Fatalf("unexpected error starting first scan: %v", err)
	}

	// Try to start a second scan while the first is running.
	time.Sleep(20 * time.Millisecond)
	err = sc.Scan(ctx, "192.168.1.100", false)
	if err == nil {
		t.Fatal("expected error for duplicate scan, got nil")
	}

	// Cancel to clean up.
	sc.Cancel()
}

func TestScanner_ProgressUpdates(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		ScanResult:   []hdhomerun.Channel{{Number: "5.1"}, {Number: "8.1"}},
		ScanDuration: 40 * time.Millisecond,
	}
	logger := newTestLogger()
	sc := scanner.New(mock, logger)

	ctx := context.Background()
	err := sc.Scan(ctx, "192.168.1.100", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Poll progress until complete.
	var maxPercent int
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("scan did not complete within timeout")
		default:
			status := sc.GetStatus()
			if status.Progress.Percent > maxPercent {
				maxPercent = status.Progress.Percent
			}
			if status.State == scanner.StateComplete {
				if maxPercent == 0 {
					t.Error("expected to see progress > 0 during scan")
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestScanner_QuickVsFull(t *testing.T) {
	t.Parallel()

	mock := &MockClient{
		ScanResult:   []hdhomerun.Channel{{Number: "5.1"}},
		ScanDuration: 10 * time.Millisecond,
	}
	logger := newTestLogger()

	// Quick scan.
	sc1 := scanner.New(mock, logger)
	ctx := context.Background()
	if err := sc1.Scan(ctx, "192.168.1.100", true); err != nil {
		t.Fatalf("quick scan start error: %v", err)
	}

	// Wait for completion.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("quick scan did not complete")
		default:
			if sc1.GetStatus().State == scanner.StateComplete {
				if !sc1.GetStatus().LastResult.Quick {
					t.Error("expected quick=true in result")
				}
				goto fullScan
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

fullScan:
	// Full scan.
	sc2 := scanner.New(mock, logger)
	if err := sc2.Scan(ctx, "192.168.1.100", false); err != nil {
		t.Fatalf("full scan start error: %v", err)
	}

	deadline = time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("full scan did not complete")
		default:
			if sc2.GetStatus().State == scanner.StateComplete {
				if sc2.GetStatus().LastResult.Quick {
					t.Error("expected quick=false in result")
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
