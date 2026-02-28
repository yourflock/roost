// sentry.go — Sentry error tracking and performance monitoring for all Roost Go services.
// P18-T04: Error Tracking & APM
//
// Usage in main.go:
//
//	import "github.com/unyeco/roost/pkg/telemetry"
//
//	func main() {
//	    telemetry.InitSentry(os.Getenv("SENTRY_DSN"), "billing", version)
//	    defer sentry.Flush(2 * time.Second)
//	    // ...
//	}
//
// Usage in handlers:
//
//	telemetry.CaptureError(err, map[string]string{
//	    "subscriber_id": subscriberID,
//	    "operation":     "checkout",
//	})
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitSentry initializes the Sentry SDK for a named service.
// Call once at process startup. dsn may be empty — Sentry will be disabled.
// serviceName identifies the Go microservice (e.g. "billing", "ingest").
// release should be the git SHA or version tag (e.g. "v1.2.3" or "abc1234").
func InitSentry(dsn, serviceName, release string) error {
	env := os.Getenv("ROOST_ENV")
	if env == "" {
		env = "development"
	}

	if dsn == "" {
		// Sentry disabled — not an error. Log and continue.
		fmt.Fprintf(os.Stderr, "[telemetry] SENTRY_DSN not set — Sentry disabled for %s\n", serviceName)
		return nil
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Environment: env,
		Release:     release,

		// Sample 20% of transactions for performance monitoring.
		// Increase when budget allows — free tier: 10K transactions/month.
		TracesSampleRate: 0.2,

		// Attach stack traces to all captured messages (not just panics).
		AttachStacktrace: true,

		// Default tags applied to every event from this service.
		Tags: map[string]string{
			"service": serviceName,
		},

		// BeforeSend scrubs PII before sending to Sentry.
		BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			return scrubPII(event)
		},
	})
	if err != nil {
		return fmt.Errorf("sentry.Init: %w", err)
	}

	return nil
}

// CaptureError sends an error to Sentry with optional context tags.
// tags may include: subscriber_id, channel_id, stream_session_id, operation.
// Safe to call when Sentry is disabled (dsn was empty).
func CaptureError(err error, tags map[string]string) {
	if err == nil {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		for k, v := range tags {
			scope.SetTag(k, v)
		}
		sentry.CaptureException(err)
	})
}

// CaptureMessage sends a non-error message to Sentry (e.g., for important events).
func CaptureMessage(message string, level sentry.Level, tags map[string]string) {
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(level)
		for k, v := range tags {
			scope.SetTag(k, v)
		}
		sentry.CaptureMessage(message)
	})
}

// Flush waits for buffered Sentry events to be sent. Call with defer in main():
//
//	defer telemetry.Flush()
func Flush() {
	sentry.Flush(2 * time.Second)
}

// PanicRecoveryMiddleware is an HTTP middleware that catches panics, reports them
// to Sentry with request context, and returns a 500 response.
func PanicRecoveryMiddleware(serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// Capture panic as a Sentry event with request context.
					hub := sentry.CurrentHub().Clone()
					hub.Scope().SetRequest(r)
					hub.Scope().SetTag("service", serviceName)
					hub.Scope().SetTag("panic", "true")

					var err error
					switch v := rec.(type) {
					case error:
						err = v
					default:
						err = fmt.Errorf("panic: %v", v)
					}
					hub.CaptureException(err)

					// Flush immediately so the event is sent before the response is written.
					hub.Flush(2 * time.Second)

					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// SetSubscriberContext attaches subscriber information to the current Sentry scope.
// Call after authentication succeeds in a request handler.
func SetSubscriberContext(ctx context.Context, subscriberID, email string) {
	if hub := sentry.GetHubFromContext(ctx); hub != nil {
		hub.Scope().SetUser(sentry.User{
			ID:    subscriberID,
			Email: email,
		})
	}
}

// scrubPII removes personally identifiable information from Sentry events
// before they are transmitted. This ensures GDPR compliance.
func scrubPII(event *sentry.Event) *sentry.Event {
	if event == nil {
		return nil
	}

	// Redact email from user context.
	if event.User.Email != "" {
		event.User.Email = "[redacted]"
	}

	// Remove IP address — Sentry should not store subscriber IPs.
	event.User.IPAddress = ""

	// Scrub Authorization headers from request context.
	if event.Request != nil {
		headers := event.Request.Headers
		for k := range headers {
			switch k {
			case "Authorization", "Cookie", "X-Api-Key", "X-Auth-Token":
				headers[k] = "[redacted]"
			}
		}
	}

	return event
}
