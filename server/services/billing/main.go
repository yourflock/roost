// main.go — Roost Billing Service.
// Port: 8085 (internal; proxied via Nginx).
// Stripe: reads STRIPE_SECRET_KEY or STRIPE_FLOCK_SECRET_KEY from environment.
package billing

import (
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"

	_ "github.com/lib/pq"

	"github.com/yourflock/roost/internal/r2"
	"github.com/yourflock/roost/internal/ratelimit"
	stripeclient "github.com/yourflock/roost/internal/stripe"
)

// Server holds all shared dependencies for the billing service.
type Server struct {
	db      *sql.DB
	stripe  *stripeclient.Client
	limiter *ratelimit.Limiter
	r2      *r2.Client // may be nil if R2 credentials are not configured
	port    string
}

// NewServer creates the billing server.
// stripeClient may be nil if STRIPE_SECRET_KEY is not yet configured.
// r2Client may be nil if R2 env vars are not set; avatar upload degrades gracefully.
func NewServer(db *sql.DB, sc *stripeclient.Client, limiter *ratelimit.Limiter, r2c *r2.Client) *Server {
	port := getEnv("BILLING_PORT", "8085")
	return &Server{db: db, stripe: sc, limiter: limiter, r2: r2c, port: port}
}

// Run starts the HTTP server with all billing routes registered.
func (s *Server) Run() error {
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	log.Printf("Roost Billing Service starting on :%s", s.port)
	return http.ListenAndServe(":"+s.port, mux)
}

// handleHealth returns service health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stripeStatus := "unconfigured"
	if s.stripe != nil {
		stripeStatus = "ok"
		if s.stripe.IsTestMode() {
			stripeStatus = "ok (test mode)"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","service":"roost-billing","stripe":"%s"}`, stripeStatus)
}

// stripeRequired returns 503 with a clear message if Stripe is not configured.
// Returns true if Stripe is unavailable (caller should return immediately).
func (s *Server) stripeRequired(w http.ResponseWriter) bool {
	if s.stripe == nil {
		writeError(w, http.StatusServiceUnavailable, "stripe_not_configured",
			"Stripe is not configured. Add STRIPE_SECRET_KEY=sk_test_... to ~/.roost-secrets.env for Roost.")
		return true
	}
	return false
}

// StartBillingService is the entrypoint for cmd/billing/main.go.
func StartBillingService() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("Billing: database connection failed: %v", err)
	}
	defer db.Close()
	log.Printf("Billing: database connected")

	// Stripe — optional at startup; degrades gracefully if key not set.
	// Key read from STRIPE_SECRET_KEY or STRIPE_FLOCK_SECRET_KEY (env var naming convention).
	sc, err := stripeclient.New()
	if err != nil {
		log.Printf("WARNING: Stripe not configured: %v", err)
		log.Printf("WARNING: /billing/checkout and /billing/admin/setup-stripe will return 503")
		sc = nil
	}

	// R2 — optional at startup; avatar upload degrades gracefully if not configured.
	r2c, err := r2.New()
	if err != nil {
		log.Printf("WARNING: R2 not configured: %v", err)
		log.Printf("WARNING: Avatar uploads will store a placeholder URL (no file will be persisted)")
		r2c = nil
	}

	limiter := ratelimit.New(nil)
	srv := NewServer(db, sc, limiter, r2c)
	// Start background goroutines
	go srv.sportsPregameNotifier()
	srv.startTrialNotifier()
	srv.startOnboardingEmailer()
	srv.startAnalyticsCollector()
	srv.startChurnNotifier()
	srv.startPauseResumeChecker()
	srv.startRetentionPurger(slog.Default())

	if err := srv.Run(); err != nil {
		log.Fatalf("Billing service failed: %v", err)
	}
}

// connectDB opens a Postgres connection from env vars.
func connectDB() (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		getEnv("POSTGRES_HOST", "localhost"),
		getEnv("POSTGRES_PORT", "5433"),
		getEnv("POSTGRES_USER", "roost"),
		getEnv("POSTGRES_PASSWORD", ""),
		getEnv("POSTGRES_DB", "roost_dev"),
	)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// getEnv returns an env var with a fallback.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// writeError writes a JSON error response. Used in handlers that don't import auth.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, message)
}
