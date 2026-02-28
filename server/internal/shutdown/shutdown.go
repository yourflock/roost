// Package shutdown provides graceful HTTP server shutdown with connection draining.
// P21.5.001: Graceful Shutdown with Connection Draining
package shutdown

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// GracefulServe starts the HTTP server and blocks until SIGTERM or SIGINT.
// On signal: stops accepting new connections, drains active connections up to
// drainTimeout, then shuts down.
func GracefulServe(srv *http.Server, drainTimeout time.Duration, logger *slog.Logger) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-serverErr:
		return err
	case sig := <-quit:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	logger.Info("draining connections", "timeout", drainTimeout.String())
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return err
	}

	logger.Info("server stopped cleanly")
	return nil
}

// WaitForSignal blocks until SIGTERM or SIGINT, then returns.
func WaitForSignal(logger *slog.Logger) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	logger.Info("shutdown signal received", "signal", sig.String())
}
