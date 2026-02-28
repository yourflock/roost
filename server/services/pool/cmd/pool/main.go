// main.go — Roost Neighborhood Pool Service.
// Families form pools to share media sources (IPTV, NAS, VPS). Pool members
// contribute source URLs; the service health-checks each source and reports
// aggregate pool health. Invite codes allow new families to join pools.
//
// Port: 8115 (env: POOL_PORT). Internal service.
//
// Routes:
//   POST /pool/groups                     — create pool group
//   GET  /pool/groups                     — list pools this family belongs to
//   GET  /pool/groups/{id}                — get pool details + members
//   DELETE /pool/groups/{id}              — delete pool (owner only)
//   POST /pool/join                       — join pool by invite code
//   POST /pool/leave/{id}                 — leave pool
//   POST /pool/groups/{id}/sources        — add source URL to pool
//   GET  /pool/groups/{id}/sources        — list sources for pool
//   DELETE /pool/groups/{id}/sources/{sid} — remove source from pool
//   POST /pool/groups/{id}/health-check   — trigger async health check of all sources
//   GET  /health
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func connectDB() (*sql.DB, error) {
	dsn := getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db, db.PingContext(ctx)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func requireFamilyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Family-ID") == "" || r.Header.Get("X-User-ID") == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "X-Family-ID and X-User-ID headers required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// generateInviteCode generates a 12-character hex invite code.
func generateInviteCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ─── models ──────────────────────────────────────────────────────────────────

type PoolGroup struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	InviteCode     string    `json:"invite_code"`
	OwnerFamilyID  string    `json:"owner_family_id"`
	MaxMembers     int       `json:"max_members"`
	MemberCount    int       `json:"member_count"`
	CreatedAt      string    `json:"created_at"`
}

type PoolMember struct {
	FamilyID string `json:"family_id"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at"`
}

type PoolSource struct {
	ID             string  `json:"id"`
	PoolID         string  `json:"pool_id"`
	FamilyID       string  `json:"family_id"`
	SourceURL      string  `json:"source_url"`
	SourceType     string  `json:"source_type"`
	HealthScore    float64 `json:"health_score"`
	LastCheckedAt  string  `json:"last_checked_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── handlers ────────────────────────────────────────────────────────────────

func (s *server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		Name       string `json:"name"`
		MaxMembers int    `json:"max_members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if body.MaxMembers <= 0 || body.MaxMembers > 50 {
		body.MaxMembers = 10
	}

	inviteCode, err := generateInviteCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate invite code")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()

	var groupID string
	err = tx.QueryRowContext(r.Context(),
		`INSERT INTO pool_groups (name, invite_code, owner_family_id, max_members)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		body.Name, inviteCode, familyID, body.MaxMembers,
	).Scan(&groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create pool")
		return
	}

	// Add creator as owner member.
	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO pool_members (pool_id, family_id, role) VALUES ($1, $2, 'owner')`,
		groupID, familyID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to add owner member")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":          groupID,
		"invite_code": inviteCode,
	})
}

func (s *server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT pg.id, pg.name, pg.invite_code, pg.owner_family_id, pg.max_members,
		        COUNT(pm2.family_id) AS member_count, pg.created_at::text
		 FROM pool_groups pg
		 JOIN pool_members pm ON pm.pool_id = pg.id AND pm.family_id = $1
		 LEFT JOIN pool_members pm2 ON pm2.pool_id = pg.id
		 GROUP BY pg.id, pg.name, pg.invite_code, pg.owner_family_id, pg.max_members, pg.created_at
		 ORDER BY pg.created_at DESC`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	groups := []PoolGroup{}
	for rows.Next() {
		var g PoolGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.InviteCode, &g.OwnerFamilyID,
			&g.MaxMembers, &g.MemberCount, &g.CreatedAt); err != nil {
			continue
		}
		groups = append(groups, g)
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	// Check membership.
	var isMember bool
	s.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM pool_members WHERE pool_id = $1 AND family_id = $2)`,
		id, familyID,
	).Scan(&isMember)
	if !isMember {
		writeError(w, http.StatusForbidden, "forbidden", "not a member of this pool")
		return
	}

	var g PoolGroup
	err := s.db.QueryRowContext(r.Context(),
		`SELECT pg.id, pg.name, pg.invite_code, pg.owner_family_id, pg.max_members,
		        COUNT(pm.family_id) AS member_count, pg.created_at::text
		 FROM pool_groups pg
		 LEFT JOIN pool_members pm ON pm.pool_id = pg.id
		 WHERE pg.id = $1
		 GROUP BY pg.id, pg.name, pg.invite_code, pg.owner_family_id, pg.max_members, pg.created_at`,
		id,
	).Scan(&g.ID, &g.Name, &g.InviteCode, &g.OwnerFamilyID,
		&g.MaxMembers, &g.MemberCount, &g.CreatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// Get members.
	memberRows, err := s.db.QueryContext(r.Context(),
		`SELECT family_id, role, joined_at::text FROM pool_members WHERE pool_id = $1 ORDER BY joined_at`,
		id,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, g)
		return
	}
	defer memberRows.Close()

	members := []PoolMember{}
	for memberRows.Next() {
		var m PoolMember
		if err := memberRows.Scan(&m.FamilyID, &m.Role, &m.JoinedAt); err != nil {
			continue
		}
		members = append(members, m)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"group": g, "members": members})
}

func (s *server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM pool_groups WHERE id = $1 AND owner_family_id = $2`,
		id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusForbidden, "forbidden", "not found or not the owner")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleJoin(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		InviteCode string `json:"invite_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "invite_code is required")
		return
	}

	var groupID string
	var maxMembers, currentCount int
	err := s.db.QueryRowContext(r.Context(),
		`SELECT pg.id, pg.max_members, COUNT(pm.family_id)
		 FROM pool_groups pg
		 LEFT JOIN pool_members pm ON pm.pool_id = pg.id
		 WHERE pg.invite_code = $1
		 GROUP BY pg.id, pg.max_members`,
		body.InviteCode,
	).Scan(&groupID, &maxMembers, &currentCount)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "invalid invite code")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if currentCount >= maxMembers {
		writeError(w, http.StatusConflict, "pool_full", "pool is at maximum capacity")
		return
	}

	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO pool_members (pool_id, family_id, role) VALUES ($1, $2, 'member')
		 ON CONFLICT (pool_id, family_id) DO NOTHING`,
		groupID, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pool_id": groupID, "status": "joined"})
}

func (s *server) handleLeave(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	// Owner cannot leave — must delete or transfer.
	var role string
	s.db.QueryRowContext(r.Context(),
		`SELECT role FROM pool_members WHERE pool_id = $1 AND family_id = $2`,
		id, familyID,
	).Scan(&role)
	if role == "owner" {
		writeError(w, http.StatusConflict, "owner_cannot_leave", "owners must delete the pool or transfer ownership first")
		return
	}

	s.db.ExecContext(r.Context(),
		`DELETE FROM pool_members WHERE pool_id = $1 AND family_id = $2`,
		id, familyID,
	)
	// Also remove their sources from the pool.
	s.db.ExecContext(r.Context(),
		`DELETE FROM pool_sources WHERE pool_id = $1 AND family_id = $2`,
		id, familyID,
	)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleAddSource(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	poolID := chi.URLParam(r, "id")

	// Verify membership.
	var exists bool
	s.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM pool_members WHERE pool_id = $1 AND family_id = $2)`,
		poolID, familyID,
	).Scan(&exists)
	if !exists {
		writeError(w, http.StatusForbidden, "forbidden", "not a pool member")
		return
	}

	var body struct {
		SourceURL  string `json:"source_url"`
		SourceType string `json:"source_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.SourceURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "source_url is required")
		return
	}
	if body.SourceType == "" {
		body.SourceType = "iptv"
	}

	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO pool_sources (pool_id, family_id, source_url, source_type)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		poolID, familyID, body.SourceURL, body.SourceType,
	).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *server) handleListSources(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	poolID := chi.URLParam(r, "id")

	var isMember bool
	s.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM pool_members WHERE pool_id = $1 AND family_id = $2)`,
		poolID, familyID,
	).Scan(&isMember)
	if !isMember {
		writeError(w, http.StatusForbidden, "forbidden", "not a pool member")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, pool_id, family_id, source_url, source_type, health_score,
		        COALESCE(last_checked_at::text,''), created_at::text
		 FROM pool_sources WHERE pool_id = $1 ORDER BY created_at DESC`,
		poolID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	sources := []PoolSource{}
	for rows.Next() {
		var ps PoolSource
		if err := rows.Scan(&ps.ID, &ps.PoolID, &ps.FamilyID, &ps.SourceURL,
			&ps.SourceType, &ps.HealthScore, &ps.LastCheckedAt, &ps.CreatedAt); err != nil {
			continue
		}
		sources = append(sources, ps)
	}
	writeJSON(w, http.StatusOK, sources)
}

func (s *server) handleRemoveSource(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	poolID := chi.URLParam(r, "id")
	sourceID := chi.URLParam(r, "sid")

	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM pool_sources WHERE id = $1 AND pool_id = $2 AND family_id = $3`,
		sourceID, poolID, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "source not found or not yours")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleHealthCheck pings each source URL (HEAD request) and updates health_score.
// Runs asynchronously; responds immediately with job acknowledgement.
func (s *server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	poolID := chi.URLParam(r, "id")
	jobID := uuid.New().String()

	go func() {
		log.Printf("[pool] health-check job %s for pool %s", jobID, poolID)
		rows, err := s.db.QueryContext(context.Background(),
			`SELECT id, source_url FROM pool_sources WHERE pool_id = $1`,
			poolID,
		)
		if err != nil {
			log.Printf("[pool] health-check %s: db error: %v", jobID, err)
			return
		}
		defer rows.Close()

		type src struct {
			ID  string
			URL string
		}
		sources := []src{}
		for rows.Next() {
			var ss src
			if err := rows.Scan(&ss.ID, &ss.URL); err == nil {
				sources = append(sources, ss)
			}
		}
		rows.Close()

		for _, ss := range sources {
			score := checkSourceHealth(ss.URL)
			s.db.ExecContext(context.Background(),
				`UPDATE pool_sources SET health_score = $1, last_checked_at = now() WHERE id = $2`,
				score, ss.ID,
			)
		}
		log.Printf("[pool] health-check job %s complete (%d sources)", jobID, len(sources))
	}()

	_ = familyID
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "running"})
}

// checkSourceHealth makes a HEAD request to source_url and returns a 0.0–1.0 health score.
func checkSourceHealth(sourceURL string) float64 {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head(sourceURL)
	if err != nil {
		return 0.0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		return 1.0
	}
	if resp.StatusCode < 500 {
		return 0.5
	}
	return 0.0
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-pool"})
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[pool] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", srv.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(requireFamilyAuth)
		r.Post("/pool/groups", srv.handleCreateGroup)
		r.Get("/pool/groups", srv.handleListGroups)
		r.Get("/pool/groups/{id}", srv.handleGetGroup)
		r.Delete("/pool/groups/{id}", srv.handleDeleteGroup)
		r.Post("/pool/join", srv.handleJoin)
		r.Post("/pool/leave/{id}", srv.handleLeave)
		r.Post("/pool/groups/{id}/sources", srv.handleAddSource)
		r.Get("/pool/groups/{id}/sources", srv.handleListSources)
		r.Delete("/pool/groups/{id}/sources/{sid}", srv.handleRemoveSource)
		r.Post("/pool/groups/{id}/health-check", srv.handleHealthCheck)
	})

	port := getEnv("POOL_PORT", "8115")
	addr := ":" + port
	log.Printf("[pool] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[pool] server error: %v", err)
	}
}

// fmt used in uuid-based logging
var _ = fmt.Sprintf
