// server.go — Flock TV service: server struct, DB connection, route registration.
// Phase FLOCKTV: content selections, stream gateway, billing events, Docker provisioning,
// Flock→Roost SSO internal API, and Roost Boost IPTV contribution management.
// Port: 8105 (internal; proxied via Nginx).
package flocktv

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

// Server holds all shared dependencies for the Flock TV service.
type Server struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *slog.Logger
	port   string
}

// NewServer creates a new FlockTV server with a DB connection and structured logger.
func NewServer(db *sql.DB) *Server {
	port := getEnv("FLOCKTV_PORT", "8105")
	s := &Server{
		db:     db,
		logger: slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		port:   port,
	}
	// Redis is optional — billing caching degrades gracefully without it.
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err == nil {
			s.rdb = redis.NewClient(opt)
		}
	}
	return s
}

// NewServerWithRedis creates a server with an explicit Redis client (for testing).
func NewServerWithRedis(db *sql.DB, rdb *redis.Client) *Server {
	s := NewServer(db)
	s.rdb = rdb
	return s
}

// ConnectDB opens and verifies a Postgres connection using the pgx stdlib driver.
func ConnectDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(15)
	db.SetMaxIdleConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db, db.PingContext(ctx)
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	s.logger.Info("Flock TV service starting", "port", s.port)
	return http.ListenAndServe(":"+s.port, s.Routes())
}

// Routes builds and returns the chi router with all Flock TV endpoints registered.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", s.handleHealth)

	// Content selections — family content library management.
	r.Route("/flocktv/selections", func(r chi.Router) {
		r.Get("/", s.handleListSelections)
		r.Post("/", s.handleAddSelection)
		r.Delete("/{canonical_id}", s.handleRemoveSelection)
	})

	// Stream gateway — validates JWT, generates signed Cloudflare CDN URLs.
	r.Post("/flocktv/stream", s.handleStreamRequest)
	r.Post("/flocktv/stream/start", s.handleStreamStart)
	r.Post("/flocktv/stream/end", s.handleStreamEnd)

	// Usage / billing summary for the current family.
	r.Get("/flocktv/usage", s.handleUsageSummary)

	// Catalog API — browse available shared content pool.
	r.Get("/flocktv/catalog", s.handleCatalog)

	// Internal Flock→Roost SSO API.
	// Protected by X-Flock-Internal-Secret header — never exposed via public Nginx.
	r.Post("/internal/flocktv/provision", s.handleSSOprovision)
	r.Delete("/internal/flocktv/revoke/{family_id}", s.handleSSOrevoke)

	// Internal billing meter — called by Flock billing cron to pull usage data.
	r.Get("/internal/billing/usage", s.handleBillingUsage)
	r.Post("/internal/billing/usage/batch", s.handleBillingUsageBatch)

	// Roost Boost — IPTV contribution management.
	r.Route("/flocktv/boost", func(r chi.Router) {
		r.Post("/contribute", s.handleContribute)
		r.Get("/status", s.handleBoostStatus)
		r.Delete("/contribute/{id}", s.handleRemoveContribution)
		r.Get("/channels", s.handleBoostChannels)
	})

	// Acquisition queue status.
	r.Get("/flocktv/acquire/{canonical_id}", s.handleAcquisitionStatus)

	// Channel deduplication.
	r.Post("/internal/flocktv/channels/dedup", s.handleChannelDedup)

	return r
}

// handleHealth returns service liveness.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "roost-flocktv",
	})
}

// getEnv returns the value of key, or fallback if not set.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
