// server.go — Sports service server: DB connection, server struct, route registration.
package sports

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/jackc/pgx/v5/stdlib"
)

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

// Server holds all shared dependencies.
type Server struct {
	db *sql.DB
}

// NewServer creates a new sports Server.
func NewServer(db *sql.DB) *Server {
	return &Server{db: db}
}

// Routes returns the chi router with all routes registered.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", s.handleHealth)

	// Public sports API
	r.Get("/sports/leagues", s.handleListLeagues)
	r.Get("/sports/leagues/{id}/teams", s.handleListTeams)
	r.Get("/sports/events", s.handleListEvents)
	r.Get("/sports/events/{id}", s.handleGetEvent)
	r.Get("/sports/live", s.handleLiveEvents)

	// Admin routes
	r.Post("/admin/sports/leagues", s.handleCreateLeague)
	r.Post("/admin/sports/teams", s.handleCreateTeam)
	r.Post("/admin/sports/events", s.handleCreateEvent)
	r.Post("/admin/sports/events/{id}/channel-mapping", s.handleChannelMapping)
	r.Post("/admin/sports/sync", s.handleAdminSync)

	// Flock TV sports endpoints (FTV.4)
	r.Get("/ftv/sports/ticker", s.handleScoreTickerSSE)
	r.Get("/ftv/sports/scores", s.handleMultiGameTicker)
	r.Get("/ftv/sports/picks", s.handleFamilyPicks)
	r.Post("/ftv/sports/picks", s.handleAddPick)
	r.Get("/ftv/sports/leaderboard", s.handleSportsLeaderboard)

	// Internal sports webhook — score updates from sync service.
	r.Post("/internal/sports/score-update", s.handleScoreNotification)

	s.registerMetadataRoutes(r)

	return r
}

// writeJSON sends a JSON response.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends a JSON error response.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// handleHealth returns service health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"service": "roost-sports",
	})
}
