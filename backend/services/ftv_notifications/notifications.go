// notifications.go — Content-ready notification service.
// Phase FLOCKTV FTV.1.T05: when content acquisition completes (acquisition_queue.status
// transitions to 'complete'), all families waiting for that canonical_id must be notified.
//
// Trigger: Hasura event on acquisition_queue.status = 'complete' calls
// POST /internal/content-ready with {canonical_id, content_type}.
//
// This service queries for all families with content_selections.canonical_id matching
// the just-acquired item and sends each family a notification via GraphQL subscription
// or a push event to the Flock notification service.
package ftv_notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContentReadyEvent is the webhook payload sent by Hasura when acquisition completes.
type ContentReadyEvent struct {
	CanonicalID  string `json:"canonical_id"`
	ContentType  string `json:"content_type"`
	R2Path       string `json:"r2_path"`
	Title        string `json:"title,omitempty"`
}

// FamilyNotification records a pending notification for one family.
type FamilyNotification struct {
	FamilyID    string    `json:"family_id"`
	CanonicalID string    `json:"canonical_id"`
	ContentType string    `json:"content_type"`
	NotifiedAt  time.Time `json:"notified_at"`
}

// NotificationService handles content-ready events and dispatches family notifications.
type NotificationService struct {
	db     *pgxpool.Pool
	logger *slog.Logger
	port   string
}

// NewNotificationService creates a NotificationService.
func NewNotificationService(db *pgxpool.Pool, logger *slog.Logger) *NotificationService {
	port := os.Getenv("FTV_NOTIFICATIONS_PORT")
	if port == "" {
		port = "8107"
	}
	return &NotificationService{db: db, logger: logger, port: port}
}

// Run starts the HTTP server.
func (s *NotificationService) Run() error {
	s.logger.Info("FTV Notifications service starting", "port", s.port)
	return http.ListenAndServe(":"+s.port, s.Routes())
}

// Routes returns the chi router.
func (s *NotificationService) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "ftv-notifications"})
	})

	// Hasura event trigger webhook.
	r.Post("/internal/content-ready", s.handleContentReady)

	// SSE stream for a family's content-ready events (polling alternative).
	r.Get("/ftv/notifications/stream", s.handleNotificationStream)

	return r
}

// handleContentReady is called by Hasura when acquisition_queue.status → 'complete'.
// POST /internal/content-ready
// Body: {"canonical_id": "imdb:tt0111161", "content_type": "movie", "r2_path": "..."}
func (s *NotificationService) handleContentReady(w http.ResponseWriter, r *http.Request) {
	var event ContentReadyEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	if event.CanonicalID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "canonical_id is required")
		return
	}

	s.logger.Info("content ready event received",
		"canonical_id", event.CanonicalID,
		"content_type", event.ContentType,
	)

	// Find all families waiting for this canonical_id.
	waitingFamilies, err := s.findWaitingFamilies(r.Context(), event.CanonicalID)
	if err != nil {
		s.logger.Error("failed to query waiting families",
			"canonical_id", event.CanonicalID,
			"error", err.Error(),
		)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to find waiting families")
		return
	}

	// Dispatch notifications.
	dispatched := 0
	for _, familyID := range waitingFamilies {
		if notifyErr := s.notifyFamily(r.Context(), familyID, event); notifyErr != nil {
			s.logger.Warn("notification dispatch failed",
				"family_id", familyID,
				"canonical_id", event.CanonicalID,
				"error", notifyErr.Error(),
			)
		} else {
			dispatched++
		}
	}

	s.logger.Info("content ready notifications dispatched",
		"canonical_id", event.CanonicalID,
		"families_notified", dispatched,
		"families_total", len(waitingFamilies),
	)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"canonical_id":       event.CanonicalID,
		"families_notified":  dispatched,
		"families_total":     len(waitingFamilies),
	})
}

// findWaitingFamilies returns all family_ids with a content_selection for canonical_id.
func (s *NotificationService) findWaitingFamilies(ctx context.Context, canonicalID string) ([]string, error) {
	if s.db == nil {
		return []string{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT family_id FROM content_selections WHERE canonical_id = $1`,
		canonicalID,
	)
	if err != nil {
		return nil, fmt.Errorf("waiting families query failed: %w", err)
	}
	defer rows.Close()

	var families []string
	for rows.Next() {
		var fid string
		if scanErr := rows.Scan(&fid); scanErr == nil {
			families = append(families, fid)
		}
	}
	return families, rows.Err()
}

// notifyFamily sends a content-ready notification to a single family.
// In production, this calls the Flock notification service push API.
// For now, it logs and writes to a notifications table for polling.
func (s *NotificationService) notifyFamily(ctx context.Context, familyID string, event ContentReadyEvent) error {
	s.logger.Info("notifying family",
		"family_id", familyID,
		"canonical_id", event.CanonicalID,
	)

	if s.db == nil {
		return nil
	}

	// Insert a pending notification record for the family to poll.
	_, err := s.db.Exec(ctx, `
		INSERT INTO family_notifications (family_id, canonical_id, content_type, event_type, created_at)
		VALUES ($1, $2, $3, 'content_ready', NOW())
		ON CONFLICT DO NOTHING`,
		familyID, event.CanonicalID, event.ContentType,
	)
	if err != nil {
		// Table may not exist yet — non-fatal, log and continue.
		s.logger.Debug("notification record insert skipped",
			"family_id", familyID,
			"error", err.Error(),
		)
	}

	// Forward to Flock notification service if configured.
	flockNotifyURL := os.Getenv("FLOCK_NOTIFICATION_URL")
	if flockNotifyURL != "" {
		payload, _ := json.Marshal(map[string]interface{}{
			"family_id":    familyID,
			"event_type":   "content_ready",
			"canonical_id": event.CanonicalID,
			"content_type": event.ContentType,
			"title":        event.Title,
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, flockNotifyURL,
			bytesReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if secret := os.Getenv("FLOCK_INTERNAL_SECRET"); secret != "" {
			req.Header.Set("X-Roost-Internal-Secret", secret)
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr == nil {
			resp.Body.Close()
		}
	}

	return nil
}

// handleNotificationStream serves a family's pending content-ready notifications.
// GET /ftv/notifications/stream?family_id=X&since=ISO8601
// Returns JSON array of notification events since the given timestamp.
func (s *NotificationService) handleNotificationStream(w http.ResponseWriter, r *http.Request) {
	familyID := r.URL.Query().Get("family_id")
	if familyID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "family_id is required")
		return
	}

	type NotificationEvent struct {
		ID          string    `json:"id"`
		CanonicalID string    `json:"canonical_id"`
		ContentType string    `json:"content_type"`
		EventType   string    `json:"event_type"`
		CreatedAt   time.Time `json:"created_at"`
	}

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"notifications": []NotificationEvent{},
			"family_id":     familyID,
		})
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT id, canonical_id, content_type, event_type, created_at
		FROM family_notifications
		WHERE family_id = $1
		ORDER BY created_at DESC
		LIMIT 50`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to retrieve notifications")
		return
	}
	defer rows.Close()

	notifications := []NotificationEvent{}
	for rows.Next() {
		var n NotificationEvent
		if scanErr := rows.Scan(&n.ID, &n.CanonicalID, &n.ContentType, &n.EventType, &n.CreatedAt); scanErr == nil {
			notifications = append(notifications, n)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"notifications": notifications,
		"family_id":     familyID,
		"count":         len(notifications),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// bytesReader wraps a byte slice in an io.Reader interface (for HTTP requests).
func bytesReader(b []byte) *bytesReaderImpl {
	return &bytesReaderImpl{data: b, pos: 0}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, fmt.Errorf("EOF")
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
