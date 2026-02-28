package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"antbox/internal/command"
	"antbox/internal/config"
	"antbox/internal/heartbeat"
	"antbox/internal/hdhomerun"
	"antbox/internal/scanner"

	"github.com/sirupsen/logrus"
)

// statusAdapter bridges the heartbeat.StatusProvider interface to the
// concrete command handler and scanner instances.
type statusAdapter struct {
	handler *command.Handler
	scanner *scanner.Scanner
}

func (a *statusAdapter) GetDeviceStatuses() []heartbeat.DeviceStatus {
	// In production, this would query discovered devices and their tuner statuses.
	// For now, return an empty slice; the heartbeat still reports other metrics.
	return []heartbeat.DeviceStatus{}
}

func (a *statusAdapter) GetActiveEventCount() int {
	return a.handler.ActiveEventCount()
}

func (a *statusAdapter) GetScannerState() string {
	return string(a.scanner.GetStatus().State)
}

// logSender is a stub heartbeat.Sender that logs the payload.
// In production, this would send over WebSocket to AntServer.
type logSender struct {
	logger *logrus.Logger
}

func (s *logSender) Send(_ context.Context, data []byte) error {
	s.logger.WithField("payload_size", len(data)).Debug("heartbeat payload ready")
	return nil
}

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetOutput(os.Stdout)

	cfg := config.Load()

	level, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)

	if err := cfg.Validate(); err != nil {
		logger.WithError(err).Fatal("invalid configuration")
	}

	logger.WithFields(logrus.Fields{
		"antbox_id":          cfg.AntBoxID,
		"server_url":         cfg.ServerURL,
		"heartbeat_interval": cfg.HeartbeatInterval.String(),
	}).Info("starting antbox daemon")

	// Create the HDHomeRun client (real HTTP implementation).
	hdClient := hdhomerun.NewHTTPClient()

	// Create the channel scanner.
	sc := scanner.New(hdClient, logger)

	// Create the command handler.
	handler := command.NewHandler(hdClient, sc, logger)

	// Create the heartbeat reporter.
	adapter := &statusAdapter{handler: handler, scanner: sc}
	sender := &logSender{logger: logger}
	reporter := heartbeat.NewReporter(cfg.AntBoxID, cfg.HeartbeatInterval, sender, adapter, logger)

	// Set up context with cancellation for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the heartbeat reporter in its own goroutine.
	go reporter.Run(ctx)

	logger.Info("antbox daemon running; waiting for commands")

	// In production, this would connect to AntServer via WebSocket and
	// process incoming commands through handler.Handle(). For now, the
	// daemon runs the heartbeat loop and waits for shutdown.
	_ = handler // used by WebSocket command loop in production

	// Wait for interrupt signal for graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	logger.WithField("signal", sig.String()).Info("shutting down antbox daemon")
	cancel()

	sent, failed := reporter.Stats()
	logger.WithFields(logrus.Fields{
		"heartbeats_sent":   sent,
		"heartbeats_failed": failed,
	}).Info("antbox daemon stopped")
}
