// handlers_onboarding.go — Subscriber onboarding progress tracking for P17-T04.
//
// GET  /onboarding/progress  — return current onboarding progress
// POST /onboarding/step      — mark a numbered step complete
// POST /onboarding/complete  — mark onboarding fully done
//
// Steps:
//   1 — signed up
//   2 — copied API token
//   3 — opened Owl download page
//   4 — added Roost to Owl (first addon API call)
//   5 — first stream (auto-detected from stream_sessions)
//
// Auto-complete: if any stream session exists, onboarding is marked complete
// on the next progress check regardless of other steps.
package billing

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// handleOnboardingProgress handles GET /onboarding/progress.
func (s *Server) handleOnboardingProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Fetch or create onboarding record.
	var stepCompleted int
	var isComplete bool
	var completedAt sql.NullTime
	err = s.db.QueryRowContext(r.Context(), `
		SELECT step_completed, is_complete, completed_at
		FROM subscriber_onboarding
		WHERE subscriber_id = $1
	`, subscriberID).Scan(&stepCompleted, &isComplete, &completedAt)
	if err == sql.ErrNoRows {
		// Create onboarding record — step 1 auto-completed on signup.
		_, _ = s.db.ExecContext(r.Context(), `
			INSERT INTO subscriber_onboarding (subscriber_id, step_completed)
			VALUES ($1, 1)
			ON CONFLICT DO NOTHING
		`, subscriberID)
		stepCompleted = 1
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "onboarding lookup failed")
		return
	}

	// Auto-complete: if subscriber has streamed, mark done.
	if !isComplete {
		var hasStream bool
		_ = s.db.QueryRowContext(r.Context(), `
			SELECT EXISTS(SELECT 1 FROM stream_sessions WHERE subscriber_id = $1 LIMIT 1)
		`, subscriberID).Scan(&hasStream)
		if hasStream {
			_, _ = s.db.ExecContext(r.Context(), `
				UPDATE subscriber_onboarding
				SET is_complete = TRUE, completed_at = now(), step_completed = 5
				WHERE subscriber_id = $1
			`, subscriberID)
			isComplete = true
			stepCompleted = 5
		}
	}

	resp := map[string]interface{}{
		"step_completed": stepCompleted,
		"total_steps":    5,
		"is_complete":    isComplete,
		"steps": []map[string]interface{}{
			{"step": 1, "label": "Sign up", "done": stepCompleted >= 1},
			{"step": 2, "label": "Copy your API token", "done": stepCompleted >= 2},
			{"step": 3, "label": "Download Owl", "done": stepCompleted >= 3},
			{"step": 4, "label": "Add Roost to Owl", "done": stepCompleted >= 4},
			{"step": 5, "label": "Start watching", "done": stepCompleted >= 5},
		},
	}
	if completedAt.Valid {
		resp["completed_at"] = completedAt.Time.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleOnboardingStep handles POST /onboarding/step.
func (s *Server) handleOnboardingStep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	var req struct {
		Step int `json:"step"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Step < 1 || req.Step > 5 {
		writeError(w, http.StatusBadRequest, "invalid_step", "step must be 1-5")
		return
	}

	_, _ = s.db.ExecContext(r.Context(), `
		INSERT INTO subscriber_onboarding (subscriber_id, step_completed)
		VALUES ($1, $2)
		ON CONFLICT (subscriber_id) DO UPDATE
		SET step_completed = GREATEST(subscriber_onboarding.step_completed, EXCLUDED.step_completed),
		    updated_at = now()
	`, subscriberID, req.Step)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"step_completed": req.Step,
		"ok":             true,
	})
}

// handleOnboardingComplete handles POST /onboarding/complete.
func (s *Server) handleOnboardingComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	_, _ = s.db.ExecContext(r.Context(), `
		INSERT INTO subscriber_onboarding (subscriber_id, step_completed, is_complete, completed_at)
		VALUES ($1, 5, TRUE, now())
		ON CONFLICT (subscriber_id) DO UPDATE
		SET is_complete = TRUE,
		    completed_at = COALESCE(subscriber_onboarding.completed_at, now()),
		    step_completed = GREATEST(subscriber_onboarding.step_completed, 5),
		    updated_at = now()
	`, subscriberID)

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
