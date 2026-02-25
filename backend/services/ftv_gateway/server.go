// server.go — FTV Gateway service entry point.
// Phase FLOCKTV FTV.1: orchestrates content deduplication, selections API,
// and stream URL generation. Runs on port 8106.
package ftv_gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Server is the FTV Gateway HTTP server.
type Server struct {
	centralDB  *pgxpool.Pool
	rdb        *redis.Client
	familyDBs  *FamilyDBManager
	gateway    *StreamGateway
	logger     *slog.Logger
	port       string
}

// NewServer creates an FTV Gateway server.
func NewServer(centralDB *pgxpool.Pool, rdb *redis.Client) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	familyDBs := NewFamilyDBManager(centralDB)
	gateway := NewStreamGateway(centralDB, familyDBs, logger)

	return &Server{
		centralDB: centralDB,
		rdb:       rdb,
		familyDBs: familyDBs,
		gateway:   gateway,
		logger:    logger,
		port:      getGatewayEnv("FTV_GATEWAY_PORT", "8106"),
	}
}

// ConnectCentralDB opens a pgxpool connection to the central Roost DB.
func ConnectCentralDB(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pool, pool.Ping(ctx)
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	s.logger.Info("FTV Gateway starting", "port", s.port)
	return http.ListenAndServe(":"+s.port, s.Routes())
}

// Routes returns the router with all FTV Gateway endpoints.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeGatewayJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "ftv-gateway"})
	})

	// Content dedup + acquisition queue.
	r.Post("/ftv/acquire", s.handleCheckAndQueue)

	// Stream URL generation (FTV.1.T04).
	r.Get("/ftv/stream/{selection_id}", s.gateway.HandleStreamGet)
	r.Post("/ftv/stream/{stream_id}/end", s.gateway.HandleStreamEnd)

	return r
}

// handleCheckAndQueue checks dedup status and queues acquisition if needed.
// POST /ftv/acquire
// Body: {"canonical_id": "imdb:tt0111161", "content_type": "movie", "family_id": "..."}
func (s *Server) handleCheckAndQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CanonicalID string `json:"canonical_id"`
		ContentType string `json:"content_type"`
		FamilyID    string `json:"family_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGatewayError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	if req.CanonicalID == "" {
		writeGatewayError(w, http.StatusBadRequest, "missing_field", "canonical_id is required")
		return
	}

	result, err := CheckAndQueueAcquisition(
		r.Context(),
		s.centralDB,
		s.rdb,
		req.FamilyID,
		req.CanonicalID,
		req.ContentType,
	)
	if err != nil {
		s.logger.Error("dedup check failed", "error", err.Error())
		writeGatewayError(w, http.StatusInternalServerError, "dedup_error", err.Error())
		return
	}

	status := "queued"
	if result.Available {
		status = "available"
	} else if result.Processing {
		status = "processing"
	}

	writeGatewayJSON(w, http.StatusOK, map[string]interface{}{
		"canonical_id": req.CanonicalID,
		"status":       status,
		"priority":     result.Priority,
	})
}

// writeGatewayJSON encodes v as JSON with the given status code.
func writeGatewayJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeGatewayError writes a standard error response.
func writeGatewayError(w http.ResponseWriter, status int, code, msg string) {
	writeGatewayJSON(w, status, map[string]string{"error": code, "message": msg})
}
