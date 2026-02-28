// Package shutdown provides graceful shutdown handling for AntBox (T-7H.1.003).
package shutdown

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// GracefulShutdown blocks until SIGINT or SIGTERM is received, then calls cleanup
// and waits up to timeout for it to complete.
func GracefulShutdown(timeout time.Duration, cleanup func(ctx context.Context)) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	sig := <-quit
	fmt.Fprintf(os.Stdout, "\nreceived signal %s — shutting down gracefully (timeout: %s)\n", sig, timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		cleanup(ctx)
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("shutdown complete")
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "shutdown timed out — forcing exit")
		os.Exit(1)
	}
}
