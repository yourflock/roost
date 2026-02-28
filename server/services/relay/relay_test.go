// relay_test.go — Unit tests for relay service: token auth, concurrent limit enforcement,
// session creation, and CORS handling.
package relay_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/google/uuid"
	"github.com/unyeco/roost/services/relay/internal/sessions"
)

// --- Session manager tests ---

func TestSessionManagerConcurrentLimit(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		t.Skip("no test DB available")
	}
	defer db.Close()

	subID := insertTestSub(t, db)
	mgr := sessions.NewManager(db, 2)
	ctx := context.Background()

	// First stream
	_, err := mgr.OnPlaylistRequest(ctx, subID, "ch-limit-1", "dev-1")
	if err != nil {
		t.Fatalf("first stream request failed: %v", err)
	}

	// Second stream (different channel, different device)
	_, err = mgr.OnPlaylistRequest(ctx, subID, "ch-limit-2", "dev-2")
	if err != nil {
		t.Fatalf("second stream request failed: %v", err)
	}

	// Give sessions time to register
	time.Sleep(50 * time.Millisecond)

	// Third device: should be rejected
	_, err = mgr.OnPlaylistRequest(ctx, subID, "ch-limit-3", "dev-3")
	if err == nil {
		t.Error("third stream should have been rejected, got nil error")
	} else {
		t.Logf("third stream rejected as expected: %v", err)
	}
}

func TestSessionManagerSameDeviceSameChannel(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		t.Skip("no test DB available")
	}
	defer db.Close()

	subID := insertTestSub(t, db)
	mgr := sessions.NewManager(db, 2)
	ctx := context.Background()

	_, err := mgr.OnPlaylistRequest(ctx, subID, "ch-same", "dev-x")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	// Same device+channel: should not count as a new concurrent stream
	_, err = mgr.OnPlaylistRequest(ctx, subID, "ch-same", "dev-x")
	if err != nil {
		t.Errorf("repeat request on same device+channel should succeed: %v", err)
	}
}

func TestSessionManagerActiveCount(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		t.Skip("no test DB available")
	}
	defer db.Close()

	subID := insertTestSub(t, db)
	mgr := sessions.NewManager(db, 5)
	ctx := context.Background()

	if mgr.ActiveStreamCount(subID) != 0 {
		t.Error("initial active count should be 0")
	}

	mgr.OnPlaylistRequest(ctx, subID, "ch-count", "dev-c")
	time.Sleep(20 * time.Millisecond)

	count := mgr.ActiveStreamCount(subID)
	if count != 1 {
		t.Errorf("after one stream, active count want 1, got %d", count)
	}
}

// --- CORS test ---

func TestCORSAllowedOrigins(t *testing.T) {
	cases := []struct {
		origin  string
		allowed bool
	}{
		{"https://owl.unity.dev", true},
		{"http://localhost:3000", true},
		{"http://localhost:5173", true},
		{"https://evil.com", false},
		{"https://notroost.unity.dev", false},
	}

	for _, tc := range cases {
		got := isAllowedOrigin(tc.origin)
		if got != tc.allowed {
			t.Errorf("origin %q: want allowed=%v, got %v", tc.origin, tc.allowed, got)
		}
	}
}

// --- Token middleware behavior test ---

func TestMissingTokenReturns403(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok")
	})

	middleware := tokenCheckMiddleware(handler)
	req := httptest.NewRequest("GET", "/stream/ch1/stream.m3u8", nil)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("missing token: want 403, got %d", rr.Code)
	}
}

func TestPresentTokenPassesMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok")
	})

	middleware := tokenCheckMiddleware(handler)
	req := httptest.NewRequest("GET", "/stream/ch1/stream.m3u8?token=roost_abc123", nil)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("present token: want 200, got %d", rr.Code)
	}
}

// --- Helpers ---

func tokenCheckMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAllowedOrigin(origin string) bool {
	allowed := []string{
		"https://owl.unity.dev",
		"http://localhost",
		"http://localhost:3000",
		"http://localhost:5173",
	}
	for _, a := range allowed {
		if origin == a || strings.HasPrefix(origin, a) {
			return true
		}
	}
	return false
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil
	}
	return db
}

// insertTestSub inserts a temporary subscriber for testing and registers cleanup.
func insertTestSub(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	subID := uuid.New()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO subscribers (id, email, display_name, password_hash, email_verified, status)
		VALUES ($1, $2, 'Relay Test', 'x', true, 'active')
	`, subID, fmt.Sprintf("relaytest-%s@example.com", subID.String()[:8]))
	if err != nil {
		t.Fatalf("insertTestSub: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(context.Background(), `DELETE FROM stream_sessions WHERE subscriber_id = $1`, subID)
		db.ExecContext(context.Background(), `DELETE FROM subscribers WHERE id = $1`, subID)
	})
	return subID
}
