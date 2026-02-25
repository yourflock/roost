// main.go — Roost AI Program Guide Service.
// Generates personalized content recommendations for families using the
// OpenAI Chat Completions API. Recommendations are cached in Postgres with a
// configurable TTL (default 6 hours). A nightly cron goroutine refreshes
// recommendations for all active families. Families can submit feedback
// (like / dislike / not_interested / already_seen) to improve future picks.
//
// Port: 8117 (env: AI_GUIDE_PORT). Internal service — called by flock backend.
//
// Routes:
//   GET  /ai-guide/recommendations            — get cached recommendations for family
//   POST /ai-guide/recommendations/refresh    — force refresh recommendations
//   POST /ai-guide/feedback                   — submit feedback for a content item
//   GET  /ai-guide/trending                   — get trending content across all families
//   GET  /health
package main

import (
	"bytes"
	"context"
	"database/sql"
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

// ─── OpenAI integration ───────────────────────────────────────────────────────

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// RecommendedItem is a single AI-suggested content item.
type RecommendedItem struct {
	ContentID   string  `json:"content_id"`
	ContentType string  `json:"content_type"`
	Title       string  `json:"title"`
	Score       float64 `json:"score"`
	Reason      string  `json:"reason"`
}

// callOpenAI sends a chat completion request and returns the response text.
func callOpenAI(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	apiKey := getEnv("OPENAI_API_KEY", "")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY not configured")
	}
	model := getEnv("OPENAI_MODEL", "gpt-4o-mini")

	reqBody := openAIRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: 800,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var openAIResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", err
	}
	if openAIResp.Error != nil {
		return "", fmt.Errorf("openai error: %s", openAIResp.Error.Message)
	}
	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("openai returned no choices")
	}
	return openAIResp.Choices[0].Message.Content, nil
}

// buildRecommendationPrompt constructs prompts based on family watch history
// and dislike feedback stored in Postgres.
func (s *server) buildRecommendationPrompt(ctx context.Context, familyID string) (string, string, error) {
	// Get recent watch history (last 20 items).
	watchRows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(content_id,''), COALESCE(content_type,'')
		 FROM watch_progress WHERE subscriber_id = $1
		 ORDER BY last_watched_at DESC LIMIT 20`,
		familyID,
	)
	var recentWatched []string
	if err == nil {
		defer watchRows.Close()
		for watchRows.Next() {
			var cid, ctype string
			if err := watchRows.Scan(&cid, &ctype); err == nil {
				recentWatched = append(recentWatched, fmt.Sprintf("%s (%s)", cid, ctype))
			}
		}
	}

	// Get dislike/not_interested feedback.
	fbRows, err := s.db.QueryContext(ctx,
		`SELECT content_id, feedback FROM ai_guide_feedback
		 WHERE family_id = $1 AND feedback IN ('dislike','not_interested')
		 ORDER BY created_at DESC LIMIT 20`,
		familyID,
	)
	var disliked []string
	if err == nil {
		defer fbRows.Close()
		for fbRows.Next() {
			var cid, fb string
			if err := fbRows.Scan(&cid, &fb); err == nil {
				disliked = append(disliked, cid)
			}
		}
	}

	// Get catalog sample for context (titles + types the system has).
	catRows, err := s.db.QueryContext(ctx,
		`SELECT id, title, type FROM vod_catalog WHERE is_active = true ORDER BY created_at DESC LIMIT 30`,
	)
	var catalog []string
	if err == nil {
		defer catRows.Close()
		for catRows.Next() {
			var id, title, ctype string
			if err := catRows.Scan(&id, &title, &ctype); err == nil {
				catalog = append(catalog, fmt.Sprintf(`{"content_id":"%s","title":"%s","type":"%s"}`, id, title, ctype))
			}
		}
	}

	systemPrompt := `You are a personalized media recommendation engine for a family streaming service.
Given a family's watch history and disliked items, recommend 10 content items from the available catalog.
Return a JSON array with objects: {"content_id","content_type","title","score","reason"}.
score is 0.0-1.0. reason is one short sentence. Return ONLY the JSON array, no other text.`

	userPrompt := fmt.Sprintf(
		"Recently watched: %v\n\nDo not recommend (disliked): %v\n\nAvailable catalog: [%s]\n\nRecommend 10 items.",
		recentWatched, disliked, joinStrings(catalog, ","),
	)
	return systemPrompt, userPrompt, nil
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

// ─── models ──────────────────────────────────────────────────────────────────

type Recommendation struct {
	ID          string  `json:"id"`
	FamilyID    string  `json:"family_id"`
	ContentID   string  `json:"content_id"`
	ContentType string  `json:"content_type"`
	Score       float64 `json:"score"`
	Reason      string  `json:"reason,omitempty"`
	ExpiresAt   string  `json:"expires_at"`
	CreatedAt   string  `json:"created_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── handlers ────────────────────────────────────────────────────────────────

func (s *server) handleGetRecommendations(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, family_id, content_id, content_type, score, COALESCE(reason,''), expires_at::text, created_at::text
		 FROM ai_guide_recommendations
		 WHERE family_id = $1 AND expires_at > now()
		 ORDER BY score DESC LIMIT 20`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	recs := []Recommendation{}
	for rows.Next() {
		var rec Recommendation
		if err := rows.Scan(&rec.ID, &rec.FamilyID, &rec.ContentID, &rec.ContentType,
			&rec.Score, &rec.Reason, &rec.ExpiresAt, &rec.CreatedAt); err != nil {
			continue
		}
		recs = append(recs, rec)
	}

	// If no cached recommendations, return empty and suggest refresh.
	if len(recs) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"recommendations": []interface{}{},
			"hint":            "No recommendations cached. POST /ai-guide/recommendations/refresh to generate.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"recommendations": recs})
}

// handleRefresh generates new recommendations for the family using OpenAI.
func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	jobID := uuid.New().String()

	go func() {
		log.Printf("[ai_guide] refresh job %s for family %s", jobID, familyID)
		ctx := context.Background()

		systemPrompt, userPrompt, err := s.buildRecommendationPrompt(ctx, familyID)
		if err != nil {
			log.Printf("[ai_guide] build prompt error: %v", err)
			return
		}

		responseText, err := callOpenAI(ctx, systemPrompt, userPrompt)
		if err != nil {
			log.Printf("[ai_guide] openai error: %v", err)
			return
		}

		var items []RecommendedItem
		if err := json.Unmarshal([]byte(responseText), &items); err != nil {
			log.Printf("[ai_guide] parse response error: %v — raw: %s", err, responseText)
			return
		}

		// Delete old recommendations for this family.
		s.db.ExecContext(ctx, `DELETE FROM ai_guide_recommendations WHERE family_id = $1`, familyID)

		ttlHours := 6
		expiresAt := time.Now().Add(time.Duration(ttlHours) * time.Hour).Format(time.RFC3339)

		for _, item := range items {
			if item.ContentID == "" || item.ContentType == "" {
				continue
			}
			s.db.ExecContext(ctx,
				`INSERT INTO ai_guide_recommendations (family_id, content_id, content_type, score, reason, expires_at)
				 VALUES ($1, $2, $3, $4, $5, $6::timestamptz)`,
				familyID, item.ContentID, item.ContentType, item.Score, item.Reason, expiresAt,
			)
		}
		log.Printf("[ai_guide] refresh job %s complete: %d recommendations stored", jobID, len(items))
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "generating"})
}

// handleFeedback records user feedback for a content item.
func (s *server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		ContentID string `json:"content_id"`
		Feedback  string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.ContentID == "" || body.Feedback == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id and feedback are required")
		return
	}
	validFeedback := map[string]bool{
		"like": true, "dislike": true, "not_interested": true, "already_seen": true,
	}
	if !validFeedback[body.Feedback] {
		writeError(w, http.StatusBadRequest, "bad_request",
			"feedback must be: like, dislike, not_interested, or already_seen")
		return
	}

	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO ai_guide_feedback (family_id, content_id, feedback) VALUES ($1, $2, $3) RETURNING id`,
		familyID, body.ContentID, body.Feedback,
	).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// If negative feedback: remove from active recommendations immediately.
	if body.Feedback == "dislike" || body.Feedback == "not_interested" {
		s.db.ExecContext(r.Context(),
			`DELETE FROM ai_guide_recommendations WHERE family_id = $1 AND content_id = $2`,
			familyID, body.ContentID,
		)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "recorded"})
}

// handleTrending returns the top-scoring recommendations across all families
// (anonymized — no family_id exposed).
func (s *server) handleTrending(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT content_id, content_type, AVG(score) AS avg_score, COUNT(*) AS family_count
		 FROM ai_guide_recommendations
		 WHERE expires_at > now()
		 GROUP BY content_id, content_type
		 ORDER BY avg_score DESC LIMIT 20`,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type TrendItem struct {
		ContentID   string  `json:"content_id"`
		ContentType string  `json:"content_type"`
		AvgScore    float64 `json:"avg_score"`
		FamilyCount int     `json:"family_count"`
	}

	items := []TrendItem{}
	for rows.Next() {
		var t TrendItem
		if err := rows.Scan(&t.ContentID, &t.ContentType, &t.AvgScore, &t.FamilyCount); err != nil {
			continue
		}
		items = append(items, t)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"trending": items})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-ai-guide"})
}

// ─── nightly cron ─────────────────────────────────────────────────────────────

// startNightlyCron runs a goroutine that refreshes recommendations for all
// families once per day at the configured hour (default 03:00 UTC).
func (s *server) startNightlyCron() {
	go func() {
		cronHour := 3
		log.Printf("[ai_guide] nightly cron scheduled for %02d:00 UTC", cronHour)
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day(), cronHour, 0, 0, 0, time.UTC)
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(next))

			log.Printf("[ai_guide] nightly cron: starting recommendation refresh for all families")
			ctx := context.Background()

			rows, err := s.db.QueryContext(ctx,
				`SELECT DISTINCT family_id FROM ai_guide_recommendations
				 UNION
				 SELECT DISTINCT subscriber_id FROM watch_progress
				 LIMIT 1000`,
			)
			if err != nil {
				log.Printf("[ai_guide] nightly cron: db error: %v", err)
				continue
			}
			families := []string{}
			for rows.Next() {
				var fid string
				if err := rows.Scan(&fid); err == nil && fid != "" {
					families = append(families, fid)
				}
			}
			rows.Close()

			log.Printf("[ai_guide] nightly cron: refreshing %d families", len(families))
			for _, fid := range families {
				systemPrompt, userPrompt, err := s.buildRecommendationPrompt(ctx, fid)
				if err != nil {
					continue
				}
				responseText, err := callOpenAI(ctx, systemPrompt, userPrompt)
				if err != nil {
					log.Printf("[ai_guide] nightly cron: openai error for %s: %v", fid, err)
					continue
				}
				var items []RecommendedItem
				if err := json.Unmarshal([]byte(responseText), &items); err != nil {
					continue
				}
				s.db.ExecContext(ctx, `DELETE FROM ai_guide_recommendations WHERE family_id = $1`, fid)
				expiresAt := time.Now().Add(28 * time.Hour).Format(time.RFC3339)
				for _, item := range items {
					if item.ContentID == "" {
						continue
					}
					s.db.ExecContext(ctx,
						`INSERT INTO ai_guide_recommendations (family_id, content_id, content_type, score, reason, expires_at)
						 VALUES ($1, $2, $3, $4, $5, $6::timestamptz)`,
						fid, item.ContentID, item.ContentType, item.Score, item.Reason, expiresAt,
					)
				}
				// Brief pause to avoid overwhelming OpenAI rate limits.
				time.Sleep(200 * time.Millisecond)
			}
			log.Printf("[ai_guide] nightly cron: complete")
		}
	}()
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[ai_guide] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}
	srv.startNightlyCron()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(45 * time.Second))

	r.Get("/health", srv.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(requireFamilyAuth)
		r.Get("/ai-guide/recommendations", srv.handleGetRecommendations)
		r.Post("/ai-guide/recommendations/refresh", srv.handleRefresh)
		r.Post("/ai-guide/feedback", srv.handleFeedback)
	})

	// Trending is public (no auth needed — no family data exposed).
	r.Get("/ai-guide/trending", srv.handleTrending)

	port := getEnv("AI_GUIDE_PORT", "8117")
	addr := ":" + port
	log.Printf("[ai_guide] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 45 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[ai_guide] server error: %v", err)
	}
}
