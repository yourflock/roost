// main.go — Roost API server entrypoint.
// Registers all auth, token, device, and health routes.
// Environment loaded from .env (for local dev) or injected by container.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	_ "github.com/lib/pq"
	goredis "github.com/redis/go-redis/v9"

	"github.com/yourflock/roost/internal/ratelimit"
	authsvc "github.com/yourflock/roost/services/auth"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3001"
	}

	// Connect to Postgres
	db, err := connectDB()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Printf("Database connected")

	// Rate limiter — wire Redis if REDIS_URL is set; degrade gracefully if absent.
	var redisStore ratelimit.Store
	if redisURL := getEnv("REDIS_URL", ""); redisURL != "" {
		rdb := goredis.NewClient(&goredis.Options{Addr: redisURL})
		redisStore = ratelimit.NewRedisStore(rdb)
		log.Printf("Redis connected: %s", redisURL)
	} else {
		log.Printf("REDIS_URL not set — rate limiting disabled (dev mode)")
	}
	limiter := ratelimit.New(redisStore)

	mux := http.NewServeMux()

	// ── Health ─────────────────────────────────────────────────────────────
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok","service":"roost"}`)
	})

	// ── Auth: Registration & Email Verification ─────────────────────────────
	mux.HandleFunc("/auth/register", authsvc.HandleRegister(db, limiter))
	mux.HandleFunc("/auth/verify-email", authsvc.HandleVerifyEmail(db))
	mux.HandleFunc("/auth/resend-verification", authsvc.HandleResendVerification(db, limiter))

	// ── Auth: Login & Session Management ───────────────────────────────────
	mux.HandleFunc("/auth/login", authsvc.HandleLogin(db, limiter))
	mux.HandleFunc("/auth/refresh", authsvc.HandleRefresh(db))

	// ── Auth: Password Reset ────────────────────────────────────────────────
	mux.HandleFunc("/auth/forgot-password", authsvc.HandleForgotPassword(db, limiter))
	mux.HandleFunc("/auth/reset-password", authsvc.HandleResetPassword(db))

	// ── Auth: Profile Management ────────────────────────────────────────────
	mux.HandleFunc("/auth/profile", profileRouter(db))
	mux.HandleFunc("/auth/account", authsvc.HandleDeleteAccount(db))

	// ── Auth: API Tokens ────────────────────────────────────────────────────
	mux.HandleFunc("/auth/tokens", tokenRouter(db))
	mux.HandleFunc("/auth/tokens/", authsvc.HandleRevokeToken(db)) // /auth/tokens/:id

	// ── Auth: Two-Factor Authentication ────────────────────────────────────
	mux.HandleFunc("/auth/2fa/setup", authsvc.HandleSetup2FA(db))
	mux.HandleFunc("/auth/2fa/verify-setup", authsvc.HandleVerifySetup2FA(db))
	mux.HandleFunc("/auth/2fa/verify", authsvc.HandleVerify2FA(db))
	mux.HandleFunc("/auth/2fa/status", authsvc.Handle2FAStatus(db))
	mux.HandleFunc("/auth/2fa/backup-codes", authsvc.HandleRegenerateBackupCodes(db))
	mux.HandleFunc("/auth/2fa", authsvc.HandleDisable2FA(db))

	// ── Auth: Device Management ─────────────────────────────────────────────
	mux.HandleFunc("/auth/devices", deviceRouter(db))
	mux.HandleFunc("/auth/devices/", deviceDetailRouter(db)) // /auth/devices/:id

	log.Printf("Roost API starting on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// connectDB establishes a Postgres connection using env vars.
func connectDB() (*sql.DB, error) {
	host := getEnv("POSTGRES_HOST", "localhost")
	port := getEnv("POSTGRES_PORT", "5433")
	user := getEnv("POSTGRES_USER", "roost")
	pass := getEnv("POSTGRES_PASSWORD", "")
	dbname := getEnv("POSTGRES_DB", "roost_dev")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, dbname)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// profileRouter routes GET and PATCH /auth/profile.
func profileRouter(db *sql.DB) http.HandlerFunc {
	getHandler := authsvc.HandleProfile(db)
	patchHandler := authsvc.HandleUpdateProfile(db)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getHandler.ServeHTTP(w, r)
		case http.MethodPatch:
			patchHandler.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, PATCH")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// tokenRouter routes GET and POST /auth/tokens.
func tokenRouter(db *sql.DB) http.HandlerFunc {
	listHandler := authsvc.HandleListTokens(db)
	generateHandler := authsvc.HandleGenerateToken(db)
	return func(w http.ResponseWriter, r *http.Request) {
		// Only handle exact /auth/tokens (not /auth/tokens/:id)
		if r.URL.Path != "/auth/tokens" {
			return
		}
		switch r.Method {
		case http.MethodGet:
			listHandler.ServeHTTP(w, r)
		case http.MethodPost:
			generateHandler.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// deviceRouter routes GET and DELETE /auth/devices (no :id).
func deviceRouter(db *sql.DB) http.HandlerFunc {
	listHandler := authsvc.HandleListDevices(db)
	revokeAllHandler := authsvc.HandleRevokeAllDevices(db)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/devices" {
			return
		}
		switch r.Method {
		case http.MethodGet:
			listHandler.ServeHTTP(w, r)
		case http.MethodDelete:
			revokeAllHandler.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// deviceDetailRouter routes PATCH and DELETE /auth/devices/:id.
func deviceDetailRouter(db *sql.DB) http.HandlerFunc {
	renameHandler := authsvc.HandleRenameDevice(db)
	revokeHandler := authsvc.HandleRevokeDevice(db)
	return func(w http.ResponseWriter, r *http.Request) {
		// Must have an ID segment
		id := strings.TrimPrefix(r.URL.Path, "/auth/devices/")
		if id == "" {
			return
		}
		switch r.Method {
		case http.MethodPatch:
			renameHandler.ServeHTTP(w, r)
		case http.MethodDelete:
			revokeHandler.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "PATCH, DELETE")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// getEnv returns an env var with a fallback default.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
